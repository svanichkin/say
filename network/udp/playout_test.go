package udp

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/svanichkin/say/device"
)

// fakeDecoder marks decoded samples with 1000+payload[0] and concealed ones with -1.
type fakeDecoder struct{ frame int }

func (d fakeDecoder) DecodeFrame(p []byte, dst []int16) (int, error) {
	for i := range dst[:d.frame] {
		dst[i] = 1000 + int16(p[0])
	}
	return d.frame, nil
}
func (d fakeDecoder) Conceal(dst []int16) (int, error) {
	for i := range dst[:d.frame] {
		dst[i] = -1
	}
	return d.frame, nil
}
func (fakeDecoder) IsDTXFrame(p []byte) bool { return len(p) > 1 && p[1] == 'S' }

// simulate plays 20 ms of audio per step: the network delivers the packets
// scheduled for that step, the receiver ticks, and the speaker takes one
// frame. It returns what the speaker played (0 = underrun).
func simulate(t *testing.T, steps int, arrivals map[int][]uint16, sid map[uint16]bool) []int16 {
	t.Helper()
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	var played []int16
	for step := 0; step < steps; step++ {
		for _, idx := range arrivals[step] {
			payload := []byte{byte(idx / audioFrameTicks), 0}
			if sid[idx] {
				payload[1] = 'S'
			}
			p.onPacket(idx, payload, now)
		}
		p.onTickAt(now)
		if f, ok := q.TakeFrame(); ok {
			played = append(played, f.Samples[0])
		} else {
			played = append(played, 0)
		}
		now = now.Add(20 * time.Millisecond)
	}
	return played
}

func underruns(played []int16, from int) int {
	n := 0
	for _, v := range played[from:] {
		if v == 0 {
			n++
		}
	}
	return n
}

func TestPlayoutConcealsLatePacketsInsteadOfUnderrun(t *testing.T) {
	arrivals := map[int][]uint16{}
	for i := 0; i < 50; i++ {
		step := i
		if i >= 20 && i < 26 {
			step = 26 // a 120 ms jitter spike delays six packets, then they burst in
		}
		arrivals[step] = append(arrivals[step], uint16(i*audioFrameTicks))
	}
	played := simulate(t, 60, arrivals, nil)
	// After priming (4 frames lead), the speaker must never starve while the peer talks.
	if n := underruns(played[:50], audioBufferLeadFrames+1); n != 0 {
		t.Fatalf("speaker underran %d times during a jitter spike: %v", n, played)
	}
}

func TestPlayoutFillsDTXSilenceWithComfortNoise(t *testing.T) {
	arrivals := map[int][]uint16{}
	sid := map[uint16]bool{}
	for i := 0; i < 10; i++ { // speech
		arrivals[i] = append(arrivals[i], uint16(i*audioFrameTicks))
	}
	for i := 10; i < 90; i += audioDTXUpdateFrames { // DTX: one SID every 8 frames
		idx := uint16(i * audioFrameTicks)
		sid[idx] = true
		arrivals[i] = append(arrivals[i], idx)
	}
	for i := 90; i < 100; i++ { // speech again
		arrivals[i] = append(arrivals[i], uint16(i*audioFrameTicks))
	}
	played := simulate(t, 100+audioBufferLeadFrames+5, arrivals, sid)
	if n := underruns(played[:100], audioBufferLeadFrames+1); n != 0 {
		t.Fatalf("speaker underran %d times across DTX silence: %v", n, played)
	}
	sids := 0
	for _, v := range played {
		if v >= 1000+10 && v < 1000+90 && (int(v)-1000-10)%audioDTXUpdateFrames == 0 {
			sids++
		}
	}
	if sids != 10 {
		t.Fatalf("played %d of 10 SID frames; SIDs must not be dropped as late: %v", sids, played)
	}
}

func TestPlayoutStartsDuringDTX(t *testing.T) {
	arrivals := map[int][]uint16{}
	sid := map[uint16]bool{}
	for i := 0; i < 120; i += audioDTXUpdateFrames {
		idx := uint16(i * audioFrameTicks)
		arrivals[i] = []uint16{idx}
		sid[idx] = true
	}
	played := simulate(t, 120, arrivals, sid)
	if n := underruns(played, audioBufferLeadFrames+1); n != 0 {
		t.Fatalf("%d empty output frames during continuously received DTX", n)
	}
}

