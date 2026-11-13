package device

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"

	"golang.org/x/term"
)

// TerminalFrame carries a fully prepared ANSI payload that the terminal driver
// should print verbatim.
type TerminalFrame struct {
	Data string
}

// screenOut, when set, receives the terminal output instead of stdout: the
// pixel window draws it (see SetScreen). screenSize reports its size in
// cells, which changes as the window is resized.
var (
	screenOut  io.Writer
	screenSize func() (cols, rows int)
)

// SetScreen sends the terminal output to w, a screen whose size in cells
// size reports, instead of stdout. Call it before the renderer starts.
func SetScreen(w io.Writer, size func() (cols, rows int)) {
	screenOut, screenSize = w, size
}

// out is where terminal output goes.
func out() io.Writer {
	if screenOut != nil {
		return screenOut
	}
	return os.Stdout
}

// Terminal represents a running terminal output device.
type Terminal struct {
	done chan struct{}
}

// StartTerminal launches a goroutine that consumes frames from frameIn and
// renders them until stopCh is closed or the frame channel closes.
func StartTerminal(frameIn <-chan *TerminalFrame, stopCh <-chan struct{}, onStop func()) (*Terminal, error) {
	if frameIn == nil {
		return nil, fmt.Errorf("frame channel is nil")
	}
	if stopCh == nil {
		return nil, fmt.Errorf("stop channel is nil")
	}

	t := &Terminal{done: make(chan struct{})}

	go func() {
		defer close(t.done)
		if screenOut == nil {
			enterAltScreen()
			defer exitAltScreen()
		}

		for {
			select {
			case <-stopCh:
				if onStop != nil {
					onStop()
				}
				return
			case frame, ok := <-frameIn:
				if !ok {
					if onStop != nil {
						onStop()
					}
					return
				}
				if frame == nil || frame.Data == "" {
					continue
				}
				BeginSyncOutput()
				fmt.Fprint(out(), frame.Data)
				EndSyncOutput()
				if q := terminalQueryAfterFrame(); q != "" {
					fmt.Fprint(out(), q)
				}
			}
		}
	}()

	return t, nil
}

// Done returns a channel that closes when the terminal driver stops.
func (t *Terminal) Done() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.done
}

// BeginSyncOutput enables synchronized output mode (OSC 2026) on terminals that support it.
func BeginSyncOutput() {
	if supportsSyncOutput && screenOut == nil {
		fmt.Print("\x1b[?2026h")
	}
}

// EndSyncOutput disables synchronized output mode.
func EndSyncOutput() {
	if supportsSyncOutput && screenOut == nil {
		fmt.Print("\x1b[?2026l")
	}
}

func enterAltScreen() {
	seq := "\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[3J\x1b[H"
	if supportsSyncOutput {
		seq += "\x1b[?2026h"
	}
	fmt.Print(seq)
}

// exitSeq is written before the terminal leaves the alternate screen (set
// by the UI: images it put there to delete, say).
var exitSeq atomic.Pointer[string]

// SetExitSequence sets what is written before the terminal leaves the
// alternate screen.
func SetExitSequence(seq string) { exitSeq.Store(&seq) }

func exitAltScreen() {
	seq := ""
	if s := exitSeq.Load(); s != nil {
		seq += *s
	}
	if supportsSyncOutput {
		seq += "\x1b[?2026l"
	}
	seq += "\x1b[?7h\x1b[?25h\x1b[?1049l"
	fmt.Print(seq)
}

// GetTermSize queries the current terminal size in character cells using stdout.
func GetTermSize() (cols, rows int, err error) {
	if screenOut != nil {
		cols, rows = screenSize()
		return cols, rows, nil
	}
	return termSize()
}

const osc52MaxClipboardBytes = 4096

// CopyToClipboard copies text via OSC-52 and falls back to common OS clipboard helpers.
func CopyToClipboard(text string) {
	if text == "" {
		return
	}
	_ = copyViaPlatformClipboard(text)
	copyViaOSC52(text)
}

func copyViaOSC52(text string) bool {
	if text == "" || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}

	payload := []byte(text)
	if len(payload) > osc52MaxClipboardBytes {
		payload = payload[:osc52MaxClipboardBytes]
	}
	encoded := base64.StdEncoding.EncodeToString(payload)

	BeginSyncOutput()
	fmt.Printf("\x1b]52;c;%s\x07", encoded)
	EndSyncOutput()
	return true
}

func copyViaPlatformClipboard(text string) error {
	switch runtime.GOOS {
	case "darwin":
		return runClipboardCommand(text, "pbcopy")
	case "windows":
		return setClipboard(text)
	case "plan9":
		return os.WriteFile("/dev/snarf", []byte(text), 0)
	default:
		commands := [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
			{"termux-clipboard-set"},
		}
		for _, cmd := range commands {
			if err := runClipboardCommand(text, cmd[0], cmd[1:]...); err == nil {
				return nil
			}
		}
		return fmt.Errorf("no clipboard helper found")
	}
}

func runClipboardCommand(text, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return err
	}
	return runClipboardCommandWithArgs(text, name, args...)
}

func runClipboardCommandWithArgs(text, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
