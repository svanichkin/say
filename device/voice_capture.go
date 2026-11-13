package device

import "time"

// systemVoiceCapture assembles the same 20 ms frames the audio encoder needs.
// Native callbacks can deliver smaller blocks; waking the sender for each one
// only makes it copy and wait again. The callback owns this state, and each
// emitted frame owns its samples until the receiver is done with them.
type systemVoiceCapture struct {
	sampleRate  int
	frameSize   int
	pending     []int16
	firstSample uint64
	epoch       time.Time
	out         chan AudioCaptureFrame
	// clock, if set, is checked against the sample count (see captureGap).
	clock func() time.Time
	// latency is how long sound takes from the microphone to write (the
	// system's input path): the samples are stamped that much earlier, when
	// they were spoken, as video frames are stamped when they were seen.
	latency time.Duration
}

func (c *systemVoiceCapture) write(samples []int16) {
	if len(samples) == 0 {
		return
	}
	clock := time.Now
	if c.clock != nil {
		clock = c.clock
	}
	now := func() time.Time { return clock().Add(-c.latency) }
	if c.clock != nil {
		next := c.firstSample + uint64(len(c.pending))
		if gap := captureGap(c.epoch, next, len(samples), c.sampleRate, now()); gap > 0 {
			// The unit delivered nothing for a while: the samples on hand
			// end before the gap, start a new frame after it.
			c.firstSample = next + gap
			c.pending = c.pending[:0]
		}
	}
	if c.epoch.IsZero() {
		c.epoch = now().Add(-time.Duration(len(samples)) * time.Second / time.Duration(c.sampleRate))
	}
	for len(samples) > 0 {
		if c.pending == nil {
			c.pending = make([]int16, 0, c.frameSize)
		}
		n := min(c.frameSize-len(c.pending), len(samples))
		c.pending = append(c.pending, samples[:n]...)
		samples = samples[n:]
		if len(c.pending) < c.frameSize {
			continue
		}
		frame := AudioCaptureFrame{
			Samples:     c.pending,
			CapturedAt:  c.epoch.Add(time.Duration(c.firstSample) * time.Second / time.Duration(c.sampleRate)),
			FirstSample: c.firstSample,
		}
		c.pending = nil
		// Count dropped frames too, so downstream can detect a discontinuity.
		c.firstSample += uint64(c.frameSize)
		select {
		case c.out <- frame:
		default:
			select {
			case <-c.out:
			default:
			}
			select {
			case c.out <- frame:
			default:
			}
		}
	}
}
