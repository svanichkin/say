package device

import (
	"context"
	"log"
	"sync"

	malgo "github.com/gen2brain/malgo"
)

// StartMicrophoneStream starts capturing microphone PCM samples with the given sample rate and channel count.
func StartMicrophoneStream(ctx context.Context, sampleRate, channels int) (<-chan []int16, error) {
	out := make(chan []int16, 16)

	mCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Printf("[audio/mic] malgo: %s", message)
	})
	if err != nil {
		close(out)
		return nil, err
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = uint32(channels)
	cfg.SampleRate = uint32(sampleRate)

	var mu sync.Mutex
	closed := false

	callbacks := malgo.DeviceCallbacks{
		Data: func(pOutput, pInput []byte, frameCount uint32) {
			if len(pInput) == 0 {
				return
			}
			samples := bytesToInt16Mic(pInput)
			select {
			case out <- samples:
			default:
			}
		},
	}

	dev, err := malgo.InitDevice(mCtx.Context, cfg, callbacks)
	if err != nil {
		mCtx.Uninit()
		close(out)
		return nil, err
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		mCtx.Uninit()
		close(out)
		return nil, err
	}

	go func() {
		<-ctx.Done()
		mu.Lock()
		if !closed {
			closed = true
			_ = dev.Stop()
			dev.Uninit()
			mCtx.Uninit()
			close(out)
		}
		mu.Unlock()
	}()

	return out, nil
}

func bytesToInt16Mic(b []byte) []int16 {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(b[2*i]) | int16(b[2*i+1])<<8
	}
	return out
}
