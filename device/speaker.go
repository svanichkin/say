package device

import (
	"log"
	"sync"
	"sync/atomic"

	malgo "github.com/gen2brain/malgo"
)

// Speaker handles audio playback via malgo.
type Speaker struct {
	dev                  *malgo.Device
	ctx                  *malgo.AllocatedContext
	once                 sync.Once
	playbackTick         atomic.Uint32
	playbackSet          atomic.Bool
	playbackLatencyTicks uint32
}

// PlaybackFrame is decoded PCM aligned to the sender's media timeline.
type PlaybackFrame struct {
	Samples    []int16
	MediaIndex uint16
}

// StartSpeaker initializes a playback device that consumes PCM frames from speakerIn.
// It reports every played sample back to speakerIn so its latency accounting stays exact.
// stopCh is observed to output silence when the session stops, and onStop is invoked when
// the underlying device stops unexpectedly.
func StartSpeaker(sampleRate, channels, frameSamples, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, onRender func([]int16), onStop func()) (*Speaker, error) {
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
	// Keep the device callback cadence equal to the AEC processing frame.
	// Feeding two 20 ms render frames at a time while capture is processed
	// every 20 ms can leave SpeexDSP's small playback queue out of step.
	deviceConfig.PeriodSizeInFrames = uint32(frameSamples)
	if deviceConfig.Periods < 2 {
		deviceConfig.Periods = 2
	}

	var pending []int16
	var pendingIndex uint16
	var pendingSamples int
	// Reused by the audio callback so it never allocates (an allocation can
	// stall the real-time thread on GC assist work).
	var outSamples []int16
	speaker := &Speaker{ctx: ctx}
	if samplesPerMediaTick > 0 {
		// The callback cursor is ahead of audible playback by approximately the
		// configured device buffer. Account for that when scheduling video.
		speaker.playbackLatencyTicks = uint32(deviceConfig.PeriodSizeInFrames * deviceConfig.Periods / uint32(samplesPerMediaTick))
	}

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
			filled := 0

			for filled < samplesNeeded {
				if len(pending) == 0 {
					if frame, ok := speakerIn.pop(); ok && frame.Samples != nil {
						pending = frame.Samples
						pendingIndex = frame.MediaIndex
						pendingSamples = 0
					}
				}
				if len(pending) == 0 {
					clear(outSamples[filled:])
					break
				}
				n := copy(outSamples[filled:], pending)
				filled += n
				pending = pending[n:]
				speakerIn.consumed(n)
				pendingSamples += n / channels
				if samplesPerMediaTick > 0 {
					speaker.playbackTick.Store(uint32(pendingIndex) + uint32(pendingSamples/samplesPerMediaTick))
					speaker.playbackSet.Store(true)
				}
			}

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

	speaker.dev = dev
	return speaker, nil
}

// PlaybackIndex estimates the media index currently audible, compensating for
// the configured device buffer latency.
func (s *Speaker) PlaybackIndex() (uint16, bool) {
	if s == nil || !s.playbackSet.Load() {
		return 0, false
	}
	return uint16(s.playbackTick.Load()-s.playbackLatencyTicks) & 0x7fff, true
}

// Close stops playback and releases device resources.
func (s *Speaker) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.dev != nil {
			_ = s.dev.Stop()
			s.dev.Uninit()
		}
		if s.ctx != nil {
			s.ctx.Uninit()
			s.ctx.Free()
		}
	})
}
