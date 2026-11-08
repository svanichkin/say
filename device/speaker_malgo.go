//go:build !plan9

package device

import (
	"log"
	"time"

	malgo "github.com/gen2brain/malgo"
)

// speakerBackend is the miniaudio device behind a Speaker.
type speakerBackend struct {
	dev *malgo.Device
	ctx *malgo.AllocatedContext
}

// stopDevice stops the device; no callback runs after it returns.
func (b *speakerBackend) stopDevice() {
	if b.dev != nil {
		_ = b.dev.Stop()
		b.dev.Uninit()
	}
}

// free releases the miniaudio context.
func (b *speakerBackend) free() {
	if b.ctx != nil {
		b.ctx.Uninit()
		b.ctx.Free()
	}
}

// StartSpeaker initializes a playback device that consumes PCM frames from speakerIn.
// It reports every played sample back to speakerIn so its latency accounting stays exact.
// stopCh is observed to output silence when the session stops, and onStop is invoked when
// the underlying device stops unexpectedly.
func StartSpeaker(sampleRate, channels, periodSamples, periods, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, onRender func([]int16), onStop func()) (*Speaker, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Printf("[malgo] %s", message)
	})
	if err != nil {
		return nil, err
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = uint32(channels)
	deviceConfig.SampleRate = uint32(sampleRate)
	// The connection profile supplies the device period and count. The period
	// goes in milliseconds: a size in frames is passed to the device as is,
	// in the device's own frames (48 kHz), so 160 frames meant to be 20 ms at
	// 8 kHz became 3.3 ms. Milliseconds are converted at the device's rate.
	deviceConfig.PeriodSizeInMilliseconds = uint32(max(1, (periodSamples*1000+sampleRate/2)/sampleRate))
	deviceConfig.Periods = uint32(max(periods, 2))

	// Reused by the audio callback so it never allocates (an allocation can
	// stall the real-time thread on GC assist work).
	var outSamples []int16
	// periodSamples is the period at our sample rate (the device's own frames
	// differ); the latency model counts in our samples.
	speaker := &Speaker{backend: speakerBackend{ctx: ctx}, latencySamples: periodSamples * int(deviceConfig.Periods)}
	if samplesPerMediaTick > 0 {
		// The callback cursor is ahead of audible playback by approximately the
		// configured device buffer. Account for that when scheduling video.
		speaker.playbackLatencyTicks = uint32(periodSamples * int(deviceConfig.Periods) / samplesPerMediaTick)
		speaker.periodTicks = uint32(max(1, periodSamples/samplesPerMediaTick))
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
	}
	r := &queueRenderer{speaker: speaker, in: speakerIn, channels: channels, samplesPerMediaTick: samplesPerMediaTick}

	callbacks := malgo.DeviceCallbacks{
		Data: func(pOutput, pInput []byte, frameCount uint32) {
			select {
			case <-stopCh:
				for i := range pOutput {
					pOutput[i] = 0
				}
				return
			default:
			}

			if len(pOutput) == 0 || speakerIn == nil {
				return
			}
			samplesNeeded := int(frameCount) * channels
			if cap(outSamples) < samplesNeeded {
				outSamples = make([]int16, samplesNeeded)
			}
			outSamples = outSamples[:samplesNeeded]
			r.render(outSamples)

			n := min(len(outSamples), len(pOutput)/2)
			for i, v := range outSamples[:n] {
				pOutput[2*i] = byte(v)
				pOutput[2*i+1] = byte(v >> 8)
			}
			clear(pOutput[2*n:])
			if onRender != nil {
				onRender(outSamples)
			}
		},
		Stop: func() {
			if onStop != nil {
				onStop()
			}
		},
	}

	speaker.release = r.release

	dev, err := malgo.InitDevice(ctx.Context, deviceConfig, callbacks)
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, err
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		ctx.Uninit()
		ctx.Free()
		return nil, err
	}

	speaker.backend.dev = dev
	return speaker, nil
}
