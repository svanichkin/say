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
// stopCh is observed to output silence when the session stops, and onStop is invoked when
// the underlying device stops unexpectedly.
func StartSpeaker(sampleRate, channels, frameSamples, samplesPerMediaTick int, speakerIn <-chan PlaybackFrame, stopCh <-chan struct{}, onStop func()) (*Speaker, error) {
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
	deviceConfig.PeriodSizeInFrames = uint32(frameSamples * 2)
	if deviceConfig.Periods < 2 {
		deviceConfig.Periods = 2
	}

	var pending []int16
	var pendingIndex uint16
	var pendingSamples int
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
			outSamples := make([]int16, samplesNeeded)
			filled := 0

			pullMore := func() {
				if len(pending) > 0 {
					return
				}
				select {
				case frame, ok := <-speakerIn:
					if ok && frame.Samples != nil {
						pending = frame.Samples
						pendingIndex = frame.MediaIndex
						pendingSamples = 0
					}
				default:
				}
			}

			for filled < samplesNeeded {
				pullMore()
				if len(pending) == 0 {
					for i := filled; i < samplesNeeded; i++ {
						outSamples[i] = 0
					}
					break
				}
				n := copy(outSamples[filled:], pending)
				filled += n
				pending = pending[n:]
				pendingSamples += n / channels
				if samplesPerMediaTick > 0 {
					speaker.playbackTick.Store(uint32(pendingIndex) + uint32(pendingSamples/samplesPerMediaTick))
					speaker.playbackSet.Store(true)
				}
			}

			b := int16ToBytes(outSamples)
			if len(b) > len(pOutput) {
				b = b[:len(pOutput)]
			}
			copy(pOutput, b)
			if len(b) < len(pOutput) {
				for i := len(b); i < len(pOutput); i++ {
					pOutput[i] = 0
				}
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

func int16ToBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, v := range samples {
		b[2*i] = byte(v)
		b[2*i+1] = byte(v >> 8)
	}
	return b
}
