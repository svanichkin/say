package udp

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"github.com/svanichkin/say/ui"
	"sync"
	"time"

	g722 "github.com/gotranspile/g722"
)

var (
	useVoice = true
	useVideo bool
)

const (
	frameAudio  byte = 0x01
	frameVideo  byte = 0x02
	maxVideoFPS      = 30

	audioSeqBytes           = 2    // uint16 sequence number per audio frame
	audioBufferLeadFrames   = 32   // frames to queue before starting playback (~0.64s)
	audioBufferResumeFrames = 16   // rebuffer threshold after an underrun (~0.32s)
	audioBufferMaxFrames    = 256  // cap (~5s) to absorb very high jitter
	audioMaxConsecutivePLCs = 8    // safety cap to avoid infinite PLC loops
	audioMaxSeqGap          = 2000 // treat large forward gaps as resync (packets skipped)
	audioSeqStaleWindow     = 0x8000
)

// Configure toggles voice/video capture for future UDP sessions.
func Configure(enableVoice, enableVideo bool) {
	useVoice = enableVoice
	useVideo = enableVideo
}

// dataSession represents a running UDP voice session that may also multiplex
// optional video frames over the same PacketConn.
type dataSession struct {
	stop func() error
	done <-chan struct{}
}

// Stop requests the session to terminate and waits for all goroutines to finish.
func (vs *dataSession) Stop() error {
	if vs == nil || vs.stop == nil {
		return nil
	}
	return vs.stop()
}

// Done returns a channel that is closed once the session has fully terminated.
func (vs *dataSession) Done() <-chan struct{} {
	if vs == nil {
		return nil
	}
	return vs.done
}

// StartSession establishes a unidirectional or bidirectional UDP voice channel using G.722
// for audio and malgo for playback. If a camera stream is active and enableVideo is true,
// TextFrame-encoded video frames are also sent over the same PacketConn, prefixed with a frame
// type byte.
func StartSession(pc net.PacketConn, remote *net.UDPAddr) (*dataSession, error) {
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	var cleanupOnce sync.Once
	var wg sync.WaitGroup
	var stop func()

	// Peer address is learned from first inbound packet if nil.
	var peerMu sync.Mutex
	peer := remote
	getPeer := func() *net.UDPAddr {
		peerMu.Lock()
		defer peerMu.Unlock()
		return peer
	}
	setPeer := func(ua *net.UDPAddr) {
		if ua == nil {
			return
		}
		peerMu.Lock()
		peer = &net.UDPAddr{IP: append([]byte(nil), ua.IP...), Port: ua.Port}
		peerMu.Unlock()
		log.Printf("[voice/udp] peer set to %s", peer.String())
	}

	// ======== Audio codec and buffers ========
	var (
		enc          *g722.Encoder
		dec          *g722.Decoder
		frameSamples int

		micCancel context.CancelFunc = func() {}

		audioMicIn <-chan []int16

		audioNetIn      chan []byte
		audioSpeakerOut chan []int16

		speaker *device.Speaker
	)

	if useVoice {
		// Init codec
		var err error
		enc, dec, frameSamples, err = initAudioCodec(0, 0)
		if err != nil {
			return nil, err
		}

		micCtx, cancel := context.WithCancel(context.Background())
		micCancel = cancel

		// microphone PCM input channel
		audioMicIn, err = device.StartMicrophoneStream(micCtx, audioSampleRate, audioChannels)
		if err != nil {
			micCancel()
			return nil, fmt.Errorf("mic stream: %w", err)
		}

		audioSpeakerOut = make(chan []int16, 128)
		audioNetIn = make(chan []byte, 256)

		speaker, err = device.StartSpeaker(audioSampleRate, audioChannels, frameSamples, audioSpeakerOut, stopCh, stop)
		if err != nil {
			return nil, fmt.Errorf("speaker: %w", err)
		}
	}

	cleanup := func() {
		cleanupOnce.Do(func() {
			if speaker != nil {
				speaker.Close()
			}
			ui.ClearLocalFrame()
			ui.ClearRemoteFrame()
		})
	}

	var (
		camCancel context.CancelFunc = func() {}

		videoCamIn <-chan device.CameraFrame

		videoNetIn         chan []byte
		videoSelfScreenOut chan *codec.TextFrame
		videoPeerScreenOut chan *codec.TextFrame
	)

	// ======== Stop function for the whole session ========
	stop = func() {
		stopOnce.Do(func() {
			micCancel()
			if camCancel != nil {
				camCancel()
			}
			close(stopCh)
		})
	}

	// ======== Video codec and buffers ========
	if useVideo {
		var err error
		camCtx, cancel := context.WithCancel(context.Background())
		camCancel = cancel

		// camera frames from local webcam (if available)
		videoCamIn, err = device.StartCameraStream(camCtx)
		if err != nil {
			cancel()
			log.Printf("[video] camera stream disabled: %v", err)
		}

		videoSelfScreenOut = make(chan *codec.TextFrame, 32)
		videoPeerScreenOut = make(chan *codec.TextFrame, 32)
		videoNetIn = make(chan []byte, 32)
	}

	if useVideo {
		ui.EnsureRenderer(context.Background())
	}

	startSender(&wg, pc, stop, stopCh, getPeer, enc, frameSamples, audioMicIn, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)

	// ======== Video screen layer: queues → local/remote slots ========
	if useVideo && (videoSelfScreenOut != nil || videoPeerScreenOut != nil) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return

				case tv := <-videoSelfScreenOut:
					if tv == nil {
						continue
					}
					ui.SetLocalFrame(tv)
					ui.RequestRedraw()

				case tv := <-videoPeerScreenOut:
					if tv == nil {
						continue
					}
					ui.SetRemoteFrame(tv)
					ui.RequestRedraw()
				}
			}
		}()
	}

	// ======== Receiver and decoders ========
	startReceiver(&wg, pc, stopCh, audioNetIn, videoNetIn, getPeer, setPeer)

	if useVoice && audioNetIn != nil && audioSpeakerOut != nil {
		startAudioReceiver(&wg, dec, frameSamples, stopCh, audioNetIn, audioSpeakerOut)
	}
	if useVideo && videoNetIn != nil {
		startVideoReceiver(&wg, stopCh, videoNetIn, videoPeerScreenOut)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		cleanup()
		close(done)
	}()

	stopFunc := func() error {
		stop()
		<-done
		return nil
	}

	return &dataSession{stop: stopFunc, done: done}, nil
}

