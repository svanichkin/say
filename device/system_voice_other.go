//go:build !darwin && !windows

package device

import "errors"

// StartSystemVoice is not available on this platform: Say's own audio
// (miniaudio and its echo canceller) is used instead.
func StartSystemVoice(sampleRate, samplesPerMediaTick, periodSamples, periods int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, mic bool, onStop func()) (*Speaker, <-chan AudioCaptureFrame, error) {
	return nil, nil, errors.New("system voice processing is not supported on this platform")
}