func TestPlayoutRecoversFromPreviousCallTimestamp(t *testing.T) {
	arrivals := map[int][]uint16{0: {5000}} // last delayed packet from a previous call
	for i := 1; i < 150; i++ {
		arrivals[i] = []uint16{uint16(i * audioFrameTicks)}
	}
	played := simulate(t, 150, arrivals, nil)
	// Recovery must be bounded; a single old packet must not silence this call.
	if n := underruns(played, audioBufferLeadFrames+12); n != 0 {
		t.Fatalf("%d empty output frames after the timestamp restarted", n)
	}
	for _, sample := range played[audioBufferLeadFrames+12:] {
		if sample < 1000 {
			t.Fatal("recovery must play received speech, not substitute concealment")
		}
	}
}

func TestPlayoutDoesNotReanchorOnLateDuplicates(t *testing.T) {
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: 160}, 160, q)
	now := time.Now()
	p.onPacket(5000, []byte{1}, now)
	for i := 1; i <= 10; i++ {
		p.onPacket(100, []byte{2}, now.Add(time.Duration(i)*20*time.Millisecond))
	}
	if p.nextIndex != 5000+audioFrameTicks {
		t.Fatalf("duplicate packets moved the timeline to %d", p.nextIndex)
	}
}

func TestAMRPlayoutWithSilenceAnd80msSpeaker(t *testing.T) {
	enc, dec, frameSamples, err := initAudioCodec("amrnb")
	if err != nil {
		t.Fatal(err)
	}
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(dec, frameSamples, q)
	pcm := make([]int16, frameSamples)
	packet := make([]byte, maxAudioPacket)
	epoch := time.Now()
	const sourceFrames = 300
	played, omitted, sidPackets, speechPeak := 0, 0, 0, 0
	playing := false
	for step := 0; step < sourceFrames+audioBufferLeadFrames+8; step++ {
		now := epoch.Add(time.Duration(step*audioFrameMillis) * time.Millisecond)
		if step < sourceFrames {
			clear(pcm)
			if (step >= 80 && step < 120) || step >= 240 {
				for i := range pcm {
					pcm[i] = int16(10000 * math.Sin(2*math.Pi*337*float64(step*frameSamples+i)/audioSampleRate))
				}
			}
			n, err := enc.EncodeFrame(pcm, packet)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				omitted++
			} else {
				if dec.IsDTXFrame(packet[:n]) {
					sidPackets++
				}
				p.onPacket(uint16(step*audioFrameTicks), packet[:n], now)
			}
		}
		p.onTickAt(now)
		if (step+1)%audioSpeakerPeriodFrames != 0 {
			continue
		}
		if !playing {
			playing = q.QueuedSamples() >= audioSpeakerPeriodFrames*frameSamples
			if !playing {
				if step > audioBufferLeadFrames+audioSpeakerPeriodFrames {
					t.Fatalf("speaker never started during DTX by frame %d", step)
				}
				continue
			}
		}
		for i := 0; i < audioSpeakerPeriodFrames && played < sourceFrames; i++ {
			f, ok := q.TakeFrame()
			if !ok {
				t.Fatalf("speaker ran dry at frame %d, played %d", step, played)
			}
			if f.MediaIndex != uint16(played*audioFrameTicks) {
				t.Fatalf("audio timeline jumped: index=%d, want %d", f.MediaIndex, played*audioFrameTicks)
			}
			if played >= 90 && played < 120 {
				for _, sample := range f.Samples {
					x := int(sample)
					speechPeak = max(speechPeak, x, -x)
				}
			}
			played++
		}
		if played == sourceFrames {
			break
		}
	}
	if played != sourceFrames || omitted == 0 || sidPackets < 2 {
		t.Fatalf("played=%d/%d, omitted DTX frames=%d, SID packets=%d", played, sourceFrames, omitted, sidPackets)
	}
	if speechPeak < 1000 {
		t.Fatalf("speech did not reach playback: peak=%d", speechPeak)
	}
	t.Logf("played all %d frames continuously; %d silent frames omitted, %d SID packets", played, omitted, sidPackets)
}

