package udp

import (
	"context"
	"errors"
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
)

var (
	useVoice    = true
	useVideo    bool
	maxVideoFPS = defaultVideoFPS
	audioCodec  = "amrnb"
	echoCancel  = false
)

const (
	defaultVideoFPS       = 25
	minVideoFPS           = 5
	maxVideoFPSLimit      = 30
	mediaKindAudio   byte = 0
	mediaKindVideo   byte = 1

	// Datagram header is [kind:1 | timestamp high:7][timestamp low:8].
	mediaHeaderBytes                = 2 // packed kind and 15-bit timestamp
	mediaIndexBytes                 = 2 // timestamp bytes in internal audio/video queues
	mediaIndexMask           uint16 = 0x7fff
	mediaIndexHalf           uint16 = 0x4000
	mediaTickDuration               = 4 * time.Millisecond
	mediaTickSamples                = 32                        // 4 ms at the 8 kHz audio clock
	audioFrameTicks                 = 5                         // 20 ms audio frame on a 4 ms common media timeline
	audioBufferLeadFrames           = 12                        // initial jitter buffer lead: 240 ms; adapts between the two limits below
	audioLeadMinFrames              = audioPlayoutLowFrames + 2 // 120 ms above nothing: one device period plus 40 ms
	audioLeadMaxFrames              = 32                        // 640 ms, the fixed lead of v0.1.3
	audioLeadGrowFrames             = 2                         // per packet that arrived after its slot
	audioLeadShrinkQuiet            = 10 * time.Second          // no late packets for this long…
	audioLeadShrinkEvery            = 5 * time.Second           // …shrinks the lead by one frame this often
	audioQuietRMS                   = 300                       // frames below this RMS count as silence when shrinking the lead
	audioBufferMaxFrames            = 128                       // queue capacity: 2.56 s
	audioMaxConsecutivePLCs         = 32                        // tolerate up to 640 ms of missing media before resync
	audioSpeakerPeriodFrames        = 4                         // 80 ms device period, matching the clean v0.1.3 playback configuration
	audioPlayoutLowFrames           = audioSpeakerPeriodFrames  // conceal once less than one device period is queued
	audioCaptureResync              = 60 * time.Millisecond     // capture timeline gap that means lost samples
	audioDTXUpdateFrames            = 8                         // AMR-NB sends a SID update at least every 8 frames in DTX
	audioQueueMaxFrames             = 64                        // 1.28 s of queued playback triggers a trim…
	audioQueueTargetFrames          = audioBufferLeadFrames     // …down to the jitter buffer lead
	videoBaseStaleThreshold         = 250 * time.Millisecond
	videoMaxStaleThreshold          = 3 * time.Second
	videoMaxQueuedFrames            = 96                     // ~3 s at 30 fps
	videoAudioClockStall            = 200 * time.Millisecond // audio clock considered stopped
	videoMaxLeadTicks               = int32(videoMaxStaleThreshold / mediaTickDuration)
)

// Configure toggles voice/video capture for future UDP sessions.
func Configure(enableVoice, enableVideo bool) {
	useVoice = enableVoice
	useVideo = enableVideo
}

// SetEchoCancellation enables or disables acoustic echo cancellation for
// future voice sessions.
func SetEchoCancellation(enabled bool) {
	echoCancel = enabled
}

