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

var (
	localFrameMu    sync.RWMutex
	remoteFrameMu   sync.RWMutex
	localFrame      frameRecord
	remoteFrame     frameRecord
	frameLimitMu    sync.Mutex
	frameInterval   time.Duration // 0: no limit (set by SetVideoFPSLimit)
	frameLastUpdate = make(map[string]time.Time)
	frameTokens     = make(map[string]float64)
)

// frameBurstTokens is how many frames may arrive back to back after a gap.
// Frames come at the target rate with a few milliseconds of jitter; a strict
// "at least one interval since the last frame" rule rejected every frame that
// arrived slightly early and held the panes at ~18 fps instead of 25.
const frameBurstTokens = 2

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
	frameLastUpdate = make(map[string]time.Time)
	frameTokens = make(map[string]float64)
	if fps <= 0 {
		frameInterval = 0
		return
	}
	frameInterval = time.Second / time.Duration(fps)
}

func allowFrame(frameType string) bool {
	return allowFrameAt(frameType, time.Now())
}

// allowFrameAt is a token bucket: on average one frame per frameInterval,
// tolerating jitter and short bursts of up to frameBurstTokens frames.
func allowFrameAt(frameType string, now time.Time) bool {
	frameLimitMu.Lock()
	defer frameLimitMu.Unlock()
	if frameInterval <= 0 {
		return true
	}
	tokens := float64(frameBurstTokens)
	if last, ok := frameLastUpdate[frameType]; ok {
		tokens = min(frameTokens[frameType]+float64(now.Sub(last))/float64(frameInterval), frameBurstTokens)
	}
	frameLastUpdate[frameType] = now
	if tokens < 1 {
		frameTokens[frameType] = tokens
		return false
	}
	frameTokens[frameType] = tokens - 1
	return true
}
