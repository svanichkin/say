package udp

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"sort"
	"sync"
	"time"

	"github.com/svanichkin/say/codec"
)

// Video in layers of small datagrams (protocol 7), so that a loss never
// stops the picture:
//
//   - the lower layer: the whole picture at a quarter of the requested rows
//     (videoLowPart), videoLowEvery, every datagram carrying the frame
//     before as well (a single loss costs nothing); always sent, the rate's
//     budget aside;
//   - the upper layer: the grid the rate allows (up to the requested one),
//     cut into bands of videoBandRows rows, each band a stream of its own
//     (its own frames, references, recovery and acknowledgements): a lost
//     datagram breaks its band only, and only until it recovers; meanwhile
//     the receiver shows the lower layer there. The bands go while the
//     rate's budget holds, the longest waiting first: short of rate, the
//     picture changes a part at a time instead of stopping.
//
// Each layer and band is sent as video was before (protocol 6): frames with
// references; the receiver asks to recover from the newest frame it decoded
// when one is lost (a keyframe before any), and for videoAckWindow after a
// loss acknowledges every frame it decodes; the sender codes against an
// acknowledged frame at most every videoAckEvery frames. Datagrams wait in
// the receiver up to videoReorderWait to go in their layer's order; the
// layers do not wait for each other.
//
// The rate (rateCtl, video_ratectl.go) from the receiver's reports every
// rateReportEvery; the upper grid fills what the lower layer leaves of it,
// from what a cell costs a second on the picture as it is; one of fewer rows
// than videoHiMinPart times the lower's is not sent at all. A new upper grid
// starts its bands afresh (a small keyframe each) under a new number.
//
// Tried on a simulated path in Babe's cmd/babecam (Say's codec, a recorded
// call): with a link narrowing to 150 kbit/s the picture never stopped for
// more than 250 ms; with 1% loss, a narrowing and a path switch in one
// minute 4% of the time; with all at once (300 kbit/s dropping to 100, 3%
// loss in bursts, two path switches with the round trip up to 6 s) 16%,
// most of it the switches, when the path carries nothing.
//
// A video datagram, after the media header and the video head (the frame
// counter there counts datagrams):
//
//	layer  1 byte: videoLayerLow, or the band's number
//	gen    1 byte: the upper grid's number
//	rows   1 byte: the upper grid's rows (0: none)
//	bands  1 byte: its bands
//	seq    1 byte: the layer's own frame counter
//	prev   the lower layer only: its length (uvarint, 0: none), then the
//	       frame before's counter (1 byte) and the frame
//	frame  the rest: the encoded frame
const (
	videoLayerLow      byte = 0xff
	videoLayerHead          = 5
	videoLowEvery           = 66 * time.Millisecond // 15 frames a second
	videoLowPart            = 4
	videoLowMinRows         = 8
	videoBandRows           = 8
	videoBandHeartbeat      = 500 * time.Millisecond // an unchanged band goes at least this often
	videoLayerSilence       = 2 * time.Second        // no datagram of a layer this long: whatever comes next is its next
	videoHiMinPart          = 1.5                    // the least upper grid, in the lower's rows
	videoGridFill           = 0.85                   // of the rate the upper grid is chosen to fill
	videoGridDownGap        = 500 * time.Millisecond
	videoGridUpGap          = 2 * time.Second
	videoGridUpMost         = 1.25
)

// Video control messages (ui.SendVideoCtl), receiver → sender:
//
//	recover, ack, keyframe  kind, layer, gen, frame id (uint16 LE)
//	report                  kind, bytes of the last second (uint32 LE), its
//	                        loss (1 byte, of 255), the standing queue (ms,
//	                        uint16 LE), the media index of the newest
//	                        datagram (uint16 LE), flags (ctlNone, ctlSilent)
const (
	ctlRecover  byte = 1
	ctlAck      byte = 2
	ctlKeyframe byte = 3
	ctlReport   byte = 4

	ctlNone   = 1 // no datagram came in the window
	ctlSilent = 2 // none for rateSilent
)

var errLayerHead = errors.New("malformed video layer head")

type layerHead struct {
	layer   byte
	gen     byte
	rows    int
	bands   int
	seq     byte
	prevSeq byte
	prev    []byte // the lower layer: the frame before
}

