package udp

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"github.com/svanichkin/say/ui"

	g722 "github.com/gotranspile/g722"
)

var (
	useVoice    = true
	useVideo    bool
	maxVideoFPS = defaultVideoFPS
)

const (
	defaultVideoFPS       = 25
	minVideoFPS           = 5
	maxVideoFPSLimit      = 30
	frameAudio       byte = 0x01
	frameVideo       byte = 0x02

	// UDP media datagrams use [kind][uint8 media index][encoded payload].
	mediaIndexBytes         = 1
	mediaTickDuration       = 4 * time.Millisecond
	mediaTickSamples        = 32 // 4 ms at the 8 kHz audio clock
	audioFrameTicks         = 5  // 20 ms audio frame on a 4 ms common media timeline
	audioBufferLeadFrames   = 4  // 80 ms startup jitter buffer
	audioBufferMaxFrames    = 8  // 160 ms cap
	audioMaxConsecutivePLCs = 8  // safety cap to avoid infinite PLC loops
)

// Configure toggles voice/video capture for future UDP sessions.
func Configure(enableVoice, enableVideo bool) {
	useVoice = enableVoice
	useVideo = enableVideo
}

// SetMaxVideoFPS clamps and sets the desired max FPS for video capture/encoding.
func SetMaxVideoFPS(fps int) {
	if fps < minVideoFPS {
		fps = minVideoFPS
	}
	if fps > maxVideoFPSLimit {
		fps = maxVideoFPSLimit
	}
	maxVideoFPS = fps
}

// mediaIndex maps local monotonic capture time onto the compact common media
// timeline. It wraps at uint8; signed modular deltas are unambiguous for
// differences shorter than 512 ms.
func mediaIndex(epoch, capturedAt time.Time) uint8 {
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	elapsed := capturedAt.Sub(epoch)
	if elapsed <= 0 {
		return 0
	}
	return uint8(elapsed / mediaTickDuration)
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
		mediaEpoch   = time.Now()

		micCancel context.CancelFunc = func() {}

		audioMicIn <-chan device.AudioCaptureFrame

		audioNetIn      chan []byte
		audioSpeakerOut chan device.PlaybackFrame

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

		audioSpeakerOut = make(chan device.PlaybackFrame, 4)
		audioNetIn = make(chan []byte, 4)

		speaker, err = device.StartSpeaker(audioSampleRate, audioChannels, frameSamples, mediaTickSamples, audioSpeakerOut, stopCh, stop)
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

		videoSelfScreenOut = make(chan *codec.TextFrame, 1)
		videoPeerScreenOut = make(chan *codec.TextFrame, 1)
		videoNetIn = make(chan []byte, 1)
	}

	if useVideo {
		ui.EnsureRenderer(context.Background())
	}

	startSender(&wg, pc, stop, stopCh, getPeer, enc, frameSamples, mediaEpoch, audioMicIn, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)

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
					if ui.SetLocalFrame(tv) {
						ui.RequestRedraw()
					}

				case tv := <-videoPeerScreenOut:
					if tv == nil {
						continue
					}
					if ui.SetRemoteFrame(tv) {
						ui.RequestRedraw()
					}
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
		var playbackIndex func() (uint8, bool)
		if speaker != nil {
			playbackIndex = speaker.PlaybackIndex
		}
		startVideoReceiver(&wg, stopCh, videoNetIn, videoPeerScreenOut, playbackIndex)
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
	mediaEpoch time.Time, audioMicIn <-chan device.AudioCaptureFrame, videoCamIn <-chan device.CameraFrame,
	videoSelfScreenOut chan *codec.TextFrame, videoPeerScreenOut chan *codec.TextFrame,
) {
	if useVoice {
		startAudioSender(wg, pc, stop, stopCh, getPeer, enc, frameSamples, mediaEpoch, audioMicIn)
	}
	if useVideo {
		startVideoSender(wg, pc, stop, stopCh, getPeer, mediaEpoch, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)
	}
}

