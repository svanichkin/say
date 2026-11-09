//go:build !plan9

package device

import (
	"context"
	"log"
	"sync"
	"time"

	malgo "github.com/gen2brain/malgo"
)

// StartMicrophoneStream starts capturing microphone PCM samples with the given sample rate and channel count.
func StartMicrophoneStream(ctx context.Context, sampleRate, channels int) (<-chan AudioCaptureFrame, error) {
	out := make(chan AudioCaptureFrame, 8)

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
	// Keep the backend's default capture period, as in v0.1.3 (60 ms blocks on
	// macOS). The sender re-frames the PCM into 20 ms codec frames.

	var mu sync.Mutex
	closed := false
	var firstSample uint64
	var captureEpoch time.Time

	callbacks := malgo.DeviceCallbacks{
		Data: func(pOutput, pInput []byte, frameCount uint32) {
			if len(pInput) == 0 {
				return
			}
			samples := bytesToInt16Mic(pInput)
			// The callback runs once the block is complete; its first sample was
			// captured one block duration earlier.
			blockFrames := len(samples) / max(channels, 1)
			// A pause of the device counts as dropped samples (captureGap).
			firstSample += captureGap(captureEpoch, firstSample, blockFrames, sampleRate, time.Now())
			if captureEpoch.IsZero() {
				captureEpoch = time.Now().Add(-time.Duration(blockFrames) * time.Second / time.Duration(sampleRate))
			}
			capturedAt := captureEpoch.Add(time.Duration(firstSample) * time.Second / time.Duration(sampleRate))
			frame := AudioCaptureFrame{Samples: samples, CapturedAt: capturedAt, FirstSample: firstSample}
			firstSample += uint64(blockFrames)
			select {
			case out <- frame:
			default:
				// Keep the newest capture time when downstream is late. The gap is
				// visible to the media timeline instead of silently compressing audio.
				select {
				case <-out:
				default:
				}
				select {
				case out <- frame:
				default:
				}
			}
		},
	}

	dev, err := malgo.InitDevice(mCtx.Context, cfg, callbacks)
	if err != nil {
		mCtx.Uninit()
		mCtx.Free()
		close(out)
		return nil, err
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		mCtx.Uninit()
		mCtx.Free()
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
			mCtx.Free()
			close(out)
		}
		mu.Unlock()
	}()

	return out, nil
}
