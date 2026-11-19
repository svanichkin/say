package udp

import (
	"time"

	"github.com/svanichkin/say/codec"
)

// Video goes in layers of small datagrams (video_layers.go), each layer and
// band a stream of frames with numbers and references (protocol 6,
// codec.Encoder.SetRefs): a receiver that misses one asks the peer to
// recover that layer from the newest frame it decoded, and the next frame
// is coded against that one, a keyframe only when it is no longer kept (two
// seconds back) or the receiver decoded none yet. For videoAckWindow after a
// loss the receiver also acknowledges every frame of the layer it decodes,
// and every videoAckEvery frames the sender codes one against the newest
// acknowledged: on a lossy path a loss then spoils at most that many frames,
// without a request (cmd/videosim, 1% loss: stalls 4.9% -> 1.6% of the
// time, wrong glyphs 1.48% -> 0.72%, 10% more bytes while it lasts). On a
// clean path frames are coded against the frame before, the smallest. The
// encoder still sends a keyframe by itself when the palette cannot be
// patched or the scene cuts.

// Recovery requests (a frame was lost): the receiver repeats its request
// while frames cannot be decoded, the sender serves at most one keyframe
// per gap. Datagrams arriving out of order wait up to videoReorderWait for a
// missing one of their layer, then it counts as lost: on a path with jitter
// the next frame often overtakes the one before, and dropping the late one
// broke the chain of change frames (stalls half the time at 60 ms of
// jitter).
const (
	videoKeyframeRetry      = 300 * time.Millisecond
	videoKeyframeRequestGap = 250 * time.Millisecond
	// videoHeartbeat: a lower layer frame with no change is sent at least
	// this often, the others are skipped.
	videoHeartbeat   = 200 * time.Millisecond
	videoReorderWait = 80 * time.Millisecond
	videoAckWindow   = 3 * time.Second
	videoAckEvery    = 3
)

// videoShapeHold is the mean per-pixel colour difference (sum of three
// channels) below which a cell keeps its previous glyph and colours
// (-video-hold; 0 disables). It stops a still picture from shimmering.
var videoShapeHold = 10

// videoHoldDebug tints held cells lilac (-video-hold-debug).
var videoHoldDebug bool

// SetVideoHoldDebug turns the held-cell tint on or off for future sessions.
func SetVideoHoldDebug(on bool) {
	videoHoldDebug = on
}

// SetVideoShapeHold sets the cell hold threshold for future sessions.
func SetVideoShapeHold(threshold int) {
	videoShapeHold = max(0, threshold)
}

// videoPeer is what the peer's handshake tells about the video it wants.
type videoPeer struct {
	mono bool // it shows brightness only (a colour filter)
}

// initVideoCodec returns the self view's encoder (the network's are the
// layers', video_layers.go). A pane shown through a colour filter uses only
// brightness.
func initVideoCodec(selfMono bool) *codec.Encoder {
	e := codec.NewEncoder(selfMono, 0, maxVideoFPS, true)
	e.SetShapeHold(videoShapeHold)
	e.SetHoldDebug(videoHoldDebug)
	return e
}
