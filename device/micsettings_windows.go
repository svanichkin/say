//go:build windows

package device

import "os/exec"

// MicSettings is what OpenMicSettings opens here.
func MicSettings() MicSettingsKind { return MicSettingsPrivacy }

// OpenMicSettings opens the microphone's privacy settings.
func OpenMicSettings() error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", "ms-settings:privacy-microphone").Start()
}
