package udp

import (
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/ui"
)

// audioPlayout turns received speech packets into a continuous PCM stream.
//
// The speaker's consumption is the playout clock. Whenever its queue runs
// low and the packet for the next slot has not arrived (network jitter, or
// DTX silence where the sender transmits nothing), the slot is concealed by
// the decoder: packet loss concealment during speech, comfort noise after a
// SID frame. The speaker therefore never has to insert hard zeros in the
// middle of a call, which is what made audio crackle. A packet that arrives
// after its slot was concealed is dropped.
type audioPlayout struct {
	dec          audioDecoder
	frameSamples int
	out          *device.PlaybackQueue
	scratch      []int16

	started   bool   // nextIndex is valid
	nextIndex uint16 // media index of the next slot handed to the speaker
	ready     bool   // jitter buffer primed; frames go straight to the speaker
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
	maxQueued    int
	targetQueued int
}

func newAudioPlayout(dec audioDecoder, frameSamples int, out *device.PlaybackQueue) *audioPlayout {
	return &audioPlayout{
		dec:          dec,
		frameSamples: frameSamples,
		out:          out,
		scratch:      make([]int16, frameSamples),
		lossWindow:   time.Now(),
		maxQueued:    frameSamples * audioQueueMaxFrames,
		targetQueued: frameSamples * audioQueueTargetFrames,
	}
}

// reset drops all playout state, e.g. when playback is paused.
func (p *audioPlayout) reset() {
	p.started, p.ready, p.haveLast = false, false, false
	p.pre, p.preSample = p.pre[:0], 0
	p.concealed = 0
	p.out.Clear()
}

// rebuffer stops feeding the speaker until the jitter buffer is primed again.
func (p *audioPlayout) rebuffer() {
	p.ready = false
	p.pre, p.preSample = p.pre[:0], 0
}

func (p *audioPlayout) onPacket(index uint16, payload []byte, now time.Time) {
	if p.started && now.Sub(p.lastAt) >= time.Duration(mediaIndexHalf)*mediaTickDuration {
		// The 15-bit index is ambiguous after half a cycle without media.
		p.reset()
	}
	p.lastAt = now

	isSID := p.dec.IsDTXFrame(payload)
	if p.haveLast {
		d := mediaIndexDelta(index, p.lastIndex)
		if d <= 0 {
			return // duplicate or reordered
		}
		p.recordLoss(d, p.lastSID, now)
	}
	p.haveLast, p.lastIndex, p.lastSID = true, index, isSID

	if !p.started {
		p.started, p.nextIndex = true, index
	}
	gap := mediaIndexDelta(index, p.nextIndex)
	if gap < 0 {
		logs.LogV("[voice/udp] drop late audio packet (%d ms after its slot was concealed)", int(-gap)*int(mediaTickDuration/time.Millisecond))
		return
	}
	frames := int((gap + audioFrameTicks/2) / audioFrameTicks)
	if frames > audioMaxConsecutivePLCs {
		// A long silence or outage: restart at the live edge.
		p.out.Clear()
		p.rebuffer()
		frames = 0
	}
	for i := 0; i < frames && p.conceal(); i++ {
	}
	p.nextIndex = index // absorb timestamp rounding

	written, err := p.dec.DecodeFrame(payload, p.scratch)
	if err != nil || written <= 0 {
		logs.LogV("[voice/udp] %s decode error: produced %d samples (len=%d): %v", audioCodec, written, len(payload), err)
		p.conceal()
		return
	}
	p.concealed = 0
	p.push(p.scratch[:min(written, p.frameSamples)])
}

// onTick conceals the next slot when the speaker is about to run dry.
func (p *audioPlayout) onTick() {
	if !p.started || !p.ready || p.out.QueuedSamples() >= p.frameSamples {
		return
	}
	if !p.conceal() && p.out.QueuedSamples() == 0 {
		// The peer stopped sending (muted, paused or gone). Prime the buffer
		// again before resuming so speech does not start with an underrun.
		p.rebuffer()
	}
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
	// burst would otherwise stay queued and delay all further speech.
	if dropped := p.out.TrimLatency(p.maxQueued, p.targetQueued); dropped > 0 {
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
