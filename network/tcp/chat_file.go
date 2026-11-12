package tcp

import (
	"encoding/binary"
	"fmt"
	"github.com/svanichkin/say/lang"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/ui"
)

// fileChunkBytes is the file data per frame: the largest that fits the
// 16-bit frame length together with the chunk header (id and offset).
const fileChunkBytes = 60 * 1024

// File transfer pacing. TCP alone would fill every queue on the path and
// delay the call's audio and video, which share it; the pacer instead keeps
// the extra delay it causes small (a delay-based controller like LEDBAT,
// which background downloads use). The control connection's ping/pong is
// the probe: it travels in the same TCP stream as the file data, so its RTT
// grows as soon as the file builds a queue.
const (
	fileRateStart     = 1 << 20   // bytes/s
	fileRateMin       = 64 * 1024 // bytes/s
	fileRateIncrease  = 1.25      // per clean probe
	fileRateDecrease  = 0.7       // when the queue or media loss grows
	fileQueueTarget   = 40 * time.Millisecond
	fileQueueSevere   = 200 * time.Millisecond
	fileLossThreshold = 0.02
	fileLossSevere    = 0.05
	fileSeverePause   = 200 * time.Millisecond
	fileProbeInterval = 250 * time.Millisecond // ping interval while a file is sent
	// fileBurstCredit lets the pacer catch up after sleeping too long (the
	// Windows timer ticks every ~15.6 ms), so the average rate holds.
	fileBurstCredit = 20 * time.Millisecond
	// The base RTT is the minimum over rttBuckets of rttBucketSpan: long
	// enough that a transfer's own queue does not become the baseline.
	rttBucketSpan = 30 * time.Second
	rttBuckets    = 10
)

var nextTransferID atomic.Uint32

type adaptiveFilePacer struct {
	mu          sync.Mutex
	rate        float64 // bytes/s
	rttMin      [rttBuckets]time.Duration
	rttBucket   int
	rttBucketAt time.Time
	activeFiles int
	next        time.Time // when the next chunk may go (token bucket)
	pauseUntil  time.Time
	limited     bool      // the rate held the sender back since the last probe
	holdUntil   time.Time // after a decrease: probes still show the old queue
}

func newAdaptiveFilePacer() *adaptiveFilePacer {
	return &adaptiveFilePacer{rate: fileRateStart}
}

func (p *adaptiveFilePacer) beginFile() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.activeFiles++
	p.limited = false
	p.mu.Unlock()
}

func (p *adaptiveFilePacer) endFile() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.activeFiles > 0 {
		p.activeFiles--
	}
	p.mu.Unlock()
}

// active reports whether a file is being sent (the keepalive then probes
// more often).
func (p *adaptiveFilePacer) active() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeFiles > 0
}

// baseRTT is the smallest RTT seen recently: the path without our queue.
func (p *adaptiveFilePacer) baseRTT() time.Duration {
	var base time.Duration
	for _, r := range p.rttMin {
		if r > 0 && (base == 0 || r < base) {
			base = r
		}
	}
	return base
}

func (p *adaptiveFilePacer) recordRTT(rtt time.Duration, now time.Time) {
	if p.rttBucketAt.IsZero() {
		p.rttBucketAt = now
	}
	for now.Sub(p.rttBucketAt) >= rttBucketSpan {
		p.rttBucket = (p.rttBucket + 1) % rttBuckets
		p.rttMin[p.rttBucket] = 0
		p.rttBucketAt = p.rttBucketAt.Add(rttBucketSpan)
	}
	if cur := p.rttMin[p.rttBucket]; cur == 0 || rtt < cur {
		p.rttMin[p.rttBucket] = rtt
	}
}

// observe adapts the rate to one probe: the control connection's RTT and
// the media loss the peer reports.
func (p *adaptiveFilePacer) observe(rtt time.Duration, loss float64) {
	p.observeAt(rtt, loss, time.Now())
}

func (p *adaptiveFilePacer) observeAt(rtt time.Duration, loss float64, now time.Time) {
	if p == nil || rtt <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recordRTT(rtt, now)
	if p.activeFiles == 0 {
		return
	}
	queue := rtt - p.baseRTT()
	limited := p.limited
	p.limited = false
	if now.Before(p.holdUntil) {
		// Probes sent before the last decrease still see the queue it
		// is draining: one decrease per round trip, not one per probe.
		return
	}
	switch {
	case loss >= fileLossSevere || queue > fileQueueSevere:
		p.rate *= 0.5
		p.pauseUntil = now.Add(fileSeverePause)
		p.holdUntil = now.Add(rtt + fileSeverePause)
	case loss >= fileLossThreshold || queue > fileQueueTarget:
		p.rate *= fileRateDecrease
		p.holdUntil = now.Add(rtt)
	case limited:
		// Grow only while the rate holds the sender back: a slow disk or
		// a small file must not leave a huge rate for the next burst.
		p.rate *= fileRateIncrease
	}
	p.rate = max(p.rate, fileRateMin)
}

