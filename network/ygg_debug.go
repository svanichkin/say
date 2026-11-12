package network

// DEBUG-TEMP: Yggdrasil's peers and the clock, traced for unreliable calls.
// Remove once calls are reliable.

import (
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/yggdrasil-network/yggdrasil-go/src/core"
)

// watchPeers logs the node's peers as they change: added, up, down, gone,
// with their latency and last error, and the root of the tree we are in
// (it changes when the network reorganises, and routes with it). It also
// logs when the clock jumps: the system slept or the process stalled.
func watchPeers(c *core.Core, stop <-chan struct{}) {
	type state struct {
		up      bool
		latency time.Duration
		err     string
		root    string
	}
	prev := map[string]state{}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			if jump := now.Sub(last); jump > 6*time.Second {
				log.Printf("[debug] clock: %v passed in a 2 s tick (the system slept or the process stalled)", jump.Round(time.Millisecond))
			}
			last = now
			cur := map[string]state{}
			for _, p := range c.GetPeers() {
				s := state{up: p.Up, latency: p.Latency, root: short(p.Root)}
				if p.LastError != nil {
					s.err = p.LastError.Error()
				}
				cur[p.URI] = s
			}
			var changes []string
			for uri, s := range cur {
				o, had := prev[uri]
				switch {
				case !had:
					changes = append(changes, fmt.Sprintf("+%s up=%v %v", uri, s.up, s.latency.Round(time.Millisecond)))
				case o.up != s.up:
					line := fmt.Sprintf("%s up %v→%v %v", uri, o.up, s.up, s.latency.Round(time.Millisecond))
					if !s.up && s.err != "" {
						line += " (" + s.err + ")"
					}
					changes = append(changes, line)
				case s.up && o.root != s.root:
					changes = append(changes, fmt.Sprintf("%s root %s→%s", uri, o.root, s.root))
				}
			}
			for uri, o := range prev {
				if _, ok := cur[uri]; !ok {
					line := "-" + uri
					if o.err != "" {
						line += " (" + o.err + ")"
					}
					changes = append(changes, line)
				}
			}
			if len(changes) > 0 {
				sort.Strings(changes)
				up := 0
				for _, s := range cur {
					if s.up {
						up++
					}
				}
				log.Printf("[debug] ygg peers %d up of %d: %s", up, len(cur), strings.Join(changes, "; "))
			}
			prev = cur
		}
	}
}

func short(key []byte) string {
	if len(key) < 4 {
		return "-"
	}
	return hex.EncodeToString(key[:4])
}
