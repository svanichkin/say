//go:build !windows

package device

// supportsSyncOutput indicates whether the terminal backend supports the ANSI
// synchronized output sequence (OSC 2026). Most non-Windows terminals support it,
// so this is enabled on all platforms except Windows.
const supportsSyncOutput = true
