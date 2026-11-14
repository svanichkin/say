package tcp

// DEBUG-TEMP: the peer's log comes over the call. The side that wants it
// (the caller started with -log debug) sends frameLogWant; the other side
// then sends what its log kept (logs.Tap) and goes on with every new line,
// zstd-compressed, at a pace that leaves the control stream to the call.
// Peers without this ignore both frames. Remove once calls are reliable.

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/dbg"
	"github.com/svanichkin/say/logs"
)

const (
	frameLogWant byte = 0x0d // caller → callee: send me your log
	frameLog     byte = 0x0e // callee → caller: lines of the log, zstd
)

// The log goes in chunks of logChunk raw bytes, one every logEvery: a few
// kB/s compressed, so that keepalives and chat are not held up behind it.
const (
	logChunk = 32 << 10
	logEvery = 400 * time.Millisecond
)

var (
	logEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	logDecoder, _ = zstd.NewReader(nil)
)

// askPeerLog asks the peer for its log when we log at debug level and keep
// the peer's log somewhere.
func askPeerLog(conn net.Conn) {
	if !dbg.On || !conf.Verbose || logs.PeerLogPath() == "" {
		return
	}
	if err := writeFrameSafe(conn, frameLogWant, nil); err != nil {
		log.Printf("[debug] asking the peer for its log: %v", err)
		return
	}
	log.Printf("[debug] asked the peer for its log → %s", logs.PeerLogPath())
}

// logSenders are the connections our log is being sent on.
var logSenders sync.Map // net.Conn → struct{}

// sendLog sends our log on conn until the connection closes.
func sendLog(conn net.Conn, peerLabel string) {
	if !dbg.On {
		return // a release build keeps no log to send
	}
	if _, busy := logSenders.LoadOrStore(conn, struct{}{}); busy {
		return
	}
	go func() {
		defer logSenders.Delete(conn)
		from := logs.Oldest()
		log.Printf("[debug] sending our log to %s from line %d (boot %s)", peerLabel, from, logs.BootID)
		tick := time.NewTicker(logEvery)
		defer tick.Stop()
		sent := 0
		for range tick.C {
			chunk, next := logs.Since(from, logChunk)
			if len(chunk) == 0 {
				continue
			}
			if err := writeFrameSafe(conn, frameLog, logEncoder.EncodeAll(chunk, nil)); err != nil {
				log.Printf("[debug] log to %s stopped after %d kB: %v", peerLabel, sent>>10, err)
				return
			}
			sent += len(chunk)
			from = next
		}
	}()
}

// receiveLog writes lines of the peer's log.
func receiveLog(payload []byte) {
	data, err := logDecoder.DecodeAll(payload, nil)
	if err != nil {
		log.Printf("[debug] a chunk of the peer's log could not be read: %v", err)
		return
	}
	logs.WritePeer(data)
}