// waitAfterChunk paces the transfer: a token bucket at the current rate.
func (p *adaptiveFilePacer) waitAfterChunk(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.mu.Lock()
	now := time.Now()
	start := p.next
	if earliest := now.Add(-fileBurstCredit); start.Before(earliest) {
		start = earliest
	}
	if start.Before(p.pauseUntil) {
		start = p.pauseUntil
	}
	p.next = start.Add(time.Duration(float64(n) / p.rate * float64(time.Second)))
	wait := p.next.Sub(now)
	if wait > 0 {
		p.limited = true
	}
	p.mu.Unlock()
	if wait > time.Millisecond {
		time.Sleep(wait)
	}
}

type incomingFile struct {
	id       uint32
	name     string
	total    int64
	received int64
	tmp      string
	file     *os.File
	began    time.Time
}

// File transfer outcome reported by the receiver in a frameFileResult:
// [id:4][status:1][utf-8 error message].
const (
	fileResultSaved  byte = 0
	fileResultFailed byte = 1
)

// fileResultTimeout bounds how long the sender waits for the receiver to
// confirm a file after the last chunk was written.
const fileResultTimeout = 60 * time.Second

type fileResult struct {
	saved bool
	msg   string
}

// pendingFileResults routes receiver confirmations to the sending goroutine.
var pendingFileResults sync.Map // uint32 transfer id → chan fileResult

func encodeFileResult(id uint32, status byte, msg string) []byte {
	if len(msg) > 512 {
		msg = msg[:512]
	}
	out := make([]byte, 5+len(msg))
	binary.BigEndian.PutUint32(out[:4], id)
	out[4] = status
	copy(out[5:], msg)
	return out
}

func installChatActions(conn net.Conn, pacer *adaptiveFilePacer) {
	ui.SetChatActions(func(text string) {
		if err := writeFrameSafe(conn, frameChat, []byte(text)); err != nil {
			ui.AddChatMessage(lang.T(lang.MsgFailed, err.Error()), false)
			return
		}
	}, func(path string) { sendFile(conn, path, pacer) })
}

// handleChatFrame processes chat and file frames. conn is used to report the
// outcome of an incoming file back to its sender.
func handleChatFrame(conn net.Conn, t byte, payload []byte, incoming **incomingFile) {
	reply := func(id uint32, status byte, msg string) {
		if conn == nil {
			return
		}
		if err := writeFrameSafe(conn, frameFileResult, encodeFileResult(id, status, msg)); err != nil {
			log.Printf("[file] result send failed: %v", err)
		}
	}
	fail := func(id uint32, name, msg string) {
		ui.FinishReceive(lang.T(lang.RxFailed, name, msg))
		reply(id, fileResultFailed, msg)
	}
	switch t {
	case frameChat:
		ui.AddChatMessage(string(payload), false)
	case frameFileResult:
		if len(payload) < 5 {
			return
		}
		id := binary.BigEndian.Uint32(payload[:4])
		if ch, ok := pendingFileResults.Load(id); ok {
			select {
			case ch.(chan fileResult) <- fileResult{saved: payload[4] == fileResultSaved, msg: string(payload[5:])}:
			default:
			}
		}
	case frameFileStart:
		if len(payload) < 14 {
			return
		}
		id := binary.BigEndian.Uint32(payload[:4])
		total := int64(binary.BigEndian.Uint64(payload[4:12]))
		nameLen := int(binary.BigEndian.Uint16(payload[12:14]))
		if total < 0 || nameLen == 0 || nameLen > 255 || len(payload) != 14+nameLen {
			reply(id, fileResultFailed, "invalid file header")
			return
		}
		name := filepath.Base(strings.TrimSpace(string(payload[14:])))
		if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
			reply(id, fileResultFailed, "invalid file name")
			return
		}
		dir, err := downloadsDir()
		if err != nil {
			fail(id, name, err.Error())
			return
		}
		// A unique name: transfer ids restart with every call, and a part
		// left by an interrupted transfer must not block the next one.
		f, err := os.CreateTemp(dir, "."+name+".*.part")
		if err != nil {
			fail(id, name, err.Error())
			return
		}
		tmp := f.Name()
		abortIncoming(incoming)
		*incoming = &incomingFile{id: id, name: name, total: total, tmp: tmp, file: f, began: time.Now()}
		ui.SetReceiveProgress(name, 0, total)
	case frameFileChunk:
		f := *incoming
		if f == nil || f.file == nil || len(payload) < 12 || binary.BigEndian.Uint32(payload[:4]) != f.id {
			return
		}
		offset := int64(binary.BigEndian.Uint64(payload[4:12]))
		chunk := payload[12:]
		if offset != f.received || f.received+int64(len(chunk)) > f.total {
			return
		}
		n, err := f.file.Write(chunk)
		f.received += int64(n)
		ui.SetReceiveProgress(f.name, f.received, f.total)
		if err == nil && n != len(chunk) {
			err = io.ErrShortWrite
		}
		if err != nil {
			closeIncoming(incoming)
			fail(f.id, f.name, err.Error())
		}
	case frameFileEnd:
		f := *incoming
		if f == nil || len(payload) != 4 || binary.BigEndian.Uint32(payload) != f.id {
			return
		}
		*incoming = nil
		if err := f.file.Close(); err != nil {
			os.Remove(f.tmp)
			fail(f.id, f.name, err.Error())
			return
		}
		if f.received != f.total {
			os.Remove(f.tmp)
			fail(f.id, f.name, fmt.Sprintf("incomplete (%d of %d bytes)", f.received, f.total))
			return
		}
		dest := uniqueDownloadPath(filepath.Join(filepath.Dir(f.tmp), f.name))
		if err := os.Rename(f.tmp, dest); err != nil {
			os.Remove(f.tmp)
			fail(f.id, f.name, err.Error())
			return
		}
		ui.FinishReceive(lang.T(lang.Received, filepath.Base(dest), ui.TransferSummary(f.received, time.Since(f.began))))
		reply(f.id, fileResultSaved, "")
	}
}

