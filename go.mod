module github.com/svanichkin/say

go 1.27.3

require (
	github.com/VoiceBlender/goamr-nb v1.1.0
	github.com/danieldonoghue/govoip-dsp v0.1.0-rc.2
	github.com/gen2brain/malgo v0.11.27
	github.com/gotranspile/g722 v0.0.0-20240123003956-384a1bb16a19
	github.com/hajimehoshi/ebiten/v2 v2.10.5
	github.com/klauspost/compress v1.20.3
	github.com/rivo/uniseg v0.4.7
	github.com/svanichkin/gocam v0.3.0
	github.com/svanichkin/ygg v0.6.3
	github.com/thesyncim/gopus v0.2.2
	github.com/yggdrasil-network/yggdrasil-go v0.5.15
	golang.org/x/image v0.45.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	gvisor.dev/gvisor v0.0.0-20261103192924-1ff810cb4f8e
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/Arceliar/ironwood v0.0.0-20261023231900-0712eccbb962 // indirect
	github.com/Arceliar/phony v0.0.0-20220903101357-530938a4b13d // indirect
	github.com/bits-and-blooms/bitset v1.29.0 // indirect
	github.com/bits-and-blooms/bloom/v3 v3.7.1 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/ebitengine/gomobile v0.0.0-20260820040257-d11f821a26a6 // indirect
	github.com/ebitengine/hideconsole v1.0.0 // indirect
	github.com/ebitengine/purego v0.11.0 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/gologme/log v1.3.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/hjson/hjson-go/v4 v4.7.1 // indirect
	github.com/jeandeaual/go-locale v0.0.0-20250612000132-0ef82f21eade // indirect
	github.com/quic-go/quic-go v0.65.0 // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

replace github.com/ebitengine/hideconsole => ./third_party/hideconsole

replace github.com/gen2brain/malgo => ./third_party/malgo