// SetAudioCodec selects the codec used by future voice sessions.
func SetAudioCodec(name string) error {
	switch name {
	case "opus", "amrnb", "g722":
		audioCodec = name
		return nil
	default:
		return fmt.Errorf("unsupported audio codec %q (choose opus, amrnb or g722)", name)
	}
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

// maxMediaDatagram is the largest media datagram we send. Every frame within
// codec.MaxFrameCells fits, with room below the 65507-byte IPv4 UDP limit.
const maxMediaDatagram = 60000

// isFatalSendError reports whether a UDP send error means the socket is gone.
// Everything else (timeouts, ICMP-triggered "connection refused", "no buffer
// space", "message too long") only loses that one packet.
func isFatalSendError(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

// sendErrorLog rate-limits logging of non-fatal send errors, which can repeat
// for every packet while the peer is unreachable.
type sendErrorLog struct {
	mu      sync.Mutex
	last    time.Time
	dropped int
}

var sendErrLog sendErrorLog

func (l *sendErrorLog) report(kind string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.last) < time.Second {
		l.dropped++
		return
	}
	log.Printf("[%s/udp] send failed (packet dropped, %d more since last report): %v", kind, l.dropped, err)
	l.last = time.Now()
	l.dropped = 0
}

// mediaIndex maps local monotonic capture time onto the common media timeline.
// It wraps at 15 bits; modular deltas are unambiguous for differences shorter
// than half the 131.072 second cycle.
func mediaIndex(epoch, capturedAt time.Time) uint16 {
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	elapsed := capturedAt.Sub(epoch)
	if elapsed <= 0 {
		return 0
	}
	return uint16(elapsed/mediaTickDuration) & mediaIndexMask
}

func encodeMediaHeader(kind byte, index uint16) [mediaHeaderBytes]byte {
	return [mediaHeaderBytes]byte{
		(kind&1)<<7 | byte((index&mediaIndexMask)>>8),
		byte(index),
	}
}

func decodeMediaHeader(header []byte) (kind byte, index uint16, ok bool) {
	if len(header) < mediaHeaderBytes {
		return 0, 0, false
	}
	kind = header[0] >> 7
	index = (uint16(header[0]&0x7f)<<8 | uint16(header[1])) & mediaIndexMask
	return kind, index, true
}

func mediaIndexDelta(newer, older uint16) int32 {
	delta := (newer - older) & mediaIndexMask
	if delta >= mediaIndexHalf {
		return int32(delta) - int32(mediaIndexMask+1)
	}
	return int32(delta)
}

func advanceMediaIndex(index uint16, ticks int32) uint16 {
	return uint16((int32(index) + ticks) & int32(mediaIndexMask))
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

// StartSession establishes a unidirectional or bidirectional UDP voice channel using the
// selected speech codec
// for audio and malgo for playback. If a camera stream is active and enableVideo is true,
// TextFrame-encoded video frames are also sent over the same PacketConn, prefixed with a frame
// type byte.
func StartSession(pc net.PacketConn, remote *net.UDPAddr) (*dataSession, error) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
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
		enc          audioEncoder
		dec          audioDecoder
		aec          *audioEcho
		frameSamples int
		mediaEpoch   = time.Now()

		audioMicIn <-chan device.AudioCaptureFrame

		audioNetIn      chan []byte
		audioSpeakerOut *device.PlaybackQueue

		speaker *device.Speaker

		videoCamIn         <-chan device.CameraFrame
		videoNetIn         chan []byte
		videoSelfScreenOut chan *codec.TextFrame
		videoPeerScreenOut chan *codec.TextFrame
	)

	// Install the stop callback before starting either audio device.
	stop = func() {
		stopOnce.Do(func() {
			close(stopCh)
			cancelSession()
		})
	}
	cleanup := func() {
		cleanupOnce.Do(func() {
			stop()
			if speaker != nil {
				speaker.Close()
			}
			// Cancellation is asynchronous. Do not let the next call open devices
			// while the previous capture goroutines are still closing them.
			if audioMicIn != nil {
				for range audioMicIn {
				}
			}
			if videoCamIn != nil {
				for range videoCamIn {
				}
			}
			ui.ClearLocalFrame()
			ui.ClearRemoteFrame()
		})
	}
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()

	// A cold camera start can take over a second. Complete it before starting
	// audio: otherwise microphone samples and AEC reference accumulate while
	// there is no sender to consume them, making the first call unlike a reconnect.
	if useVideo {
		camCtx, cancel := context.WithCancel(sessionCtx)
		var err error
		videoCamIn, err = device.StartCameraStream(camCtx)
		if err != nil {
			cancel()
			log.Printf("[video] camera stream disabled: %v", err)
		}
		videoSelfScreenOut = make(chan *codec.TextFrame, 1)
		videoPeerScreenOut = make(chan *codec.TextFrame, 1)
		videoNetIn = make(chan []byte, 16) // Yggdrasil delivers bursts; keep them for the decoder
		ui.EnsureRenderer(context.Background())
	}

	if useVoice {
		// Init codec
		var err error
		enc, dec, frameSamples, err = initAudioCodec(audioCodec)
		if err != nil {
			return nil, err
		}
		if echoCancel {
			aec, err = newAudioEcho(frameSamples)
			if err != nil {
				return nil, fmt.Errorf("echo canceller: %w", err)
			}
		} else {
			log.Printf("[audio/aec] echo cancellation disabled")
		}

		audioSpeakerOut = device.NewPlaybackQueue(audioBufferMaxFrames)
		audioNetIn = make(chan []byte, audioBufferMaxFrames)

		var onRender func([]int16)
		if aec != nil {
			onRender = aec.playback
		}
		speaker, err = device.StartSpeaker(audioSampleRate, audioChannels, frameSamples*audioSpeakerPeriodFrames, mediaTickSamples, audioSpeakerOut, stopCh, onRender, stop)
		if err != nil {
			return nil, fmt.Errorf("speaker: %w", err)
		}
		if aec != nil {
			// Echo of rendered audio reaches the mic about one device buffer later.
			delay := min(speaker.LatencySamples()-audioSampleRate*audioEchoDelayMarginMs/1000, audioEchoMaxDelaySamples)
			aec.setReferenceDelay(delay)
			log.Printf("[audio/aec] reference delay %d ms (speaker buffer %d ms)", delay*1000/audioSampleRate, speaker.LatencySamples()*1000/audioSampleRate)
		}

		// Open capture last, immediately before starting its consumer.
		audioMicIn, err = device.StartMicrophoneStream(sessionCtx, audioSampleRate, audioChannels)
		if err != nil {
			return nil, fmt.Errorf("mic stream: %w", err)
		}
	}
	if err := sessionCtx.Err(); err != nil {
		return nil, fmt.Errorf("media stopped during startup: %w", err)
	}

	startSender(&wg, pc, stop, stopCh, getPeer, enc, aec, frameSamples, mediaEpoch, audioMicIn, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)

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
		var playbackIndex func() (uint16, bool)
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

	started = true
	return &dataSession{stop: stopFunc, done: done}, nil
}

