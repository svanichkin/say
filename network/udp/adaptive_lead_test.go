package udp

import (
	"testing"
	"time"

	"github.com/svanichkin/say/device"
)

// toneDecoder decodes every packet to a loud frame, except packets whose
// second byte is 'Q', which decode to silence.
type toneDecoder struct{ frame int }

func (d toneDecoder) DecodeFrame(p []byte, dst []int16) (int, error) {
	v := int16(8000)
	if len(p) > 1 && p[1] == 'Q' {
		v = 0
	}
	for i := range dst[:d.frame] {
		dst[i] = v
	}
	return d.frame, nil
}
func (d toneDecoder) Conceal(dst []int16) (int, error) { clear(dst[:d.frame]); return d.frame, nil }
func (toneDecoder) IsDTXFrame([]byte) bool             { return false }

func TestAdaptiveLeadGrowsOnJitterAndShrinksWhenQuiet(t *testing.T) {
	const frame = 160
	q := device.NewPlaybackQueue(audioBufferMaxFrames)
	p := newAudioPlayout(toneDecoder{frame: frame}, frame, q)
	start := time.Now()

	type arrival struct {
		at    int // ms
		index int
	}
	var arrivals []arrival
	const jitterFrames, quietFrames = 1500, 2000 // 30 s with spikes, then 40 s steady
	for i := 0; i < jitterFrames+quietFrames; i++ {
		at := i * audioFrameMillis
		if i < jitterFrames && i%250 == 100 { // every 5 s a 300 ms stall, then a burst
			for j := 0; j < 15; j++ {
				arrivals = append(arrivals, arrival{at: at + 300, index: i + j})
			}
			i += 14
			continue
		}
		arrivals = append(arrivals, arrival{at: at, index: i})
	}

	lateBefore, maxLead, carry := 0, 0, 0
	next := 0
	for ms := 0; ms < (jitterFrames+quietFrames)*audioFrameMillis; ms++ {
		now := start.Add(time.Duration(ms) * time.Millisecond)
		for next < len(arrivals) && arrivals[next].at <= ms {
			a := arrivals[next]
			payload := []byte{byte(a.index), 0}
			if a.index%10 == 0 {
				payload[1] = 'Q' // regular pauses between words
			}
			p.onPacket(uint16(a.index*audioFrameTicks), payload, now)
			next++
		}
		if ms%5 == 0 {
			p.onTickAt(now)
		}
		if ms%(audioSpeakerPeriodFrames*audioFrameMillis) == 0 && p.ready { // speaker pulls one period
			for carry < audioSpeakerPeriodFrames*frame {
				f, ok := q.TakeFrame()
				if !ok {
					break
				}
				carry += len(f.Samples)
			}
			carry = max(0, carry-audioSpeakerPeriodFrames*frame)
		}
		if ms == jitterFrames*audioFrameMillis/2 {
			lateBefore = int(p.lateEvents)
		}
		maxLead = max(maxLead, p.lead)
	}
	t.Logf("lead: start %d ms, max %d ms, end %d ms; late packets: %d in the first 15 s, %d after",
		audioBufferLeadFrames*audioFrameMillis, maxLead*audioFrameMillis, p.lead*audioFrameMillis, lateBefore, int(p.lateEvents)-lateBefore)
	if maxLead <= audioBufferLeadFrames {
		t.Fatalf("lead did not grow under 300 ms stalls")
	}
	if late := int(p.lateEvents) - lateBefore; late > lateBefore/4 {
		t.Fatalf("still %d late packets after the lead grew (%d before)", late, lateBefore)
	}
	if p.lead >= maxLead {
		t.Fatalf("lead did not shrink during 40 s of steady arrival (%d ms)", p.lead*audioFrameMillis)
	}
}
