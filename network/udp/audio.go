package udp

import (
	"fmt"
	"log"
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

// audioEcho pairs the rendered far-end audio with microphone frames. The
// library's playback/capture queue expects both streams to be processed in
// order, so its state and partial render frame are protected by one mutex.
type audioEcho struct {
	mu             sync.Mutex
	canceller      *echo.Canceller
	frameSamples   int
	renderPending  []int16
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
		canceller:     canceller,
		frameSamples:  frameSamples,
		renderPending: make([]int16, 0, frameSamples*2),
	}, nil
}

// playback receives the PCM block that is about to be written to the speaker.
func (a *audioEcho) playback(samples []int16) {
	if a == nil || len(samples) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.renderPending = append(a.renderPending, samples...)
	for len(a.renderPending) >= a.frameSamples {
		if err := a.canceller.Playback(a.renderPending[:a.frameSamples]); err != nil {
			log.Printf("[audio/aec] playback frame: %v", err)
			return
		}
		copy(a.renderPending, a.renderPending[a.frameSamples:])
		a.renderPending = a.renderPending[:len(a.renderPending)-a.frameSamples]
	}
}

func (a *audioEcho) setCaptureEnabled(enabled bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.captureKnown || a.captureEnabled != enabled {
		a.canceller.Reset()
		a.renderPending = a.renderPending[:0]
		a.captureEnabled = enabled
		a.captureKnown = true
	}
}

func (a *audioEcho) capture(mic, out []int16) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
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