// startSender launches goroutines responsible for sending:
//   - audio: mic → G.722 → UDP (frameAudio)
//   - video: camera → TextFrame → UDP (frameVideo)
func startSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc *g722.Encoder, frameSamples int,
	audioMicIn <-chan []int16, videoCamIn <-chan device.CameraFrame,
	videoSelfScreenOut chan *codec.TextFrame, videoPeerScreenOut chan *codec.TextFrame,
) {
	if useVoice {
		startAudioSender(wg, pc, stop, stopCh, getPeer, enc, frameSamples, audioMicIn)
	}
	if useVideo {
		startVideoSender(wg, pc, stop, stopCh, getPeer, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)
	}
}

// startAudioSender: mic → G.722 → UDP (frameAudio)
func startAudioSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc *g722.Encoder,
	frameSamples int, audioMicIn <-chan []int16,
) {
	if !useVoice || enc == nil || audioMicIn == nil {
		return
	}

	// ======== Audio sender: mic → G.722 → UDP ========
	wg.Add(1)
	go func() {
		defer wg.Done()

		var (
			sentPkts, sentBytes uint64
			seq                 uint16
		)
		var pcmBuf []int16 // post-processed PCM ready for encoding

		tick := time.NewTicker(time.Second)
		lastRateSample := time.Now()
		defer tick.Stop()

		need := frameSamples * audioChannels

		sendChunk := func(chunk []int16) bool {
			addr := getPeer()
			if addr == nil {
				return true
			}

			if len(chunk) == 0 {
				return true
			}

			encodedLen := (len(chunk)*g722BitsPerSample + 7) / 8
			if encodedLen <= 0 {
				return true
			}

			buf := make([]byte, encodedLen)
			written := enc.Encode(buf, chunk)
			if written <= 0 {
				log.Printf("[voice/udp] encode error: produced %d bytes", written)
				if stop != nil {
					stop()
				}
				return false
			}

			out := make([]byte, written+1+audioSeqBytes)
			out[0] = frameAudio
			binary.BigEndian.PutUint16(out[1:], seq)
			copy(out[1+audioSeqBytes:], buf[:written])
			seq++

			n, err := pc.WriteTo(out, addr)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					log.Printf("[voice/udp] write timeout: %v", err)
					return true
				}
				log.Printf("[voice/udp] write error: %v", err)
				if stop != nil {
					stop()
				}
				return false
			}
			sentPkts++
			if n > 1+audioSeqBytes {
				sentBytes += uint64(n - 1 - audioSeqBytes) // exclude frame type + seq header
			}
			return true
		}

		drainAvailable := func() bool {
			for len(pcmBuf) >= need {
				chunk := pcmBuf[:need]
				pcmBuf = pcmBuf[need:]
				if !sendChunk(chunk) {
					return false
				}
			}
			return true
		}

		for {
			select {
			case <-stopCh:
				return

			case <-tick.C:
				elapsed := time.Since(lastRateSample)
				if elapsed <= 0 {
					elapsed = time.Second
				}
				rateKBps := (float64(sentBytes) / 1024) / elapsed.Seconds()
				logs.LogV("[voice/udp] tx: %d pkts, %.2f kB/s", sentPkts, rateKBps)
				ui.UpdateTxAudioRate(rateKBps)
				sentPkts, sentBytes = 0, 0
				lastRateSample = time.Now()

			case frame, ok := <-audioMicIn:
				if !ok {
					if !drainAvailable() {
						return
					}
					return
				}

				if frame == nil {
					continue
				}

				if !mediactrl.CaptureEnabled() {
					pcmBuf = pcmBuf[:0]
					continue
				}

				pcmBuf = append(pcmBuf, frame...)

				if !drainAvailable() {
					return
				}
			}
		}
	}()
}

