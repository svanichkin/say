package tcp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/ui"
)

const fileChunkBytes = 8 * 1024

const (
	fileRateMin       = 16 * 1024
	fileRateStart     = 64 * 1024
	fileRateMax       = 256 * 1024
	fileRateStep      = 8 * 1024
	fileLossThreshold = 0.02
	rttBaselineWindow = 30 * time.Second
)

var nextTransferID atomic.Uint32

type adaptiveFilePacer struct {
	mu          sync.Mutex
	rate        int
	baseRTT     time.Duration
	baseRTTAt   time.Time
	goodReports int
	pauseUntil  time.Time
}

func newAdaptiveFilePacer() *adaptiveFilePacer {
	return &adaptiveFilePacer{rate: fileRateStart}
}

func (p *adaptiveFilePacer) observe(rtt time.Duration, loss float64) {
	if p == nil || rtt <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.baseRTT == 0 || now.Sub(p.baseRTTAt) > rttBaselineWindow {
		p.baseRTT = rtt
		p.baseRTTAt = now
	} else if rtt < p.baseRTT {
		p.baseRTT = rtt
		p.baseRTTAt = now
	}
	congestionRTT := p.baseRTT + 100*time.Millisecond
	if scaled := p.baseRTT + p.baseRTT/2; scaled > congestionRTT {
		congestionRTT = scaled
	}
	congested := loss >= fileLossThreshold || rtt > congestionRTT
	if congested {
		p.rate /= 2
		if p.rate < fileRateMin {
			p.rate = fileRateMin
		}
		p.goodReports = 0
		if loss >= 0.05 || rtt > p.baseRTT+250*time.Millisecond {
			p.pauseUntil = time.Now().Add(400 * time.Millisecond)
		}
		return
	}
	p.goodReports++
	if p.goodReports >= 3 {
		p.rate += fileRateStep
		if p.rate > fileRateMax {
			p.rate = fileRateMax
		}
		p.goodReports = 0
	}
}

func (p *adaptiveFilePacer) waitAfterChunk(n int) {
	if p == nil || n <= 0 {
		return
	}
	p.mu.Lock()
	rate := p.rate
	pause := time.Until(p.pauseUntil)
	p.mu.Unlock()
	if rate < 1 {
		rate = fileRateMin
	}
	delay := time.Duration((int64(n)*int64(time.Second) + int64(rate) - 1) / int64(rate))
	if pause > 0 {
		delay += pause
	}
	time.Sleep(delay)
}

type incomingFile struct {
	id       uint32
	name     string
	total    int64
	received int64
	tmp      string
	file     *os.File
}

func installChatActions(conn net.Conn, pacer *adaptiveFilePacer) {
	ui.SetChatActions(func(text string) {
		if err := writeFrameSafe(conn, frameChat, []byte(text)); err != nil {
			ui.AddChatMessage("Message send failed: "+err.Error(), false)
			return
		}
	}, func(path string) { sendFile(conn, path, pacer) })
}

