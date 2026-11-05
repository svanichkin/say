package udp

import (
	"fmt"
	"sync"

	amrnb "github.com/VoiceBlender/goamr-nb"
	"github.com/danieldonoghue/govoip-dsp/echo"
	g722 "github.com/gotranspile/g722"
	"github.com/thesyncim/gopus"
)

const (
	audioSampleRate  = 8000
	audioChannels    = 1
	audioBitrate     = 12000
	audioFrameMillis = 20
	maxAudioPacket   = 1275
	audioEchoTailMs  = 200
	// audioEchoMaxDelaySamples bounds the reference delay (1 s at 8 kHz).
	audioEchoMaxDelaySamples = audioSampleRate
	// audioEchoDelayMarginMs keeps the reference this much ahead of its echo.
	audioEchoDelayMarginMs = 40
)

type audioEncoder interface {
	EncodeFrame(samples []int16, dst []byte) (int, error)
}

type audioDecoder interface {
	DecodeFrame(payload []byte, dst []int16) (int, error)
	Conceal(dst []int16) (int, error)
	IsDTXFrame(payload []byte) bool
}

type opusAudioEncoder struct{ encoder *gopus.Encoder }
type opusAudioDecoder struct{ decoder *gopus.Decoder }

func (e opusAudioEncoder) EncodeFrame(samples []int16, dst []byte) (int, error) {
	return e.encoder.EncodeInt16(samples, dst)
}

func (d opusAudioDecoder) DecodeFrame(payload []byte, dst []int16) (int, error) {
	return d.decoder.DecodeInt16(payload, dst)
}

func (d opusAudioDecoder) Conceal(dst []int16) (int, error) {
	return d.decoder.DecodeInt16(nil, dst)
}

func (opusAudioDecoder) IsDTXFrame([]byte) bool { return false }

type amrAudioEncoder struct{ encoder *amrnb.Encoder }
type amrAudioDecoder struct{ decoder *amrnb.Decoder }

func (e amrAudioEncoder) EncodeFrame(samples []int16, dst []byte) (int, error) {
	return e.encoder.EncodeTo(dst, samples)
}

func (d amrAudioDecoder) DecodeFrame(payload []byte, dst []int16) (int, error) {
	samples, err := d.decoder.Decode(payload)
	if err != nil {
		return 0, err
	}
	if len(samples) > len(dst) {
		return 0, fmt.Errorf("AMR-NB decoded %d samples into %d-sample buffer", len(samples), len(dst))
	}
	return copy(dst, samples), nil
}

func (d amrAudioDecoder) Conceal(dst []int16) (int, error) {
	// RFC 4867 octet-aligned CMR=15 plus one FT=15 NO_DATA frame. After a SID
	// frame, the decoder synthesizes AMR comfort noise for this missing interval.
	noData := [...]byte{0xf0, byte(amrnb.FTNoData<<3 | 0x04)}
	return d.DecodeFrame(noData[:], dst)
}

// G.722 as in v0.1.3: 8 kHz PCM, 48 kbit/s mode, packed codewords.
const (
	g722Rate    = g722.Rate48000
	g722Options = g722.FlagSampleRate8000 | g722.FlagPacked
)

type g722AudioEncoder struct{ encoder *g722.Encoder }

func (e g722AudioEncoder) EncodeFrame(samples []int16, dst []byte) (int, error) {
	n := e.encoder.Encode(dst, samples)
	if n <= 0 {
		return 0, fmt.Errorf("G.722 encoded %d bytes from %d samples", n, len(samples))
	}
	return n, nil
}

// g722AudioDecoder has no built-in loss concealment. Like v0.1.3 it repeats
// the last decoded frame, but fades each repetition so a long loss turns
// into silence instead of a buzz.
type g722AudioDecoder struct {
	decoder *g722.Decoder
	last    []int16
	fade    float64
}

func (d *g722AudioDecoder) DecodeFrame(payload []byte, dst []int16) (int, error) {
	n := d.decoder.Decode(dst, payload)
	if n <= 0 {
		return 0, fmt.Errorf("G.722 decoded %d samples from %d bytes", n, len(payload))
	}
	d.last = append(d.last[:0], dst[:n]...)
	d.fade = 1
	return n, nil
}

func (d *g722AudioDecoder) Conceal(dst []int16) (int, error) {
	d.fade *= 0.5
	n := min(len(d.last), len(dst))
	for i := 0; i < n; i++ {
		dst[i] = int16(float64(d.last[i]) * d.fade)
	}
	clear(dst[n:])
	return len(dst), nil
}

