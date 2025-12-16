package ui

import (
	"github.com/svanichkin/say/codec"
	"sync"
	"time"
)

type frameRecord struct {
	frame     *codec.TextFrame
	version   uint64
	updatedAt time.Time
}

const (
	defaultViewportFPS = 25
	maxViewportFPS     = 30
)

var (
	localFrameMu    sync.RWMutex
	remoteFrameMu   sync.RWMutex
	localFrame      frameRecord
	remoteFrame     frameRecord
	frameLimitMu    sync.Mutex
	frameInterval   = time.Second / defaultViewportFPS
	frameLastUpdate = make(map[string]time.Time)
)

// SetLocalFrame updates the cached local preview frame if the viewport is ready.
// It returns true when the frame was accepted.
func SetLocalFrame(frame *codec.TextFrame) bool {
	if frame == nil || !allowFrame(localFrameType) {
		return false
	}
	localFrameMu.Lock()
	localFrame.frame = frame
	localFrame.version++
	localFrame.updatedAt = time.Now()
	localFrameMu.Unlock()
	noteFrameArrival(localFrameType)
	return true
}

// SetRemoteFrame updates the cached remote preview frame.
// It returns true when the frame was accepted.
func SetRemoteFrame(frame *codec.TextFrame) bool {
	if frame == nil || !allowFrame(remoteFrameType) {
		return false
	}
	remoteFrameMu.Lock()
	remoteFrame.frame = frame
	remoteFrame.version++
	remoteFrame.updatedAt = time.Now()
	remoteFrameMu.Unlock()
	noteFrameArrival(remoteFrameType)
	return true
}

// ClearLocalFrame removes the cached local preview so the UI can fall back to status text.
func ClearLocalFrame() {
	localFrameMu.Lock()
	localFrame.frame = nil
	localFrame.version++
	localFrame.updatedAt = time.Now()
	localFrameMu.Unlock()
	RequestRedraw()
}

// ClearRemoteFrame removes the cached remote preview, allowing the UI to show status text.
func ClearRemoteFrame() {
	remoteFrameMu.Lock()
	remoteFrame.frame = nil
	remoteFrame.version++
	remoteFrame.updatedAt = time.Now()
	remoteFrameMu.Unlock()
	RequestRedraw()
}

func localFrameSnapshot() (*codec.TextFrame, uint64, time.Time) {
	localFrameMu.RLock()
	defer localFrameMu.RUnlock()
	return localFrame.frame, localFrame.version, localFrame.updatedAt
}

func remoteFrameSnapshot() (*codec.TextFrame, uint64, time.Time) {
	remoteFrameMu.RLock()
	defer remoteFrameMu.RUnlock()
	return remoteFrame.frame, remoteFrame.version, remoteFrame.updatedAt
}

// SetVideoFPSLimit changes how often viewport frames are accepted.
// Pass <=0 to disable throttling.
func SetVideoFPSLimit(fps int) {
	frameLimitMu.Lock()
	defer frameLimitMu.Unlock()
	if fps <= 0 {
		frameInterval = 0
		frameLastUpdate = make(map[string]time.Time)
		return
	}
	if fps > maxViewportFPS {
		fps = maxViewportFPS
	}
	frameInterval = time.Second / time.Duration(fps)
	frameLastUpdate = make(map[string]time.Time)
}

func allowFrame(frameType string) bool {
	frameLimitMu.Lock()
	defer frameLimitMu.Unlock()
	if frameInterval <= 0 {
		return true
	}
	now := time.Now()
	if last, ok := frameLastUpdate[frameType]; ok {
		if now.Sub(last) < frameInterval {
			return false
		}
	}
	frameLastUpdate[frameType] = now
	return true
}
