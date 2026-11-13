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

	started    bool   // nextIndex is valid
	nextIndex  uint16 // media index of the next slot handed to the speaker
	pending    []queuedPacket
	ready      bool // jitter buffer primed; frames go straight to the speaker
	pre        []device.PlaybackFrame
	preSample  int
	concealed  int        // consecutive concealed slots
	soundLevel levelMeter // DEBUG-TEMP: what is played, logged each second
	// The delay the packets met as they arrived, over the last
	// audioLagWindow, and what of it is to be shed (see trackLag).
	arrivals  []arrivalLag
	lagExcess int // samples
	// DEBUG-TEMP: what catching up did since the last summary (logLag).
	lagLogAt                         time.Time
	shedSlots, shedQuiet, shedSpeech int  // slots, frames, samples
	skipped                          bool // a slot was skipped: the next frame does not continue the last
	// hist: the last samples handed to the speaker, where accelerate looks
	// for the pitch period; accelCredit: samples it may cut, earned frame
	// by frame (audioAccelPermille), so speech never runs too fast.
	hist          []int16
	accelCredit   int
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
	lead     int
	baseLead int
	catchUp  int // samples of extra audio still to drain after the lead came down
	// backlogMin and backlogFrames: the lowest queue level in the current
	// window and the frames seen in it (see audioCatchUpWindowFrames).
	backlogMin    int
	backlogFrames int
	lastLate      time.Time
	lastShrink    time.Time
	level         float64 // smoothed PCM queue occupancy for clock drift correction
	missed        [audioBufferMaxFrames]missedSlot
	missedHead    int
	lateEvents    int64 // packets that arrived after their slot was concealed

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

// playoutRingMarginMs is device.RingMarginMs: the device plays from a ring
// that goroutines fill, so the playout keeps that much more queued than
// the device's period. (Tests of a speaker that takes straight from the
// queue set it to 0.)
var playoutRingMarginMs = device.RingMarginMs

func newAudioPlayout(dec audioDecoder, frameSamples int, out *device.PlaybackQueue) *audioPlayout {
	startVoiceTrace() // DEBUG-TEMP
	return &audioPlayout{
		dec:          dec,
		frameSamples: frameSamples,
		out:          out,
		scratch:      make([]int16, frameSamples),
		lossWindow:   time.Now(),
		soundLevel:   levelMeter{name: "peer's voice, as played"}, // DEBUG-TEMP
		// One device period, and the margin the device's ring needs to
		// play on while Go is held up (playoutRingMarginMs).
		lowWater:     frameSamples*audioPlayoutLowFrames + audioSampleRate*playoutRingMarginMs/1000,
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
	p.catchUp = 0
	p.backlogFrames = 0
	p.missed = [audioBufferMaxFrames]missedSlot{}
	p.ready = false
	p.pre, p.preSample = p.pre[:0], 0
	p.primeDeadline = time.Time{}
	p.arrivals, p.lagExcess = p.arrivals[:0], 0
	p.hist, p.accelCredit = p.hist[:0], 0
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
				p.onLate(now, index, payload)
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
			if missing := audioPacketsExpected(d, p.lastSID) - 1; missing > 0 { // DEBUG-TEMP
				logs.LogV("[debug] voice: %d packets lost before index %d (%d ms without audio)", missing, index, d*audioFrameMillis/audioFrameTicks)
			}
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
		logs.LogV("[debug] voice: outage of %d ms, playback restarts at the live edge", receivedGap*audioFrameMillis/audioFrameTicks) // DEBUG-TEMP
		p.pending = p.pending[:0]
		p.out.Clear()
		p.rebuffer()
		p.concealed = 0
		p.nextIndex = index
	}
	if !p.ready && p.primeDeadline.IsZero() {
		p.primeDeadline = now.Add(time.Duration(p.lead*audioFrameMillis) * time.Millisecond)
	}
	p.noteArrival(index, now)
	p.insert(queuedPacket{index: index, payload: append([]byte(nil), payload...)})
	p.pump(now)
}

