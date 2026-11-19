package device

// MicSettingsKind is what the system offers for the microphone that Say can
// open: on macOS the Mic Mode picker (Standard, Voice Isolation, Wide
// Spectrum), on Windows the microphone's privacy settings.
type MicSettingsKind int

const (
	MicSettingsNone MicSettingsKind = iota
	MicSettingsMode
	MicSettingsPrivacy
)