// startVideoSender: camera → TextFrame → UDP (frameVideo)
func startVideoSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr,
	videoCamIn <-chan device.CameraFrame,
	videoSelfScreenOut chan *codec.TextFrame, videoPeerScreenOut chan *codec.TextFrame,
) {
	_ = videoPeerScreenOut

	if !useVideo || videoCamIn == nil {
		return
	}

	// ======== Video sender: camera → textFrame → UDP ========
	previewEncoder, netEncoder, previewDecoder := initVideoCodec()
	wg.Add(1)
	go func() {
		defer wg.Done()

		var (
			sentPkts, sentBytes uint64

			frames                   uint64
			tEncodeLocal, tEncodeNet time.Duration
		)

		tick := time.NewTicker(time.Second)
		lastRateSample := time.Now()
		defer tick.Stop()

		var fpsLimiter *time.Ticker
		if maxVideoFPS > 0 {
			interval := time.Second / time.Duration(maxVideoFPS)
			if interval <= 0 {
				interval = time.Second / 30
			}
			fpsLimiter = time.NewTicker(interval)
			defer fpsLimiter.Stop()
		}

		for {
			select {
			case <-stopCh:
				return

			case <-tick.C:
				// network TX stats
				elapsed := time.Since(lastRateSample)
				if elapsed <= 0 {
					elapsed = time.Second
				}
				rateKBps := (float64(sentBytes) / 1024) / elapsed.Seconds()
				logs.LogV("[video/udp] tx: %d pkts, %.2f kB/s",
					sentPkts, rateKBps)
				ui.UpdateTxVideoRate(rateKBps)

				if frames > 0 {
					logs.LogV(
						"[video/prof] fps=%d encodeLocal=%.1fms encodeNet=%.1fms",
						frames,
						float64(tEncodeLocal/time.Millisecond)/float64(frames),
						float64(tEncodeNet/time.Millisecond)/float64(frames),
					)
				}

				sentPkts, sentBytes = 0, 0
				frames = 0
				tEncodeLocal, tEncodeNet = 0, 0
				lastRateSample = time.Now()

			case frame, ok := <-videoCamIn:
				if !ok {
					return
				}
				if frame.Data == nil {
					continue
				}
				if !mediactrl.CaptureEnabled() {
					continue
				}
				if fpsLimiter != nil {
					select {
					case <-fpsLimiter.C:
					case <-stopCh:
						return
					}
				}

				frames++

				rawFrame := codec.Frame{
					Data:   frame.Data,
					Width:  frame.Width,
					Height: frame.Height,
				}

				// ----- local preview (self view) -----
				if cols, rows, ok := ui.SelfSlot(); ok {
					start := time.Now()
					dataLocal, err := previewEncoder.Encode(rawFrame, cols, rows)
					tEncodeLocal += time.Since(start)

					if err != nil {
						log.Printf("[video/udp] Encode local: %v", err)
					} else if tv, err := previewDecoder.Decode(dataLocal); err != nil {
						log.Printf("[video/udp] Decode local: %v", err)
					} else if videoSelfScreenOut != nil {
						select {
						case videoSelfScreenOut <- tv:
						default:
							select {
							case <-videoSelfScreenOut:
								logs.LogV("[video/udp] drop local preview frame (videoLocalOut full, dropped oldest)")
							default:
							}
							select {
							case videoSelfScreenOut <- tv:
							default:
								logs.LogV("[video/udp] drop local preview frame (videoLocalOut full, dropped newest)")
							}
						}
					}
				}

				// ----- send encoded frame over the network -----
				if cols, rows, ok := ui.PeerSlot(); ok {
					addr := getPeer()
					if addr == nil {
						continue
					}

					start := time.Now()
					dataNet, err := netEncoder.Encode(rawFrame, cols, rows)
					tEncodeNet += time.Since(start)

					if err != nil {
						log.Printf("[video/udp] Encode net: %v", err)
						continue
					}

					out := make([]byte, len(dataNet)+1)
					out[0] = frameVideo
					copy(out[1:], dataNet)

					n, err := pc.WriteTo(out, addr)
					if err != nil {
						if ne, ok := err.(net.Error); ok && ne.Timeout() {
							log.Printf("[video/udp] send timeout: %v", err)
							continue
						}
						log.Printf("[video/udp] send: %v", err)
						if stop != nil {
							stop()
						}
						return
					}
					sentPkts++
					if n > 1 {
						sentBytes += uint64(n - 1) // exclude frame type marker
					}
				}
			}
		}
	}()
}