// playedCounts drains the queue and counts decoded (>= 1000) and concealed (-1) frames.
func playedCounts(q *device.PlaybackQueue) (decoded, concealed int, order []int16) {
	for {
		f, ok := q.TakeFrame()
		if !ok {
			return
		}
		if f.Samples[0] == -1 {
			concealed++
		} else {
			decoded++
		}
		order = append(order, f.Samples[0])
	}
}

func TestPlayoutToleratesTimestampJitter(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(64)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	var decoded, concealed int
	for i := 0; i < 50; i++ {
		idx := uint16(i * audioFrameTicks)
		if i%2 == 1 {
			idx-- // every second block timestamped one 4 ms tick early
		}
		p.onPacket(idx, []byte{byte(i), 0}, now)
		if i >= audioBufferLeadFrames { // the speaker plays one frame per packet
			if f, ok := q.TakeFrame(); ok {
				if f.Samples[0] == -1 {
					concealed++
				} else {
					decoded++
				}
			}
		}
		now = now.Add(20 * time.Millisecond)
	}
	d, c, _ := playedCounts(q)
	decoded, concealed = decoded+d, concealed+c
	if decoded != 50 || concealed != 0 {
		t.Fatalf("decoded %d, concealed %d of 50 jittered packets; want all 50 decoded", decoded, concealed)
	}
}

func TestPlayoutReordersPacketsBeforeTheirSlotIsDue(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(64)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	for _, i := range []int{0, 2, 1, 3, 4, 6, 5, 7, 8, 9} {
		p.onPacket(uint16(i*audioFrameTicks), []byte{byte(i), 0}, now)
	}
	total := max(10, audioBufferLeadFrames+2)
	for i := 10; i < total; i++ {
		p.onPacket(uint16(i*audioFrameTicks), []byte{byte(i), 0}, now)
	}
	decoded, concealed, order := playedCounts(q)
	if decoded != total || concealed != 0 {
		t.Fatalf("decoded %d, concealed %d of %d reordered packets: %v", decoded, concealed, total, order)
	}
	for i, v := range order {
		if v != int16(1000+i) {
			t.Fatalf("played out of order: %v", order)
		}
	}
}

// Packets with 0-80 ms of jitter: nothing that arrives is lost (late
// packets are played, stretching the playback), the lead grows while the
// path jitters and returns to the start once it is clean again.
func TestPlayoutLeadAdaptsToJitter(t *testing.T) {
	// The buffers of a call (10 ms speaker periods, 40 ms lead); the
	// package defaults are restored afterwards for the other tests.
	period, periods, lead := audioSpeakerPeriodMs, audioSpeakerPeriods, audioBufferLeadFrames*audioFrameMillis
	ResetAudioSettings()
	defer func() { SetSpeakerBuffer(period, periods); SetJitterLead(lead) }()
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	rng := rand.New(rand.NewSource(11))
	const jitterSteps, cleanSteps = 750, 2000 // 15 s of jitter, 40 s clean (20 ms steps)
	arrivals := map[int][]uint16{}
	for i := 0; i < jitterSteps+cleanSteps; i++ {
		delay := 0
		if i < jitterSteps {
			delay = rng.Intn(5) // 0..80 ms
		}
		arrivals[i+delay] = append(arrivals[i+delay], uint16(i*audioFrameTicks)&mediaIndexMask)
	}
	now := time.Now()
	maxLead := p.lead
	played, underrun := 0, 0
	for step := 0; step < jitterSteps+cleanSteps; step++ {
		for _, idx := range arrivals[step] {
			p.onPacket(idx, []byte{byte(idx / audioFrameTicks), 0}, now)
		}
		p.onTickAt(now)
		// The speaker takes 20 ms of audio per step, whatever the frames.
		for need := frame; need > 0; {
			f, ok := q.TakeFrame()
			if !ok {
				if step > 10 {
					underrun++
				}
				break
			}
			if f.Samples[0] >= 1000 {
				played++
			}
			need -= len(f.Samples)
		}
		maxLead = max(maxLead, p.lead)
		now = now.Add(20 * time.Millisecond)
	}
	sent := jitterSteps + cleanSteps
	if lost := sent - played; lost > sent/100 {
		t.Fatalf("%d of %d frames never played (late events %d)", lost, sent, p.lateEvents)
	}
	if maxLead*audioFrameMillis < 80 || p.lead != p.baseLead {
		t.Fatalf("lead peaked at %d ms and ended at %d ms (start %d ms)", maxLead*audioFrameMillis, p.lead*audioFrameMillis, p.baseLead*audioFrameMillis)
	}
	if underrun > 0 {
		t.Fatalf("%d underruns", underrun)
	}
	t.Logf("%d of %d frames played, %d late packets (played, not dropped); lead peaked at %d ms, back to %d ms",
		played, sent, p.lateEvents, maxLead*audioFrameMillis, p.lead*audioFrameMillis)
}

