//go:build !darwin

package device

import (
	"errors"
	"time"
)

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

// VoiceLatency is one direction's latency of the system voice unit.
type VoiceLatency struct {
	FromUnit      bool
	Rate          float64
	BufferFrames  int
	DeviceLatency int
	SafetyOffset  int
	StreamLatency int
	UnitLatency   time.Duration
}

// Total is zero where there is no system voice unit.
func (l VoiceLatency) Total() time.Duration { return 0 }

func (l VoiceLatency) String() string { return "n/a" }

// Latency is not available on this platform.
func (vio *VoiceIO) Latency(input bool) VoiceLatency { return VoiceLatency{} }