func appendLayerHead(dst []byte, h layerHead) []byte {
	dst = append(dst, h.layer, h.gen, byte(h.rows), byte(h.bands), h.seq)
	if h.layer == videoLayerLow {
		dst = binary.AppendUvarint(dst, uint64(len(h.prev)))
		if len(h.prev) > 0 {
			dst = append(dst, h.prevSeq)
			dst = append(dst, h.prev...)
		}
	}
	return dst
}

// splitLayerHead parses a layer head; everything returned aliases p.
func splitLayerHead(p []byte) (h layerHead, frame []byte, err error) {
	if len(p) < videoLayerHead {
		return h, nil, errLayerHead
	}
	h = layerHead{layer: p[0], gen: p[1], rows: int(p[2]), bands: int(p[3]), seq: p[4]}
	p = p[videoLayerHead:]
	if h.layer == videoLayerLow {
		n, w := binary.Uvarint(p)
		if w <= 0 || n > uint64(len(p)-w) {
			return h, nil, errLayerHead
		}
		p = p[w:]
		if n > 0 {
			if uint64(len(p)) < 1+n {
				return h, nil, errLayerHead
			}
			h.prevSeq, h.prev, p = p[0], p[1:1+n], p[1+n:]
		}
	}
	return h, p, nil
}

// bandRanges cuts rows into bands of videoBandRows (a short last one joins
// the one before).
func bandRanges(rows int) [][2]int {
	var b [][2]int
	for r0 := 0; r0 < rows; {
		r1 := min(rows, r0+videoBandRows)
		if rows-r1 < videoBandRows/2 {
			r1 = rows
		}
		b = append(b, [2]int{r0, r1})
		r0 = r1
	}
	return b
}

// ===== the sender =====

// layerTx is a layer or band at the sender.
type layerTx struct {
	enc           *codec.Encoder
	cols, rows    int
	r0, r1        int // the upper grid's rows it covers
	seq           byte
	lastSent      time.Duration
	sinceAckRef   int
	acked         uint16
	haveAck       bool
	keyReq        bool
	lastForcedKey time.Duration
	prev          []byte // the lower layer: the frame before, sent again
	prevSeq       byte
	havePrev      bool
}

// layerPacket is a layer's frame to send.
type layerPacket struct {
	head  layerHead
	frame []byte
	key   bool
}

// size is the datagram's video part.
func (p *layerPacket) size() int { return videoLayerHead + len(p.prev()) + len(p.frame) + 2 }

func (p *layerPacket) prev() []byte { return p.head.prev }

// layerSender codes camera frames into the layers.
type layerSender struct {
	mono               bool
	paneCols, paneRows int // the grid the peer asks for
	low                layerTx
	hi                 []*layerTx
	hiCols, hiRows     int // the upper grid (0 rows: none)
	gen                byte
	rc                 rateCtl
	lowWin             []sentAt // the lower layer's bytes, for what it leaves of the rate
	lowNext            time.Duration
	lastGrid           time.Duration
}

func newLayerSender(mono bool) *layerSender {
	s := &layerSender{mono: mono, rc: newRateCtl(), lastGrid: -time.Hour}
	s.low = layerTx{lastSent: -time.Hour, lastForcedKey: -time.Hour}
	s.low.enc = s.newEncoder(videoShapeHold)
	return s
}

func (s *layerSender) newEncoder(hold int) *codec.Encoder {
	e := codec.NewEncoder(s.mono, 0, maxVideoFPS, false)
	e.SetRefs(true)
	e.SetShapeHold(hold)
	e.SetHoldDebug(videoHoldDebug)
	return e
}

// colsFor is the pane's aspect at rows.
func (s *layerSender) colsFor(rows int) int {
	return max(1, (s.paneCols*rows+s.paneRows/2)/s.paneRows)
}

// setPane takes the grid the peer asks for: the lower layer follows it, the
// upper never goes past it.
func (s *layerSender) setPane(now time.Duration, cols, rows int) {
	if cols == s.paneCols && rows == s.paneRows {
		return
	}
	first := s.paneRows == 0
	s.paneCols, s.paneRows = cols, rows
	s.low.rows = min(rows, max(videoLowMinRows, rows/videoLowPart))
	s.low.cols = s.colsFor(s.low.rows)
	switch {
	case first || s.hiRows > rows:
		s.setHi(now, rows)
	case s.hiRows > 0:
		s.setHi(now, s.hiRows) // the pane's aspect changed
	}
}

