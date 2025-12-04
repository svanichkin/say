package device

import (
	"log"
	"sync"

	malgo "github.com/gen2brain/malgo"
)

// Speaker handles audio playback via malgo.
type Speaker struct {
	dev  *malgo.Device
	ctx  *malgo.AllocatedContext
	once sync.Once
}

// StartSpeaker initializes a playback device that consumes PCM frames from speakerIn.
// stopCh is observed to output silence when the session stops, and onStop is invoked when
// the underlying device stops unexpectedly.
func StartSpeaker(sampleRate, channels, frameSamples int, speakerIn <-chan []int16, stopCh <-chan struct{}, onStop func()) (*Speaker, error) {
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
	deviceConfig.PeriodSizeInFrames = uint32(frameSamples * 4)
	if deviceConfig.Periods < 4 {
		deviceConfig.Periods = 4
	}

	var pending []int16

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
				case frame := <-speakerIn:
					if frame != nil {
						pending = frame
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

	return &Speaker{dev: dev, ctx: ctx}, nil
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
