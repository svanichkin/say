package udp

import (
	"os"
	"testing"
)

// The playout tests model a speaker that takes frames straight from the
// queue, as a device callback did: no ring to keep a margin for (see
// TestPlayoutKeepsTheRingMargin).
func TestMain(m *testing.M) {
	playoutRingMarginMs = 0
	os.Exit(m.Run())
}
