package udp

import (
	"log"
	"sync"

	"github.com/danieldonoghue/govoip-dsp/echo"
	"github.com/thesyncim/gopus"
)

const (
	audioSampleRate  = 8000
	audioChannels    = 1
	audioBitrate     = 12000
	audioFrameMillis = 20
	maxOpusPacket    = 1275
	audioEchoTailMs  = 200
)

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

// initAudioCodec configures pure-Go Opus for low-bandwidth mono speech.
func initAudioCodec() (*gopus.Encoder, *gopus.Decoder, int, error) {
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

	return enc, dec, audioSampleRate * audioFrameMillis / 1000, nil
}
