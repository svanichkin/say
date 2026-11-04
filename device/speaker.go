package device

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	malgo "github.com/gen2brain/malgo"
	"github.com/svanichkin/say/diag"
)

// Speaker handles audio playback via malgo.
type Speaker struct {
	dev                  *malgo.Device
	ctx                  *malgo.AllocatedContext
	once                 sync.Once
	playbackTick         atomic.Uint32
	playbackSet          atomic.Bool
	playbackLatencyTicks uint32
	latencySamples       int
}

// LatencySamples is the configured device buffer (period × periods) in
// frames: roughly how long rendered audio takes to become audible.
func (s *Speaker) LatencySamples() int {
	if s == nil {
		return 0
	}
	return s.latencySamples
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
func StartSpeaker(sampleRate, channels, periodSamples, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, onRender func([]int16), onStop func()) (*Speaker, error) {
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
	// Preserve v0.1.3's 80 ms period and minimum of four device periods.
	// The 40 ms/two-period configuration crackled with video even when the
	// application playback queue stayed full and callbacks arrived on time.
	deviceConfig.PeriodSizeInFrames = uint32(periodSamples)
	if deviceConfig.Periods < 4 {
		deviceConfig.Periods = 4
	}

	var pending []int16
	var pendingIndex uint16
	var pendingSamples int
	// Reused by the audio callback so it never allocates (an allocation can
	// stall the real-time thread on GC assist work).
	var outSamples []int16
	var declick crossfade
	var lastOut int16 // last sample written to the device
	var starved bool  // the previous callback ran out of audio
	var lastCallbackAt time.Time
	speaker := &Speaker{ctx: ctx, latencySamples: int(deviceConfig.PeriodSizeInFrames * deviceConfig.Periods)}
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
			cbStart := time.Now()
			if !lastCallbackAt.IsZero() {
				ivl := cbStart.Sub(lastCallbackAt).Microseconds()
				diag.SpkIntervalMaxUs.Observe(ivl)
				diag.SpkIntervalMinUs.Observe(ivl)
			}
			lastCallbackAt = cbStart
			diag.SpkCallbacks.Inc()
			diag.SpkFrameCount.Observe(int64(frameCount))
			defer func() { diag.SpkCallbackMaxUs.Observe(time.Since(cbStart).Microseconds()) }()
			wasStarved := starved

			samplesNeeded := int(frameCount) * channels
			if cap(outSamples) < samplesNeeded {
				outSamples = make([]int16, samplesNeeded)
			}
			outSamples = outSamples[:samplesNeeded]
			filled := 0

			for filled < samplesNeeded {
				if len(pending) == 0 {
					if frame, gap, ok := speakerIn.pop(); ok && frame.Samples != nil {
						diag.SpkFrames.Inc()
						if gap {
							diag.SpkGaps.Inc()
						}
						pending = frame.Samples
						pendingIndex = frame.MediaIndex
						pendingSamples = 0
						if gap || starved {
							// Queued audio was discarded or the queue ran dry: the
							// new frame does not continue the last output sample.
							declick.start(lastOut)
						}
						starved = false
					}
				}
				if len(pending) == 0 {
					if filled == 0 && wasStarved {
						diag.SpkIdle.Inc()
					} else {
						diag.SpkUnderruns.Inc()
						diag.SpkUnderrunSamples.Add(int64(samplesNeeded - filled))
					}
					// Underrun: ramp the last sample down instead of stepping to zero.
					declick.fadeOut(outSamples[filled:], lastOut)
					lastOut = 0
					starved = true
					break
				}
				n := copy(outSamples[filled:], pending)
				declick.apply(outSamples[filled : filled+n])
				filled += n
				lastOut = outSamples[filled-1]
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

// declickSamples is the length of the ramps that hide PCM discontinuities
// (5 ms at 8 kHz): long enough to remove the click, short enough to be inaudible.
const declickSamples = 40

// crossfade blends from a held sample value into a new signal, so a jump in
// the PCM stream (dropped frames, resuming after an underrun) does not click.
type crossfade struct {
	from int16
	left int
}

func (c *crossfade) start(from int16) {
	c.from, c.left = from, declickSamples
}

func (c *crossfade) apply(samples []int16) {
	for i := range samples {
		if c.left <= 0 {
			return
		}
		w := float64(c.left) / float64(declickSamples+1) // weight of the old value
		samples[i] = int16(float64(c.from)*w + float64(samples[i])*(1-w))
		c.left--
	}
}

// fadeOut ramps from the last output sample to silence across dst.
func (c *crossfade) fadeOut(dst []int16, from int16) {
	c.left = 0
	for i := range dst {
		if i < declickSamples {
			dst[i] = int16(float64(from) * float64(declickSamples-i) / float64(declickSamples+1))
		} else {
			dst[i] = 0
		}
	}
}
