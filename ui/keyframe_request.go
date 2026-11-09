package ui

import "sync/atomic"

// Keyframe requests travel from the video receiver (UDP) to the peer over
// the control connection (TCP), and from there to the peer's video sender.

var keyframeRequests = make(chan struct{}, 1)

// RequestKeyframe asks the peer for a keyframe: a video frame was lost, and
// change frames cannot be shown until the next full frame.
func RequestKeyframe() {
	select {
	case keyframeRequests <- struct{}{}:
	default: // one is already pending
	}
}

// KeyframeRequests delivers RequestKeyframe calls to the control connection.
func KeyframeRequests() <-chan struct{} {
	return keyframeRequests
}

var peerWantsKeyframe atomic.Bool

// NotePeerKeyframeRequest records that the peer asked for a keyframe.
func NotePeerKeyframeRequest() {
	peerWantsKeyframe.Store(true)
}

// TakePeerKeyframeRequest reports (once) whether the peer asked for a keyframe.
func TakePeerKeyframeRequest() bool {
	return peerWantsKeyframe.Swap(false)
}