// startSender launches goroutines responsible for sending:
//   - audio: mic → selected speech codec → UDP (mediaKindAudio)
//   - video: camera → TextFrame → UDP (mediaKindVideo)
func startSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc audioEncoder, aec *audioEcho, frameSamples int,
	mediaEpoch time.Time, audioMicIn <-chan device.AudioCaptureFrame, videoCamIn <-chan device.CameraFrame,
	videoSelfScreenOut chan *codec.TextFrame, videoPeerScreenOut chan *codec.TextFrame,
) {
	if useVoice {
		startAudioSender(wg, pc, stop, stopCh, getPeer, enc, aec, frameSamples, mediaEpoch, audioMicIn)
	}
	if useVideo {
		startVideoSender(wg, pc, stop, stopCh, getPeer, mediaEpoch, videoCamIn, videoSelfScreenOut, videoPeerScreenOut)
	}
}

// startAudioSender: mic → selected speech codec → UDP (mediaKindAudio)
func startAudioSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc audioEncoder, aec *audioEcho,
	frameSamples int, mediaEpoch time.Time, audioMicIn <-chan device.AudioCaptureFrame,
) {
	if !useVoice || enc == nil || audioMicIn == nil {
		return
	}

	// ======== Audio sender: mic → selected codec → UDP ========
	wg.Add(1)
	go func() {
		defer wg.Done()

		var (
			sentPkts, sentBytes uint64
		)
		var pcmBuf []int16       // post-processed PCM ready for encoding
		var pcmStartAt time.Time // capture time of pcmBuf[0], or of the next sample when empty
		var timelineValid bool

		tick := time.NewTicker(time.Second)
		lastRateSample := time.Now()
		defer tick.Stop()

		need := frameSamples * audioChannels
		aecSamples := make([]int16, need)

		sendChunk := func(chunk []int16, capturedAt time.Time) bool {
			pcm := chunk
			if aec != nil {
				if err := aec.capture(chunk, aecSamples); err != nil {
					log.Printf("[audio/aec] capture frame: %v", err)
				} else {
					pcm = aecSamples
				}
			}

			addr := getPeer()
			if addr == nil {
				return true
			}

			if len(chunk) == 0 {
				return true
			}

			buf := make([]byte, maxAudioPacket)
			written, err := enc.EncodeFrame(pcm, buf)
			if err != nil {
				log.Printf("[voice/udp] %s encode error: bytes=%d err=%v", audioCodec, written, err)
				if stop != nil {
					stop()
				}
				return false
			}
			if written == 0 {
				// AMR-NB DTX omits NO_DATA frames from the wire while the timestamp
				// still advances with the captured PCM timeline.
				return true
			}

			header := encodeMediaHeader(mediaKindAudio, mediaIndex(mediaEpoch, capturedAt))
			out := make([]byte, mediaHeaderBytes+written)
			copy(out[:mediaHeaderBytes], header[:])
			copy(out[mediaHeaderBytes:], buf[:written])

			n, err := pc.WriteTo(out, addr)
			if err != nil {
				if !isFatalSendError(err) {
					sendErrLog.report("voice", err)
					return true
				}
				log.Printf("[voice/udp] write error: %v", err)
				if stop != nil {
					stop()
				}
				return false
			}
			sentPkts++
			if n > mediaHeaderBytes {
				sentBytes += uint64(n - mediaHeaderBytes) // exclude packed media header
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
					if aec != nil {
						aec.setCaptureEnabled(false)
					}
					pcmBuf = pcmBuf[:0]
					timelineValid = false
					continue
				}
				if aec != nil {
					aec.setCaptureEnabled(true)
				}

				// The timeline follows the sample count, also across frames that
				// drained pcmBuf completely (drainAvailable advances pcmStartAt).
				// Callback times jitter by several milliseconds, which is not a
				// gap; re-reading the clock for every block made packet timestamps
				// jump by a tick or two. Resynchronize only when samples were
				// really lost, e.g. capture blocks dropped while we were late.
				expected := pcmStartAt.Add(time.Duration(len(pcmBuf)) * time.Second / time.Duration(audioSampleRate))
				if delta := capture.CapturedAt.Sub(expected); !timelineValid || delta > audioCaptureResync || delta < -audioCaptureResync {
					pcmBuf = pcmBuf[:0]
					pcmStartAt = capture.CapturedAt
					timelineValid = true
				}
				pcmBuf = append(pcmBuf, capture.Samples...)

				if !drainAvailable() {
					return
				}
			}
		}
	}()
}