func (*g722AudioDecoder) IsDTXFrame([]byte) bool { return false }

func (amrAudioDecoder) IsDTXFrame(payload []byte) bool {
	return len(payload) >= 2 && int(payload[1]>>3)&0x0f == amrnb.FTSID
}

// Echo reference buffering. The speaker and the microphone are separate
// devices whose callbacks run on separate threads with independent jitter,
// while the canceller's own queue holds at most three frames: it discards a
// Playback when full and passes the microphone through unprocessed when
// empty. Feeding it directly from both callbacks made the reference jump by
// a frame back and forth, which corrupted the cancelled microphone signal
// (audible as a hoarse, crackling voice on the far side).
//
// The speaker appends rendered samples to refRing. Each captured frame takes
// exactly one reference frame and calls Cancel directly, avoiding the DSP's
// additional buffered Playback/Capture delay. Alignment follows sample counts;
// any queue discontinuity resets the adaptive filter before it is used again.
// audioEchoRefMaxFrames/TargetFrames (udp.go) allow overlap between whole
// speaker periods and batched microphone callbacks.

// audioEcho pairs the rendered far-end audio with microphone frames.
type audioEcho struct {
	// refMu guards refRing only; the speaker callback never waits for the
	// canceller's processing.
	refMu   sync.Mutex
	refRing []int16

	// delaySamples holds the reference back by about the speaker's device
	// latency: rendered audio reaches the microphone only after the device
	// buffer has played it, and the filter models echo up to audioEchoTailMs
	// after the reference. primed is false until that much is queued.
	delaySamples     int
	refDiscontinuity bool // guarded by refMu; reset the filter before using a new alignment
	primed           bool
	refMax           int // reference queue limit (samples, beyond the delay)…
	refTarget        int // …and its trim depth

	mu             sync.Mutex // canceller state, used by the sender goroutine
	canceller      *echo.Canceller
	trace          *audioTrace
	frameSamples   int
	ref            []int16
	captureKnown   bool
	captureEnabled bool
}

func newAudioEcho(frameSamples int) (*audioEcho, error) {
	canceller, err := echo.New(echo.Config{
		FrameSize:    frameSamples,
		FilterLength: audioSampleRate * audioEchoTailMs / 1000,
		SampleRate:   audioSampleRate,
	})
	if err != nil {
		return nil, err
	}
	return &audioEcho{
		canceller:    canceller,
		frameSamples: frameSamples,
		refRing:      make([]int16, 0, frameSamples*(audioEchoRefMaxFrames+2)+audioEchoMaxDelaySamples),
		refMax:       frameSamples * audioEchoRefMaxFrames,
		refTarget:    frameSamples * audioEchoRefTargetFrames,
		ref:          make([]int16, frameSamples),
	}, nil
}

// playback receives the PCM block that is about to be written to the
// speaker. It runs on the audio thread, so it only copies.
func (a *audioEcho) playback(samples []int16) {
	if a == nil || len(samples) == 0 {
		return
	}
	a.refMu.Lock()
	a.refRing = append(a.refRing, samples...)
	if len(a.refRing) > a.delaySamples+a.refMax {
		drop := len(a.refRing) - (a.delaySamples + a.refTarget)
		a.refRing = append(a.refRing[:0], a.refRing[drop:]...)
		a.refDiscontinuity = true
	}
	a.refMu.Unlock()
}

// nextReference fills a.ref with the oldest frame of rendered audio, or
// silence if the speaker has not produced one (nothing to cancel then).
func (a *audioEcho) nextReference() bool {
	a.refMu.Lock()
	if !a.primed && len(a.refRing) >= a.delaySamples+a.frameSamples {
		a.primed = true
	}
	if a.primed && len(a.refRing) >= a.frameSamples {
		copy(a.ref, a.refRing[:a.frameSamples])
		a.refRing = append(a.refRing[:0], a.refRing[a.frameSamples:]...)
	} else {
		// Not primed yet, or the speaker stalled: hold the delay again.
		if a.primed {
			a.refDiscontinuity = true
		}
		a.primed = false
		clear(a.ref)
	}
	reset := a.refDiscontinuity
	a.refDiscontinuity = false
	a.refMu.Unlock()
	return reset
}

