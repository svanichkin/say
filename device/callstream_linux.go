package device

import (
	"os"
	"strings"
	"sync"
)

var callStreamOnce sync.Once

// callStreams marks the process's sound as a call's for PulseAudio and
// PipeWire (whose Pulse service reads the same): media.role=phone, which
// lowers other sound while a call plays (role ducking) and sends it
// through echo cancelling where the system has it set up. Set before the
// first stream opens; a role already given by the user stays.
func callStreams() {
	callStreamOnce.Do(func() {
		props := os.Getenv("PULSE_PROP")
		if strings.Contains(props, "media.role") {
			return
		}
		if props != "" {
			props += " "
		}
		os.Setenv("PULSE_PROP", props+"media.role=phone")
	})
}
