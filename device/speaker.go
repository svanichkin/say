package device

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

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
	latencySamples       int

	// The callback hands a whole device period at once, so playbackTick
	// advances in period-sized steps (80 ms). baseTick/baseAt record the
	// position and time at the start of the latest callback; PlaybackIndex
	// advances smoothly from there in real time, by at most one period.
	baseTick    atomic.Uint32
	baseAt      atomic.Int64 // UnixNano
	periodTicks uint32
	tickDur     time.Duration
	lastPos     atomic.Uint32 // last returned position + 1 (0: none yet)

	// release returns the unplayed rest of the frame the callback was on to
	// the queue's sample count; called by Close once the device has stopped.
	release func()
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
	// MediaSamples is the original duration before PCM rate adjustment. Zero
	// means len(Samples), for unmodified frames.
	MediaSamples  int
	Discontinuous bool // an earlier PCM segment was removed
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
	// The connection profile supplies the requested device period and count.
	deviceConfig.PeriodSizeInFrames = uint32(periodSamples)
	deviceConfig.Periods = uint32(max(periods, 2))

	var pending []int16
	var pendingIndex uint16
	var pendingSamples int
	var pendingLength, pendingMediaSamples int
	// Reused by the audio callback so it never allocates (an allocation can
	// stall the real-time thread on GC assist work).
	var outSamples []int16
	var declick crossfade
	var lastOut int16 // last sample written to the device
	var starved bool  // the previous callback ran out of audio
	speaker := &Speaker{ctx: ctx, latencySamples: int(deviceConfig.PeriodSizeInFrames * deviceConfig.Periods)}
	if samplesPerMediaTick > 0 {
		// The callback cursor is ahead of audible playback by approximately the
		// configured device buffer. Account for that when scheduling video.
		speaker.playbackLatencyTicks = uint32(deviceConfig.PeriodSizeInFrames * deviceConfig.Periods / uint32(samplesPerMediaTick))
		speaker.periodTicks = deviceConfig.PeriodSizeInFrames / uint32(samplesPerMediaTick)
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
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
			if speaker.playbackSet.Load() {
				speaker.baseTick.Store(speaker.playbackTick.Load())
				speaker.baseAt.Store(time.Now().UnixNano())
			}
			samplesNeeded := int(frameCount) * channels
			if cap(outSamples) < samplesNeeded {
				outSamples = make([]int16, samplesNeeded)
			}
			outSamples = outSamples[:samplesNeeded]
			filled := 0

			for filled < samplesNeeded {
				if len(pending) == 0 {
					if frame, gap, ok := speakerIn.pop(); ok && frame.Samples != nil {
						pending = frame.Samples
						pendingIndex = frame.MediaIndex
						pendingSamples = 0
						pendingLength = len(frame.Samples) / channels
						pendingMediaSamples = frame.MediaSamples
						if pendingMediaSamples == 0 {
							pendingMediaSamples = pendingLength
						}
						if gap || frame.Discontinuous || starved {
							// Queued audio was discarded or the queue ran dry: the
							// new frame does not continue the last output sample.
							declick.start(lastOut)
						}
						starved = false
					}
				}
				if len(pending) == 0 {
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
					speaker.playbackTick.Store(uint32(pendingIndex) + uint32(mediaSamplePosition(pendingSamples, pendingLength, pendingMediaSamples)/samplesPerMediaTick))
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

	speaker.release = func() {
		// The device popped this frame but will never play the rest of it.
		// Left counted, it inflates QueuedSamples for the queue's next
		// speaker (-audio restarts it), and the playout, believing audio is
		// queued, stops concealing gaps and stalls on the first missing packet.
		speakerIn.consumed(len(pending))
		pending = nil
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
	at := s.baseAt.Load()
	if at == 0 {
		return uint16(s.playbackTick.Load()-s.playbackLatencyTicks) & 0x7fff, true
	}
	elapsed := time.Since(time.Unix(0, at))
	pos := playbackPosition(s.baseTick.Load(), elapsed, s.tickDur, s.periodTicks, s.playbackLatencyTicks)
	if last := s.lastPos.Load(); last != 0 {
		pos = monotonicPosition(uint16(last-1), pos, s.periodTicks)
	}
	s.lastPos.Store(uint32(pos) + 1)
	return pos, true
}

// monotonicPosition keeps the clock from stepping back by less than two
// periods (a callback that handed less than a period after an underrun);
// larger jumps back are real resynchronisations and pass through.
func monotonicPosition(prev, pos uint16, periodTicks uint32) uint16 {
	d := int32((pos - prev) & 0x7fff)
	if d >= 0x4000 {
		d -= 0x8000
	}
	if d < 0 && d > -2*int32(periodTicks) {
		return prev
	}
	return pos
}

// playbackPosition is the audible media index: the position at the start of
// the latest callback, advanced by the real time since then (capped at one
// device period, so a stalled device does not run ahead), minus the latency.
func playbackPosition(base uint32, elapsed, tick time.Duration, periodTicks, latencyTicks uint32) uint16 {
	var advance uint32
	if tick > 0 && elapsed > 0 {
		advance = min(uint32(elapsed/tick), periodTicks)
	}
	return uint16(base+advance-latencyTicks) & 0x7fff
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
		if s.release != nil {
			s.release()
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

// Map stretched PCM back to the sender clock for video synchronization.
func mediaSamplePosition(played, outputLength, mediaLength int) int {
	if outputLength <= 0 {
		return 0
	}
	return played * mediaLength / outputLength
}
