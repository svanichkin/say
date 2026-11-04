package udp

import "github.com/svanichkin/say/codec"

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
// videoKeyframeInterval forces a keyframe (with its palette) this often on
// the network stream. Delta frames reference the last keyframe's palette, so
// a lost keyframe otherwise left the peer's pane blank or garbled until the
// picture changed enough to trigger one (the palette costs at most 512 bytes).
const videoKeyframeInterval = 10

func initVideoCodec() (previewEncoder, netEncoder *codec.Encoder, previewDecoder *codec.Decoder) {
	previewEncoder = codec.NewEncoder(false, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(false, videoKeyframeInterval, maxVideoFPS, false)
	previewDecoder = codec.NewDecoder()
	return
}