// startVideoSender: camera → TextFrame → UDP (mediaKindVideo)
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

					if mediaHeaderBytes+len(dataNet) > maxMediaDatagram {
						// Cannot happen within codec.MaxFrameCells; never send an
						// oversized datagram that the network would drop or fragment.
						log.Printf("[video/udp] drop frame: %d bytes exceeds datagram limit %d", mediaHeaderBytes+len(dataNet), maxMediaDatagram)
						continue
					}
					header := encodeMediaHeader(mediaKindVideo, mediaIndex(mediaEpoch, frame.CapturedAt))
					out := make([]byte, mediaHeaderBytes+len(dataNet))
					copy(out[:mediaHeaderBytes], header[:])
					copy(out[mediaHeaderBytes:], dataNet)

					n, err := pc.WriteTo(out, addr)
					if err != nil {
						// A lost video frame is harmless; only a closed socket ends the session.
						if !isFatalSendError(err) {
							sendErrLog.report("video", err)
							continue
						}
						log.Printf("[video/udp] send: %v", err)
						if stop != nil {
							stop()
						}
						return
					}
					sentPkts++
					if n > mediaHeaderBytes {
						sentBytes += uint64(n - mediaHeaderBytes) // exclude packed media header
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

			if n < mediaHeaderBytes+1 {
				continue
			}
			t, index, ok := decodeMediaHeader(buf[:mediaHeaderBytes])
			if !ok {
				continue
			}
			payload := buf[mediaHeaderBytes:n]

			switch t {
			case mediaKindAudio:
				rxAudioPkts++
				rxAudioBytes += uint64(len(payload))
				if netAudioIn == nil {
					continue
				}
				// Preserve the media index for the audio decoder; buf is reused on next ReadFrom.
				pkt := make([]byte, mediaIndexBytes+len(payload))
				pkt[0], pkt[1] = byte(index>>8), byte(index)
				copy(pkt[mediaIndexBytes:], payload)
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

			case mediaKindVideo:
				rxVideoPkts++
				rxVideoBytes += uint64(len(payload))
				if netVideoIn == nil {
					continue
				}
				// Preserve the media index for the video scheduler; buf is reused on next ReadFrom.
				pkt := make([]byte, mediaIndexBytes+len(payload))
				pkt[0], pkt[1] = byte(index>>8), byte(index)
				copy(pkt[mediaIndexBytes:], payload)
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

// audioPacketsExpected returns how many packets the sender transmitted in a
// timestamp gap of delta ticks, counting the packet that ends the gap. After a
// SID frame the sender is in DTX and only transmits a SID update every
// audioDTXUpdateFrames frames, so suppressed silence is not loss; only a missed
// update is.
func audioPacketsExpected(delta int32, afterSID bool) int32 {
	frames := (delta + audioFrameTicks/2) / audioFrameTicks
	if frames < 1 {
		frames = 1
	}
	if afterSID {
		return 1 + (frames-1)/audioDTXUpdateFrames
	}
	return frames
}

// startAudioReceiver launches a goroutine that decodes speech payloads from
// netAudioIn and keeps speakerOut continuously fed (see audioPlayout).
func startAudioReceiver(wg *sync.WaitGroup, dec audioDecoder, frameSamples int,
	stopCh <-chan struct{}, netAudioIn <-chan []byte, speakerOut *device.PlaybackQueue) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ui.UpdateRxAudioLoss(0)
		playout := newAudioPlayout(dec, frameSamples, speakerOut)
		// Well below one 20 ms frame, so a slot is concealed before the
		// speaker's queue empties.
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				if mediactrl.PlaybackEnabled() {
					playout.onTick()
				}
			case pkt, ok := <-netAudioIn:
				if !ok {
					return
				}
				if !mediactrl.PlaybackEnabled() {
					playout.reset()
					ui.UpdateRxAudioLoss(0)
					continue
				}
				if len(pkt) <= mediaIndexBytes {
					continue
				}
				index := uint16(pkt[0])<<8 | uint16(pkt[1])
				playout.onPacket(index, pkt[mediaIndexBytes:], time.Now())
			}
		}
	}()
}

