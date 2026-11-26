//go:build plan9

package device

// PlayTone plays nothing on Plan 9 yet: the ring and the fail sound are
// the other systems'.
func PlayTone(pcm []int16, rate int, loop bool) (stop func()) { return func() {} }

// WarmTones does nothing on Plan 9.
func WarmTones(rate int) {}
