//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly || windows || plan9)

package conf

// lockFile: no lock where Say has none to take.
func lockFile(string) (func(), error) { return func() {}, nil }
