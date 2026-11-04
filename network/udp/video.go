package udp

import "github.com/svanichkin/say/codec"

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
// videoKeyframeInterval forces a keyframe (with its palette) this often on
// the network stream. Delta frames reference the last keyframe's palette, so
// a lost keyframe otherwise left the peer's pane blank or garbled until the
// picture changed enough to trigger one (the palette costs at most 512 bytes).
const videoKeyframeInterval = 10

// videoShapeHold is how many of a glyph's 32 pixels may change before a cell
// takes a new glyph (-video-hold; 0 disables). It stops a static picture from
// shimmering between similar glyphs because of camera noise.
var videoShapeHold = 8

// SetVideoShapeHold sets the shape hold threshold for future sessions.
func SetVideoShapeHold(bits int) {
	videoShapeHold = max(0, min(bits, 32))
}

func initVideoCodec() (previewEncoder, netEncoder *codec.Encoder, previewDecoder *codec.Decoder) {
	previewEncoder = codec.NewEncoder(false, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(false, videoKeyframeInterval, maxVideoFPS, false)
	previewEncoder.SetShapeHold(videoShapeHold)
	netEncoder.SetShapeHold(videoShapeHold)
	previewDecoder = codec.NewDecoder()
	return
}