// setHi starts an upper grid of rows (0: none).
func (s *layerSender) setHi(now time.Duration, rows int) {
	s.gen++
	s.lastGrid = now
	s.hi = nil
	s.hiRows, s.hiCols = rows, 0
	if rows <= 0 {
		return
	}
	s.hiCols = s.colsFor(rows)
	hold := baseVideoHold(videoShapeHold, s.hiCols*rows)
	for _, r := range bandRanges(rows) {
		s.hi = append(s.hi, &layerTx{enc: s.newEncoder(hold), cols: s.hiCols, rows: r[1] - r[0], r0: r[0], r1: r[1],
			lastSent: -time.Hour, lastForcedKey: -time.Hour})
	}
}

// setMono sends brightness only, or colour again.
func (s *layerSender) setMono(on bool) {
	s.mono = on
	s.low.enc.SetBW(on)
	for _, t := range s.hi {
		t.enc.SetBW(on)
	}
}

// forceKeyframes makes every layer's next frame a keyframe.
func (s *layerSender) forceKeyframes() {
	s.low.enc.ForceKeyframe()
	for _, t := range s.hi {
		t.enc.ForceKeyframe()
	}
}

// control takes a message from the peer's receiver; sentAt maps a media
// index back to when it was sent.
func (s *layerSender) control(now time.Duration, msg []byte, sentAt func(index uint16) time.Duration) {
	if len(msg) == 0 {
		return
	}
	if msg[0] == ctlReport {
		if len(msg) < 11 {
			return
		}
		r := rateReport{
			bytes1s: int(binary.LittleEndian.Uint32(msg[1:])),
			loss1s:  float64(msg[5]) / 255,
			queue:   time.Duration(binary.LittleEndian.Uint16(msg[6:])) * time.Millisecond,
			none:    msg[10]&ctlNone != 0,
			silent:  msg[10]&ctlSilent != 0,
		}
		r.newestAt = sentAt(binary.LittleEndian.Uint16(msg[8:]))
		s.rc.report(now, r)
		return
	}
	if len(msg) < 5 {
		return
	}
	t := &s.low
	if msg[1] != videoLayerLow {
		if msg[2] != s.gen || int(msg[1]) >= len(s.hi) {
			return // a grid gone
		}
		t = s.hi[msg[1]]
	}
	id := binary.LittleEndian.Uint16(msg[3:])
	switch msg[0] {
	case ctlKeyframe:
		t.keyReq = true
	case ctlRecover:
		t.enc.RecoverFrom(id)
	case ctlAck:
		t.acked, t.haveAck = id, true
	}
}

// adaptGrid picks the upper grid for what the lower layer leaves of the rate.
func (s *layerSender) adaptGrid(now time.Duration) {
	if s.rc.perCellN < 1 || s.rc.perCell <= 0 || s.paneRows == 0 {
		return // not known yet
	}
	lowBytes := 0
	for _, w := range s.lowWin {
		lowBytes += w.bytes
	}
	avail := s.rc.rate - float64(lowBytes*8)/1000
	aspect := float64(s.paneCols) / float64(s.paneRows)
	cells := max(0, videoGridFill*avail*1000/s.rc.perCell)
	want := min(s.paneRows, int(math.Sqrt(cells/aspect)))
	least := int(math.Ceil(videoHiMinPart * float64(s.low.rows)))
	if want < least {
		want = 0
	}
	switch {
	case s.hiRows > 0 && want < s.hiRows*9/10 && now-s.lastGrid >= videoGridDownGap:
		s.setHi(now, want)
	case s.hiRows == 0 && want >= least*6/5 && now-s.lastGrid >= videoGridUpGap:
		s.setHi(now, want)
	case s.hiRows > 0 && want > s.hiRows*11/10 && now-s.lastGrid >= videoGridUpGap:
		s.setHi(now, min(want, max(s.hiRows+1, int(float64(s.hiRows)*videoGridUpMost))))
	}
}