// startAudioSender: mic → G.722 → UDP (frameAudio)
func startAudioSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc *g722.Encoder,
	frameSamples int, mediaEpoch time.Time, audioMicIn <-chan device.AudioCaptureFrame,
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
		)
		var pcmBuf []int16 // post-processed PCM ready for encoding
		var pcmStartAt time.Time

		tick := time.NewTicker(time.Second)
		lastRateSample := time.Now()
		defer tick.Stop()

		need := frameSamples * audioChannels

		sendChunk := func(chunk []int16, capturedAt time.Time) bool {
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

			out := make([]byte, written+1+mediaIndexBytes)
			out[0] = frameAudio
			out[1] = mediaIndex(mediaEpoch, capturedAt)
			copy(out[1+mediaIndexBytes:], buf[:written])

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
			if n > 1+mediaIndexBytes {
				sentBytes += uint64(n - 1 - mediaIndexBytes) // exclude frame type + media index
			}
			return true
		}

		drainAvailable := func() bool {
			for len(pcmBuf) >= need {
				chunk := pcmBuf[:need]
				pcmBuf = pcmBuf[need:]
				if !sendChunk(chunk, pcmStartAt) {
					return false
				}
				pcmStartAt = pcmStartAt.Add(time.Duration(need) * time.Second / time.Duration(audioSampleRate))
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

			case capture, ok := <-audioMicIn:
				if !ok {
					if !drainAvailable() {
						return
					}
					return
				}

				if len(capture.Samples) == 0 {
					continue
				}

				if !mediactrl.CaptureEnabled() {
					pcmBuf = pcmBuf[:0]
					continue
				}

				if len(pcmBuf) == 0 {
					pcmStartAt = capture.CapturedAt
				} else {
					expected := pcmStartAt.Add(time.Duration(len(pcmBuf)) * time.Second / time.Duration(audioSampleRate))
					if delta := capture.CapturedAt.Sub(expected); delta > 4*time.Millisecond || delta < -4*time.Millisecond {
						// Do not concatenate samples across a capture gap. The next packet's
						// media index carries the discontinuity to the receiver.
						pcmBuf = pcmBuf[:0]
						pcmStartAt = capture.CapturedAt
					}
				}
				pcmBuf = append(pcmBuf, capture.Samples...)

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
	mediaEpoch time.Time, videoCamIn <-chan device.CameraFrame,
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

					out := make([]byte, len(dataNet)+1+mediaIndexBytes)
					out[0] = frameVideo
					out[1] = mediaIndex(mediaEpoch, frame.CapturedAt)
					copy(out[1+mediaIndexBytes:], dataNet)

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
				if payloadLen <= mediaIndexBytes {
					logs.LogV("[voice/udp] drop rx audio frame (payload too small)")
					continue
				}
				rxAudioPkts++
				rxAudioBytes += uint64(payloadLen - mediaIndexBytes)
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
				if payloadLen <= mediaIndexBytes {
					continue
				}
				rxVideoPkts++
				rxVideoBytes += uint64(payloadLen - mediaIndexBytes)
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
					// Keep the newest frames so decode backlog cannot turn into latency.
					select {
					case <-netVideoIn:
					default:
					}
					select {
					case netVideoIn <- pkt:
					default:
						logs.LogV("[video/udp] drop rx video frame (queue full)")
					}
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
	stopCh <-chan struct{}, netAudioIn <-chan []byte, speakerOut chan device.PlaybackFrame) {
	wg.Add(1)
	go func() {
		defer wg.Done()

		buffer := make([]device.PlaybackFrame, 0, audioBufferMaxFrames)
		bufferReady := false
		bufferedSamples := 0

		var (
			pendingIndex  uint8
			pendingData   []byte
			havePending   bool
			plcStreak     int
			lastGoodFrame []int16
			pcmScratch    = make([]int16, frameSamples)
			lastPacketAt  time.Time
		)

		clearSpeakerQueue := func() {
			for {
				select {
				case <-speakerOut:
				default:
					return
				}
			}
		}

		resetState := func() {
			buffer = buffer[:0]
			bufferReady = false
			bufferedSamples = 0
			clearSpeakerQueue()
			pendingData = nil
			havePending = false
			plcStreak = 0
			lastGoodFrame = nil
		}

		enqueueFrame := func(pcm []int16, index uint8) {
			if len(buffer) >= audioBufferMaxFrames {
				bufferedSamples -= len(buffer[0].Samples)
				buffer = buffer[1:]
				logs.LogV("[voice/udp] drop rx audio frame (buffer overflow)")
			}
			buffer = append(buffer, device.PlaybackFrame{Samples: pcm, MediaIndex: index})
			bufferedSamples += len(pcm)
			if !bufferReady && bufferedSamples >= frameSamples*audioBufferLeadFrames {
				bufferReady = true
			}
		}

		popFrame := func() (device.PlaybackFrame, bool) {
			if len(buffer) == 0 {
				return device.PlaybackFrame{}, false
			}
			frame := buffer[0]
			buffer = buffer[1:]
			bufferedSamples -= len(frame.Samples)
			return frame, true
		}

		generatePLC := func(sampleCount int) []int16 {
			if plcStreak >= audioMaxConsecutivePLCs {
				return nil
			}
			plcStreak++
			frame := make([]int16, sampleCount)
			if lastGoodFrame != nil {
				for offset := 0; offset < len(frame); {
					offset += copy(frame[offset:], lastGoodFrame)
				}
			}
			return frame
		}

		decodeAndEnqueue := func(data []byte, index uint8) bool {
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
			enqueueFrame(frame, index)
			return true
		}

		flushPending := func() {
			if !havePending {
				return
			}
			if !decodeAndEnqueue(pendingData, pendingIndex) {
				if frame := generatePLC(frameSamples); frame != nil {
					enqueueFrame(frame, pendingIndex)
				}
			}
			havePending = false
			pendingData = nil
		}

		feedSpeaker := func(frame device.PlaybackFrame) {
			if frame.Samples == nil {
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
		feedBuffered := func() {
			if !bufferReady {
				return
			}
			for len(buffer) > 0 {
				frame, ok := popFrame()
				if !ok {
					return
				}
				feedSpeaker(frame)
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
				if len(pkt) <= mediaIndexBytes {
					continue
				}

				index := pkt[0]
				payload := pkt[mediaIndexBytes:]
				if len(payload) == 0 {
					continue
				}

				if !havePending {
					pendingIndex = index
					pendingData = payload
					havePending = true
					lastPacketAt = time.Now()
					continue
				}
				if time.Since(lastPacketAt) > 500*time.Millisecond {
					// The byte index is ambiguous after half a cycle without media.
					resetState()
					pendingIndex = index
					pendingData = payload
					havePending = true
					lastPacketAt = time.Now()
					continue
				}

				delta := index - pendingIndex
				if delta == 0 {
					// Duplicate or reordered packet. Keep waiting for a frame after
					// the current one before advancing the decoder.
					continue
				}
				if int8(delta) < 0 {
					continue
				}

				if !decodeAndEnqueue(pendingData, pendingIndex) {
					if frame := generatePLC(frameSamples); frame != nil {
						enqueueFrame(frame, pendingIndex)
					}
				}

				missingTicks := int(delta) - audioFrameTicks
				if missingTicks > audioMaxConsecutivePLCs*audioFrameTicks {
					// A long loss burst is better handled by jumping to the live edge
					// than by playing stale audio after a long PLC run.
					buffer = buffer[:0]
					bufferReady = false
					bufferedSamples = 0
					clearSpeakerQueue()
					pendingIndex = index
					pendingData = payload
					lastPacketAt = time.Now()
					continue
				}
				if missingTicks > 0 {
					if frame := generatePLC(missingTicks * mediaTickSamples); frame != nil {
						enqueueFrame(frame, pendingIndex+uint8(audioFrameTicks))
					}
				}

				pendingIndex = index
				pendingData = payload
				havePending = true
				lastPacketAt = time.Now()
				feedBuffered()
			}
		}
	}()
}

// startVideoReceiver launches a goroutine that decodes textFrame video frames from netVideoIn
// and updates the remote preview independently of the audio receive loop.
func startVideoReceiver(wg *sync.WaitGroup,
	stopCh <-chan struct{}, netVideoIn <-chan []byte, screenOut chan *codec.TextFrame,
	playbackIndex func() (uint8, bool)) {
	decoder := codec.NewDecoder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		type timedFrame struct {
			index uint8
			frame *codec.TextFrame
		}
		queue := make([]timedFrame, 0, 2)
		lastFrameAt := time.Time{}
		ticker := time.NewTicker(4 * time.Millisecond)
		defer ticker.Stop()

		display := func(frame *codec.TextFrame) {
			if frame == nil || screenOut == nil {
				return
			}
			select {
			case screenOut <- frame:
			default:
				select {
				case <-screenOut:
				default:
				}
				select {
				case screenOut <- frame:
				default:
				}
			}
		}

		presentDue := func() {
			if !mediactrl.PlaybackEnabled() {
				queue = queue[:0]
				return
			}
			if len(queue) == 0 {
				return
			}
			if playbackIndex == nil {
				display(queue[len(queue)-1].frame)
				queue = queue[:0]
				return
			}
			current, ok := playbackIndex()
			if !ok {
				return
			}
			selected := -1
			for i := range queue {
				if int8(current-queue[i].index) >= 0 {
					selected = i
				}
			}
			if selected < 0 {
				return
			}
			candidate := queue[selected]
			queue = append(queue[:0], queue[selected+1:]...)
			// A frame that missed the audio playhead by more than 250 ms is stale.
			if int8(current-candidate.index) <= 62 {
				display(candidate.frame)
			}
		}

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				presentDue()
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
				if len(pkt) <= mediaIndexBytes {
					continue
				}
				index := pkt[0]
				if !lastFrameAt.IsZero() && time.Since(lastFrameAt) > 500*time.Millisecond {
					queue = queue[:0]
				}
				lastFrameAt = time.Now()
				tv, err := decoder.Decode(pkt[mediaIndexBytes:])
				if err != nil {
					log.Printf("[video/udp] textFrame decode: %v", err)
					continue
				}

				queue = append(queue, timedFrame{index: index, frame: tv})
				sort.Slice(queue, func(i, j int) bool {
					return int8(queue[i].index-queue[j].index) < 0
				})
				if len(queue) > 2 {
					queue = queue[len(queue)-2:]
					logs.LogV("[video/udp] drop stale queued frame")
				}
			}
		}
	}()
}
