//go:build !darwin && !windows

package device

import "errors"

// MicSettings is what OpenMicSettings opens here: nothing.
func MicSettings() MicSettingsKind { return MicSettingsNone }

// OpenMicSettings has nothing to open here.
func OpenMicSettings() error { return errors.New("no microphone settings to open") }
