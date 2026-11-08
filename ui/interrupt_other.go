//go:build !plan9

package ui

// isInterruptKey: elsewhere the terminal turns Ctrl-C into SIGINT itself.
func isInterruptKey(b byte) bool { return false }

// deleteKeyInterrupts: elsewhere ESC[3~ (Delete) is just consumed.
const deleteKeyInterrupts = false

// exitOnInputLoss: elsewhere the terminal's hangup (SIGHUP) ends Say.
const exitOnInputLoss = false
