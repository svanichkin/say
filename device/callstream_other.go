//go:build !linux && !plan9

package device

// callStreams does nothing here: on Windows the miniaudio copy opens a
// call's streams itself (third_party/malgo); on macOS the voice unit is
// the system's mode for calls.
func callStreams() {}
