package logs

import (
	"fmt"
	"log"
	"time"

	"github.com/svanichkin/say/conf"
)

// LogV prints a formatted log message only when verbose logging is enabled.
// DEBUG-TEMP: without it the message still goes to the ring (ring.go), so
// that the peer gets our debug log whatever flags we were started with.
func LogV(format string, args ...interface{}) {
	if conf.Verbose {
		log.Printf(format, args...)
		return
	}
	line := time.Now().Format("2006/01/02 15:04:05.000000 ") + fmt.Sprintf(format, args...)
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line += "\n"
	}
	keep([]byte(line))
}
