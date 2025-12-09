package device

import (
	"log"
	"strings"

	"github.com/svanichkin/say/logs"
	_ "unsafe"
)

//go:linkname gocamLogger github.com/svanichkin/gocam.camLog
var gocamLogger *log.Logger

func init() {
	if gocamLogger == nil {
		return
	}
	gocamLogger.SetOutput(gocamLogWriter{})
	gocamLogger.SetFlags(0)
	gocamLogger.SetPrefix("")
}

type gocamLogWriter struct{}

func (gocamLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	if msg == "" {
		return len(p), nil
	}
	logs.LogV("%s", msg)
	return len(p), nil
}