// frame codes a camera frame into the layers' frames to send now.
func (s *layerSender) frame(now time.Duration, f codec.Frame) ([]layerPacket, error) {
	if s.paneRows == 0 {
		return nil, nil
	}
	s.rc.tick(now)
	s.adaptGrid(now)
	s.rc.allow(now)
	var out []layerPacket
	if now >= s.lowNext {
		s.lowNext = max(s.lowNext+videoLowEvery, now-videoLowEvery)
		p, err := s.encode(now, &s.low, videoLayerLow, f)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	// The bands, the longest waiting first, while the budget holds (an
	// unchanged band's heartbeat aside).
	order := make([]int, len(s.hi))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return s.hi[order[i]].lastSent < s.hi[order[j]].lastSent })
	for _, b := range order {
		t := s.hi[b]
		if s.rc.tokens <= 0 && now-t.lastSent < videoBandHeartbeat {
			s.rc.secHeld = true
			continue
		}
		y0, y1 := f.Height*t.r0/s.hiRows, f.Height*t.r1/s.hiRows
		band := codec.Frame{Data: f.Data[3*f.Width*y0 : 3*f.Width*y1], Width: f.Width, Height: y1 - y0}
		p, err := s.encode(now, t, byte(b), band)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, nil
}

// encode codes a layer's frame (nil: nothing changed).
func (s *layerSender) encode(now time.Duration, t *layerTx, layer byte, f codec.Frame) (*layerPacket, error) {
	if t.keyReq && now-t.lastForcedKey >= videoKeyframeRequestGap {
		t.lastForcedKey = now
		t.enc.ForceKeyframe()
	}
	t.keyReq = false
	if t.haveAck && t.sinceAckRef >= videoAckEvery {
		t.enc.CodeAgainst(t.acked)
		t.haveAck, t.sinceAckRef = false, 0
	}
	t.sinceAckRef++
	hb := videoHeartbeat
	if layer != videoLayerLow {
		hb = videoBandHeartbeat
	}
	t.enc.SetSkipEmpty(now-t.lastSent < hb)
	b, err := t.enc.Encode(f, t.cols, t.rows)
	if err != nil || b == nil {
		return nil, err
	}
	t.lastSent = now
	p := &layerPacket{head: layerHead{layer: layer, gen: s.gen, rows: s.hiRows, bands: len(s.hi), seq: t.seq}, frame: b,
		key: len(b) > 4 && b[4] == 5} // a keyframe with references
	t.seq++
	if layer == videoLayerLow {
		if t.havePrev {
			p.head.prevSeq, p.head.prev = t.prevSeq, t.prev
		}
		t.prev, t.prevSeq, t.havePrev = b, p.head.seq, true
		s.lowWin = append(s.lowWin, sentAt{now, p.size()})
		for len(s.lowWin) > 0 && now-s.lowWin[0].at > time.Second {
			s.lowWin = s.lowWin[1:]
		}
	} else {
		s.rc.measure(now, len(b), s.hiCols*s.hiRows, p.key)
	}
	s.rc.spent(now, p.size())
	return p, nil
}

// ===== the receiver =====

// layerRx is a layer or band at the receiver.
type layerRx struct {
	dec         *codec.Decoder
	haveSeq     bool
	lastSeq     byte
	tf          *codec.TextFrame
	broken      bool // a frame could not be decoded: waiting to recover
	lastArrival time.Duration
	lastRequest time.Duration
	lastLoss    time.Duration
}

func newLayerRx() *layerRx {
	return &layerRx{dec: codec.NewDecoder(), lastArrival: -time.Hour, lastRequest: -time.Hour, lastLoss: -time.Hour}
}

// layerArrival is a datagram waiting for its turn.
type layerArrival struct {
	head  layerHead
	frame []byte
	index uint16
	at    time.Duration
}

