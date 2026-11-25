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
)

// watchPresence asks round after round until ctx ends (probe: whether Say
// answers at host:port).
func (a *homeApp) watchPresence(ctx context.Context, probe func(host string, port int) bool) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !tcpsignal.InCall() {
			a.askPresence(probe)
		}
		t.Reset(presenceEvery)
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
	a.mu.Unlock()
	if changed {
		ui.HomeRefresh()
	}
}

// deviceOnline reports whether the device's Say answered last time.
func (a *homeApp) deviceOnline(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.online[host]
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
