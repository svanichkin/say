package udp

import (
	"github.com/thesyncim/gopus"
)

const (
	audioSampleRate  = 8000
	audioChannels    = 1
	audioBitrate     = 12000
	audioFrameMillis = 20
	maxOpusPacket    = 1275
)

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