// layerReceiver decodes the layers, asks the sender to recover what was
// lost, reports what arrives, and puts the picture together.
type layerReceiver struct {
	start   time.Time
	send    func(msg []byte) // to the peer's sender (ui.SendVideoCtl)
	low     *layerRx
	hi      []*layerRx
	haveGen bool
	gen     byte
	rows    int // the upper grid's
	pending []layerArrival
	lowUp   *codec.TextFrame // the lower layer drawn in the upper grid's cells
	lowUpOf *codec.TextFrame

	// what arrived, for the reports
	nextReport  time.Duration
	haveG       bool
	lastG       byte
	rBytes      int
	rExpected   int
	rGot        int
	rQueue      time.Duration // the least a datagram waited since the last report (-1: none)
	lossWin     [10][3]int
	qWin        [5]time.Duration
	lossK       int
	lastArrival time.Duration
	haveRel     bool
	prevRel     int
	relU        int64 // arrival less sending, in media ticks, unwrapped (the clocks' offset in it)
	newestU     int64 // the newest datagram's sending, in the receiver's ticks
	floor       owdFloor
}

func newLayerReceiver(send func([]byte)) *layerReceiver {
	return &layerReceiver{start: time.Now(), send: send, low: newLayerRx(), rQueue: -1, lastArrival: -time.Hour}
}

// arrive takes a datagram's video part: seq the datagram counter, index its
// media index.
func (r *layerReceiver) arrive(at time.Time, seq byte, index uint16, payload []byte) error {
	h, frame, err := splitLayerHead(payload)
	if err != nil {
		return err
	}
	now := at.Sub(r.start)
	// Loss by the datagram counter: those expected (as the counter goes) and
	// those that came, over a second (a late one comes in a later report
	// than its number, out of order with the bands it went with).
	switch gap := seq - r.lastG; {
	case !r.haveG:
		r.haveG, r.lastG = true, seq
		r.rExpected++
	case gap > 0 && gap <= 128:
		r.rExpected += int(gap)
		r.lastG = seq
	}
	r.rGot++
	r.rBytes += len(payload)
	r.lastArrival = now
	// The trip, by the media clock: arrival less sending, the clocks'
	// offset in it (it cancels against the shortest trip).
	local := int(now/mediaTickDuration) & int(mediaIndexMask)
	rel := (local - int(index)) & int(mediaIndexMask)
	if !r.haveRel {
		r.haveRel, r.prevRel, r.relU = true, rel, int64(rel)
	} else {
		d := (rel-r.prevRel+int(mediaIndexHalf))&int(mediaIndexMask) - int(mediaIndexHalf)
		r.relU += int64(d)
		r.prevRel = rel
	}
	owd := time.Duration(r.relU) * mediaTickDuration
	r.floor.note(now, owd)
	if q := owd - r.floor.get(now); r.rQueue < 0 || q < r.rQueue {
		r.rQueue = q
	}
	r.newestU = max(r.newestU, int64(now/mediaTickDuration)-r.relU)
	r.pending = append(r.pending, layerArrival{head: h, frame: frame, index: index, at: now})
	return nil
}

// rxOf is the layer a datagram is for (nil: none).
func (r *layerReceiver) rxOf(h layerHead) *layerRx {
	switch {
	case h.layer == videoLayerLow:
		return r.low
	case r.haveGen && h.gen == r.gen && int(h.layer) < len(r.hi):
		return r.hi[h.layer]
	}
	return nil
}

// drain decodes what is due by at: each layer's datagrams in order of its
// own counter, a missing one waited for up to videoReorderWait. It reports
// whether the picture changed, and the media index of the newest frame
// decoded.
func (r *layerReceiver) drain(at time.Time) (changed bool, index uint16) {
	now := at.Sub(r.start)
	for moved := true; moved; {
		moved = false
		for k := 0; k < len(r.pending); k++ {
			p := r.pending[k]
			rx := r.rxOf(p.head)
			if rx != nil && rx.haveSeq && p.head.seq != rx.lastSeq+1 && p.head.seq-rx.lastSeq <= 128 && now-p.at < videoReorderWait {
				continue // wait a little for the one before
			}
			r.pending = append(r.pending[:k], r.pending[k+1:]...)
			if r.process(p, max(now, p.at)) {
				changed, index = true, p.index
			}
			moved = true
			break
		}
	}
	return changed, index
}

