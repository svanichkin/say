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

// The call is lost after keepaliveTimeout without anything from the peer;
// its media arriving keeps the call alive while the pongs are held up.
func TestKeepaliveExpiresOnlyInSilence(t *testing.T) {
	start := time.Now()
	ka := &tcpKeepalive{lastPong: start, timeout: keepaliveTimeout, pending: map[uint64]time.Time{}}
	if ka.expired(start.Add(keepaliveTimeout - time.Second)) {
		t.Fatal("expired before the timeout")
	}
	if !ka.expired(start.Add(keepaliveTimeout + time.Second)) {
		t.Fatal("not expired after the timeout in silence")
	}
	ui.NotePeerHeard() // a packet of the call, now
	if ka.expired(time.Now().Add(keepaliveTimeout - time.Second)) {
		t.Fatal("expired while the peer's media comes in")
	}
	if !ka.expired(time.Now().Add(keepaliveTimeout + time.Second)) {
		t.Fatal("not expired after the media stopped too")
	}
}
