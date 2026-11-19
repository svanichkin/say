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

// With frame references (protocol 6) the receiver asks the peer to recover
// from the newest frame it decoded instead of for a keyframe, and for a
// while after a loss acknowledges every frame it decodes, so the peer codes
// against frames it is known to hold (codec.Encoder.RecoverFrom,
// CodeAgainst). Only the newest of each matters: a newer request or
// acknowledgement replaces one still waiting.

var (
	recoverRequests = make(chan uint16, 1)
	videoAcks       = make(chan uint16, 1)
)

// offerNewest puts v in ch, replacing one still waiting there.
func offerNewest(ch chan uint16, v uint16) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// RequestRecover asks the peer to code its next frame against frame id, the
// newest this side decoded: a later one was lost.
func RequestRecover(id uint16) { offerNewest(recoverRequests, id) }

// RecoverRequests delivers RequestRecover calls to the control connection.
func RecoverRequests() <-chan uint16 { return recoverRequests }

// AckVideo tells the peer this side decoded frame id.
func AckVideo(id uint16) { offerNewest(videoAcks, id) }

// VideoAcks delivers AckVideo calls to the control connection.
func VideoAcks() <-chan uint16 { return videoAcks }

var peerRecover, peerAck atomic.Int64

func init() {
	peerRecover.Store(-1)
	peerAck.Store(-1)
}

// NotePeerRecover records that the peer asked to recover from frame id.
func NotePeerRecover(id uint16) { peerRecover.Store(int64(id)) }

// TakePeerRecover reports (once) the frame the peer asked to recover from.
func TakePeerRecover() (uint16, bool) {
	v := peerRecover.Swap(-1)
	return uint16(v), v >= 0
}

// NotePeerAck records that the peer decoded frame id.
func NotePeerAck(id uint16) { peerAck.Store(int64(id)) }

// TakePeerAck reports (once) the newest frame the peer acknowledged.
func TakePeerAck() (uint16, bool) {
	v := peerAck.Swap(-1)
	return uint16(v), v >= 0
}