func handleChatFrame(t byte, payload []byte, incoming **incomingFile) {
	switch t {
	case frameChat:
		ui.AddChatMessage(string(payload), false)
	case frameFileStart:
		if len(payload) < 14 {
			return
		}
		id := binary.BigEndian.Uint32(payload[:4])
		total := int64(binary.BigEndian.Uint64(payload[4:12]))
		nameLen := int(binary.BigEndian.Uint16(payload[12:14]))
		if total < 0 || nameLen == 0 || nameLen > 255 || len(payload) != 14+nameLen {
			return
		}
		name := filepath.Base(strings.TrimSpace(string(payload[14:])))
		if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
			return
		}
		dir, err := downloadsDir()
		if err != nil {
			ui.AddChatMessage("File receive failed: "+err.Error(), false)
			return
		}
		tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.part", name, id))
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			ui.AddChatMessage("File receive failed: "+err.Error(), false)
			return
		}
		if *incoming != nil && (*incoming).file != nil {
			(*incoming).file.Close()
			os.Remove((*incoming).tmp)
		}
		*incoming = &incomingFile{id: id, name: name, total: total, tmp: tmp, file: f}
		ui.AddChatMessage("Receiving "+name, false)
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
		if err != nil || n != len(chunk) {
			abortIncoming(incoming)
			ui.AddChatMessage("File receive failed while writing", false)
		}
	case frameFileEnd:
		f := *incoming
		if f == nil || len(payload) != 4 || binary.BigEndian.Uint32(payload) != f.id {
			return
		}
		if err := f.file.Close(); err != nil {
			os.Remove(f.tmp)
			*incoming = nil
			ui.AddChatMessage("File receive failed: "+err.Error(), false)
			return
		}
		if f.received != f.total {
			os.Remove(f.tmp)
			*incoming = nil
			ui.AddChatMessage("File transfer incomplete", false)
			return
		}
		dest := uniqueDownloadPath(filepath.Join(filepath.Dir(f.tmp), f.name))
		if err := os.Rename(f.tmp, dest); err != nil {
			os.Remove(f.tmp)
			ui.AddChatMessage("File receive failed: "+err.Error(), false)
		} else {
			ui.AddChatMessage("Saved "+filepath.Base(dest)+" to Downloads", false)
		}
		*incoming = nil
	}
}

func abortIncoming(incoming **incomingFile) {
	if *incoming == nil {
		return
	}
	_ = (*incoming).file.Close()
	_ = os.Remove((*incoming).tmp)
	*incoming = nil
}

func sendFile(conn net.Conn, path string, pacer *adaptiveFilePacer) {
	f, err := os.Open(path)
	if err != nil {
		ui.SetTransferProgress(filepath.Base(path), 0, 1, true)
		ui.AddChatMessage("File send failed: "+err.Error(), false)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		ui.SetTransferProgress(filepath.Base(path), 0, 1, true)
		ui.AddChatMessage("Only regular files can be sent", false)
		return
	}
	name := filepath.Base(path)
	if len(name) > 255 {
		name = name[len(name)-255:]
	}
	if info.Size() < 0 {
		return
	}
	id := nextTransferID.Add(1)
	start := make([]byte, 14+len(name))
	binary.BigEndian.PutUint32(start[:4], id)
	binary.BigEndian.PutUint64(start[4:12], uint64(info.Size()))
	binary.BigEndian.PutUint16(start[12:14], uint16(len(name)))
	copy(start[14:], name)
	if err := writeFrameSafe(conn, frameFileStart, start); err != nil {
		ui.SetTransferProgress(name, 0, info.Size(), true)
		ui.AddChatMessage("File send failed: "+err.Error(), false)
		return
	}
	ui.AddChatMessage("Sending "+name, true)
	buf := make([]byte, fileChunkBytes)
	var sent int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, 12+n)
			binary.BigEndian.PutUint32(chunk[:4], id)
			binary.BigEndian.PutUint64(chunk[4:12], uint64(sent))
			copy(chunk[12:], buf[:n])
			if err := writeFrameSafe(conn, frameFileChunk, chunk); err != nil {
				ui.SetTransferProgress(name, sent, info.Size(), true)
				ui.AddChatMessage("File send failed: "+err.Error(), false)
				return
			}
			sent += int64(n)
			ui.SetTransferProgress(name, sent, info.Size(), false)
			pacer.waitAfterChunk(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			ui.SetTransferProgress(name, sent, info.Size(), true)
			ui.AddChatMessage("File read failed: "+readErr.Error(), false)
			return
		}
	}
	end := make([]byte, 4)
	binary.BigEndian.PutUint32(end, id)
	if err := writeFrameSafe(conn, frameFileEnd, end); err != nil {
		ui.AddChatMessage("File send failed: "+err.Error(), false)
	} else {
		ui.AddChatMessage("Sent "+name, true)
	}
	ui.SetTransferProgress(name, info.Size(), info.Size(), true)
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