// Adaptive jitter buffer, as players like WebRTC's NetEq do it: nothing
// that arrives is thrown away, the playback time stretches instead. The lead
// starts small (the profile's), so a good path has little delay. A packet
// that arrives after its slot was concealed is played right after that
// concealment (speech stretched by a frame, not lost) and raises the lead
// by a frame, up to audioLeadAdaptMaxFrames. After audioLeadShrinkAfter
// without late packets the lead comes down a frame per audioLeadShrinkEvery,
// and push drains the extra audio by skipping quiet frames (pauses between
// words) and playing speech faster (accelerate), never by dropping speech.
const (
	// audioLeadAdaptMaxFrames: 600 ms. A path over loaded public relays
	// delays packets by hundreds of ms at times.
	audioLeadAdaptMaxFrames = 30
	audioLeadShrinkAfter    = 5 * time.Second
	audioLeadShrinkEvery    = time.Second
	// audioCatchUpPermille: how much faster speech that has no pitch period
	// to cut (unvoiced, noisy) plays while catching up, by resampling (25:
	// 2.5%, hardly audible).
	audioCatchUpPermille = 25
	// audioCatchUpStartFrames: extra audio in the queue, beyond the target,
	// that starts the catch-up by itself, wherever it came from: frames that
	// piled up while the speaker was starting (the system's voice unit opens
	// seconds into a call), or a stall shorter than the trim threshold. The
	// clock-drift correction alone takes minutes over it, and video, which
	// follows the audio clock, would lag as long.
	audioCatchUpStartFrames = 2
	// audioCatchUpWindowFrames: the window (1 s) whose lowest queue level
	// tells extra audio from the sawtooth of the speaker's periods.
	audioCatchUpWindowFrames = 50
)

// onLate handles a packet that arrived after its slot was concealed: the
// playback clock (which video follows) holds while the extra audio plays.
func (p *audioPlayout) onLate(now time.Time, index uint16, payload []byte) {
	traceVoice(index, "arrived after its slot was made up") // DEBUG-TEMP
	p.lastLate = now
	if !p.ready {
		return
	}
	grow := p.lead < max(p.baseLead, audioLeadAdaptMaxFrames)
	if grow {
		p.lead++
	}
	var samples []int16
	if d := mediaIndexDelta(p.nextIndex, index); d >= audioFrameTicks/2 && d <= audioFrameTicks+audioFrameTicks/2 {
		// Its slot was the last one queued: its audio follows the
		// concealment that stood in for it.
		if written, err := p.dec.DecodeFrame(payload, p.scratch); err == nil && written > 0 {
			samples = make([]int16, p.frameSamples)
			copy(samples, p.scratch[:min(written, p.frameSamples)])
		}
	}
	if samples == nil {
		// Newer audio is queued already: the packet's audio has no place
		// in order. Only raise the lead (with concealment), if it can.
		if !grow {
			return
		}
		written, err := p.dec.Conceal(p.scratch)
		if err != nil || written <= 0 {
			clear(p.scratch)
			written = p.frameSamples
		}
		samples = make([]int16, p.frameSamples)
		copy(samples, p.scratch[:min(written, p.frameSamples)])
	}
	p.out.Push(device.PlaybackFrame{Samples: samples, MediaIndex: p.nextIndex, MediaSamples: 1, Discontinuous: true})
	p.hist = append(p.hist[:0], samples...)
	p.level = 0 // the drift correction starts over at the new level
	logs.LogV("[voice/udp] late packet: jitter buffer %d ms", p.lead*audioFrameMillis)
}

// shrinkLead lowers a raised lead once the path has been calm for a while.
func (p *audioPlayout) shrinkLead(now time.Time) {
	if p.lead <= p.baseLead || now.Sub(p.lastLate) < audioLeadShrinkAfter || now.Sub(p.lastShrink) < audioLeadShrinkEvery {
		return
	}
	p.lead--
	p.lastShrink = now
	p.catchUp += p.frameSamples
	if p.lead == p.baseLead {
		logs.LogV("[voice/udp] no late packets: jitter buffer back to %d ms", p.lead*audioFrameMillis)
	}
}