func (r *layerReceiver) process(p layerArrival, now time.Duration) bool {
	h := p.head
	// A newer upper grid (any datagram tells it): its bands start afresh.
	if !r.haveGen || h.gen != r.gen && byte(h.gen-r.gen) < 128 {
		r.haveGen, r.gen, r.rows = true, h.gen, h.rows
		r.hi = make([]*layerRx, h.bands)
		for i := range r.hi {
			r.hi[i] = newLayerRx()
		}
	}
	if h.layer == videoLayerLow {
		rx := r.low
		decoded := false
		if rx.haveSeq && len(h.prev) > 0 && h.prevSeq == rx.lastSeq+1 && h.seq == h.prevSeq+1 && now-rx.lastArrival < videoLayerSilence {
			decoded = r.take(rx, h.layer, h.gen, h.prevSeq, h.prev, now) // the frame before, lost: here again
		}
		return r.take(rx, h.layer, h.gen, h.seq, p.frame, now) || decoded
	}
	if h.gen == r.gen && int(h.layer) < len(r.hi) {
		return r.take(r.hi[h.layer], h.layer, h.gen, h.seq, p.frame, now)
	}
	return false
}

// take decodes a layer's frame, as video was received before (protocol 6).
func (r *layerReceiver) take(rx *layerRx, layer, gen, seq byte, data []byte, now time.Duration) bool {
	if rx.haveSeq && now-rx.lastArrival >= videoLayerSilence {
		rx.haveSeq = false // frames may have been lost unnoticed: this is the next
	}
	rx.lastArrival = now
	if rx.haveSeq {
		switch gap := seq - rx.lastSeq; {
		case gap == 0 || gap > 128:
			return false // a duplicate or older than the last: its turn has passed
		case gap != 1:
			rx.lastLoss = now
		}
	}
	rx.haveSeq, rx.lastSeq = true, seq
	tf, err := rx.dec.Decode(data)
	if err != nil {
		rx.broken, rx.lastLoss = true, now
		if now-rx.lastRequest >= videoKeyframeRetry {
			rx.lastRequest = now
			if id, ok := rx.dec.LastGood(); ok {
				r.send(ctlMessage(ctlRecover, layer, gen, id))
			} else {
				r.send(ctlMessage(ctlKeyframe, layer, gen, 0))
			}
		}
		return false
	}
	rx.broken, rx.tf = false, tf
	if now-rx.lastLoss < videoAckWindow {
		if id, ok := rx.dec.LastGood(); ok {
			r.send(ctlMessage(ctlAck, layer, gen, id))
		}
	}
	return true
}

func ctlMessage(kind, layer, gen byte, id uint16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{kind, layer, gen}, id)
}

// report sends the sender a report every rateReportEvery.
func (r *layerReceiver) report(at time.Time) {
	now := at.Sub(r.start)
	for now >= r.nextReport {
		r.lossWin[r.lossK%len(r.lossWin)] = [3]int{r.rExpected, r.rGot, r.rBytes}
		r.qWin[r.lossK%len(r.qWin)] = r.rQueue
		r.lossK++
		e1, g1, b1 := 0, 0, 0
		for _, w := range r.lossWin {
			e1, g1, b1 = e1+w[0], g1+w[1], b1+w[2]
		}
		standing := time.Duration(-1)
		for _, q := range r.qWin {
			if q >= 0 && (standing < 0 || q < standing) {
				standing = q
			}
		}
		loss := 0
		if e1 > 0 {
			loss = 255 * max(0, e1-g1) / e1
		}
		var flags byte
		if r.rQueue < 0 {
			flags |= ctlNone
		}
		if r.lastArrival >= 0 && r.nextReport-r.lastArrival >= rateSilent {
			flags |= ctlSilent
		}
		if r.nextReport > 0 && r.haveRel {
			msg := []byte{ctlReport}
			msg = binary.LittleEndian.AppendUint32(msg, uint32(b1))
			msg = append(msg, byte(loss))
			msg = binary.LittleEndian.AppendUint16(msg, uint16(min(max(0, standing), 65535*time.Millisecond)/time.Millisecond))
			msg = binary.LittleEndian.AppendUint16(msg, uint16(r.newestU)&mediaIndexMask)
			msg = append(msg, flags)
			r.send(msg)
		}
		r.rBytes, r.rExpected, r.rGot, r.rQueue = 0, 0, 0, -1
		r.nextReport += rateReportEvery
	}
}

