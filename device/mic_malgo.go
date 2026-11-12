//go:build !plan9

package device

import (
	"context"
	"fmt"
	"log"

	malgo "github.com/gen2brain/malgo"
)

// StartMicrophoneStream starts capturing microphone PCM samples with the given sample rate and
// channel count (mono only), in 20 ms frames. The device's audio thread runs only C: it puts the
// blocks in a ring, stamped with the time they were captured, and a goroutine takes them
// from there (ring.go).
func StartMicrophoneStream(ctx context.Context, sampleRate, channels int) (<-chan AudioCaptureFrame, error) {
	if channels != 1 {
		return nil, fmt.Errorf("microphone: %d channels, only mono is supported", channels)
	}
	out := make(chan AudioCaptureFrame, 24) // 480 ms of 20 ms frames

	callStreams()
	mCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Printf("[audio/mic] malgo: %s", message)
	})
	if err != nil {
		close(out)
		return nil, err
	}
	ring, err := newNativeRing(sampleRate, channels)
	if err != nil {
		mCtx.Uninit()
		mCtx.Free()
		close(out)
		return nil, err
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = uint32(channels)
	cfg.SampleRate = uint32(sampleRate)
	// Keep the backend's default capture period, as in v0.1.3 (60 ms blocks on
	// macOS). The blocks are re-framed into 20 ms codec frames.

	dev, err := malgo.InitDeviceRaw(mCtx.Context, cfg, deviceDataProc(), ring.ptr(), nil)
	if err != nil {
		ring.free()
		mCtx.Uninit()
		mCtx.Free()
		close(out)
		return nil, err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		ring.free()
		mCtx.Uninit()
		mCtx.Free()
		close(out)
		return nil, err
	}

	quit := make(chan struct{})
	pumped := make(chan struct{})
	capture := &systemVoiceCapture{sampleRate: sampleRate, frameSize: max(1, sampleRate/50), out: out}
	go func() {
		defer close(pumped)
		pumpMic(ring, capture, quit)
	}()
	go func() {
		<-ctx.Done()
		_ = dev.Stop()
		close(quit)
		<-pumped
		dev.Uninit()
		ring.free()
		mCtx.Uninit()
		mCtx.Free()
		close(out)
	}()
	return out, nil
}
