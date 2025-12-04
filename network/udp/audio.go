package udp

import (
	g722 "github.com/gotranspile/g722"
)

const (
	audioSampleRate   = 8000
	audioChannels     = 1
	g722Bitrate       = g722.Rate48000
	g722BitsPerSample = 6
	g722Options       = g722.FlagSampleRate8000 | g722.FlagPacked
)

// initAudioCodec normalizes the frame size and returns configured G.722 encoder/decoder
// instances together with the number of PCM samples per frame.
func initAudioCodec(frameszMs, _ int) (*g722.Encoder, *g722.Decoder, int, error) {
	if frameszMs != 10 && frameszMs != 20 && frameszMs != 40 {
		frameszMs = 20
	}

	frameSamples := audioSampleRate * frameszMs / 1000
	if frameSamples <= 0 {
		frameSamples = audioSampleRate / 50 // fallback to 20 ms
	}

	enc := g722.NewEncoder(g722Bitrate, g722Options)
	dec := g722.NewDecoder(g722Bitrate, g722Options)

	return enc, dec, frameSamples, nil
}
