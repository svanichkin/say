package udp

import (
	"container/heap"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// A simulated bad path for the call's outgoing media (-netsim), for testing
// what the peer sees: the datagrams this side sends go through a link of
// limited rate and its queue (a datagram that would wait longer than
// netSimQueue is dropped), are lost by 1400-byte IP fragment in bursts, and
// are delayed by half the scenario's round trip plus up to netSimJitter
// more (so they may arrive out of order), on top of the real path. The
// scenarios are Babe's cmd/babecam's, repeating. Only this side's media is
// touched: for both ways both peers set it; the control connection (TCP)
// goes as it is.
type netSimState struct {
	ping  time.Duration // round trip, without the jitter and the queue
	rate  float64       // the link's rate in kbit/s (0: unlimited)
	loss  float64       // packet loss rate
	burst float64       // mean length of a loss burst in packets
	down  bool          // nothing gets through
}

const (
	netSimJitter = 50 * time.Millisecond
	netSimQueue  = time.Second
	netSimFrag   = 1400
	netSimHeader = 28 // IP and UDP
)

// netSimSwitchPing is the round trip t after the path switched to another:
// up to 6 s within a second, then back down to about 200 ms over some 15 s.
func netSimSwitchPing(t time.Duration) time.Duration {
	const base, peak = 200 * time.Millisecond, 6000 * time.Millisecond
	if t < time.Second {
		return base + time.Duration(float64(peak-base)*t.Seconds())
	}
	return base + time.Duration(float64(peak-base)*math.Exp(-(t-time.Second).Seconds()/4))
}

// netSimScenarios are the paths -netsim offers, by name.
var netSimScenarios = map[string]func(t time.Duration) netSimState{
	// 3% loss in bursts.
	"loss": func(t time.Duration) netSimState {
		return netSimState{ping: 100 * time.Millisecond, loss: 0.03, burst: 3}
	},
	// A link of 1000 kbit/s narrowing to 150 for 10 s of every 30.
	"narrow": func(t time.Duration) netSimState {
		if t%(30*time.Second) >= 20*time.Second {
			return netSimState{ping: 100 * time.Millisecond, rate: 150}
		}
		return netSimState{ping: 100 * time.Millisecond, rate: 1000}
	},
	// A path switch every 40 s: 1 s of outage, the round trip up to 6 s and
	// back down.
	"switch": func(t time.Duration) netSimState {
		switch t %= 40 * time.Second; {
		case t < 20*time.Second:
			return netSimState{ping: 200 * time.Millisecond}
		case t < 21*time.Second:
			return netSimState{ping: 200 * time.Millisecond, down: true}
		default:
			return netSimState{ping: netSimSwitchPing(t - 21*time.Second)}
		}
	},
	// All of these in turn, 60 s round: 1% loss, a narrowing, a switch,
	// 5% loss in bursts.
	"mixed": func(t time.Duration) netSimState {
		s := netSimState{ping: 150 * time.Millisecond, rate: 1000, loss: 0.01, burst: 2}
		switch t %= 60 * time.Second; {
		case t >= 15*time.Second && t < 27*time.Second:
			s.rate = 150
		case t >= 30*time.Second && t < 31*time.Second:
			s.down = true
		case t >= 31*time.Second && t < 45*time.Second:
			s.ping = netSimSwitchPing(t - 31*time.Second)
		case t >= 45*time.Second && t < 55*time.Second:
			s.loss, s.burst = 0.05, 3
		}
		return s
	},
	// All at once, 30 s round: 300 kbit/s dropping to 100, 3% loss in
	// bursts, a path switch.
	"heavy": func(t time.Duration) netSimState {
		s := netSimState{ping: 200 * time.Millisecond, rate: 300, loss: 0.03, burst: 3}
		t %= 30 * time.Second
		if t >= 5*time.Second && t < 12*time.Second {
			s.rate = 100
		}
		switch {
		case t >= 15*time.Second && t < 16*time.Second:
			s.down = true
		case t >= 16*time.Second:
			s.ping = netSimSwitchPing(t - 16*time.Second)
		}
		return s
	},
}

// netSim is the scenario for future sessions ("": none).
var netSim string

// NetSimNames are the scenarios -netsim takes.
func NetSimNames() []string {
	var names []string
	for n := range netSimScenarios {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SetNetSim sends future sessions' media through a simulated bad path
// ("": none).
func SetNetSim(name string) error {
	if _, ok := netSimScenarios[name]; name != "" && !ok {
		return fmt.Errorf("unknown -netsim %q: want one of %s", name, strings.Join(NetSimNames(), ", "))
	}
	netSim = name
	return nil
}

// simPacketConn sends what is written through the simulated path: a
// datagram lost is dropped (the writer sees it sent), the others go out
// when they arrive at the far end of the simulated link.
type simPacketConn struct {
	net.PacketConn
	at       func(t time.Duration) netSimState
	start    time.Time
	stop     <-chan struct{}
	mu       sync.Mutex
	rng      *rand.Rand
	bad      bool          // the loss model's bad state (Gilbert)
	linkFree time.Duration // when the link has sent what it holds
	due      simQueue
	wake     chan struct{}
}

type simDatagram struct {
	at   time.Duration
	data []byte
	addr net.Addr
}

type simQueue []simDatagram

func (q simQueue) Len() int           { return len(q) }
func (q simQueue) Less(i, j int) bool { return q[i].at < q[j].at }
func (q simQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *simQueue) Push(x any)        { *q = append(*q, x.(simDatagram)) }
func (q *simQueue) Pop() any {
	old := *q
	d := old[len(old)-1]
	*q = old[:len(old)-1]
	return d
}

// newSimPacketConn wraps pc in the scenario name (as SetNetSim checked).
func newSimPacketConn(pc net.PacketConn, name string, stop <-chan struct{}) *simPacketConn {
	c := &simPacketConn{PacketConn: pc, at: netSimScenarios[name], start: time.Now(), stop: stop,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())), wake: make(chan struct{}, 1)}
	log.Printf("[netsim] outgoing media through the simulated path %q", name)
	go c.run()
	return c
}

// lose says whether the next packet is lost: the bad state loses every
// packet, its mean stay burst packets, the long-run loss rate s.loss.
func (c *simPacketConn) lose(s netSimState) bool {
	if s.loss <= 0 {
		c.bad = false
		return false
	}
	toGood := 1 / max(1, s.burst)
	toBad := s.loss * toGood / (1 - s.loss)
	if c.bad {
		c.bad = c.rng.Float64() >= toGood
	} else {
		c.bad = c.rng.Float64() < toBad
	}
	return c.bad
}

func (c *simPacketConn) WriteTo(data []byte, addr net.Addr) (int, error) {
	select {
	case <-c.stop:
		return 0, net.ErrClosed
	default:
	}
	c.mu.Lock()
	now := time.Since(c.start)
	s := c.at(now)
	size := len(data) + netSimHeader
	ok := !s.down
	var arrive time.Duration
	for off := 0; off < size; off += netSimFrag {
		part := min(netSimFrag, size-off)
		depart := now
		if s.rate > 0 {
			begin := max(now, c.linkFree)
			if begin-now > netSimQueue {
				ok = false // the queue is full
				continue
			}
			depart = begin + time.Duration(float64(part*8)/s.rate*float64(time.Millisecond))
			c.linkFree = depart
		}
		if c.lose(s) {
			ok = false
		}
		arrive = max(arrive, depart+s.ping/2+time.Duration(c.rng.Int63n(int64(netSimJitter))))
	}
	if ok {
		heap.Push(&c.due, simDatagram{at: arrive, data: append([]byte(nil), data...), addr: addr})
	}
	c.mu.Unlock()
	if ok {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

// run sends the datagrams when they are due.
func (c *simPacketConn) run() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		c.mu.Lock()
		now := time.Since(c.start)
		var out []simDatagram
		for c.due.Len() > 0 && c.due[0].at <= now {
			out = append(out, heap.Pop(&c.due).(simDatagram))
		}
		wait := time.Hour
		if c.due.Len() > 0 {
			wait = c.due[0].at - now
		}
		c.mu.Unlock()
		for _, d := range out {
			if _, err := c.PacketConn.WriteTo(d.data, d.addr); err != nil && isFatalSendError(err) {
				return
			}
		}
		timer.Reset(wait)
		select {
		case <-c.stop:
			return
		case <-c.wake:
		case <-timer.C:
		}
	}
}