// startReceiver launches a goroutine that reads UDP packets, demuxes frame types, and forwards
// audio and video payloads to dedicated decoder goroutines.
func startReceiver(wg *sync.WaitGroup, pc net.PacketConn,
	stopCh <-chan struct{}, netAudioIn chan []byte, netVideoIn chan []byte,
	getPeer func() *net.UDPAddr, setPeer func(*net.UDPAddr)) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		buf := make([]byte, 65535)

		var (
			rxAudioPkts, rxAudioBytes uint64
			rxVideoPkts, rxVideoBytes uint64
			lastRateSample            = time.Now()
		)
		publishRxRates := func(force bool) {
			now := time.Now()
			elapsed := now.Sub(lastRateSample)
			if !force && elapsed < time.Second {
				return
			}
			if elapsed <= 0 {
				elapsed = time.Second
			}
			seconds := elapsed.Seconds()

			if useVoice {
				audioRate := (float64(rxAudioBytes) / 1024) / seconds
				logs.LogV("[voice/udp] rx: %d pkts, %.2f kB/s", rxAudioPkts, audioRate)
				ui.UpdateRxAudioRate(audioRate)
				rxAudioPkts, rxAudioBytes = 0, 0
			}
			if useVideo {
				videoRate := (float64(rxVideoBytes) / 1024) / seconds
				logs.LogV("[video/udp] rx: %d pkts, %.2f kB/s", rxVideoPkts, videoRate)
				ui.UpdateRxVideoRate(videoRate)
				rxVideoPkts, rxVideoBytes = 0, 0
			}
			lastRateSample = now
		}
		defer publishRxRates(true)

		var deadlineSetter interface {
			SetReadDeadline(time.Time) error
		}
		if ds, ok := pc.(interface {
			SetReadDeadline(time.Time) error
		}); ok {
			deadlineSetter = ds
		}

		for {
			select {
			case <-stopCh:
				return
			default:
			}

			publishRxRates(false)

			if deadlineSetter != nil {
				_ = deadlineSetter.SetReadDeadline(time.Now().Add(time.Second))
			}
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					log.Printf("[voice/udp] read timeout: %v", err)
					continue
				}
				log.Printf("[voice/udp] read error: %v", err)
				return
			}
			if ua, ok := from.(*net.UDPAddr); ok {
				cur := getPeer()
				if cur == nil {
					setPeer(ua)
				} else if !ua.IP.Equal(cur.IP) || ua.Port != cur.Port {
					// Ignore packets from unexpected peers.
					continue
				}
			}

			if n < 1 {
				continue
			}
			t := buf[0]
			payload := buf[1:n]

			switch t {
			case frameAudio:
				payloadLen := len(payload)
				if payloadLen <= audioSeqBytes {
					logs.LogV("[voice/udp] drop rx audio frame (payload too small)")
					continue
				}
				rxAudioPkts++
				rxAudioBytes += uint64(payloadLen - audioSeqBytes)
				if netAudioIn == nil {
					continue
				}
				// Copy payload because buf is reused on the next ReadFrom.
				pkt := make([]byte, payloadLen)
				copy(pkt, payload)
				select {
				case netAudioIn <- pkt:
				default:
					// Drop oldest audio packet if the queue is full to avoid blocking receiver.
					// Non-blocking drain one element if possible, then try again.
					select {
					case <-netAudioIn:
						logs.LogV("[voice/udp] drop rx audio frame (queue full, dropped oldest)")
					default:
						logs.LogV("[voice/udp] drop rx audio frame (queue full, could not drain oldest)")
					}
					select {
					case netAudioIn <- pkt:
					default:
						// If we still can't enqueue, drop the new packet as well.
						logs.LogV("[voice/udp] drop rx audio frame (queue full, dropped newest)")
					}
				}

			case frameVideo:
				payloadLen := len(payload)
				rxVideoPkts++
				rxVideoBytes += uint64(payloadLen)
				if netVideoIn == nil {
					continue
				}
				// Enqueue textFrame payload for asynchronous decode/render.
				// Copy because buf is reused on the next ReadFrom.
				pkt := make([]byte, payloadLen)
				copy(pkt, payload)
				select {
				case netVideoIn <- pkt:
				default:
					// Drop frame if video decoder is too slow; do not block audio.
					logs.LogV("[video/udp] drop rx video frame (queue full)")
				}

			default:
				continue
			}
		}
	}()
}

