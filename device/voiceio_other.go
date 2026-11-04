//go:build !darwin

package device

import "errors"

// VoiceIO is the system's voice processing audio unit; see voiceio_darwin.go.
type VoiceIO struct{}

// VoiceIOSupported reports whether this platform has a system voice unit.
const VoiceIOSupported = false

// StartVoiceIO is not available on this platform yet.
func StartVoiceIO(sampleRate int, onInput func(samples []int16), onOutput func(out []int16)) (*VoiceIO, error) {
	return nil, errors.New("system voice processing is not supported on this platform")
}

// Close does nothing.
func (vio *VoiceIO) Close() {}
