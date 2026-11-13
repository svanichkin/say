package device

import (
	"log"
	"sync"
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
//
// The unit's real-time thread does not run Go code (see voiceio_darwin.go):
// one goroutine takes its microphone blocks, stamped with the time they
// were captured, another keeps its output ring filled from speakerIn.
//
// periodSamples, periods and onStop are for platforms that keep Say's own
// speaker; the macOS unit plays itself.
func StartSystemVoice(sampleRate, samplesPerMediaTick, periodSamples, periods int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, mic bool, onStop func()) (*Speaker, <-chan AudioCaptureFrame, error) {
	speaker := &Speaker{}
	if samplesPerMediaTick > 0 {
		// The unit hands blocks of about 10 ms; its latency is estimated as
		// systemVoiceLatencyBlocks of them (for video sync only). Fixed up
		// front: the clock fields are read by other goroutines.
		block := sampleRate / 100
		speaker.periodTicks = uint32(max(1, block/samplesPerMediaTick))
		speaker.playbackLatencyTicks = uint32(systemVoiceLatencyBlocks * block / samplesPerMediaTick)
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
	}

	vio, err := StartVoiceIO(sampleRate, mic)
	if err != nil {
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
	log.Printf("[audio] system voice: output %v; input %v; other apps lowered by voice activity: %v", out, in, vio.AdvancedDucking())

	quit := make(chan struct{})
	var wg sync.WaitGroup
	var micOut chan AudioCaptureFrame
	if mic {
		micOut = make(chan AudioCaptureFrame, 24) // 480 ms of 20 ms codec frames
		capture := &systemVoiceCapture{sampleRate: sampleRate, frameSize: max(1, sampleRate/50), out: micOut, latency: in.Total()}
		wg.Add(1)
		go func() { defer wg.Done(); pumpMic(vio.ring, capture, quit) }()
	}
	w := &ringRenderer{
		speaker: speaker, in: speakerIn, ring: vio.ring,
		samplesPerMediaTick: samplesPerMediaTick,
		ahead:               sampleRate * ringAheadMs / 1000,
	}
	wg.Add(1)
	go func() { defer wg.Done(); pumpSpeaker(w, stopCh, quit) }()

	speaker.closeDevice = func() {
		close(quit)
		wg.Wait()
		vio.Stop()
		w.final = vio.State()
		vio.Close()
		if micOut != nil {
			close(micOut)
		}
	}
	speaker.release = w.release
	return speaker, micOut, nil
}

// systemVoicePoll is how often the goroutines look at the unit's rings;
// systemVoiceAheadMs is how much audio the output ring is kept filled with.
// Audio in the ring still counts as queued (the playout's target covers
// it), so the ring adds no delay; it plays on while Go is held up.
const (
	systemVoicePoll    = 5 * time.Millisecond
	systemVoiceAheadMs = 300
)
