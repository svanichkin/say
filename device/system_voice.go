package device

import (
	"log"
	"time"
)

// systemVoiceLatencyBlocks estimates the playback latency of the system
// voice unit in callback blocks, used only if the system reports none.
const systemVoiceLatencyBlocks = 2

// StartSystemVoice runs a call's audio through the system voice unit (see
// StartVoiceIO): it plays speakerIn like StartSpeaker and, with mic set,
// delivers the system-processed microphone (echo cancelled, noise
// suppressed) like StartMicrophoneStream. Closing the returned Speaker
// stops both and closes the microphone channel.
func StartSystemVoice(sampleRate, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, mic bool) (*Speaker, <-chan AudioCaptureFrame, error) {
	speaker := &Speaker{}
	r := &queueRenderer{speaker: speaker, in: speakerIn, channels: 1, samplesPerMediaTick: samplesPerMediaTick}

	var micOut chan AudioCaptureFrame
	var onInput func([]int16)
	if mic {
		micOut = make(chan AudioCaptureFrame, 48) // ~0.5 s of ~10 ms blocks
		var firstSample uint64
		var captureEpoch time.Time
		onInput = func(samples []int16) {
			// The callback runs once the block is complete; its first sample
			// was captured one block duration earlier.
			if captureEpoch.IsZero() {
				captureEpoch = time.Now().Add(-time.Duration(len(samples)) * time.Second / time.Duration(sampleRate))
			}
			frame := AudioCaptureFrame{
				Samples:     append([]int16(nil), samples...), // the unit reuses its buffer
				CapturedAt:  captureEpoch.Add(time.Duration(firstSample) * time.Second / time.Duration(sampleRate)),
				FirstSample: firstSample,
			}
			firstSample += uint64(len(samples))
			select {
			case micOut <- frame:
			default:
				// Downstream is late: drop the oldest block, keep the newest.
				select {
				case <-micOut:
				default:
				}
				select {
				case micOut <- frame:
				default:
				}
			}
		}
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
