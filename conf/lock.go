package conf

import (
	"errors"
	"fmt"
	"os"
)

// One configuration is one Yggdrasil key, and so one address. Two copies of
// Say started with the same configuration are two nodes with one address:
// the network delivers the peer's packets now to one, now to the other, and
// each resets the calls of the other (the other copy's TCP connections are
// unknown to it). They also fight over the camera and the microphone. So a
// configuration is taken by the first Say that starts with it, until it
// exits: the system releases the lock even when Say crashes.

// errLocked is what lockFile returns when another process holds the lock.
var errLocked = errors.New("locked")

// LockConfig takes configPath for this process. It fails while another Say
// runs with the same configuration. release gives it back (the system does
// that at exit anyway).
func LockConfig(configPath string) (release func(), err error) {
	if configPath == "" {
		return func() {}, nil
	}
	path := configPath + ".lock"
	release, err = lockFile(path)
	if errors.Is(err, errLocked) {
		return nil, fmt.Errorf("another Say is already running with %s: one configuration is one Yggdrasil address, and two copies of it break each other's calls; close the other one (or give this one its own -config)", configPath)
	}
	if err != nil {
		// A lock that cannot be made (a read-only directory, say) must not
		// stop Say: it only guards against a mistake.
		fmt.Fprintf(os.Stderr, "[say] cannot lock %s: %v\n", path, err)
		return func() {}, nil
	}
	return release, nil
}