// startVideoReceiver launches a goroutine that decodes textFrame video frames from netVideoIn
// and updates the remote preview independently of the audio receive loop.
func startVideoReceiver(wg *sync.WaitGroup,
	stopCh <-chan struct{}, netVideoIn <-chan []byte, screenOut chan *codec.TextFrame,
	playbackIndex func() (uint16, bool)) {
	decoder := codec.NewDecoder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		type timedFrame struct {
			index uint16
			frame *codec.TextFrame
		}
		queue := make([]timedFrame, 0, videoMaxQueuedFrames)
		lastFrameAt := time.Time{}
		var (
			playbackSeen     bool
			lastPlayback     uint16
			lastPlaybackMove time.Time
		)
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
			now := time.Now()
			if ok && (!playbackSeen || current != lastPlayback) {
				playbackSeen = true
				lastPlayback = current
				lastPlaybackMove = now
			}
			// Without a running audio clock (peer mic muted, DTX silence,
			// speaker underrun) there is nothing to sync to: show the newest
			// frame instead of holding video until audio resumes.
			if !ok || now.Sub(lastPlaybackMove) > videoAudioClockStall ||
				mediaIndexDelta(queue[0].index, current) > videoMaxLeadTicks {
				display(queue[len(queue)-1].frame)
				queue = queue[:0]
				return
			}
			selected := -1
			for i := range queue {
				if mediaIndexDelta(current, queue[i].index) >= 0 {
					selected = i
				}
			}
			if selected < 0 {
				return
			}
			candidate := queue[selected]
			queue = append(queue[:0], queue[selected+1:]...)
			// Allow for network transit delay reported by the TCP ping, while
			// still dropping frames that are too old to be useful for sync.
			staleLimit := videoBaseStaleThreshold + ui.PeerLatencySnapshot()
			if staleLimit > videoMaxStaleThreshold {
				staleLimit = videoMaxStaleThreshold
			}
			staleTicks := int32((staleLimit + mediaTickDuration - 1) / mediaTickDuration)
			if mediaIndexDelta(current, candidate.index) <= staleTicks {
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
				index := uint16(pkt[0])<<8 | uint16(pkt[1])
				if !lastFrameAt.IsZero() && time.Since(lastFrameAt) >= time.Duration(mediaIndexHalf)*mediaTickDuration {
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
					return mediaIndexDelta(queue[i].index, queue[j].index) < 0
				})
				// Frames wait here for the audio playback clock, which runs behind
				// arrival by the jitter buffer plus device latency, so the queue
				// must hold more than that span of frames.
				if len(queue) > videoMaxQueuedFrames {
					queue = queue[len(queue)-videoMaxQueuedFrames:]
					logs.LogV("[video/udp] drop stale queued frame")
				}
			}
		}
	}()
}
