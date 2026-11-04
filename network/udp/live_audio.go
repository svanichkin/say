package udp

import (
	"fmt"
	"sync"
	"time"
)

// Live audio settings: the chat command "-audio" shows and changes the
// jitter buffer lead and the speaker buffer of the call in progress. They
// apply to this side's playback only, and the next call starts from the
// defaults again (see ResetAudioSettings).

// liveAudio is the running session's hook for live changes.
type liveAudio struct {
	playoutCmds    chan<- func(*audioPlayout)
	stopCh         <-chan struct{}
	restartSpeaker func(periodMs, periods int) error
	aec            *audioEcho
	speakerInfo    func() string // the system-managed device's latencies (-sysaudio), or ""
}

var (
	liveMu sync.Mutex
	live   *liveAudio
)

func registerLiveAudio(l *liveAudio) {
	liveMu.Lock()
	live = l
	liveMu.Unlock()
}

func unregisterLiveAudio(l *liveAudio) {
	liveMu.Lock()
	if live == l {
		live = nil
	}
	liveMu.Unlock()
}

// run executes f on the playout goroutine and waits for it (bounded).
func (l *liveAudio) run(f func(*audioPlayout)) bool {
	done := make(chan struct{})
	select {
	case l.playoutCmds <- func(p *audioPlayout) { f(p); close(done) }:
	case <-l.stopCh:
		return false
	case <-time.After(500 * time.Millisecond):
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(500 * time.Millisecond):
		return false
	}
}

// AudioSettings describes the buffers in use, for the "-audio" chat command.
func AudioSettings() string {
	liveMu.Lock()
	l := live
	liveMu.Unlock()
	system := ""
	if l != nil && l.speakerInfo != nil {
		system = l.speakerInfo()
	}
	text := fmt.Sprintf("audio: lead %d ms", audioBufferLeadFrames*audioFrameMillis)
	if system == "" {
		text += fmt.Sprintf(", speaker %d ms x %d", audioSpeakerPeriodMs, audioSpeakerPeriods)
	}
	if l != nil {
		var cur, queued int
		if l.run(func(p *audioPlayout) { cur = p.lead; queued = p.out.QueuedSamples() * 1000 / audioSampleRate }) {
			text += fmt.Sprintf(" (lead now %d ms, queued %d ms)", cur*audioFrameMillis, queued)
		}
	}
	if system != "" {
		// The system sets the speaker buffer: only the lead can change.
		return text + "; " + system + ". Change: -audio LEAD PERIOD PERIODS (only LEAD applies with -sysaudio)"
	}
	return text + ". Change: -audio LEAD PERIOD PERIODS, e.g. -audio 40 10 2"
}

// ApplyAudioSettings sets the lead (ms), speaker period (ms) and number of
// periods, live for the call in progress. The speaker is restarted only if
// its buffer changed, which interrupts playback briefly.
func ApplyAudioSettings(leadMs, periodMs, periods int) (string, error) {
	if leadMs < 1 || periodMs < 1 || periods < 2 {
		return "", fmt.Errorf("want -audio LEAD PERIOD PERIODS with positive milliseconds and at least 2 periods")
	}
	liveMu.Lock()
	l := live
	liveMu.Unlock()
	// With -sysaudio the system sets the speaker buffer: only the lead applies.
	system := l != nil && l.speakerInfo != nil && l.speakerInfo() != ""

	oldPeriod, oldPeriods := audioSpeakerPeriodMs, audioSpeakerPeriods
	if !system {
		SetSpeakerBuffer(periodMs, periods)
	}
	SetJitterLead(leadMs)
	lead, leadMin, low := audioBufferLeadFrames, audioLeadMinFrames, audioPlayoutLowFrames

	if l != nil {
		if !system && (audioSpeakerPeriodMs != oldPeriod || audioSpeakerPeriods != oldPeriods) {
			if err := l.restartSpeaker(audioSpeakerPeriodMs, audioSpeakerPeriods); err != nil {
				return "", fmt.Errorf("speaker restart: %w", err)
			}
		}
		if !l.run(func(p *audioPlayout) { p.configure(lead, leadMin, low) }) {
			return "", fmt.Errorf("audio settings were not acknowledged by the receiver")
		}
	}
	return AudioSettings(), nil
}

// SetEchoCancellation turns echo cancellation of the call in progress off or
// on (chat command "-aec off|on"), to tell whether it damages the microphone
// signal. The next call has it on again.
func SetEchoCancellation(on bool) (string, error) {
	liveMu.Lock()
	l := live
	liveMu.Unlock()
	if l == nil || l.aec == nil {
		return "", fmt.Errorf("no call with echo cancellation in progress")
	}
	l.aec.setBypass(!on)
	return EchoCancellationState(), nil
}

// EchoCancellationState describes the echo canceller of the call in progress.
func EchoCancellationState() string {
	liveMu.Lock()
	l := live
	liveMu.Unlock()
	if l == nil || l.aec == nil {
		return "aec: no call in progress"
	}
	state := "off"
	if l.aec.enabled() {
		state = "on"
	}
	l.aec.refMu.Lock()
	delay := l.aec.delaySamples * 1000 / audioSampleRate
	l.aec.refMu.Unlock()
	return fmt.Sprintf("aec: %s, reference delay %d ms. Change: -aec on|off", state, delay)
}
