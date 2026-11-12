//go:build !darwin && !windows && !linux

package device

// raiseThread does nothing where Say knows no way to raise a thread.
func raiseThread() {}
