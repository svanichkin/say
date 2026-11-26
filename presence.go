package main

import (
	"context"
	"sync"
	"time"

	"github.com/svanichkin/say/contacts"
	tcpsignal "github.com/svanichkin/say/network/tcp"
	"github.com/svanichkin/say/ui"
)

// Who is online: every device in the contacts (but this one) is asked
// whether Say runs there (tcpsignal.Probe: PING, no call) now and then,
// while no call is on; the people with one that answers have a green dot
// in the list, and so have their devices to call. A mesh peer cannot tell
// us it came up, so asking is the only way to know.

const (
	presenceEvery   = 30 * time.Second // between two rounds
	presenceTimeout = 5 * time.Second  // a device that has not answered by then is not there
	presenceAtOnce  = 8                // devices asked at the same time
	// For presenceStartup after Say starts, the rounds come every
	// presenceStartEvery: the mesh is still finding its routes, and a device
	// not reached at once turns green seconds later, not 30.
	presenceStartup    = time.Minute
	presenceStartEvery = 5 * time.Second
)

// watchPresence asks round after round until ctx ends (probe: whether Say
// answers at host:port).
func (a *homeApp) watchPresence(ctx context.Context, probe func(host string, port int) bool) {
	t := time.NewTimer(0) // at once: as soon as the node is up
	defer t.Stop()
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.presenceKick: // the contacts came or changed
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		if !tcpsignal.InCall() {
			a.askPresence(probe)
		}
		if time.Since(started) < presenceStartup {
			t.Reset(presenceStartEvery) // the mesh finds its routes: ask again soon
		} else {
			t.Reset(presenceEvery)
		}
	}
}

// askPresenceSoon makes watchPresence ask now: at start the first round
// came before the contacts were read, and the dots waited 30 s.
func (a *homeApp) askPresenceSoon() {
	select {
	case a.presenceKick <- struct{}{}:
	default:
	}
}

// askPresence asks every device once and keeps who answered; the menu is
// drawn again when that changed.
func (a *homeApp) askPresence(probe func(host string, port int) bool) {
	sn := a.store.Snapshot()
	seen := map[string]bool{}
	var devs []contacts.Device
	for _, p := range sn.All {
		for _, d := range p.Devices {
			if d.Host == "" || d.Host == sn.Self || seen[d.Host] {
				continue
			}
			seen[d.Host] = true
			if d.Port == 0 {
				d.Port = contacts.DefaultPort
			}
			devs = append(devs, d)
		}
	}
	online := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, presenceAtOnce)
	for _, d := range devs {
		wg.Add(1)
		go func(d contacts.Device) {
			defer wg.Done()
			slots <- struct{}{}
			ok := probe(d.Host, d.Port)
			<-slots
			if ok {
				mu.Lock()
				online[d.Host] = true
				mu.Unlock()
			}
		}(d)
	}
	wg.Wait()
	a.mu.Lock()
	changed := len(online) != len(a.online)
	for h := range online {
		changed = changed || !a.online[h]
	}
	a.online = online
	a.asked = seen
	a.mu.Unlock()
	if changed {
		ui.HomeRefresh()
		a.refreshTray()
	}
}

// deviceOnline reports whether the device's Say answered last time.
func (a *homeApp) deviceOnline(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online[host]
}

// deviceOffline reports whether the device was asked and did not answer:
// it is not called (one not asked yet may be).
func (a *homeApp) deviceOffline(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asked[host] && !a.online[host]
}

// personOffline reports whether none of the person's devices (but this
// one) answered: a red ● from the start, green once one answers.
func (a *homeApp) personOffline(p *contacts.Person, self string) bool {
	if p == nil {
		return false
	}
	has := false
	for _, d := range p.Devices {
		if d.Host == self {
			continue
		}
		if a.deviceOnline(d.Host) {
			return false
		}
		has = true
	}
	return has
}

// personOnline reports whether any of the person's devices (but this one)
// answered last time.
func (a *homeApp) personOnline(p *contacts.Person, self string) bool {
	if p == nil {
		return false
	}
	for _, d := range p.Devices {
		if d.Host != self && a.deviceOnline(d.Host) {
			return true
		}
	}
	return false
}
