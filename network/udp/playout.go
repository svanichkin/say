package udp

import (
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

// audioPlayout turns received speech packets into a continuous PCM stream.
//
// Packets wait in a small reorder buffer, sorted by media index, and are
// decoded strictly in order. A missing slot is concealed only when it falls
// due: when the speaker's queue drops below one device period (or, while the
// jitter buffer is still priming, once the initial wait expires or a full
// lead of later audio has arrived). Until then a reordered packet can still
// fill it. Concealment is
// packet loss concealment during speech and comfort noise after a SID frame,
// so the speaker does not run dry in the middle of a call. A packet that
// arrives after its slot was concealed is dropped.
type audioPlayout struct {
	dec          audioDecoder
	frameSamples int
	out          *device.PlaybackQueue
	scratch      []int16

	started       bool   // nextIndex is valid
	nextIndex     uint16 // media index of the next slot handed to the speaker
	pending       []queuedPacket
	ready         bool // jitter buffer primed; frames go straight to the speaker
	pre           []device.PlaybackFrame
	preSample     int
	concealed     int // consecutive concealed slots
	lastAt        time.Time
	primeDeadline time.Time

	lateIndex uint16
	lateCount int
	lateSince time.Time

	// Loss statistics, based on received packet timestamps.
	haveLast     bool
	lastIndex    uint16
	lastSID      bool
	lossExpected uint64
	lossMissing  uint64
	lossWindow   time.Time

	// Jitter buffer lead in frames: the profile or -audio value at the
	// start (baseLead), raised by late packets (see onLate).
	lead       int
	baseLead   int
	lastLate   time.Time
	lastShrink time.Time
	level      float64 // smoothed PCM queue occupancy for clock drift correction
	missed     [audioBufferMaxFrames]missedSlot
	missedHead int
	lateEvents int64 // packets that arrived after their slot was concealed

	lowWater     int // samples; conceal below this
	maxQueued    int
	targetQueued int
}

type missedSlot struct {
	index uint16
	valid bool
}

type queuedPacket struct {
	index   uint16
	payload []byte
}

func newAudioPlayout(dec audioDecoder, frameSamples int, out *device.PlaybackQueue) *audioPlayout {
	return &audioPlayout{
		dec:          dec,
		frameSamples: frameSamples,
		out:          out,
		scratch:      make([]int16, frameSamples),
		lossWindow:   time.Now(),
		lowWater:     frameSamples * audioPlayoutLowFrames,
		lead:         audioBufferLeadFrames,
		baseLead:     audioBufferLeadFrames,
		maxQueued:    frameSamples * audioQueueMaxFrames,
		targetQueued: frameSamples * audioQueueTargetFrames,
	}
}

// reset drops all playout state, e.g. when playback is paused.
func (p *audioPlayout) reset() {
	p.started, p.haveLast = false, false
	p.pending = p.pending[:0]
	p.concealed = 0
	p.lateCount = 0
	p.level = 0
	p.missed = [audioBufferMaxFrames]missedSlot{}
	p.out.Clear()
	p.rebuffer()
}

// rebuffer stops feeding the speaker until the jitter buffer is primed again.
func (p *audioPlayout) rebuffer() {
	p.level = 0
	p.missed = [audioBufferMaxFrames]missedSlot{}
	p.ready = false
	p.pre, p.preSample = p.pre[:0], 0
	p.primeDeadline = time.Time{}
}

func (p *audioPlayout) onPacket(index uint16, payload []byte, now time.Time) {
	if p.started && now.Sub(p.lastAt) >= time.Duration(mediaIndexHalf)*mediaTickDuration {
		// The 15-bit index is ambiguous after half a cycle without media.
		p.reset()
	}
	p.lastAt = now
	if p.started && mediaIndexDelta(index, p.nextIndex) < -audioFrameTicks/2 {
		// A delayed packet from the preceding call can establish the wrong
		// initial timeline. Recover only while not playing, after observing a
		// sustained, advancing sequence; ordinary duplicates/reordering must
		// not move the playback clock backwards.
		d := mediaIndexDelta(index, p.lateIndex)
		if p.lateCount == 0 || d <= 0 || d > audioMaxConsecutivePLCs*audioFrameTicks {
			p.lateCount, p.lateSince = 1, now
		} else {
			p.lateCount++
		}
		p.lateIndex = index
		if !p.ready && p.out.QueuedSamples() == 0 && p.lateCount >= 3 && now.Sub(p.lateSince) >= 80*time.Millisecond {
			logs.LogV("[voice/udp] reanchor idle playback from index %d to %d after %d advancing late packets", p.nextIndex, index, p.lateCount)
			p.reset()
		} else {
			// Only an actually concealed slot counts as late; a decoded
			// duplicate must not.
			if p.takeMissed(index) {
				p.lateEvents++
				p.onLate(now)
			}
			return
		}
	}
	p.lateCount = 0

	isSID := p.dec.IsDTXFrame(payload)
	var receivedGap int32
	if p.haveLast {
		receivedGap = mediaIndexDelta(index, p.lastIndex)
		switch d := receivedGap; {
		case d > 0:
			p.recordLoss(d, p.lastSID, now)
			p.lastIndex, p.lastSID = index, isSID
		case d < 0 && p.lossMissing > 0:
			p.lossMissing-- // a reordered packet was counted as missing
		}
	} else {
		p.haveLast, p.lastIndex, p.lastSID = true, index, isSID
	}

	if !p.started {
		p.started, p.nextIndex = true, index
	}
	rel := mediaIndexDelta(index, p.nextIndex)
	if rel > audioMaxConsecutivePLCs*audioFrameTicks && receivedGap > audioMaxConsecutivePLCs*audioFrameTicks {
		// A long silence or outage: restart at the live edge.
		// Compare received packets too: during DTX, nextIndex can lag behind
		// perfectly regular SID updates while their playout slots are not due.
		p.pending = p.pending[:0]
		p.out.Clear()
		p.rebuffer()
		p.concealed = 0
		p.nextIndex = index
	}
	if !p.ready && p.primeDeadline.IsZero() {
		p.primeDeadline = now.Add(time.Duration(p.lead*audioFrameMillis) * time.Millisecond)
	}
	p.insert(queuedPacket{index: index, payload: append([]byte(nil), payload...)})
	p.pump(now)
}

// Adaptive jitter buffer. The lead starts small (the profile's), so a good
// path has little delay; a packet that arrives after its slot was concealed
// raises it by a frame at once, up to audioLeadAdaptMaxFrames, and a path
// without late packets for audioLeadShrinkAfter lowers it a frame at a time,
// back to the start.
const (
	audioLeadAdaptMaxFrames = 10 // 200 ms
	audioLeadShrinkAfter    = 10 * time.Second
)

// onLate raises the lead after a late packet. The extra frame of audio goes
// into the queue at once (concealment, so speech continues naturally), with
// almost no media time: the playback clock, which video follows, holds
// while it plays, and every later packet has a frame more to arrive.
func (p *audioPlayout) onLate(now time.Time) {
	p.lastLate = now
	if !p.ready || p.lead >= max(p.baseLead, audioLeadAdaptMaxFrames) {
		return
	}
	p.lead++
	written, err := p.dec.Conceal(p.scratch)
	if err != nil || written <= 0 {
		clear(p.scratch)
		written = p.frameSamples
	}
	samples := make([]int16, p.frameSamples)
	copy(samples, p.scratch[:min(written, p.frameSamples)])
	p.out.Push(device.PlaybackFrame{Samples: samples, MediaIndex: p.nextIndex, MediaSamples: 1, Discontinuous: true})
	p.level = 0 // the drift correction starts over at the new level
	logs.LogV("[voice/udp] late packet: jitter buffer %d ms", p.lead*audioFrameMillis)
}

// shrinkLead lowers a raised lead after a while without late packets. The
// drift correction then drains the queue towards the lower target.
func (p *audioPlayout) shrinkLead(now time.Time) {
	if p.lead <= p.baseLead || now.Sub(p.lastLate) < audioLeadShrinkAfter || now.Sub(p.lastShrink) < audioLeadShrinkAfter {
		return
	}
	p.lead--
	p.lastShrink = now
	logs.LogV("[voice/udp] no late packets: jitter buffer %d ms", p.lead*audioFrameMillis)
}

// insert adds a packet to the reorder buffer, keeping it sorted by index.
func (p *audioPlayout) insert(pkt queuedPacket) {
	rel := mediaIndexDelta(pkt.index, p.nextIndex)
	i := len(p.pending)
	for i > 0 {
		prev := mediaIndexDelta(p.pending[i-1].index, p.nextIndex)
		if prev == rel {
			return // duplicate
		}
		if prev < rel {
			break
		}
		i--
	}
	p.pending = append(p.pending, queuedPacket{})
	copy(p.pending[i+1:], p.pending[i:])
	p.pending[i] = pkt
}

// pump hands slots to the speaker in order: decoded packets as soon as they
// are next, concealment for a missing slot only once it is due.
func (p *audioPlayout) pump(now time.Time) {
	for len(p.pending) > 0 {
		head := p.pending[0]
		rel := mediaIndexDelta(head.index, p.nextIndex)
		switch {
		case rel < -audioFrameTicks/2:
			p.pending = p.pending[1:] // its slot was concealed meanwhile
		case rel <= audioFrameTicks/2:
			p.pending = p.pending[1:]
			p.nextIndex = head.index // absorb timestamp rounding
			p.decode(head.payload)
		default:
			if !p.slotDue(now) {
				return
			}
			if !p.conceal() {
				// Concealment budget spent: continue at the waiting packet.
				p.nextIndex = head.index
			}
		}
	}
}

// slotDue reports whether a missing slot must be concealed now.
func (p *audioPlayout) slotDue(now time.Time) bool {
	if p.ready {
		return p.out.QueuedSamples() < p.lowWater
	}
	if !p.primeDeadline.IsZero() && !now.Before(p.primeDeadline) {
		return true
	}
	// While priming nothing plays yet; give reordered packets a full lead
	// of later audio to arrive before declaring the slot lost.
	newest := p.pending[len(p.pending)-1].index
	return mediaIndexDelta(newest, p.nextIndex) >= int32(p.lead*audioFrameTicks)
}

// onTick conceals the next slot when the speaker is about to run dry.
func (p *audioPlayout) onTick() {
	p.onTickAt(time.Now())
}

func (p *audioPlayout) onTickAt(now time.Time) {
	if !p.started {
		return
	}
	p.shrinkLead(now)
	p.pump(now)
	// DTX deliberately sends no packets for most 20 ms slots. Prime by
	// elapsed time, not by waiting for a full buffer of later packets.
	if !p.ready && !p.primeDeadline.IsZero() && !now.Before(p.primeDeadline) {
		for !p.ready && p.conceal() {
		}
	}
	if !p.ready || len(p.pending) > 0 || p.out.QueuedSamples() >= p.lowWater {
		return
	}
	// Nothing received for the next slot (jitter, or DTX silence).
	if !p.conceal() && p.out.QueuedSamples() == 0 {
		// The peer stopped sending (muted, paused or gone). Prime the buffer
		// again before resuming so speech does not start with an underrun.
		p.rebuffer()
	}
}

func (p *audioPlayout) decode(payload []byte) {
	p.takeMissed(p.nextIndex) // retire history from an earlier timestamp cycle
	written, err := p.dec.DecodeFrame(payload, p.scratch)
	if err != nil || written <= 0 {
		logs.LogV("[voice/udp] %s decode error: produced %d samples (len=%d): %v", audioCodec, written, len(payload), err)
		p.conceal()
		return
	}
	p.concealed = 0
	p.push(p.scratch[:min(written, p.frameSamples)])
}

// conceal fills the next slot from the decoder. It returns false once the
// concealment budget is spent.
func (p *audioPlayout) conceal() bool {
	if p.concealed >= audioMaxConsecutivePLCs {
		return false
	}
	p.takeMissed(p.nextIndex)
	p.missed[p.missedHead] = missedSlot{index: p.nextIndex, valid: true}
	p.missedHead = (p.missedHead + 1) % len(p.missed)
	p.concealed++
	written, err := p.dec.Conceal(p.scratch)
	if err != nil || written <= 0 {
		clear(p.scratch)
		written = p.frameSamples
	}
	p.push(p.scratch[:min(written, p.frameSamples)])
	return true
}

// push queues one decoded frame (copied) for the next slot.
func (p *audioPlayout) push(pcm []int16) {
	samples := make([]int16, p.frameSamples)
	copy(samples, pcm)
	index := p.nextIndex
	p.nextIndex = advanceMediaIndex(p.nextIndex, audioFrameTicks)
	if p.ready {
		// Average the device-period sawtooth over several seconds. A tiny
		// continuous rate correction compensates independent audio clocks.
		q := p.out.QueuedSamples()
		delta := 0
		level := float64(q + len(samples))
		if p.level == 0 {
			p.level = level
		}
		p.level += (level - p.level) / 200
		target := float64(max(p.lead*p.frameSamples, p.lowWater+p.frameSamples))
		band := float64(p.frameSamples) / 2
		if p.level > target+band && q > p.lowWater {
			delta = -1
		}
		if p.level < target-band {
			delta = 1
		}
		if delta != 0 {
			samples = resizePCM(samples, len(samples)+delta)
		}
	}
	frame := device.PlaybackFrame{Samples: samples, MediaIndex: index, MediaSamples: p.frameSamples}

	if !p.ready {
		p.pre = append(p.pre, frame)
		p.preSample += len(samples)
		if p.preSample < p.frameSamples*p.lead {
			return
		}
		for _, f := range p.pre {
			p.out.Push(f)
		}
		p.pre, p.preSample = p.pre[:0], 0
		p.ready = true
	} else {
		p.out.Push(frame)
	}
	// Bound latency by queued audio time: after a network stall the delayed
	// burst would otherwise stay queued and delay all further speech. The
	// speaker crossfades over the resulting discontinuity.
	if dropped := p.out.TrimLatency(p.maxQueued, p.frameSamples*p.lead); dropped > 0 {
		p.level = 0
		logs.LogV("[voice/udp] drop %d stale audio frames (playback latency over %d ms)",
			dropped, audioQueueMaxFrames*audioFrameMillis)
	}
}

// takeMissed consumes a recently concealed slot once. Decoded duplicates and
// repeats of a late packet are not counted again.
func (p *audioPlayout) takeMissed(index uint16) bool {
	found := false
	for i := range p.missed {
		slot := &p.missed[i]
		if slot.valid && absIndexDelta(index, slot.index) <= audioFrameTicks/2 {
			slot.valid = false
			found = true
		}
	}
	return found
}

func absIndexDelta(a, b uint16) int32 {
	d := mediaIndexDelta(a, b)
	if d < 0 {
		return -d
	}
	return d
}

// isQuiet reports whether a frame is near-silent (pause between words).
func isQuiet(samples []int16) bool {
	var acc int64
	for _, v := range samples {
		acc += int64(v) * int64(v)
	}
	return acc < int64(len(samples))*audioQuietRMS*audioQuietRMS
}

func (p *audioPlayout) recordLoss(delta int32, afterSID bool, now time.Time) {
	expected := audioPacketsExpected(delta, afterSID)
	p.lossExpected += uint64(expected)
	p.lossMissing += uint64(expected - 1)
	if now.Sub(p.lossWindow) >= time.Second {
		loss := 0.0
		if p.lossExpected > 0 {
			loss = float64(p.lossMissing) / float64(p.lossExpected)
		}
		ui.UpdateRxAudioLoss(loss)
		p.lossExpected, p.lossMissing = 0, 0
		p.lossWindow = now
	}
}
