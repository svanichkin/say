package udp

import "github.com/svanichkin/say/codec"

// initVideoCodec возвращает набор кодеков для локального предпросмотра и сетевой отправки.
// Выделяем отдельную функцию, чтобы инициализация видеокодеков была симметрична audio.go.
func initVideoCodec() (previewEncoder, netEncoder *codec.Encoder, previewDecoder *codec.Decoder) {
	previewEncoder = codec.NewEncoder(false, 0, maxVideoFPS, true)
	netEncoder = codec.NewEncoder(false, 0, maxVideoFPS, false)
	previewDecoder = codec.NewDecoder()
	return
}
