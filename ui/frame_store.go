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
	localFrameMu  sync.RWMutex
	remoteFrameMu sync.RWMutex
	localFrame    frameRecord
	remoteFrame   frameRecord
)

// SetLocalFrame updates the cached local preview frame.
func SetLocalFrame(frame *codec.TextFrame) {
	if frame == nil {
		return
	}
	localFrameMu.Lock()
	localFrame.frame = frame
	localFrame.version++
	localFrame.updatedAt = time.Now()
	localFrameMu.Unlock()
	noteFrameArrival(localFrameType)
}

// SetRemoteFrame updates the cached remote preview frame.
func SetRemoteFrame(frame *codec.TextFrame) {
	if frame == nil {
		return
	}
	remoteFrameMu.Lock()
	remoteFrame.frame = frame
	remoteFrame.version++
	remoteFrame.updatedAt = time.Now()
	remoteFrameMu.Unlock()
	noteFrameArrival(remoteFrameType)
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