// abortIncoming drops a file still being received (a new file replaced it,
// or the call ended) and says so in its chat entry.
func abortIncoming(incoming **incomingFile) {
	if *incoming == nil {
		return
	}
	ui.FinishReceive(lang.T(lang.RxInterrupted, (*incoming).name))
	closeIncoming(incoming)
}

// closeIncoming deletes the partial file of an incoming transfer.
func closeIncoming(incoming **incomingFile) {
	if *incoming == nil {
		return
	}
	_ = (*incoming).file.Close()
	_ = os.Remove((*incoming).tmp)
	*incoming = nil
}

// sendFile streams path to the peer and reports a single chat entry once the
// receiver confirms the file was saved (or reports why it was not).
func sendFile(conn net.Conn, path string, pacer *adaptiveFilePacer) {
	pacer.beginFile()
	defer pacer.endFile()

	name := filepath.Base(path)
	var size int64
	finish := func(sent int64, msg string) {
		ui.FinishTransfer(msg)
	}

	f, err := os.Open(path)
	if err != nil {
		finish(0, lang.T(lang.TxFailed, name, err.Error()))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		finish(0, lang.T(lang.TxFailed, name, lang.T(lang.NotRegular)))
		return
	}
	size = info.Size()
	if len(name) > 255 {
		name = name[len(name)-255:]
	}

	id := nextTransferID.Add(1)
	results := make(chan fileResult, 1)
	pendingFileResults.Store(id, results)
	defer pendingFileResults.Delete(id)

	start := make([]byte, 14+len(name))
	binary.BigEndian.PutUint32(start[:4], id)
	binary.BigEndian.PutUint64(start[4:12], uint64(size))
	binary.BigEndian.PutUint16(start[12:14], uint16(len(name)))
	copy(start[14:], name)
	if err := writeFrameSafe(conn, frameFileStart, start); err != nil {
		finish(0, lang.T(lang.TxFailed, name, err.Error()))
		return
	}
	ui.SetTransferProgress(name, 0, size)
	began := time.Now()
	// sentSummary is the final chat entry: size, average speed and time.
	sentSummary := func() string {
		return lang.T(lang.Sent, name, ui.TransferSummary(size, time.Since(began)))
	}

	buf := make([]byte, fileChunkBytes)
	var sent int64
	for {
		// The receiver reports failures (e.g. a full disk) as soon as they happen.
		select {
		case res := <-results:
			if !res.saved {
				finish(sent, lang.T(lang.TxFailed, name, lang.T(lang.PeerSaid, res.msg)))
				return
			}
		default:
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, 12+n)
			binary.BigEndian.PutUint32(chunk[:4], id)
			binary.BigEndian.PutUint64(chunk[4:12], uint64(sent))
			copy(chunk[12:], buf[:n])
			if err := writeFrameSafe(conn, frameFileChunk, chunk); err != nil {
				finish(sent, lang.T(lang.TxFailed, name, err.Error()))
				return
			}
			sent += int64(n)
			ui.SetTransferProgress(name, sent, size)
			pacer.waitAfterChunk(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			finish(sent, lang.T(lang.TxFailed, name, readErr.Error()))
			return
		}
	}
	end := make([]byte, 4)
	binary.BigEndian.PutUint32(end, id)
	if err := writeFrameSafe(conn, frameFileEnd, end); err != nil {
		finish(sent, lang.T(lang.TxFailed, name, err.Error()))
		return
	}

	ui.SetTransferStatus(lang.T(lang.AwaitConfirm, name))
	select {
	case res := <-results:
		if res.saved {
			finish(sent, sentSummary())
		} else {
			finish(sent, lang.T(lang.TxFailed, name, lang.T(lang.PeerSaid, res.msg)))
		}
	case <-time.After(fileResultTimeout):
		finish(sent, lang.T(lang.Unconfirmed, sentSummary()))
	}
}

func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func uniqueDownloadPath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}
