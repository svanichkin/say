package udp

import "github.com/svanichkin/say/codec"

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
// videoKeyframeInterval forces a keyframe (with its palette) this often on
// the network stream. Delta frames reference the last keyframe's palette, so
// a lost keyframe otherwise left the peer's pane blank or garbled until the
// picture changed enough to trigger one (the palette costs at most 512 bytes).
const videoKeyframeInterval = 10

// videoKeyframeFrames keeps full frames about every half second at any
// frame rate: after a lost packet, change-only frames resume from the next
// full frame, so this bounds how long the peer's picture can freeze.
func videoKeyframeFrames(fps int) int {
	return max(2, min(videoKeyframeInterval, fps/2))
}

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

func initVideoCodec() (previewEncoder, netEncoder *codec.Encoder, previewDecoder *codec.Decoder) {
	previewEncoder = codec.NewEncoder(false, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(false, videoKeyframeInterval, maxVideoFPS, false)
	previewEncoder.SetShapeHold(videoShapeHold)
	netEncoder.SetShapeHold(videoShapeHold)
	previewEncoder.SetHoldDebug(videoHoldDebug)
	netEncoder.SetHoldDebug(videoHoldDebug)
	previewDecoder = codec.NewDecoder()
	return
}
