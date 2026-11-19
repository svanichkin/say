//go:build darwin

package device

/*
#cgo LDFLAGS: -framework AVFoundation
int sayShowMicModes(void);
*/
import "C"

import "errors"

// MicSettings is what OpenMicSettings opens here.
func MicSettings() MicSettingsKind { return MicSettingsMode }

// OpenMicSettings opens the Mic Mode picker.
func OpenMicSettings() error {
	if C.sayShowMicModes() == 0 {
		return errors.New("the Mic Mode picker needs macOS 12")
	}
	return nil
}