// A packet that arrives after its slot was concealed is still played: its
// audio follows the concealment, the playback clock holding meanwhile.
func TestLatePacketIsPlayedNotDropped(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	for i := 0; i < 8; i++ {
		p.onPacket(uint16(i*audioFrameTicks), []byte{byte(i), 0}, now)
	}
	late := p.nextIndex
	p.conceal() // the slot's turn came without its packet
	lead := p.lead
	p.onPacket(late, []byte{77, 0}, now)
	if p.lead != lead+1 || p.lateEvents != 1 {
		t.Fatalf("lead %d (was %d), late %d", p.lead, lead, p.lateEvents)
	}
	var got []int16
	var last device.PlaybackFrame
	for {
		f, ok := q.TakeFrame()
		if !ok {
			break
		}
		got = append(got, f.Samples[0])
		last = f
	}
	if len(got) < 2 || got[len(got)-1] != 1000+77 || got[len(got)-2] != -1 {
		t.Fatalf("queue ends %v: want the concealment, then the late packet's audio", got)
	}
	if last.MediaIndex != p.nextIndex || last.MediaSamples > 1 {
		t.Fatalf("late audio at index %d (%d media samples): the clock must hold at %d", last.MediaIndex, last.MediaSamples, p.nextIndex)
	}
}

// Audio that piled up before the speaker started (or in a short stall) is
// drained: pauses are skipped, speech plays a little faster, nothing is
// dropped, and the queue comes back to its target.
func TestPlayoutDrainsABacklogBelowTheTrim(t *testing.T) {
	period, periods, lead := audioSpeakerPeriodMs, audioSpeakerPeriods, audioBufferLeadFrames*audioFrameMillis
	ResetAudioSettings()
	defer func() { SetSpeakerBuffer(period, periods); SetJitterLead(lead) }()
	for _, tc := range []struct {
		name  string
		quiet bool
		limit time.Duration
	}{{"pauses", true, 3 * time.Second}, {"speech", false, 40 * time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			const frame = 160
			q := device.NewPlaybackQueue(audioBufferMaxFrames)
			dec := fakeDecoder{frame: frame}
			p := newAudioPlayout(dec, frame, q)
			if tc.quiet {
				p = newAudioPlayout(quietDecoder{frame}, frame, q)
			}
			now := time.Now()
			idx := 0
			send := func() {
				p.onPacket(uint16(idx*audioFrameTicks)&mediaIndexMask, []byte{byte(idx), 0}, now)
				idx++
			}
			// 600 ms arrive while the speaker is not taking anything yet.
			for i := 0; i < 30; i++ {
				send()
				p.onTickAt(now)
				now = now.Add(20 * time.Millisecond)
			}
			target := max(p.lead*frame, p.lowWater+frame)
			start := q.QueuedSamples()
			if start < target+10*frame {
				t.Fatalf("no backlog to drain: %d samples queued, target %d", start, target)
			}
			// Then real time: a frame arrives every 20 ms and the speaker
			// plays 20 ms of samples (what it took beyond that counts
			// towards the next step: frames come shortened or whole).
			var took time.Duration
			need := 0
			next := int16(-1) // speech: every frame plays, in order
			for step := 0; step < 3000; step++ {
				send()
				p.onTickAt(now)
				for need += frame; need > 0; {
					f, ok := q.TakeFrame()
					if !ok {
						t.Fatalf("underrun at step %d", step)
					}
					need -= len(f.Samples)
					if !tc.quiet {
						if v := f.Samples[0]; next >= 0 && v != next {
							t.Fatalf("played frame %d after %d: speech was dropped", v-1000, next-1001)
						}
						next = 1000 + (f.Samples[0]-1000+1)%256
					}
				}
				now = now.Add(20 * time.Millisecond)
				if took == 0 && q.QueuedSamples() <= target+frame {
					took = time.Duration(step+1) * 20 * time.Millisecond
				}
			}
			if took == 0 || took > tc.limit {
				t.Fatalf("backlog of %d ms drained in %v (limit %v); %d samples queued at the end, target %d",
					(start-target)/8, took, tc.limit, q.QueuedSamples(), target)
			}
			t.Logf("backlog of %d ms drained in %v", (start-target)/8, took)
		})
	}
}

