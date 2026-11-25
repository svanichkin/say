//go:build !darwin && !linux && !windows

package device

func hasBattery() bool { return false }
