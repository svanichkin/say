package logs

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/dbg"
)

// debugLine reports whether a log line (its message, after any timestamp)
// is a debug build's: [debug] or [trace].
func debugLine(msg string) bool {
	return strings.HasPrefix(msg, "[debug]") || strings.HasPrefix(msg, "[trace]")
}

// LogV prints a formatted log message only when verbose logging is enabled.
// DEBUG-TEMP: without it the message still goes to the ring (ring.go), so
// that the peer gets our debug log whatever flags we were started with.
//
// A release build (see package dbg) has no [debug] and [trace] lines at all.
func LogV(format string, args ...interface{}) {
	if !dbg.On && debugLine(format) {
		return
	}
	if conf.Verbose {
		log.Printf(format, args...)
		return
	}
	if !dbg.On {
		return
	}
	line := time.Now().Format("2006/01/02 15:04:05.000000 ") + fmt.Sprintf(format, args...)
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line += "\n"
	}
	keep([]byte(line))
}
