package mediactrl

import (
	"sync"
	"sync/atomic"
)

// State describes the current local capture and remote playback switches.
type State struct {
	CaptureEnabled  bool
	PlaybackEnabled bool
	VideoEnabled    bool
}

var (
	captureEnabled  atomic.Bool
	playbackEnabled atomic.Bool
	videoEnabled    atomic.Bool
	peerVideoShown  atomic.Bool
)

func init() {
	captureEnabled.Store(true)
	playbackEnabled.Store(true)
	videoEnabled.Store(true)
	peerVideoShown.Store(true)
}

// PeerVideoShown reports whether the peer's video is shown. Off, its pane
// shows no signal (its frames still come and are decoded: shown again at
// once).
func PeerVideoShown() bool {
	return peerVideoShown.Load()
}

// TogglePeerVideo flips whether the peer's video is shown and returns the
// new value.
func TogglePeerVideo() bool {
	for {
		current := peerVideoShown.Load()
		if peerVideoShown.CompareAndSwap(current, !current) {
			notifyListeners()
			return !current
		}
	}
}

// CaptureEnabled reports whether the local microphone is on.
func CaptureEnabled() bool {
	return captureEnabled.Load()
}

// PlaybackEnabled reports whether the peer's sound is played (the peer
// view's menu). Its video is not affected (PeerVideoShown).
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

// VideoEnabled reports whether the camera's frames go out (and to the self
// view). Off, the camera keeps running but its frames are dropped: the peer
// sees its pane lose the signal.
func VideoEnabled() bool {
	return videoEnabled.Load()
}

// ToggleVideo flips the camera switch and returns the new value.
func ToggleVideo() bool {
	for {
		current := videoEnabled.Load()
		next := !current
		if videoEnabled.CompareAndSwap(current, next) {
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
		VideoEnabled:    videoEnabled.Load(),
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
