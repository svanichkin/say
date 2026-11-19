package udp

import (
	"time"

	"github.com/svanichkin/say/codec"
)

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
// No periodic keyframes. Frames carry numbers and references (protocol 6,
// codec.Encoder.SetRefs): a receiver that misses one asks the peer to
// recover from the newest frame it decoded (ui.RequestRecover), and the
// next frame is coded against that one, a keyframe only when it is no
// longer kept (two seconds back) or the receiver decoded none yet
// (ui.RequestKeyframe). For videoAckWindow after a loss the receiver also
// acknowledges every frame it decodes (ui.AckVideo), and every
// videoAckEvery frames the sender codes one against the newest acknowledged:
// on a lossy path a loss then spoils at most that many frames, without a
// request (cmd/videosim, 1% loss: stalls 4.9% -> 1.6% of the time, wrong
// glyphs 1.48% -> 0.72%, 10% more bytes while it lasts; against every
// frame 1.0% and 0.54% for 33% more). On a clean path frames are coded
// against the frame before, the smallest.
// The encoder still sends a keyframe by itself when the palette cannot be
// patched or the scene cuts.

// Recovery requests (a frame was lost): the receiver repeats its request
// while frames cannot be decoded, the sender serves at most one keyframe
// per gap. Frames arriving out of order wait up to videoReorderWait for a
// missing one, then it counts as lost: on a path with jitter the next
// frame often overtakes the one before, and dropping the late one broke
// the chain of change frames (stalls half the time at 60 ms of jitter).
const (
	videoKeyframeRetry      = 300 * time.Millisecond
	videoKeyframeRequestGap = 250 * time.Millisecond
	videoSilenceGap         = time.Second
	// videoHeartbeat: a frame with no change is sent at least this often
	// (well under videoSilenceGap), the others are skipped.
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

// initVideoCodec returns the self view and the network encoders. A pane
// shown through a colour filter uses only brightness, so it is encoded
// without colour: the glyphs then follow brightness edges, and the stream
// to such a peer is smaller.
func initVideoCodec(peer videoPeer, selfMono bool) (previewEncoder, netEncoder *codec.Encoder) {
	previewEncoder = codec.NewEncoder(selfMono, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(peer.mono, 0, maxVideoFPS, false)
	netEncoder.SetRefs(true) // frames with numbers and references (protocol 6)
	previewEncoder.SetShapeHold(videoShapeHold)
	netEncoder.SetShapeHold(videoShapeHold)
	previewEncoder.SetHoldDebug(videoHoldDebug)
	netEncoder.SetHoldDebug(videoHoldDebug)
	return
}
