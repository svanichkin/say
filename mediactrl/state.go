package mediactrl

import (
	"sync"
	"sync/atomic"
)

// State describes the current local capture and remote playback switches.
type State struct {
	CaptureEnabled  bool
	PlaybackEnabled bool
}

var (
	captureEnabled  atomic.Bool
	playbackEnabled atomic.Bool
)

func init() {
	captureEnabled.Store(true)
	playbackEnabled.Store(true)
}

// CaptureEnabled reports whether local microphone/camera capture is active.
func CaptureEnabled() bool {
	return captureEnabled.Load()
}

// PlaybackEnabled reports whether remote audio/video playback is active.
func PlaybackEnabled() bool {
	return playbackEnabled.Load()
}

// SetCaptureEnabled updates the capture switch and notifies listeners.
func SetCaptureEnabled(enabled bool) {
	if captureEnabled.Swap(enabled) == enabled {
		return
	}
	notifyListeners()
}

// ToggleCapture flips the capture state and returns the new value.
func ToggleCapture() bool {
	for {
		current := captureEnabled.Load()
		next := !current
		if captureEnabled.CompareAndSwap(current, next) {
			notifyListeners()
			return next
		}
	}
}

// SetPlaybackEnabled updates the playback switch and notifies listeners.
func SetPlaybackEnabled(enabled bool) {
	if playbackEnabled.Swap(enabled) == enabled {
		return
	}
	notifyListeners()
}

// TogglePlayback flips the playback state and returns the new value.
func TogglePlayback() bool {
	for {
		current := playbackEnabled.Load()
		next := !current
		if playbackEnabled.CompareAndSwap(current, next) {
			notifyListeners()
			return next
		}
	}
}

// StateSnapshot returns a copy of the current media switches.
func StateSnapshot() State {
	return State{
		CaptureEnabled:  captureEnabled.Load(),
		PlaybackEnabled: playbackEnabled.Load(),
	}
}

var (
	listenerMu sync.Mutex
	listeners  = make(map[int]func(State))
	nextID     int
)

// Subscribe registers a callback invoked whenever either switch flips.
// It returns a function that removes the listener.
func Subscribe(fn func(State)) func() {
	if fn == nil {
		return func() {}
	}
	listenerMu.Lock()
	id := nextID
	nextID++
	listeners[id] = fn
	listenerMu.Unlock()
	return func() {
		listenerMu.Lock()
		delete(listeners, id)
		listenerMu.Unlock()
	}
}

func notifyListeners() {
	state := StateSnapshot()
	listenerMu.Lock()
	snapshot := make([]func(State), 0, len(listeners))
	for _, fn := range listeners {
		snapshot = append(snapshot, fn)
	}
	listenerMu.Unlock()
	for _, fn := range snapshot {
		func(cb func(State)) {
			defer func() { recover() }()
			cb(state)
		}(fn)
	}
}
