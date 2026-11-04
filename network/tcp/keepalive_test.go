package tcp

import (
	"testing"
	"time"

	"github.com/svanichkin/say/ui"
)

func TestPongCarriesAudioAndVideoLoss(t *testing.T) {
	ka := &tcpKeepalive{lastPong: time.Now(), timeout: time.Minute, pending: map[uint64]time.Time{}}
	ui.UpdateRxAudioLoss(0.04)
	ui.UpdateRxVideoLoss(0.12)
	ping := ka.newPing()
	pong := ka.pongPayload(ping)
	if len(pong) != pongPayloadLen || pong[pingPayloadLen] != 4 || pong[pingPayloadLen+1] != 12 {
		t.Fatalf("pong = %v, want audio 4%% and video 12%%", pong)
	}
	_, audio, ok := ka.acceptPong(pong)
	if !ok || audio != 0.04 {
		t.Fatalf("acceptPong: ok=%v audio=%v", ok, audio)
	}
	if loss, at := ui.PeerVideoLossSnapshot(); at.IsZero() || loss != 0.12 {
		t.Fatalf("peer video loss = %v, want 0.12", loss)
	}
	// A pong from an older peer has no video byte and is still accepted.
	legacy := ka.pongPayload(ka.newPing())[:pongLegacyLen]
	if _, _, ok := ka.acceptPong(legacy); !ok {
		t.Fatal("legacy 9-byte pong rejected")
	}
}