// quietDecoder decodes every frame to near-silence (pauses between words).
type quietDecoder struct{ frame int }

func (d quietDecoder) DecodeFrame(p []byte, dst []int16) (int, error) {
	for i := range dst[:d.frame] {
		dst[i] = 1
	}
	return d.frame, nil
}
func (d quietDecoder) Conceal(dst []int16) (int, error) { return d.DecodeFrame(nil, dst) }
func (quietDecoder) IsDTXFrame([]byte) bool             { return false }

// A stall that leaves the playback clock far behind the sender's (here: a
// second of audio arrives at once) is shed within seconds of speech with
// pauses (DTX), even when every one-second window of the queue holds a
// pause, so the queue alone never shows the delay.
func TestStallBacklogIsShedInPauses(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	now := time.Now()
	idx := uint16(1000)
	send := func(n int, silent bool) {
		for range n {
			payload := []byte{1, 0}
			if silent {
				payload[1] = 'S'
			}
			p.onPacket(idx, payload, now)
			idx = advanceMediaIndex(idx, audioFrameTicks)
		}
	}
	step := func() {
		p.onTickAt(now)
		q.TakeFrame()
		now = now.Add(20 * time.Millisecond)
	}
	// Start, then a second's backlog at once.
	for range 20 {
		send(1, false)
		step()
	}
	send(50, false)
	// Speech in bursts of 15 frames, then a pause of 25 slots with a
	// silence update every 8 (DTX), as the peer sends; 20 ms a slot.
	for cycle := 0; cycle < 15; cycle++ {
		for range 15 {
			send(1, false)
			step()
		}
		for k := range 25 {
			if k%8 == 0 {
				send(1, true)
			} else {
				idx = advanceMediaIndex(idx, audioFrameTicks) // not sent
			}
			step()
		}
	}
	if lag := p.lag() * 1000 / audioSampleRate; lag > 300 {
		t.Fatalf("still %d ms behind the sender after 15 s of speech with pauses", lag)
	}
}

// With a device ring, the playout keeps the ring's margin queued beyond
// the device's period, also through silence (comfort noise made up ahead),
// so the device plays on while the goroutines filling the ring are late.
func TestPlayoutKeepsTheRingMargin(t *testing.T) {
	playoutRingMarginMs = 40
	defer func() { playoutRingMarginMs = 0 }()
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(fakeDecoder{frame: frame}, frame, q)
	margin := audioSampleRate * 40 / 1000
	now := time.Now()
	for i := 0; i < 10; i++ { // speech primes the buffer
		p.onPacket(uint16(i*audioFrameTicks)&mediaIndexMask, []byte{byte(i), 0}, now)
		p.onTickAt(now)
		now = now.Add(20 * time.Millisecond)
	}
	// Then silence (DTX): a silence frame every 8 slots, the slots between
	// made up; the device takes 20 ms each step.
	for step := 0; step < 100; step++ {
		if i := 10 + step; i%8 == 0 {
			p.onPacket(uint16(i*audioFrameTicks)&mediaIndexMask, []byte{byte(i), 0}, now)
		}
		p.onTickAt(now)
		if got := q.QueuedSamples(); got < margin {
			t.Fatalf("step %d: %d samples queued, below the ring margin %d", step, got, margin)
		}
		for need := frame; need > 0; {
			f, ok := q.TakeFrame()
			if !ok {
				t.Fatalf("step %d: underrun", step)
			}
			need -= len(f.Samples)
		}
		now = now.Add(20 * time.Millisecond)
	}
}