// insert adds a packet to the reorder buffer, keeping it sorted by index.
func (p *audioPlayout) insert(pkt queuedPacket) {
	rel := mediaIndexDelta(pkt.index, p.nextIndex)
	i := len(p.pending)
	for i > 0 {
		prev := mediaIndexDelta(p.pending[i-1].index, p.nextIndex)
		if prev == rel {
			traceVoice(pkt.index, "duplicate in the reorder buffer") // DEBUG-TEMP
			return                                                   // duplicate
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
	p.trackLag(now)
	for len(p.pending) > 0 {
		head := p.pending[0]
		rel := mediaIndexDelta(head.index, p.nextIndex)
		switch {
		case rel < -audioFrameTicks/2:
			traceVoice(head.index, "not played: its slot was made up meanwhile") // DEBUG-TEMP
			p.pending = p.pending[1:]                                            // its slot was concealed meanwhile
		case rel <= audioFrameTicks/2:
			p.pending = p.pending[1:]
			p.nextIndex = head.index // absorb timestamp rounding
			p.decode(head.payload)
		default:
			if !p.slotDue(now) {
				return
			}
			if p.skipLag() {
				continue
			}
			if !p.conceal() {
				// Concealment budget spent: continue at the waiting packet.
				logs.LogV("[debug] voice: %d slots concealed in a row, skipping to index %d", p.concealed, head.index) // DEBUG-TEMP
				p.nextIndex = head.index
			}
		}
	}
}

// lag is the whole delay ahead of the next slot: the frames waiting in the
// reorder buffer up to the newest received, and the queue.
func (p *audioPlayout) lag() int {
	behind := 0
	if len(p.pending) > 0 {
		behind = int(max(0, mediaIndexDelta(p.pending[len(p.pending)-1].index, p.nextIndex))) * p.frameSamples / audioFrameTicks
	}
	return behind + p.out.QueuedSamples()
}

// arrivalLag is the delay a packet met: when it arrived, and how long it
// was to wait before playing (samples), less what was shed since.
type arrivalLag struct {
	at  time.Time
	lag int
}

// audioLagWindow: the delay that stays through this long, through the
// packet that waited least, is extra. Long enough to hold a few silence
// updates (one every 160 ms) and to ride out the jitter of a path; short,
// so that a stall's delay is shed half a second after it.
const audioLagWindow = 500 * time.Millisecond

// noteArrival measures the delay a packet meets: the slots ahead of it in
// the reorder buffer and the queue, that is how long after it arrived it
// will start to play.
func (p *audioPlayout) noteArrival(index uint16, now time.Time) {
	if !p.ready {
		return
	}
	ahead := int(max(0, mediaIndexDelta(index, p.nextIndex))) * p.frameSamples / audioFrameTicks
	p.arrivals = append(p.arrivals, arrivalLag{at: now, lag: ahead + p.out.QueuedSamples()})
}

// trackLag finds extra delay: the delay the packets met on arrival
// (noteArrival) over the target through audioLagWindow (the least of it:
// the packet that waited least). It is left by a stall (of the sender, the
// path or this side): the playback clock fell behind the sender's. In
// pauses (DTX) it waits in the reorder buffer, where the packets for the
// next slots are not sent at all, so the buffer between two silence
// updates holds nothing and shows no delay: only the packets show it. In
// speech it is in the queue. skipLag sheds it in pauses, push in speech.
func (p *audioPlayout) trackLag(now time.Time) {
	if !p.ready {
		return
	}
	drop := 0
	for drop < len(p.arrivals) && now.Sub(p.arrivals[drop].at) > audioLagWindow {
		drop++
	}
	p.arrivals = append(p.arrivals[:0], p.arrivals[drop:]...)
	// The window must have been watched whole: the first packets after a
	// stall arrive together and say nothing yet of how the rest will.
	if len(p.arrivals) > 0 && now.Sub(p.arrivals[0].at) >= audioLagWindow/2 {
		least := p.arrivals[0].lag
		for _, a := range p.arrivals[1:] {
			least = min(least, a.lag)
		}
		excess := least - p.target()
		switch {
		case p.lagExcess > 0:
			p.lagExcess = max(0, excess)
		case excess >= audioCatchUpStartFrames*p.frameSamples:
			logs.LogV("[voice/udp] %d ms behind the sender: skipping pauses and playing speech faster to catch up", excess*1000/audioSampleRate)
			p.lagExcess = excess
		}
	}
	p.logLag(now)
}

// logLag logs, once a second, the delay and what catching up did about it
// (DEBUG-TEMP).
func (p *audioPlayout) logLag(now time.Time) {
	if now.Sub(p.lagLogAt) < time.Second {
		return
	}
	p.lagLogAt = now
	ms := func(samples int) int { return samples * 1000 / audioSampleRate }
	least, most := -1, 0
	for _, a := range p.arrivals {
		if least < 0 || a.lag < least {
			least = a.lag
		}
		most = max(most, a.lag)
	}
	logs.LogV("[voice/udp] delay: packets wait %d..%d ms to play (target %d, lead %d), queued %d ms, to shed %d ms; shed in 1 s: %d slots, %d quiet frames, %d ms of speech",
		ms(max(least, 0)), ms(most), ms(p.target()), p.lead*audioFrameMillis, ms(p.out.QueuedSamples()), ms(max(p.lagExcess, p.catchUp)),
		p.shedSlots, p.shedQuiet, ms(p.shedSpeech))
	p.shedSlots, p.shedQuiet, p.shedSpeech = 0, 0, 0
}

// target is the delay the playout keeps: the lead, and at least a frame
// over the low water mark.
func (p *audioPlayout) target() int {
	return max(p.lead*p.frameSamples, p.lowWater+p.frameSamples)
}

// skipLag drops a missing slot that is due instead of concealing it while
// there is extra delay to shed (trackLag). A missing slot is a pause or a
// lost frame: skipping it is not heard.
func (p *audioPlayout) skipLag() bool {
	if !p.ready || p.lagExcess < p.frameSamples {
		return false
	}
	index := p.nextIndex
	p.takeMissed(index) // a packet for it arriving after all is late
	p.missed[p.missedHead] = missedSlot{index: index, valid: true}
	p.missedHead = (p.missedHead + 1) % len(p.missed)
	p.nextIndex = advanceMediaIndex(index, audioFrameTicks)
	p.lagExcess -= p.frameSamples
	for i := range p.arrivals {
		p.arrivals[i].lag -= p.frameSamples
	}
	p.shedSlots++ // DEBUG-TEMP
	p.skipped = true
	traceVoice(index, "skipped: a missing slot, catching up (%d ms behind)", (p.lagExcess+p.frameSamples)*1000/audioSampleRate) // DEBUG-TEMP
	return true
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
		logs.LogV("[debug] voice: nothing to play (the peer's audio stopped), buffering again") // DEBUG-TEMP
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
	p.soundLevel.add(p.scratch[:min(written, p.frameSamples)], true)                                       // DEBUG-TEMP
	traceVoiceDecoded(p.nextIndex, p.scratch[:min(written, p.frameSamples)], p.out.QueuedSamples(), false) // DEBUG-TEMP
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
	p.soundLevel.conceal() // DEBUG-TEMP
	written, err := p.dec.Conceal(p.scratch)
	if err != nil || written <= 0 {
		clear(p.scratch)
		written = p.frameSamples
	}
	traceVoiceDecoded(p.nextIndex, p.scratch[:min(written, p.frameSamples)], p.out.QueuedSamples(), true) // DEBUG-TEMP
	p.push(p.scratch[:min(written, p.frameSamples)])
	return true
}

// push queues one decoded frame (copied) for the next slot.
func (p *audioPlayout) push(pcm []int16) {
	samples := make([]int16, p.frameSamples)
	copy(samples, pcm)
	discontinuous := p.skipped // a slot before it was skipped (skipLag)
	p.skipped = false
	index := p.nextIndex
	p.nextIndex = advanceMediaIndex(p.nextIndex, audioFrameTicks)
	if discontinuous {
		p.hist = p.hist[:0] // what played before does not lead into it
	}
	if p.ready && p.catchUp == 0 {
		// The queue's low point over a window: the speaker takes whole
		// periods (up to 80 ms at once), so the queue saws by a period, and
		// only what stays queued through the dips is extra.
		level := p.out.QueuedSamples() + len(samples)
		if p.backlogFrames == 0 || level < p.backlogMin {
			p.backlogMin = level
		}
		p.backlogFrames++
		if p.backlogFrames >= audioCatchUpWindowFrames {
			if excess := p.backlogMin - p.target(); excess >= audioCatchUpStartFrames*p.frameSamples {
				p.catchUp = excess
			}
			p.backlogFrames = 0
		}
	}
	if p.ready && (p.catchUp > 0 || p.lagExcess > 0) {
		// Extra audio to drain (the lead came down, or a stall left the
		// clock behind: trackLag), but only what is really queued over the
		// target. A pause is skipped, speech plays faster; nothing is
		// dropped.
		excess := min(max(p.catchUp, p.lagExcess), p.out.QueuedSamples()+len(samples)-p.target())
		if excess <= 0 {
			// Whatever is over is in the reorder buffer: skipLag sheds it.
			p.catchUp, p.accelCredit = 0, 0
		} else {
			p.level = 0
			if excess >= len(samples) && isQuiet(samples) {
				traceVoice(index, "skipped: a quiet frame, catching up (%d ms over)", excess*1000/audioSampleRate) // DEBUG-TEMP
				p.shed(len(samples))
				p.shedQuiet++                          // DEBUG-TEMP
				p.skipped = p.skipped || discontinuous // the next frame does not continue the last either
				return
			}
			p.accelCredit = min(p.accelCredit+len(samples)*audioAccelPermille/1000, accelMaxPeriod)
			var cut int
			if p.accelCredit >= accelMinPeriod {
				samples, cut = accelerate(p.hist, samples, min(excess, p.accelCredit))
				p.accelCredit -= cut
				if cut > 0 {
					traceVoice(index, "shortened by a %d ms pitch period, catching up (%d ms over)", cut*1000/audioSampleRate, excess*1000/audioSampleRate) // DEBUG-TEMP
				}
			}
			if cut == 0 && p.accelCredit >= accelMaxPeriod {
				// No period to cut (unvoiced, noise): a little faster by
				// resampling.
				cut = min(excess, max(1, len(samples)*audioCatchUpPermille/1000))
				samples = resizePCM(samples, len(samples)-cut)
				p.accelCredit -= cut
				traceVoice(index, "shortened by %d samples, catching up", cut) // DEBUG-TEMP
			}
			p.shed(cut)
			p.shedSpeech += cut // DEBUG-TEMP
			p.pushFrame(device.PlaybackFrame{Samples: samples, MediaIndex: index, MediaSamples: p.frameSamples, Discontinuous: discontinuous})
			return
		}
	}
	if p.ready {
		q := p.out.QueuedSamples()
		target := p.target()
		// Average the device-period sawtooth over several seconds. A tiny
		// continuous rate correction compensates independent audio clocks.
		delta := 0
		level := float64(q + len(samples))
		if p.level == 0 {
			p.level = level
		}
		p.level += (level - p.level) / 200
		band := float64(p.frameSamples) / 2
		if p.level > float64(target)+band && q > p.lowWater {
			delta = -1
		}
		if p.level < float64(target)-band {
			delta = 1
		}
		if delta != 0 {
			samples = resizePCM(samples, len(samples)+delta)
		}
	}
	frame := device.PlaybackFrame{Samples: samples, MediaIndex: index, MediaSamples: p.frameSamples, Discontinuous: discontinuous}

	if !p.ready {
		p.pre = append(p.pre, frame)
		p.preSample += len(samples)
		if p.preSample < p.frameSamples*p.lead {
			return
		}
		for _, f := range p.pre {
			p.pushFrame(f)
		}
		p.pre, p.preSample = p.pre[:0], 0
		p.ready = true
	} else {
		p.pushFrame(frame)
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

// pushFrame hands a frame to the speaker and keeps its end in hist.
func (p *audioPlayout) pushFrame(f device.PlaybackFrame) {
	p.out.Push(f)
	if f.Discontinuous {
		p.hist = p.hist[:0]
	}
	p.hist = append(p.hist, f.Samples...)
	if extra := len(p.hist) - accelMaxPeriod; extra > 0 {
		p.hist = append(p.hist[:0], p.hist[extra:]...)
	}
}

// shed counts n samples of extra audio as drained: the packets measured
// so far would now wait that much less.
func (p *audioPlayout) shed(n int) {
	p.catchUp = max(0, p.catchUp-n)
	p.lagExcess = max(0, p.lagExcess-n)
	for i := range p.arrivals {
		p.arrivals[i].lag -= n
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
