package udp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"sync/atomic"
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
	maxVideoFPS = 0 // optional -fps cap; 0: no cap, the rate adapts to loss
	audioCodec  = "amrnb"
)

const (
	minVideoFPS           = 5
	videoFPSUncapped      = 60 // limiter rate without -fps; cameras deliver at most ~30
	mediaKindAudio   byte = 0
	mediaKindVideo   byte = 1

	// Datagram header is [kind:1 | timestamp high:7][timestamp low:8].
	mediaHeaderBytes               = 2 // packed kind and 15-bit timestamp
	mediaIndexBytes                = 2 // timestamp bytes in internal audio/video queues
	mediaIndexMask          uint16 = 0x7fff
	mediaIndexHalf          uint16 = 0x4000
	mediaTickDuration              = 4 * time.Millisecond
	mediaTickSamples               = 32  // 4 ms at the 8 kHz audio clock
	audioFrameTicks                = 5   // 20 ms audio frame on a 4 ms common media timeline
	audioLeadMaxFrames             = 32  // 640 ms, the largest lead (the fixed lead of v0.1.3)
	audioQuietRMS                  = 300 // frames below this RMS count as silence when shrinking the lead
	audioBufferMaxFrames           = 128 // queue capacity: 2.56 s
	audioMaxConsecutivePLCs        = 32  // tolerate up to 640 ms of missing media before resync
	audioDTXUpdateFrames           = 8   // AMR-NB sends a SID update at least every 8 frames in DTX
	audioQueueMaxFrames            = 64  // 1.28 s of queued playback triggers a trim…
	videoBaseStaleThreshold        = 250 * time.Millisecond
	videoMaxStaleThreshold         = 3 * time.Second
	videoMaxQueuedFrames           = 96                     // ~3 s at 30 fps
	videoAudioClockStall           = 200 * time.Millisecond // audio clock considered stopped
	videoMaxLeadTicks              = int32(videoMaxStaleThreshold / mediaTickDuration)
)

// Audio buffer settings, adjustable with -lead, -speaker-period and
// -speaker-periods (see SetJitterLead, SetSpeakerBuffer). Derived values are
// recomputed by the setters.
var (
	audioSpeakerPeriodMs     = 80                           // device period in real time (any device sample rate)
	audioSpeakerPeriodFrames = 4                            // the device period in whole 20 ms frames, rounded up
	audioSpeakerPeriods      = 4                            // device buffer: 4 periods = 320 ms
	audioPlayoutLowFrames    = audioSpeakerPeriodFrames     // conceal once less than one device period is queued
	audioBufferLeadFrames    = 6                            // jitter buffer lead: 120 ms (see SetJitterLead)
	audioQueueTargetFrames   = audioBufferLeadFrames        // queue trim target
	audioEchoRefMaxFrames    = 3 * audioSpeakerPeriodFrames // echo reference queue limit…
	audioEchoRefTargetFrames = 2 * audioSpeakerPeriodFrames // …and its trim depth
)

// SetSpeakerBuffer sets the speaker device period (ms) and number of
// periods for future sessions. Zero keeps a value. The playout and the echo
// canceller work in whole 20 ms frames: they use the period rounded up.
func SetSpeakerBuffer(periodMs, periods int) {
	if periodMs > 0 {
		audioSpeakerPeriodMs = periodMs
		audioSpeakerPeriodFrames = max(1, (periodMs+audioFrameMillis-1)/audioFrameMillis)
	}
	if periods > 0 {
		audioSpeakerPeriods = max(2, periods)
	}
	audioPlayoutLowFrames = audioSpeakerPeriodFrames
	audioEchoRefMaxFrames = 3 * audioSpeakerPeriodFrames
	audioEchoRefTargetFrames = 2 * audioSpeakerPeriodFrames
	SetJitterLead(audioBufferLeadFrames * audioFrameMillis)
}

// The audio buffers every call starts with, in the LAN and over the
// internet alike. The speaker runs like the system voice unit on macOS (an
// IO buffer of 512 frames at 48 kHz, ~10.7 ms): two 10 ms periods.
const (
	defaultSpeakerPeriodMs = 10
	defaultSpeakerPeriods  = 2
	defaultLeadMs          = 40
)

// ResetAudioSettings restores the default buffers for the next session,
// undoing -audio changes made during the previous call.
func ResetAudioSettings() {
	SetSpeakerBuffer(defaultSpeakerPeriodMs, defaultSpeakerPeriods)
	SetJitterLead(defaultLeadMs)
}