// startAudioReceiver launches a goroutine that decodes G.722 payloads from netAudioIn into PCM frames
// and feeds them into speakerOut.
func startAudioReceiver(wg *sync.WaitGroup, dec *g722.Decoder, frameSamples int,
	stopCh <-chan struct{}, netAudioIn <-chan []byte, speakerOut chan []int16) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		frameDuration := time.Duration(frameSamples) * time.Second / audioSampleRate
		if frameDuration <= 0 {
			frameDuration = 20 * time.Millisecond
		}
		ticker := time.NewTicker(frameDuration)
		defer ticker.Stop()

		buffer := make([][]int16, 0, audioBufferMaxFrames)
		bufferReady := false

		var (
			pendingSeq    uint16
			pendingData   []byte
			havePending   bool
			plcStreak     int
			lastGoodFrame []int16
			pcmScratch    = make([]int16, frameSamples)
		)

		resetState := func() {
			buffer = buffer[:0]
			bufferReady = false
			pendingData = nil
			havePending = false
			plcStreak = 0
			lastGoodFrame = nil
		}

		enqueueFrame := func(pcm []int16) {
			if len(buffer) >= audioBufferMaxFrames {
				buffer = buffer[1:]
				logs.LogV("[voice/udp] drop rx audio frame (buffer overflow)")
			}
			buffer = append(buffer, pcm)
			if !bufferReady && len(buffer) >= audioBufferLeadFrames {
				bufferReady = true
			}
		}

		popFrame := func() ([]int16, bool) {
			if len(buffer) == 0 {
				return nil, false
			}
			frame := buffer[0]
			buffer = buffer[1:]
			if bufferReady && len(buffer) < audioBufferResumeFrames {
				bufferReady = false
			}
			return frame, true
		}

		generatePLC := func() []int16 {
			if plcStreak >= audioMaxConsecutivePLCs {
				return nil
			}
			plcStreak++
			frame := make([]int16, frameSamples)
			if lastGoodFrame != nil {
				copy(frame, lastGoodFrame)
			}
			return frame
		}

		decodeAndEnqueue := func(data []byte) bool {
			if len(data) == 0 {
				return false
			}
			if len(pcmScratch) < frameSamples {
				pcmScratch = make([]int16, frameSamples)
			}
			written := dec.Decode(pcmScratch[:frameSamples], data)
			if written <= 0 {
				logs.LogV("[voice/udp] decode error: produced %d samples (len=%d)", written, len(data))
				return false
			}
			plcStreak = 0
			frame := make([]int16, frameSamples)
			toCopy := written
			if toCopy > frameSamples {
				toCopy = frameSamples
			}
			copy(frame, pcmScratch[:toCopy])
			if toCopy < frameSamples {
				for i := toCopy; i < frameSamples; i++ {
					frame[i] = 0
				}
			}
			if lastGoodFrame == nil {
				lastGoodFrame = make([]int16, frameSamples)
			}
			copy(lastGoodFrame, frame)
			enqueueFrame(frame)
			return true
		}

		flushPending := func() {
			if !havePending {
				return
			}
			if !decodeAndEnqueue(pendingData) {
				if frame := generatePLC(); frame != nil {
					enqueueFrame(frame)
				}
			}
			havePending = false
			pendingData = nil
		}

		feedSpeaker := func(frame []int16) {
			if frame == nil {
				return
			}
			select {
			case speakerOut <- frame:
				// normal path
			default:
				select {
				case <-speakerOut:
					logs.LogV("[voice/udp] drop local audio frame (speakerOut full, dropped oldest)")
				default:
				}

				select {
				case speakerOut <- frame:
				default:
					logs.LogV("[voice/udp] drop local audio frame (speakerOut full, dropped newest)")
				}
			}
		}

		for {
			select {
			case <-stopCh:
				flushPending()
				return
			case pkt, ok := <-netAudioIn:
				if !ok {
					flushPending()
					return
				}
				if pkt == nil {
					continue
				}
				if !mediactrl.PlaybackEnabled() {
					resetState()
					continue
				}
				if len(pkt) <= audioSeqBytes {
					continue
				}

				seq := binary.BigEndian.Uint16(pkt[:audioSeqBytes])
				payload := pkt[audioSeqBytes:]
				if len(payload) == 0 {
					continue
				}

				if !havePending {
					pendingSeq = seq
					pendingData = payload
					havePending = true
					continue
				}

				if !decodeAndEnqueue(pendingData) {
					if frame := generatePLC(); frame != nil {
						enqueueFrame(frame)
					}
				}

				gap := uint16(seq - pendingSeq)
				if gap == 0 {
					// duplicate packet; wait for the next unique sequence.
					continue
				}
				if gap > audioSeqStaleWindow {
					// stale/out-of-order packet, drop it.
					continue
				}
				if gap > audioMaxSeqGap {
					// large jump: resync decoder state.
					pendingSeq = seq
					pendingData = payload
					continue
				}
				if gap > 1 {
					missing := int(gap) - 1
					for i := 0; i < missing; i++ {
						if frame := generatePLC(); frame != nil {
							enqueueFrame(frame)
						}
					}
				}

				pendingSeq = seq
				pendingData = payload
				havePending = true

			case <-ticker.C:
				if !mediactrl.PlaybackEnabled() {
					resetState()
					continue
				}

				if !bufferReady {
					if len(buffer) < audioBufferLeadFrames {
						continue
					}
					bufferReady = true
				}

				frame, ok := popFrame()
				if !ok {
					if plc := generatePLC(); plc != nil {
						feedSpeaker(plc)
					}
					continue
				}

				feedSpeaker(frame)
			}
		}
	}()
}

// startVideoReceiver launches a goroutine that decodes textFrame video frames from netVideoIn
// and updates the remote preview independently of the audio receive loop.
func startVideoReceiver(wg *sync.WaitGroup,
	stopCh <-chan struct{}, netVideoIn <-chan []byte, screenOut chan *codec.TextFrame) {
	decoder := codec.NewDecoder()
	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			select {
			case <-stopCh:
				return
			case pkt, ok := <-netVideoIn:
				if !ok {
					return
				}
				if pkt == nil {
					continue
				}
				if !mediactrl.PlaybackEnabled() {
					continue
				}
				tv, err := decoder.Decode(pkt)
				if err != nil {
					log.Printf("[video/udp] textFrame decode: %v", err)
					continue
				}

				if screenOut == nil {
					continue
				}

				select {
				case screenOut <- tv:
				default:
					select {
					case <-screenOut:
						logs.LogV("[video/udp] drop remote frame (videoRemoteOut full, dropped oldest)")
					default:
					}
					select {
					case screenOut <- tv:
					default:
						logs.LogV("[video/udp] drop remote frame (videoRemoteOut full, dropped newest)")
					}
				}
			}
		}
	}()
}
