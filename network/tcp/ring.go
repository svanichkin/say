package tcp

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/tones"
	"github.com/svanichkin/say/ui"
)

// A call rings before it is answered (protocol 17): to a caller of 17 on,
// the one called replies RING at once, the line already taken (a third
// caller gets BUSY), and asks its user; ✓ replies OK as before, x replies
// DECLINED and closes. The caller waits up to ringTimeout for the answer
// and hangs up then (no answer); hanging up closes the line, which is how
// the one called learns of it. A caller of 14-16 is answered at once, as
// before.

const (
	ringReply     = "RING"
	declinedReply = "DECLINED"
	ringProto     = 17
	// ringTimeout is how long a call rings unanswered.
	ringTimeout = 30 * time.Second
)

var (
	// ErrDeclined: the one called declined the call.
	ErrDeclined = errors.New("the call was declined")
	// ErrNoAnswer: the call rang unanswered for ringTimeout.
	ErrNoAnswer = errors.New("no answer")
)

// playTone plays the ring (device.PlayTone; the tests' plays nothing).
var playTone = device.PlayTone

// rings reports whether a call from peer rings before it is answered.
func rings(peer PeerInfo) bool { return peer.Proto >= ringProto }

// awaitAnswer rings a call from who (a name, an address), telling the
// caller (RING) once it shows: the screen asks whether to answer it, until
// the user does, the caller hangs up (its line closes: it sends nothing
// before our answer) or ringTimeout and a little more pass. It reports whether the call was answered; br is left as it
// was, for the call.
func awaitAnswer(conn net.Conn, br *bufio.Reader, who string) bool {
	answer := make(chan bool, 1)
	ui.Ringing(who, func(yes bool) {
		select {
		case answer <- yes:
		default:
		}
	})
	defer ui.RingingOver()
	defer playTone(tones.Ring(), tones.Rate, true)()              // stopped before the call's sound starts
	fmt.Fprint(conn, helloLine(ringReply, conf.HostnameOr("me"))) // ringing here: told once it shows

	gone := make(chan struct{})
	go func() {
		_, _ = br.Peek(1) // the caller hanging up (or, unexpected, sending)
		close(gone)
	}()
	defer func() { // the watch stops before the call reads br
		_ = conn.SetReadDeadline(time.Now())
		<-gone
		_ = conn.SetReadDeadline(time.Time{})
	}()

	timer := time.NewTimer(ringTimeout + 5*time.Second)
	defer timer.Stop()
	select {
	case yes := <-answer:
		return yes
	case <-gone:
		return false
	case <-timer.C:
		return false
	}
}
