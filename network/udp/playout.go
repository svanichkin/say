package udp

import (
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/diag"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

// audioPlayout turns received speech packets into a continuous PCM stream.
//
// Packets wait in a small reorder buffer, sorted by media index, and are
// decoded strictly in order. A missing slot is concealed only when it falls
// due: when the speaker's queue drops below one device period (or, while the
// jitter buffer is still priming, once a full lead of later audio has
// arrived). Until then a reordered packet can still fill it. Concealment is
// packet loss concealment during speech and comfort noise after a SID frame,
// so the speaker does not run dry in the middle of a call. A packet that
// arrives after its slot was concealed is dropped.
type audioPlayout struct {
	dec          audioDecoder
	frameSamples int
	out          *device.PlaybackQueue
	scratch      []int16

	started   bool   // nextIndex is valid
	nextIndex uint16 // media index of the next slot handed to the speaker
	pending   []queuedPacket
	ready     bool // jitter buffer primed; frames go straight to the speaker
	pre       []device.PlaybackFrame
	preSample int
	concealed int // consecutive concealed slots
	lastAt    time.Time

	// Loss statistics, based on received packet timestamps.
	haveLast     bool
	lastIndex    uint16
	lastSID      bool
	lossExpected uint64
	lossMissing  uint64
	lossWindow   time.Time

	lowWater     int // samples; conceal below this
	maxQueued    int
	targetQueued int
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
		maxQueued:    frameSamples * audioQueueMaxFrames,
		targetQueued: frameSamples * audioQueueTargetFrames,
	}
}

// reset drops all playout state, e.g. when playback is paused.
func (p *audioPlayout) reset() {
	p.started, p.haveLast = false, false
	p.pending = p.pending[:0]
	p.concealed = 0
	p.out.Clear()
	p.rebuffer()
}

// rebuffer stops feeding the speaker until the jitter buffer is primed again.
func (p *audioPlayout) rebuffer() {
	p.ready = false
	p.pre, p.preSample = p.pre[:0], 0
}

func (p *audioPlayout) onPacket(index uint16, payload []byte, now time.Time) {
	diag.RxPackets.Inc()
	if p.started && now.Sub(p.lastAt) >= time.Duration(mediaIndexHalf)*mediaTickDuration {
		// The 15-bit index is ambiguous after half a cycle without media.
		p.reset()
	}
	p.lastAt = now

	isSID := p.dec.IsDTXFrame(payload)
	if p.haveLast {
		switch d := mediaIndexDelta(index, p.lastIndex); {
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
	if rel < -audioFrameTicks/2 {
		diag.RxLate.Inc()
		logs.LogV("[voice/udp] drop late audio packet (%d ms after its slot was concealed)", int(-rel)*int(mediaTickDuration/time.Millisecond))
		return
	}
	if rel > audioMaxConsecutivePLCs*audioFrameTicks {
		// A long silence or outage: restart at the live edge.
		diag.RxResets.Inc()
		p.pending = p.pending[:0]
		p.out.Clear()
		p.rebuffer()
		p.concealed = 0
		p.nextIndex = index
		rel = 0
	}
	p.insert(queuedPacket{index: index, payload: append([]byte(nil), payload...)})
	p.pump()
}

// insert adds a packet to the reorder buffer, keeping it sorted by index.
func (p *audioPlayout) insert(pkt queuedPacket) {
	rel := mediaIndexDelta(pkt.index, p.nextIndex)
	i := len(p.pending)
	for i > 0 {
		prev := mediaIndexDelta(p.pending[i-1].index, p.nextIndex)
		if prev == rel {
			diag.RxDuplicates.Inc()
			return // duplicate
		}
		if prev < rel {
			break
		}
		i--
	}
	if i < len(p.pending) {
		diag.RxReordered.Inc()
	}
	p.pending = append(p.pending, queuedPacket{})
	copy(p.pending[i+1:], p.pending[i:])
	p.pending[i] = pkt
	diag.RxPendingMax.Observe(int64(len(p.pending)))
}

// pump hands slots to the speaker in order: decoded packets as soon as they
// are next, concealment for a missing slot only once it is due.
func (p *audioPlayout) pump() {
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
			if !p.slotDue() {
				return
			}
			diag.RxConcealGap.Inc()
			if !p.conceal() {
				diag.RxBudgetOut.Inc()
				// Concealment budget spent: continue at the waiting packet.
				p.nextIndex = head.index
			}
		}
	}
}

// slotDue reports whether a missing slot must be concealed now.
func (p *audioPlayout) slotDue() bool {
	if p.ready {
		return p.out.QueuedSamples() < p.lowWater
	}
	// While priming nothing plays yet; give reordered packets a full lead
	// of later audio to arrive before declaring the slot lost.
	newest := p.pending[len(p.pending)-1].index
	return mediaIndexDelta(newest, p.nextIndex) >= audioBufferLeadFrames*audioFrameTicks
}

// onTick conceals the next slot when the speaker is about to run dry.
func (p *audioPlayout) onTick() {
	if !p.started {
		return
	}
	q := int64(p.out.QueuedSamples() / 8) // ms at 8 kHz
	diag.RxQueueMinMs.Observe(q)
	diag.RxQueueMaxMs.Observe(q)
	p.pump()
	if !p.ready || len(p.pending) > 0 || p.out.QueuedSamples() >= p.lowWater {
		return
	}
	// Nothing received for the next slot (jitter, or DTX silence).
	diag.RxConcealTick.Inc()
	if !p.conceal() && p.out.QueuedSamples() == 0 {
		diag.RxRebuffers.Inc()
		// The peer stopped sending (muted, paused or gone). Prime the buffer
		// again before resuming so speech does not start with an underrun.
		p.rebuffer()
	}
}

func (p *audioPlayout) decode(payload []byte) {
	written, err := p.dec.DecodeFrame(payload, p.scratch)
	if err != nil || written <= 0 {
		diag.RxDecodeErrors.Inc()
		logs.LogV("[voice/udp] %s decode error: produced %d samples (len=%d): %v", audioCodec, written, len(payload), err)
		p.conceal()
		return
	}
	p.concealed = 0
	diag.RxDecoded.Inc()
	p.push(p.scratch[:min(written, p.frameSamples)])
}

// conceal fills the next slot from the decoder. It returns false once the
// concealment budget is spent.
func (p *audioPlayout) conceal() bool {
	if p.concealed >= audioMaxConsecutivePLCs {
		return false
	}
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
	frame := device.PlaybackFrame{Samples: samples, MediaIndex: p.nextIndex}
	p.nextIndex = advanceMediaIndex(p.nextIndex, audioFrameTicks)

	if !p.ready {
		p.pre = append(p.pre, frame)
		p.preSample += len(samples)
		if p.preSample < p.frameSamples*audioBufferLeadFrames {
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
	if dropped := p.out.TrimLatency(p.maxQueued, p.targetQueued); dropped > 0 {
		diag.RxTrimmed.Add(int64(dropped))
		logs.LogV("[voice/udp] drop %d stale audio frames (playback latency over %d ms)",
			dropped, audioQueueMaxFrames*audioFrameMillis)
	}
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