// picture is what the receiver shows: the upper grid, each band that is
// whole as decoded, the others from the lower layer; the lower layer alone
// without an upper grid (nil: nothing yet).
func (r *layerReceiver) picture() *codec.TextFrame {
	low := r.low.tf
	var cols int
	whole := false
	for _, rx := range r.hi {
		if rx.tf != nil && !rx.broken {
			cols, whole = rx.tf.Cols, true
			break
		}
	}
	if r.rows == 0 || !whole {
		return low
	}
	rows := r.rows
	out := &codec.TextFrame{Cols: cols, Rows: rows, Width: cols << 2, Height: rows << 3,
		Glyphs: make([]byte, cols*rows), FgYCbCr: make([]byte, 3*cols*rows), BgYCbCr: make([]byte, 3*cols*rows)}
	for i, b := range bandRanges(rows) {
		rx := (*layerRx)(nil)
		if i < len(r.hi) {
			rx = r.hi[i]
		}
		if rx != nil && rx.tf != nil && !rx.broken && rx.tf.Cols == cols && rx.tf.Rows == b[1]-b[0] {
			o := b[0] * cols
			copy(out.Glyphs[o:], rx.tf.Glyphs)
			copy(out.FgYCbCr[3*o:], rx.tf.FgYCbCr)
			copy(out.BgYCbCr[3*o:], rx.tf.BgYCbCr)
			continue
		}
		if low == nil {
			continue
		}
		up := r.lowIn(cols, rows)
		o, e := b[0]*cols, b[1]*cols
		copy(out.Glyphs[o:e], up.Glyphs[o:e])
		copy(out.FgYCbCr[3*o:3*e], up.FgYCbCr[3*o:3*e])
		copy(out.BgYCbCr[3*o:3*e], up.BgYCbCr[3*o:3*e])
	}
	return out
}

// lowIn draws the lower layer in a grid of cols x rows: each cell's 4x8
// pixels sampled from the lower layer's, the glyph that matches them best
// (or its inverse), the colours of the lower cell under its middle. Kept
// until the lower layer changes.
func (r *layerReceiver) lowIn(cols, rows int) *codec.TextFrame {
	low := r.low.tf
	if r.lowUpOf == low && r.lowUp != nil && r.lowUp.Cols == cols && r.lowUp.Rows == rows {
		return r.lowUp
	}
	pats := glyphPatterns()
	up := &codec.TextFrame{Cols: cols, Rows: rows, Width: cols << 2, Height: rows << 3,
		Glyphs: make([]byte, cols*rows), FgYCbCr: make([]byte, 3*cols*rows), BgYCbCr: make([]byte, 3*cols*rows)}
	lw, lh := low.Cols*4, low.Rows*8
	w, h := cols*4, rows*8
	for y := range rows {
		for x := range cols {
			var want uint32
			for k := range 32 {
				lx, ly := (x*4+k%4)*lw/w, (y*8+k/4)*lh/h
				c := (ly/8)*low.Cols + lx/4
				if pats[low.Glyphs[c]]>>(31-((ly%8)*4+lx%4))&1 != 0 {
					want |= 1 << (31 - k)
				}
			}
			best, bestD, inv := 0, 33, false
			for g, p := range pats[:len(codec.GlyphCodepoints())] {
				if d := bits.OnesCount32(p ^ want); d < bestD {
					best, bestD, inv = g, d, false
				}
				if d := bits.OnesCount32(^p ^ want); d < bestD {
					best, bestD, inv = g, d, true
				}
			}
			mx, my := (x*4+2)*lw/w, (y*8+4)*lh/h
			c := (my/8)*low.Cols + mx/4
			i := y*cols + x
			up.Glyphs[i] = byte(best)
			fg, bg := low.FgYCbCr[3*c:3*c+3], low.BgYCbCr[3*c:3*c+3]
			if inv {
				fg, bg = bg, fg
			}
			copy(up.FgYCbCr[3*i:], fg)
			copy(up.BgYCbCr[3*i:], bg)
		}
	}
	r.lowUp, r.lowUpOf = up, low
	return up
}

var (
	glyphPats     [256]uint32
	glyphPatsOnce sync.Once
)

// glyphPatterns is each glyph's 4x8 pattern, by its index.
func glyphPatterns() *[256]uint32 {
	glyphPatsOnce.Do(func() {
		for i, cp := range codec.GlyphCodepoints() {
			glyphPats[i], _ = codec.GlyphPattern(cp)
		}
	})
	return &glyphPats
}
