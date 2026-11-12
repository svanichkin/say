//go:build !plan9

package device

import "time"

// ringRenderer feeds the device's output ring from a PlaybackQueue, like
// queueRenderer feeds a device callback: it hides discontinuities, reports
// played samples to the queue and advances the speaker's media clock, from
// what the device has actually taken.
type ringRenderer struct {
	speaker             *Speaker
	in                  *PlaybackQueue
	ring                *nativeRing
	onRender            func([]int16) // played samples, silence included (software echo cancelling)
	samplesPerMediaTick int
	channels            int // interleaved in the ring (0: mono)
	ahead               int // samples to keep the ring filled with

	pending []int16 // the rest of the frame being written
	history []int16 // written, not yet played (kept for onRender)
	dry     uint64  // silence the device played, as last seen (for onRender)
	declick crossfade
	lastOut int16 // last sample written

	// frames written, oldest first: where each starts in the ring's sample
	// count, for the media clock and the frame trace.
	frames  []ringFrame
	played  uint64 // samples reported to the queue as played
	lastIdx uint16 // the frame played last, for the trace

	final RingState // after the unit stopped, for release

	stats      playStats // DEBUG-TEMP
	lastState  RingState // DEBUG-TEMP
	statsAt    RingState // DEBUG-TEMP
	statsSince time.Time // DEBUG-TEMP
}

type ringFrame struct {
	start        uint64
	length       int
	index        uint16
	mediaSamples int
	started      bool
}

func (r *ringRenderer) step() {
	st := r.ring.State()

	// What the device took since the last step is played.
	if st.Played > r.played {
		r.in.consumed(int(st.Played - r.played))
	}
	if r.onRender != nil {
		r.renderedSince(st)
	}
	r.played = st.Played
	r.traceAndClock(st)
	r.noteStats(st) // DEBUG-TEMP

	// Keep the ring filled ahead.
	queued := int(st.Written - st.Played)
	for queued < r.ahead {
		if len(r.pending) == 0 {
			frame, gap, ok := r.in.pop()
			if !ok || frame.Samples == nil {
				break
			}
			media := frame.MediaSamples
			if media == 0 {
				media = len(frame.Samples) / r.ch()
			}
			r.frames = append(r.frames, ringFrame{start: st.Written, length: len(frame.Samples), index: frame.MediaIndex, mediaSamples: media})
			r.pending = append([]int16(nil), frame.Samples...)
			if gap || frame.Discontinuous {
				// Queued audio was discarded: the new frame does not
				// continue the last sample written.
				r.declick.start(r.lastOut)
			}
		}
		n := min(len(r.pending), r.ahead-queued)
		r.declick.apply(r.pending[:n])
		n = r.ring.Write(r.pending[:n])
		if n == 0 {
			break
		}
		r.stats.noteWritten(r.pending[:n]) // DEBUG-TEMP
		if r.onRender != nil {
			r.history = append(r.history, r.pending[:n]...)
		}
		r.lastOut = r.pending[n-1]
		r.pending = r.pending[n:]
		st.Written += uint64(n)
		queued += n
	}
}

func (r *ringRenderer) ch() int { return max(1, r.channels) }

// renderedSince hands onRender what the device played since the last step:
// the silence it played on running dry, then the samples it took. Their
// order within a step is approximate; the count is exact, which is what
// keeps the echo canceller's reference in step with the microphone.
func (r *ringRenderer) renderedSince(st RingState) {
	if d := int(st.DrySamples - r.dry); d > 0 {
		r.onRender(make([]int16, d))
	}
	r.dry = st.DrySamples
	if d := min(int(st.Played-r.played), len(r.history)); d > 0 {
		r.onRender(r.history[:d])
		r.history = append(r.history[:0], r.history[d:]...)
	}
}

// traceAndClock moves the speaker's media clock to what the device has taken
// and notes the frames it started.
func (r *ringRenderer) traceAndClock(st RingState) {
	// Frames played through are of no more use, but the one playing.
	for len(r.frames) > 1 && r.frames[1].start <= st.Played {
		r.noteStarted(&r.frames[0], st)
		r.frames = r.frames[1:]
	}
	if len(r.frames) == 0 {
		return
	}
	if r.frames[0].start <= st.Played {
		r.noteStarted(&r.frames[0], st)
	}
	if r.samplesPerMediaTick <= 0 {
		return
	}
	tickAt := func(pos uint64) (uint32, bool) {
		for i := len(r.frames) - 1; i >= 0; i-- {
			f := r.frames[i]
			if f.start <= pos {
				into := min(int(pos-f.start), f.length) / r.ch()
				return uint32(f.index) + uint32(mediaSamplePosition(into, f.length/r.ch(), f.mediaSamples)/r.samplesPerMediaTick), true
			}
		}
		return 0, false
	}
	end, ok := tickAt(st.Played)
	if !ok {
		return
	}
	s := r.speaker
	if base, ok := tickAt(st.CallbackPos); ok && !st.CallbackAt.IsZero() {
		s.baseTick.Store(base)
		s.baseAt.Store(st.CallbackAt.UnixNano())
	}
	s.playbackTick.Store(end)
	s.playbackSet.Store(true)
}

func (r *ringRenderer) noteStarted(f *ringFrame, st RingState) {
	if f.started {
		return
	}
	f.started = true
	r.lastIdx = f.index
	tracePlay(PlayEvent{Kind: PlayStarted, Index: f.index, At: time.Now(), Samples: f.length / r.ch()}) // DEBUG-TEMP
}

// release returns what was queued but never played to the queue's sample
// count (see queueRenderer.release); called once the unit has stopped.
func (r *ringRenderer) release() {
	r.in.consumed(int(r.final.Written-r.played) + len(r.pending))
	r.pending = nil
}
