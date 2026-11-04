package device

import (
	"context"
	"log"
	"sync"
	"time"

	malgo "github.com/gen2brain/malgo"
	"github.com/svanichkin/say/diag"
)

// AudioCaptureFrame is a microphone block and the monotonic time at which
// capture delivered its first sample.
type AudioCaptureFrame struct {
	Samples    []int16
	CapturedAt time.Time
}

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
	var lastCallbackAt time.Time

	callbacks := malgo.DeviceCallbacks{
		Data: func(pOutput, pInput []byte, frameCount uint32) {
			if len(pInput) == 0 {
				return
			}
			samples := bytesToInt16Mic(pInput)
			now := time.Now()
			if !lastCallbackAt.IsZero() {
				ivl := now.Sub(lastCallbackAt).Microseconds()
				diag.MicIntervalMaxUs.Observe(ivl)
				diag.MicIntervalMinUs.Observe(ivl)
			}
			lastCallbackAt = now
			diag.MicCallbacks.Inc()
			diag.MicSamples.Add(int64(len(samples)))
			diag.MicBlockMin.Observe(int64(len(samples)))
			diag.MicBlockMax.Observe(int64(len(samples)))
			var peak int64
			for _, v := range samples {
				a := diag.Abs16(v)
				peak = max(peak, a)
				if a >= 32000 {
					diag.MicClipped.Inc()
				}
			}
			diag.MicPeak.Observe(peak)
			// The callback runs once the block is complete; its first sample was
			// captured one block duration earlier.
			blockFrames := len(samples) / max(channels, 1)
			capturedAt := time.Now().Add(-time.Duration(blockFrames) * time.Second / time.Duration(sampleRate))
			frame := AudioCaptureFrame{Samples: samples, CapturedAt: capturedAt}
			select {
			case out <- frame:
			default:
				diag.MicDrops.Inc()
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