// SetJitterLead sets the jitter buffer lead (ms) for future sessions. It
// must exceed one device period and is at most audioLeadMaxFrames.
func SetJitterLead(ms int) {
	if ms <= 0 {
		return
	}
	frames := max(audioPlayoutLowFrames+1, (ms+audioFrameMillis/2)/audioFrameMillis)
	audioBufferLeadFrames = min(frames, audioLeadMaxFrames)
	audioQueueTargetFrames = audioBufferLeadFrames
}

// Configure toggles voice/video capture for future UDP sessions.
func Configure(enableVoice, enableVideo bool) {
	useVoice = enableVoice
	useVideo = enableVideo
}

// micEnabled is false with -nomic: the session only plays the peer's audio.
var micEnabled = true

// SetMicrophone enables or disables microphone capture for future sessions.
func SetMicrophone(enabled bool) {
	micEnabled = enabled
}

// systemVoice (-sysaudio) runs the call's audio through the system's voice
// processing (macOS VoiceProcessingIO; the Windows Voice Capture DSP for
// the microphone) instead of Say's own echo canceller. Falls back to Say's
// own audio where unavailable.
var systemVoice bool

// SetSystemVoice selects the system voice unit for future sessions.
func SetSystemVoice(enabled bool) {
	systemVoice = enabled
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
	maxVideoFPS = max(fps, 0)
}

