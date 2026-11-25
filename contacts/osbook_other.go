//go:build !darwin || !cgo

package contacts

import "time"

// OSBookAvailable reports whether there is a system address book Say can
// use here: macOS only (Windows keeps its book for Store apps; Linux has
// none of its own).
func OSBookAvailable() bool { return false }

// OSBookAccess reports whether Say may use the system's address book.
func OSBookAccess() bool { return false }

func readOS() ([]byte, error)           { return nil, ErrNoAccess }
func readOSMe() (string, error)         { return "", ErrNoAccess }
func writeOS(in []byte) ([]byte, error) { return nil, ErrNoAccess }

var accessTook time.Duration

// watchSystem: no system book here.
func watchSystem(changed func()) (stop func()) { return func() {} }

// OSBookStatus: no system book here.
func OSBookStatus() int { return Denied }
