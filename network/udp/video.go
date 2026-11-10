package udp

import (
	"time"

	"github.com/svanichkin/say/codec"
)

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
// No periodic keyframes: a receiver that misses a frame asks for one
// (ui.RequestKeyframe), and the encoder sends one by itself when the palette
// cannot be patched or the scene cuts. A keyframe is the whole picture, so
// periodic ones made the stream jump every few seconds for nothing.

// Keyframe requests (a frame was lost): the receiver repeats its request
// while no keyframe arrives, the sender serves at most one per gap. A frame
// after videoSilenceGap without video counts as a lost frame too: the 8-bit
// frame counter can wrap around during a long outage unnoticed.
const (
	videoKeyframeRetry      = 300 * time.Millisecond
	videoKeyframeRequestGap = 250 * time.Millisecond
	videoSilenceGap         = time.Second
	// videoHeartbeat: a frame with no change is sent at least this often
	// (well under videoSilenceGap), the others are skipped.
	videoHeartbeat = 200 * time.Millisecond
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

// Protocol versions (network/tcp ProtocolVersion) from which a peer decodes
// palette patches and range-coded change frames.
const (
	videoPalettePatchProtocol = 3
	videoRangeCodingProtocol  = 4
)

// videoPeer is what the peer's handshake tells about the video it wants.
type videoPeer struct {
	palettePatches bool // its decoder knows palette patches
	rangeCoding    bool // its decoder knows range-coded frames
	mono           bool // it shows brightness only (a colour filter)
}

// initVideoCodec returns the self view and the network encoders. A pane
// shown through a colour filter uses only brightness, so it is encoded
// without colour: the glyphs then follow brightness edges, and the stream
// to such a peer is smaller.
func initVideoCodec(peer videoPeer, selfMono bool) (previewEncoder, netEncoder *codec.Encoder) {
	previewEncoder = codec.NewEncoder(selfMono, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(peer.mono, 0, maxVideoFPS, false)
	previewEncoder.SetShapeHold(videoShapeHold)
	netEncoder.SetShapeHold(videoShapeHold)
	previewEncoder.SetHoldDebug(videoHoldDebug)
	netEncoder.SetHoldDebug(videoHoldDebug)
	netEncoder.SetPalettePatches(peer.palettePatches)
	netEncoder.SetRangeCoding(peer.rangeCoding)
	return
}
