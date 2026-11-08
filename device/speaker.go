package device

import (
	"sync"
	"sync/atomic"
	"time"
)

// Speaker handles audio playback: miniaudio (malgo), or the platform's own
// device where miniaudio is not available (speaker_plan9.go).
type Speaker struct {
	backend              speakerBackend
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
	// closeDevice replaces the malgo device for other backends (system voice,
	// Plan 9).
	closeDevice func()
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

// queueRenderer fills device buffers from a PlaybackQueue: it hides
// discontinuities, ramps down on underrun, reports played samples to the
// queue and advances the speaker's media clock. It runs on the audio thread.
type queueRenderer struct {
	speaker             *Speaker
	in                  *PlaybackQueue
	channels            int
	samplesPerMediaTick int

	pending             []int16
	pendingIndex        uint16
	pendingSamples      int
	pendingLength       int
	pendingMediaSamples int
	declick             crossfade
	lastOut             int16 // last sample written to the device
	starved             bool  // the previous callback ran out of audio
}

// render fills out completely with the next queued audio (silence after it
// runs out).
func (r *queueRenderer) render(out []int16) {
	speaker := r.speaker
	if speaker.playbackSet.Load() {
		speaker.baseTick.Store(speaker.playbackTick.Load())
		speaker.baseAt.Store(time.Now().UnixNano())
	}
	filled := 0
	for filled < len(out) {
		if len(r.pending) == 0 {
			if frame, gap, ok := r.in.pop(); ok && frame.Samples != nil {
				r.pending = frame.Samples
				r.pendingIndex = frame.MediaIndex
				r.pendingSamples = 0
				r.pendingLength = len(frame.Samples) / r.channels
				r.pendingMediaSamples = frame.MediaSamples
				if r.pendingMediaSamples == 0 {
					r.pendingMediaSamples = r.pendingLength
				}
				if gap || frame.Discontinuous || r.starved {
					// Queued audio was discarded or the queue ran dry: the
					// new frame does not continue the last output sample.
					r.declick.start(r.lastOut)
				}
				r.starved = false
			}
		}
		if len(r.pending) == 0 {
			// Underrun: ramp the last sample down instead of stepping to zero.
			r.declick.fadeOut(out[filled:], r.lastOut)
			r.lastOut = 0
			r.starved = true
			return
		}
		n := copy(out[filled:], r.pending)
		r.declick.apply(out[filled : filled+n])
		filled += n
		r.lastOut = out[filled-1]
		r.pending = r.pending[n:]
		r.in.consumed(n)
		r.pendingSamples += n / r.channels
		if r.samplesPerMediaTick > 0 {
			speaker.playbackTick.Store(uint32(r.pendingIndex) + uint32(mediaSamplePosition(r.pendingSamples, r.pendingLength, r.pendingMediaSamples)/r.samplesPerMediaTick))
			speaker.playbackSet.Store(true)
		}
	}
}

// release returns the unplayed rest of the current frame to the queue's
// sample count. The device popped this frame but will never play the rest
// of it. Left counted, it inflates QueuedSamples for the queue's next
// speaker (-audio restarts it), and the playout, believing audio is queued,
// stops concealing gaps and stalls on the first missing packet.
func (r *queueRenderer) release() {
	r.in.consumed(len(r.pending))
	r.pending = nil
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
		s.backend.stopDevice()
		if s.closeDevice != nil {
			s.closeDevice()
		}
		// Only once no callback can run any more.
		if s.release != nil {
			s.release()
		}
		s.backend.free()
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
