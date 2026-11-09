package device

import (
	"log"
	"time"
)

// systemVoiceLatencyBlocks estimates the playback latency of the system
// voice unit in callback blocks, used only if the system reports none.
const systemVoiceLatencyBlocks = 2

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
}

func (c *systemVoiceCapture) write(samples []int16) {
	if len(samples) == 0 {
		return
	}
	if c.epoch.IsZero() {
		c.epoch = time.Now().Add(-time.Duration(len(samples)) * time.Second / time.Duration(c.sampleRate))
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

// StartSystemVoice runs a call's audio through the system voice unit (see
// StartVoiceIO): it plays speakerIn like StartSpeaker and, with mic set,
// delivers the system-processed microphone (echo cancelled, noise
// suppressed) like StartMicrophoneStream. Closing the returned Speaker
// stops both and closes the microphone channel.
//
// periodSamples, periods and onStop are for platforms that keep Say's own
// speaker; the macOS unit plays itself.
func StartSystemVoice(sampleRate, samplesPerMediaTick, periodSamples, periods int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, mic bool, onStop func()) (*Speaker, <-chan AudioCaptureFrame, error) {
	speaker := &Speaker{}
	r := &queueRenderer{speaker: speaker, in: speakerIn, channels: 1, samplesPerMediaTick: samplesPerMediaTick}

	var micOut chan AudioCaptureFrame
	var onInput func([]int16)
	if mic {
		micOut = make(chan AudioCaptureFrame, 24) // 480 ms of 20 ms codec frames
		capture := &systemVoiceCapture{
			sampleRate: sampleRate,
			frameSize:  max(1, sampleRate/50),
			out:        micOut,
		}
		onInput = capture.write
	}

	onOutput := func(out []int16) {
		select {
		case <-stopCh:
			clear(out)
			return
		default:
		}
		r.render(out)
	}
	if samplesPerMediaTick > 0 {
		// The unit hands blocks of about 10 ms; its latency is estimated as
		// systemVoiceLatencyBlocks of them (for video sync only). Fixed up
		// front: the clock fields are read by other goroutines.
		block := sampleRate / 100
		speaker.periodTicks = uint32(max(1, block/samplesPerMediaTick))
		speaker.playbackLatencyTicks = uint32(systemVoiceLatencyBlocks * block / samplesPerMediaTick)
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
	}

	vio, err := StartVoiceIO(sampleRate, onInput, onOutput)
	if err != nil {
		if micOut != nil {
			close(micOut)
		}
		return nil, nil, err
	}
	// Ask the system how late rendered audio becomes audible, for video
	// sync, instead of guessing. Set before the speaker is handed out: the
	// clock fields are read by other goroutines afterwards.
	out, in := vio.Latency(false), vio.Latency(true)
	if total := out.Total(); total > 0 && samplesPerMediaTick > 0 {
		speaker.playbackLatencyTicks = uint32(total * time.Duration(sampleRate) / time.Second / time.Duration(samplesPerMediaTick))
	}
	speaker.latencySamples = int(out.Total() * time.Duration(sampleRate) / time.Second)
	log.Printf("[audio] system voice: output %v; input %v", out, in)

	speaker.closeDevice = func() {
		vio.Close()
		if micOut != nil {
			close(micOut)
		}
	}
	speaker.release = r.release
	return speaker, micOut, nil
}
