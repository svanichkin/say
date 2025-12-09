package logs

import (
	"log"
	"github.com/svanichkin/say/conf"
)

// LogV prints a formatted log message only when verbose logging is enabled.
func LogV(format string, args ...interface{}) {
	if conf.Verbose {
		log.Printf(format, args...)
	}
}