// setSpeakerPeriod adapts the reference queue limits to a new speaker period
// (frames of 20 ms); the speaker hands a whole period at once.
func (a *audioEcho) setSpeakerPeriod(periodFrames int) {
	if a == nil {
		return
	}
	a.refMu.Lock()
	a.refMax = a.frameSamples * 3 * periodFrames
	a.refTarget = a.frameSamples * 2 * periodFrames
	a.refMu.Unlock()
}

// setReferenceDelay holds the reference back by samples (see delaySamples).
func (a *audioEcho) setReferenceDelay(samples int) {
	if a == nil {
		return
	}
	a.refMu.Lock()
	a.delaySamples = max(0, samples)
	a.primed = false
	a.refRing = a.refRing[:0]
	a.refDiscontinuity = true
	a.refMu.Unlock()
}

func (a *audioEcho) setCaptureEnabled(enabled bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.captureKnown || a.captureEnabled != enabled {
		a.canceller.Reset()
		a.refMu.Lock()
		a.refRing = a.refRing[:0]
		a.primed = false
		a.refMu.Unlock()
		a.captureEnabled = enabled
		a.captureKnown = true
	}
}

// capture cancels echo from one microphone frame into out.
func (a *audioEcho) capture(mic, out []int16) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.nextReference() {
		a.canceller.Reset()
	}
	// Pair explicitly: our reference queue already aligns whole frames.
	// Playback/Capture would add a second, hidden two-frame queue.
	err := a.canceller.Cancel(mic, a.ref, out)
	if err == nil && a.trace != nil {
		a.trace.record(mic, a.ref, out)
	}
	return err
}

// audioCodecLabel is the codec's name for the user.
func audioCodecLabel(name string) string {
	switch name {
	case "amrnb":
		return "AMR-NB"
	case "opus":
		return "Opus"
	case "g722":
		return "G722"
	}
	return name
}

// initAudioCodec creates a mono 8 kHz speech codec. AMR-NB is fixed at its
// lowest 4.75 kbit/s mode with DTX; Opus retains the current low-bandwidth setup.
func initAudioCodec(name string) (audioEncoder, audioDecoder, int, error) {
	if name == "g722" {
		frameSamples := audioSampleRate * audioFrameMillis / 1000
		return g722AudioEncoder{encoder: g722.NewEncoder(g722Rate, g722Options)},
			&g722AudioDecoder{decoder: g722.NewDecoder(g722Rate, g722Options)}, frameSamples, nil
	}
	if name == "amrnb" {
		enc, err := amrnb.NewEncoder(amrnb.EncoderConfig{
			Mode:         amrnb.Mode0475,
			OctetAligned: true,
			DTX:          true,
		})
		if err != nil {
			return nil, nil, 0, err
		}
		dec := amrnb.NewDecoder(amrnb.DecoderConfig{OctetAligned: true})
		return amrAudioEncoder{encoder: enc}, amrAudioDecoder{decoder: dec}, amrnb.FrameSamples, nil
	}
	if name != "opus" {
		return nil, nil, 0, fmt.Errorf("unsupported audio codec %q", name)
	}
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{
		SampleRate:  audioSampleRate,
		Channels:    audioChannels,
		Application: gopus.ApplicationVoIP,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if err := enc.SetBitrate(audioBitrate); err != nil {
		return nil, nil, 0, err
	}
	if err := enc.SetComplexity(10); err != nil {
		return nil, nil, 0, err
	}
	if err := enc.SetSignal(gopus.SignalVoice); err != nil {
		return nil, nil, 0, err
	}
	enc.SetVBR(true)
	enc.SetVBRConstraint(true)
	enc.SetDTX(true)
	enc.SetFEC(false)

	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(audioSampleRate, audioChannels))
	if err != nil {
		return nil, nil, 0, err
	}

	return opusAudioEncoder{encoder: enc}, opusAudioDecoder{decoder: dec}, audioSampleRate * audioFrameMillis / 1000, nil
}

// skipCapture accounts for discarded microphone PCM before pairing the next
// frame. Never continue adapting an echo filter across an unreported jump.
func (a *audioEcho) skipCapture(samples int) {
	if a == nil {
		return
	}
	a.refMu.Lock()
	n := min(max(samples, 0), len(a.refRing))
	a.refRing = append(a.refRing[:0], a.refRing[n:]...)
	if len(a.refRing) == 0 {
		a.primed = false
	}
	a.refDiscontinuity = true
	a.refMu.Unlock()
}