// videoFPSCeiling returns the highest rate the sender may use: the -fps cap
// if set, otherwise videoFPSUncapped (above any camera, so the camera's own
// rate is what goes out).
func videoFPSCeiling() int {
	if maxVideoFPS > 0 {
		return maxVideoFPS
	}
	return videoFPSUncapped
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
//
// peerCodec is the codec the peer encodes its voice with, as announced in
// the handshake; "" (an older peer) means it uses ours.
func StartSession(pc net.PacketConn, remote *net.UDPAddr, peerCodec string) (*dataSession, error) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	stopCh := make(chan struct{})
	pc = newMediaPacketConn(pc, stopCh)
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

		curSpeaker       atomic.Pointer[device.Speaker]
		usingSystemVoice bool // -sysaudio: one system unit plays and records

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
			if sp := curSpeaker.Swap(nil); sp != nil {
				sp.Close()
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
		// Each side encodes with its own codec; decode the peer's voice
		// with the one it announced, so -audio-codec may differ.
		rxCodec := audioCodec
		if peerCodec != "" && peerCodec != audioCodec {
			_, peerDec, peerFrame, err := initAudioCodec(peerCodec)
			switch {
			case err != nil:
				log.Printf("[audio] the peer sends %q, which this version cannot decode: %v", peerCodec, err)
				rxCodec = peerCodec
				ui.AddChatMessage(fmt.Sprintf("The peer sends voice as %s, which this Say version cannot decode. Update both sides to the same version.", peerCodec), false)
			case peerFrame != frameSamples:
				return nil, fmt.Errorf("peer codec %s uses %d-sample frames, ours %d", peerCodec, peerFrame, frameSamples)
			default:
				dec, rxCodec = peerDec, peerCodec
			}
		}
		log.Printf("[audio] sending voice as %s, receiving %s", audioCodec, rxCodec)
		if rxCodec != audioCodec {
			ui.AddChatMessage(fmt.Sprintf("Voice: you send %s, the peer sends %s.", audioCodecLabel(audioCodec), audioCodecLabel(rxCodec)), false)
		}
		audioSpeakerOut = device.NewPlaybackQueue(audioBufferMaxFrames)
		audioNetIn = make(chan []byte, audioBufferMaxFrames)

		if systemVoice {
			// The system unit cancels echo itself: no AEC of ours, and the
			// microphone comes from the same unit as the speaker.
			sp, micIn, err := device.StartSystemVoice(audioSampleRate, mediaTickSamples, audioSampleRate*audioSpeakerPeriodMs/1000, audioSpeakerPeriods,
				audioSpeakerOut, stopCh, micEnabled, stop)
			if err != nil {
				log.Printf("[audio] system voice unavailable, using Say's own audio: %v", err)
			} else {
				log.Printf("[audio] system voice processing (-sysaudio): the system cancels echo")
				usingSystemVoice = true
				curSpeaker.Store(sp)
				audioMicIn = micIn
				if !micEnabled {
					log.Printf("[audio] microphone disabled (-nomic)")
				}
			}
		}
	}
	if useVoice && !usingSystemVoice {
		var err error
		aec, err = newAudioEcho(frameSamples)
		if err != nil {
			return nil, fmt.Errorf("echo canceller: %w", err)
		}

		var onRender func([]int16)
		if aec != nil {
			onRender = aec.playback
		}
		log.Printf("[audio] speaker buffer %d ms (%d x %d ms), jitter buffer lead %d ms",
			audioSpeakerPeriodMs*audioSpeakerPeriods, audioSpeakerPeriods, audioSpeakerPeriodMs, audioBufferLeadFrames*audioFrameMillis)
		sp, err := device.StartSpeaker(audioSampleRate, audioChannels, audioSampleRate*audioSpeakerPeriodMs/1000, audioSpeakerPeriods,
			mediaTickSamples, audioSpeakerOut, stopCh, onRender, stop)
		if err != nil {
			return nil, fmt.Errorf("speaker: %w", err)
		}
		if aec != nil {
			// Echo of rendered audio reaches the mic about one device buffer later.
			delay := min(sp.LatencySamples()-audioSampleRate*audioEchoDelayMarginMs/1000, audioEchoMaxDelaySamples)
			aec.setSpeakerPeriod(audioSpeakerPeriodFrames)
			aec.setReferenceDelay(delay)
			log.Printf("[audio/aec] reference delay %d ms (speaker buffer %d ms)", delay*1000/audioSampleRate, sp.LatencySamples()*1000/audioSampleRate)
		}
		curSpeaker.Store(sp)

		// Open capture last, immediately before starting its consumer.
		if micEnabled {
			audioMicIn, err = device.StartMicrophoneStream(sessionCtx, audioSampleRate, audioChannels)
			if err != nil {
				return nil, fmt.Errorf("mic stream: %w", err)
			}
		} else {
			// Listen only: no capture, so no audio is sent (the sender exits).
			log.Printf("[audio] microphone disabled (-nomic)")
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
		if curSpeaker.Load() != nil {
			playbackIndex = func() (uint16, bool) {
				if sp := curSpeaker.Load(); sp != nil {
					return sp.PlaybackIndex()
				}
				return 0, false
			}
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
	previewEncoder, netEncoder := initVideoCodec()
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
		var (
			ceiling        = videoFPSCeiling()
			lastLossReport time.Time
			videoSeq       byte
			// adapted to the peer's video loss: hold first, then fps
			adapt = videoAdapt{fps: ceiling, hold: videoShapeHold}
		)
		fpsLimiter = time.NewTicker(time.Second / time.Duration(adapt.fps))
		defer fpsLimiter.Stop()

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

				sentFPS := int(frames) // what actually went out in the last second
				sentPkts, sentBytes = 0, 0
				frames = 0
				tEncodeLocal, tEncodeNet = 0, 0
				lastRateSample = time.Now()

				// Adapt the frame rate to the peer's latest loss report.
				if loss, at := ui.PeerVideoLossSnapshot(); fpsLimiter != nil && at.After(lastLossReport) {
					lastLossReport = at
					cells := 0
					if cols, rows, ok := ui.PeerSlot(); ok {
						cells = cols * rows
					}
					prevFPS, prevHold := adapt.fps, adapt.hold
					adapt.update(loss, sentFPS, ceiling, videoShapeHold, maxVideoHold(cells))
					if adapt.hold != prevHold {
						logs.LogV("[video/udp] peer video loss %.0f%%: cell hold %d -> %d", loss*100, prevHold, adapt.hold)
						netEncoder.SetShapeHold(adapt.hold)
					}
					if adapt.fps != prevFPS {
						logs.LogV("[video/udp] peer video loss %.0f%%: %d -> %d fps", loss*100, prevFPS, adapt.fps)
						fpsLimiter.Reset(time.Second / time.Duration(adapt.fps))
						netEncoder.SetKeyframeInterval(videoKeyframeFrames(adapt.fps))
					}
				}

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

				selfCols, selfRows, selfOK := ui.SelfSlot()
				peerCols, peerRows, peerOK := ui.PeerSlot()
				var addr *net.UDPAddr
				if peerOK {
					addr = getPeer()
				}
				sendNet := addr != nil

				// ----- encode -----
				var dataNet []byte
				var selfView *codec.TextFrame
				var errLocal, errNet error
				if selfOK && sendNet && selfCols == peerCols && selfRows == peerRows {
					// Both panes have one size: the cells are built once and
					// the self view mirrors them.
					start := time.Now()
					dataNet, selfView, errNet = netEncoder.EncodeWithSelfView(rawFrame, peerCols, peerRows, previewEncoder)
					errLocal = errNet
					tEncodeNet += time.Since(start)
				} else {
					if selfOK {
						start := time.Now()
						// The self view never leaves this process: no palette or payload.
						selfView, errLocal = previewEncoder.SelfView(rawFrame, selfCols, selfRows)
						tEncodeLocal += time.Since(start)
					}
					if sendNet {
						start := time.Now()
						dataNet, errNet = netEncoder.Encode(rawFrame, peerCols, peerRows)
						tEncodeNet += time.Since(start)
					}
				}

				// ----- local preview (self view) -----
				if selfOK {
					if err := errLocal; err != nil {
						log.Printf("[video/udp] Encode local: %v", err)
					} else if tv := selfView; videoSelfScreenOut != nil {
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
				if sendNet {
					if err := errNet; err != nil {
						log.Printf("[video/udp] Encode net: %v", err)
						continue
					}

					if mediaHeaderBytes+videoSeqBytes+len(dataNet) > maxMediaDatagram {
						// Cannot happen within codec.MaxFrameCells; never send an
						// oversized datagram that the network would drop or fragment.
						log.Printf("[video/udp] drop frame: %d bytes exceeds datagram limit %d", mediaHeaderBytes+len(dataNet), maxMediaDatagram)
						// The encoder counts this frame as sent; skipping its
						// number shows the receiver the gap, so it waits for a
						// full frame instead of applying changes to a stale one.
						videoSeq++
						continue
					}
					header := encodeMediaHeader(mediaKindVideo, mediaIndex(mediaEpoch, frame.CapturedAt))
					out := make([]byte, mediaHeaderBytes+videoSeqBytes+len(dataNet))
					copy(out[:mediaHeaderBytes], header[:])
					out[mediaHeaderBytes] = videoSeq // frame counter for the peer's loss estimate
					videoSeq++
					copy(out[mediaHeaderBytes+videoSeqBytes:], dataNet)

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
			lastRxAudio, lastRxVideo  uint16
			haveRxAudio, haveRxVideo  bool
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
			if haveRxAudio && haveRxVideo {
				// Sender clocks: audio and video stamps should stay within a frame or two.
				logs.LogV("[av/rx] newest audio - newest video = %d ms",
					mediaIndexDelta(lastRxAudio, lastRxVideo)*int32(mediaTickDuration/time.Millisecond))
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
				lastRxAudio, haveRxAudio = index, true
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
				lastRxVideo, haveRxVideo = index, true
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
		var loss videoLossMeter
		var (
			haveSeq bool
			lastSeq byte
		)
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

		var syncLogAt time.Time
		var newestIndex uint16
		var haveNewest bool
		presentDue := func() {
			if now := time.Now(); haveNewest && playbackIndex != nil && now.Sub(syncLogAt) >= time.Second {
				syncLogAt = now
				cur, ok := playbackIndex()
				logs.LogV("[av/sync] newest video - playback = %d ms (clock ok=%v), queued %d",
					mediaIndexDelta(newestIndex, cur)*int32(mediaTickDuration/time.Millisecond), ok, len(queue))
			}
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
				if len(pkt) <= mediaIndexBytes+videoSeqBytes {
					continue
				}
				index := uint16(pkt[0])<<8 | uint16(pkt[1])
				if !lastFrameAt.IsZero() && time.Since(lastFrameAt) >= time.Duration(mediaIndexHalf)*mediaTickDuration {
					queue = queue[:0]
				}
				now := time.Now()
				lastFrameAt = now
				seq := pkt[mediaIndexBytes]
				loss.observe(seq, now)
				loss.report(now)
				if haveSeq {
					switch gap := seq - lastSeq; {
					case gap == 0 || gap > 128:
						continue // duplicate or stale: its turn has passed
					case gap != 1:
						// A frame is missing: change-only frames have no base
						// until the next full frame.
						decoder.ResetBase()
					}
				}
				haveSeq, lastSeq = true, seq
				tv, err := decoder.Decode(pkt[mediaIndexBytes+videoSeqBytes:])
				if err != nil {
					if !errors.Is(err, codec.ErrNoBase) { // the gap was already counted
						loss.failed()
						log.Printf("[video/udp] textFrame decode: %v", err)
					}
					continue
				}

				newestIndex, haveNewest = index, true
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
