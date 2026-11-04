package udp

import (
	"fmt"
	"sync"

	amrnb "github.com/VoiceBlender/goamr-nb"
	"github.com/danieldonoghue/govoip-dsp/echo"
	"github.com/thesyncim/gopus"
)

const (
	audioSampleRate  = 8000
	audioChannels    = 1
	audioBitrate     = 12000
	audioFrameMillis = 20
	maxAudioPacket   = 1275
	audioEchoTailMs  = 200
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
// Instead the speaker only appends rendered samples to refRing. Each captured
// frame takes exactly one frame of reference from it and calls Playback
// immediately before Capture, so the canceller's queue never over- or
// underflows. Alignment follows sample counts, not callback timing.
const (
	audioEchoRefMaxFrames    = 5 // more queued reference than this means clock drift…
	audioEchoRefTargetFrames = 2 // …and is trimmed back to this depth
)

// audioEcho pairs the rendered far-end audio with microphone frames.
type audioEcho struct {
	// refMu guards refRing only; the speaker callback never waits for the
	// canceller's processing.
	refMu   sync.Mutex
	refRing []int16

	mu             sync.Mutex // canceller state, used by the sender goroutine
	canceller      *echo.Canceller
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
		refRing:      make([]int16, 0, frameSamples*(audioEchoRefMaxFrames+2)),
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
	if len(a.refRing) > a.frameSamples*audioEchoRefMaxFrames {
		drop := len(a.refRing) - a.frameSamples*audioEchoRefTargetFrames
		a.refRing = append(a.refRing[:0], a.refRing[drop:]...)
	}
	a.refMu.Unlock()
}

// nextReference fills a.ref with the oldest frame of rendered audio, or
// silence if the speaker has not produced one (nothing to cancel then).
func (a *audioEcho) nextReference() {
	a.refMu.Lock()
	if len(a.refRing) >= a.frameSamples {
		copy(a.ref, a.refRing[:a.frameSamples])
		a.refRing = append(a.refRing[:0], a.refRing[a.frameSamples:]...)
	} else {
		clear(a.ref)
	}
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
	a.nextReference()
	if err := a.canceller.Playback(a.ref); err != nil {
		return err
	}
	return a.canceller.Capture(mic, out)
}

// initAudioCodec creates a mono 8 kHz speech codec. AMR-NB is fixed at its
// lowest 4.75 kbit/s mode with DTX; Opus retains the current low-bandwidth setup.
func initAudioCodec(name string) (audioEncoder, audioDecoder, int, error) {
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
