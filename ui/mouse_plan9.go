//go:build plan9

package ui

import "os"

// prepareTTYForMouse turns off line editing and echo, so keys reach the
// chat one at a time: on Plan 9, "rawon" written to /dev/consctl, which
// holds while the file stays open. (vt(1) sends no mouse reports, only
// keys.)
func prepareTTYForMouse(fd int) (func(), error) {
	ctl, err := os.OpenFile("/dev/consctl", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	if _, err := ctl.Write([]byte("rawon")); err != nil {
		ctl.Close()
		return nil, err
	}
	return func() {
		_, _ = ctl.Write([]byte("rawoff"))
		ctl.Close()
	}, nil
}

// isInterruptKey: on Plan 9 Del is the interrupt key; in raw mode it arrives
// as 0x7f instead of a note (Backspace is ^H there, so the chat keeps it).
// Ctrl-C works too, as on other systems.
func isInterruptKey(b byte) bool { return b == 0x7f || b == 0x03 }

// deleteKeyInterrupts: vt(1) in xterm mode sends Del as ESC[3~.
const deleteKeyInterrupts = true

// exitOnInputLoss: closing vt(1) posts no hangup note to Say.
const exitOnInputLoss = true
