package udp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"math/bits"
	"sort"
	"sync"
	"time"

	"github.com/svanichkin/say/codec"
	"github.com/svanichkin/say/ui"
)

// Video in bands of small datagrams (protocol 8), so that a loss never stops
// the picture: the grid the rate allows (up to the requested one) is cut into
// bands of videoBandRows rows (-video-band), each band a stream of its own
// (its own frames, references, recovery and acknowledgements): a lost
// datagram breaks its band only, and only until the band recovers;
// meanwhile the receiver shows the no-signal noise there (ui.NoiseRows). The
// bands go while the rate's budget holds, the longest waiting first: short
// of rate, the picture changes a part at a time instead of stopping.
//
// Each band is sent as video was before (protocol 6): frames with
// references; the receiver asks to recover from the newest frame it decoded
// when one is lost (a keyframe before any), and for videoAckWindow after a
// loss acknowledges every frame it decodes (the bands together every
// videoAckFlush); the sender then codes every frame against the newest frame
// acknowledged, so a lost datagram is lost alone and the frames after it
// decode. Datagrams wait in the receiver up
// to videoReorderWait to go in their band's order; the bands do not wait for
// each other.
//
// The rate (rateCtl, video_ratectl.go) from the receiver's reports every
// rateReportEvery; the grid fills it, from what a cell costs a second on the
// picture as it is, down to a quarter of the requested rows, changed only
// for a clear difference and not often (each grid starts its bands afresh,
// a small keyframe each). Until a band of a new grid comes, the receiver
// shows the picture before there.
//
// Tried on a simulated path in Babe's cmd/babecam (Say's codec, a recorded
// call): a link narrowing from 1000 to 150 kbit/s queued at most 0.3 s with
// no band in noise; with 1% loss, a narrowing and a path switch in a minute
// the picture stood still 1.5% of the time; with all at once (300 kbit/s
// dropping to 100, 3% loss in bursts, two path switches with the round trip
// up to 6 s) 7%, most of it the switches, when the path carries nothing.
//
// A video datagram, after the media header and the video head (the frame
// counter there counts datagrams):
//
//	band   1 byte: the band's number
//	gen    1 byte: the grid's number
//	rows   1 byte: the grid's rows
//	bands  1 byte: its bands
//	seq    1 byte: the band's own frame counter
//	frame  the rest: the encoded frame
const (
	videoLayerHead     = 5
	videoBandHeartbeat = 500 * time.Millisecond // an unchanged band goes at least this often
	videoLayerSilence  = 2 * time.Second        // no datagram of a band this long: whatever comes next is its next
	videoAckHold       = time.Second            // acknowledgements this recent: the peer is after a loss
	videoGridFill      = 0.85                   // of the rate the grid is chosen to fill
	videoGridDownGap   = time.Second            // between grids
	videoGridUpGap     = 2 * time.Second
	videoGridUpMost    = 1.25
	videoGridDownAt    = 0.8 // a smaller grid only when the rate asks for this share of the rows or less
	videoGridUpAt      = 1.2 // a larger one at this share or more
	videoGridLeastPart = 4   // the least grid: the requested rows over this
	videoAckFlush      = 40 * time.Millisecond
)

// videoBandRows is the bands' height in rows (-video-band; 0: one band, the
// whole grid). 0 by default: over Yggdrasil every datagram costs some 300
// bytes of headers, and bands of a row sent 360 datagrams a second instead of
// 30, several times the traffic, with the path's delay swinging and the
// sound's jitter buffer growing to 600 ms; a whole frame per datagram kept it
// near 130 ms on the same route. 16 rows looked the most whole by eye (Babe's
// cmd/babecam), the share of the picture in noise about the same from 1 to 32.
var videoBandRows = 0

// SetVideoBandRows sets the bands' height for future sessions.
func SetVideoBandRows(rows int) { videoBandRows = max(0, rows) }

// Video control messages (ui.SendVideoCtl), receiver → sender:
//
//	recover, keyframe       kind, band, gen, frame id (uint16 LE)
//	acks                    kind, gen, then per band acknowledged since the
//	                        last: band, frame id (uint16 LE); every
//	                        videoAckFlush at most
//	report                  kind, bytes of the last second (uint32 LE), its
//	                        loss (1 byte, of 255), the standing queue (ms,
//	                        uint16 LE), the media index of the newest
//	                        datagram (uint16 LE), flags (ctlNone, ctlSilent)
const (
	ctlRecover  byte = 1
	ctlAcks     byte = 2
	ctlKeyframe byte = 3
	ctlReport   byte = 4

	ctlNone   = 1 // no datagram came in the window
	ctlSilent = 2 // none for rateSilent
)

var errLayerHead = errors.New("malformed video band head")

type layerHead struct {
	layer byte // the band
	gen   byte
	rows  int
	bands int
	seq   byte
}

func appendLayerHead(dst []byte, h layerHead) []byte {
	return append(dst, h.layer, h.gen, byte(h.rows), byte(h.bands), h.seq)
}

// splitLayerHead parses a band head; the frame aliases p.
func splitLayerHead(p []byte) (h layerHead, frame []byte, err error) {
	if len(p) < videoLayerHead {
		return h, nil, errLayerHead
	}
	return layerHead{layer: p[0], gen: p[1], rows: int(p[2]), bands: int(p[3]), seq: p[4]}, p[videoLayerHead:], nil
}

// bandRanges cuts rows into bands of videoBandRows (a short last one joins
// the one before).
func bandRanges(rows int) [][2]int {
	band := videoBandRows
	if band <= 0 {
		band = rows
	}
	var b [][2]int
	for r0 := 0; r0 < rows; {
		r1 := min(rows, r0+band)
		if rows-r1 < band/2 {
			r1 = rows
		}
		b = append(b, [2]int{r0, r1})
		r0 = r1
	}
	return b
}

// ===== the sender =====

// layerTx is a band at the sender.
type layerTx struct {
	enc           bandCoder
	cols, rows    int
	r0, r1        int // the grid's rows it covers
	seq           byte
	lastSent      time.Duration
	acked         uint16
	haveAck       bool
	ackedAt       time.Duration
	keyReq        bool
	lastForcedKey time.Duration
}

// layerPacket is a band's frame to send.
type layerPacket struct {
	head  layerHead
	frame []byte
	key   bool
}

// size is the datagram's video part.
func (p *layerPacket) size() int { return videoLayerHead + len(p.frame) }

// layerSender codes camera frames into the bands.
type layerSender struct {
	mono               bool
	paneCols, paneRows int // the grid the peer asks for
	hi                 []*layerTx
	hiCols, hiRows     int // the grid
	gen                byte
	rc                 rateCtl
	lastGrid           time.Duration

	// smooth video (video_smooth.go): the receiver's newest report
	queue      time.Duration // its standing queue
	got, went  float64       // kbit/s arrived in its second, sent in the second before its newest datagram
	prevQNow   time.Duration // what its newest datagram waited, by the report before
	lastReport time.Duration
	silentAt   time.Duration     // the reports stopped last (an outage)
	rxHas      map[byte][]uint16 // per band of the grid gen: the frames the receiver has (newest, mark)
	trouble    time.Duration     // the path showed trouble last (loss, a queue, a cut, a recovery)
	qCap       float64           // the smooth coder's quality allowed now
	qCapAt     time.Duration
	badQ       float64 // the quality allowed when trouble came last above videoSmoothQ
	badAt      time.Duration
	capNow     time.Duration // the frame's time, for smoothQCap
	crop       []byte        // the camera's frame in the pane's shape
	cropW      int           // its size, kept while the pane's shape stays near (smoothCropKeep)
	cropH      int
	small      []byte // and at the pane's pixels
}

func newLayerSender(mono bool) *layerSender {
	return &layerSender{mono: mono, rc: newRateCtl(), lastGrid: -time.Hour, silentAt: -time.Hour}
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

// setPane takes the grid the peer asks for: the grid never goes past it.
func (s *layerSender) setPane(now time.Duration, cols, rows int) {
	if cols == s.paneCols && rows == s.paneRows {
		return
	}
	first := s.paneRows == 0
	s.paneCols, s.paneRows = cols, rows
	switch {
	case first || s.hiRows > rows:
		s.setHi(now, rows)
	default:
		s.setHi(now, s.hiRows) // the pane's aspect changed
	}
}

// setHi starts a grid of rows.
func (s *layerSender) setHi(now time.Duration, rows int) {
	s.gen++
	s.lastGrid = now
	old := s.hi
	s.hi = nil
	s.hiRows, s.hiCols = rows, s.colsFor(rows)
	hold := baseVideoHold(videoShapeHold, s.hiCols*rows)
	ranges := bandRanges(rows)
	for i, r := range ranges {
		var enc bandCoder = sayBand{s.newEncoder(hold)}
		if videoSmooth {
			share := float64(r[1]-r[0]) / float64(rows)
			if b, ok := smoothKept(old, ranges, i); ok {
				// The pixels do not depend on the grid: the band's coder goes
				// on (its references, its quality), no keyframe.
				b.share, enc = share, b
			} else {
				enc = newSmoothBand(s.effRate, s.smoothQCap, share)
			}
		}
		s.hi = append(s.hi, &layerTx{enc: enc, cols: s.hiCols, rows: r[1] - r[0], r0: r[0], r1: r[1],
			lastSent: -time.Hour, lastForcedKey: -time.Hour})
	}
}

// smoothKept is band i's smooth coder of the grid before when the grid
// keeps as many bands (one, by default): it goes on in the new grid.
func smoothKept(old []*layerTx, ranges [][2]int, i int) (*smoothBand, bool) {
	if len(old) != len(ranges) || i >= len(old) {
		return nil, false
	}
	b, ok := old[i].enc.(*smoothBand)
	return b, ok
}

// forceKeyframes makes every band's next frame a keyframe.
func (s *layerSender) forceKeyframes() {
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
		if videoSmooth {
			s.smoothReport(now, r, msg[11:])
		}
		return
	}
	if msg[0] == ctlAcks {
		if len(msg) < 2 || msg[1] != s.gen {
			return // a grid gone
		}
		for p := msg[2:]; len(p) >= 3; p = p[3:] {
			if int(p[0]) < len(s.hi) {
				t := s.hi[p[0]]
				t.acked, t.haveAck, t.ackedAt = binary.LittleEndian.Uint16(p[1:]), true, now
			}
		}
		return
	}
	if len(msg) < 5 || msg[2] != s.gen || int(msg[1]) >= len(s.hi) {
		return // a grid gone
	}
	t := s.hi[msg[1]]
	id := binary.LittleEndian.Uint16(msg[3:])
	switch msg[0] {
	case ctlKeyframe:
		t.keyReq = true
	case ctlRecover:
		t.enc.RecoverFrom(append([]uint16{id}, s.rxHas[msg[1]]...)...)
	}
	s.troubled(now, "кадр потерян, просят восстановить") // a frame lost
}

// smoothReport takes a report's smooth part (after Say's): what the
// newest datagram waited (ms, uint16 LE; 0xffff none), what arrived in the
// last three windows (uint32 LE), the grid, and per band the frames the
// receiver has (band, newest, mark; uint16 LE each).
func (s *layerSender) smoothReport(now time.Duration, r rateReport, ext []byte) {
	if s.lastReport > 0 && now-s.lastReport >= smoothSilent {
		s.silentAt = now // an outage: the path may be another
	}
	switch {
	case r.silent:
		s.troubled(now, "тишина")
	case now-s.rc.lastCut < rateReportEvery:
		s.troubled(now, "срез скорости: "+s.rc.why)
	case r.loss1s >= smoothTroubleLoss:
		s.troubled(now, fmt.Sprintf("потери %.1f%%", 100*r.loss1s))
	case r.queue >= rateHoldQ:
		s.troubled(now, fmt.Sprintf("очередь %d мс", r.queue.Milliseconds()))
	}
	s.lastReport = now
	if len(ext) < 8 {
		return
	}
	qNow := time.Duration(binary.LittleEndian.Uint16(ext)) * time.Millisecond
	got300 := float64(binary.LittleEndian.Uint32(ext[2:])*8) / 1000 / smoothFastWin.Seconds()
	if !r.none {
		if ext[0] != 0xff || ext[1] != 0xff {
			if qNow >= smoothQueueFast && qNow > s.prevQNow && now-s.silentAt >= smoothAfterSilent &&
				got300 < rateOverShare*s.rc.sentBetween(r.newestAt-smoothFastWin, r.newestAt) {
				s.rc.queueing(now, got300, qNow)
			}
			s.prevQNow = qNow
		}
		s.queue, s.got = r.queue, float64(r.bytes1s*8)/1000
		s.went = s.rc.sentBetween(r.newestAt-time.Second, r.newestAt)
	}
	if ext[6] != s.gen {
		return
	}
	n := int(ext[7])
	has := map[byte][]uint16{}
	for p := ext[8:]; n > 0 && len(p) >= 5; n, p = n-1, p[5:] {
		has[p[0]] = []uint16{binary.LittleEndian.Uint16(p[1:]), binary.LittleEndian.Uint16(p[3:])}
	}
	s.rxHas = has
}

// smoothQCap is the smooth coder's quality allowed now: videoSmoothQ after
// trouble, up to videoSmoothQMax a point every smoothQRise once the path
// has been clean for smoothCleanFor.
func (s *layerSender) smoothQCap() float64 {
	now := s.capNow
	if s.qCap == 0 || now-s.trouble < smoothCleanFor {
		s.qCap, s.qCapAt = float64(videoSmoothQ), now
		return s.qCap
	}
	top := float64(videoSmoothQMax)
	if s.badQ > 0 && now-s.badAt < smoothBadHold {
		// Trouble came at badQ a while ago: no higher than a little below it.
		top = max(float64(videoSmoothQ), min(top, s.badQ-smoothBadMargin))
	}
	if steps := float64((now - s.qCapAt) / smoothQRise); steps > 0 {
		s.qCap = min(top, s.qCap+steps)
		s.qCapAt += time.Duration(steps) * smoothQRise
	}
	return s.qCap
}

// troubled notes trouble on the path at now (why, for the log): the
// quality allowed falls to videoSmoothQ; when it was above, that height is
// remembered for smoothBadHold (smoothQCap).
func (s *layerSender) troubled(now time.Duration, why string) {
	if s.qCap > float64(videoSmoothQ) {
		s.badQ, s.badAt = s.qCap, now
	}
	if now-s.trouble >= smoothCleanFor && s.qCap > float64(videoSmoothQ) {
		log.Printf("[video/smooth] trouble at q%.0f: %s; the quality allowed back to %d, no higher than %.0f for %v",
			s.qCap, why, videoSmoothQ, max(float64(videoSmoothQ), s.badQ-smoothBadMargin), smoothBadHold)
	}
	s.trouble = now
}

// queued says whether a standing queue is on the way (smooth video): the
// receiver's standing queue of smoothQueueHigh with less arriving than went
// (a path turned shorter after a longer one looks like a queue, but more
// arrives).
func (s *layerSender) queued() bool {
	afterSilent := s.lastReport-s.rc.silentAt < rateAfterSilent // a new path's delay is no queue
	return videoSmooth && !afterSilent && s.queue >= smoothQueueHigh && s.got > 0 && s.got < rateOverShare*s.went
}

// effRate is the kbit/s to send at: the rate's, but while a queue stands at
// most smoothDrain of what arrives, so that it drains (the rate's own cuts
// come a round trip apart, and the round trip grows with the queue).
func (s *layerSender) effRate() float64 {
	if s.queued() {
		return min(s.rc.rate, smoothDrain*s.got)
	}
	return s.rc.rate
}

// adaptGrid picks the grid for the rate.
func (s *layerSender) adaptGrid(now time.Duration) {
	if s.rc.perCellN < 1 || s.rc.perCell <= 0 || s.paneRows == 0 {
		return // not known yet
	}
	if videoSmooth {
		return // the pane's grid: the smooth coder meets the rate by its quality
	}
	aspect := float64(s.paneCols) / float64(s.paneRows)
	cells := max(0, videoGridFill*s.rc.rate*1000/s.rc.perCell)
	want := min(s.paneRows, int(math.Sqrt(cells/aspect)))
	want = max(want, min(s.paneRows, max(4, s.paneRows/videoGridLeastPart)))
	switch {
	case float64(want) <= videoGridDownAt*float64(s.hiRows) && now-s.lastGrid >= videoGridDownGap:
		s.setHi(now, want)
	case want > s.hiRows && (float64(want) >= videoGridUpAt*float64(s.hiRows) || want == s.paneRows) && now-s.lastGrid >= videoGridUpGap:
		s.setHi(now, min(want, max(s.hiRows+1, int(float64(s.hiRows)*videoGridUpMost))))
	}
}

// frame codes a camera frame into the bands' frames to send now.
func (s *layerSender) frame(now time.Duration, f codec.Frame) ([]layerPacket, error) {
	if s.paneRows == 0 {
		return nil, nil
	}
	s.rc.tick(now)
	s.capNow = now
	if videoSmooth {
		// Only what the peer's pane shows of the camera is sent, and no
		// more pixels than it has (4 x 8 a cell: a terminal's pane is
		// often smaller than the camera).
		nw, nh := cropSize(f.Width, f.Height, s.hiCols, s.hiRows)
		if s.cropW == 0 || math.Abs(float64(nw*s.cropH)/float64(nh*s.cropW)-1) > smoothCropKeep || s.cropW > f.Width || s.cropH > f.Height {
			s.cropW, s.cropH = nw, nh
		}
		f = cropTo(f, s.cropW, s.cropH, &s.crop)
		if !peerPixels.Load() {
			f = shrinkCover(f, s.hiCols*4, s.hiRows*8, &s.small)
		}
	}
	s.rc.demand = s.rc.perCell * float64(s.paneCols*s.paneRows) / 1000
	if videoSmooth {
		// A still picture's smooth frames are a few copies: the rate held to
		// twice them left no room when the picture moved (the quality fell
		// to its least each time). A queue or loss cuts it at once.
		s.rc.demand = max(s.rc.demand, smoothRateFloor/1.5)
	}
	s.adaptGrid(now)
	s.rc.allow(now)
	// The bands, the longest waiting first, while the budget holds (an
	// unchanged band's heartbeat aside).
	order := make([]int, len(s.hi))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return s.hi[order[i]].lastSent < s.hi[order[j]].lastSent })
	var out []layerPacket
	for _, b := range order {
		t := s.hi[b]
		if (s.rc.tokens <= 0 || s.queued() && s.rc.sending(now) >= s.effRate()) && now-t.lastSent < videoBandHeartbeat {
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

// encode codes a band's frame (nil: nothing changed).
func (s *layerSender) encode(now time.Duration, t *layerTx, band byte, f codec.Frame) (*layerPacket, error) {
	if t.keyReq && now-t.lastForcedKey >= videoKeyframeRequestGap {
		t.lastForcedKey = now
		t.enc.ForceKeyframe()
	}
	t.keyReq = false
	// After a loss (the peer acknowledges then), every frame against the
	// newest frame acknowledged: a lost datagram is lost alone.
	switch has := s.rxHas[band]; {
	case t.haveAck && now-t.ackedAt < videoAckHold:
		t.enc.CodeAgainst(append([]uint16{t.acked}, has...)...)
	case videoSmooth && len(has) > 0 && s.lastReport > 0 && now-s.lastReport >= smoothSilent:
		t.enc.CodeAgainst(has...) // an outage: what goes in it may be lost
	}
	t.enc.SetSkipEmpty(now-t.lastSent < videoBandHeartbeat)
	if c, ok := t.enc.(interface{ setNow(time.Duration) }); ok {
		c.setNow(now) // the smooth coder steers its quality by the frames' times
	}
	b, err := t.enc.Encode(f, t.cols, t.rows)
	if err != nil || b == nil {
		return nil, err
	}
	p := &layerPacket{head: layerHead{layer: band, gen: s.gen, rows: s.hiRows, bands: len(s.hi), seq: t.seq}, frame: b,
		key: isKeyframe(b)}
	t.seq++
	if !p.key {
		s.rc.measure(len(b), t.cols*t.rows, min(now-t.lastSent, time.Second))
	}
	t.lastSent = now
	s.rc.spent(now, p.size())
	return p, nil
}

// ===== the receiver =====

// layerRx is a band at the receiver.
type layerRx struct {
	dec         bandDecoder
	haveSeq     bool
	lastSeq     byte
	tf          *codec.TextFrame
	broken      bool // a frame could not be decoded: waiting to recover
	lastArrival time.Duration
	lastRequest time.Duration
	lastLoss    time.Duration
}

func newLayerRx() *layerRx {
	var dec bandDecoder = codec.NewDecoder()
	if videoSmooth {
		dec = newSmoothRx()
	}
	return &layerRx{dec: dec, lastArrival: -time.Hour, lastRequest: -time.Hour, lastLoss: -time.Hour}
}

// layerArrival is a datagram waiting for its turn.
type layerArrival struct {
	head  layerHead
	frame []byte
	index uint16
	at    time.Duration
}

// layerReceiver decodes the bands, asks the sender to recover what was
// lost, reports what arrives, and puts the picture together.
type layerReceiver struct {
	start   time.Time
	send    func(msg []byte) // to the peer's sender (ui.SendVideoCtl)
	hi      []*layerRx
	haveGen bool
	gen     byte
	rows    int // the grid's
	pending []layerArrival
	last    *codec.TextFrame // the picture shown last
	lastIn  *codec.TextFrame // it in the grid's cells, for bands of a new grid not here yet
	lastOf  *codec.TextFrame
	acks    map[byte]uint16 // bands acknowledged since the last flush, of the grid gen
	ackedAt time.Duration   // the last flush

	// what arrived, for the reports
	nextReport  time.Duration
	haveG       bool
	lastG       byte
	rBytes      int
	rExpected   int
	rGot        int
	rQueue      time.Duration // the least a datagram waited since the last report (-1: none)
	qNow        time.Duration // what the newest sent of them waited (smooth video's report; -1: none)
	qNowU       int64         // its sending, in the receiver's ticks
	lossWin     [10][3]int
	qWin        [3]time.Duration // rateStanding
	lossK       int
	lastArrival time.Duration
	haveRel     bool
	prevRel     int
	relU        int64 // arrival less sending, in media ticks, unwrapped (the clocks' offset in it)
	newestU     int64 // the newest datagram's sending, in the receiver's ticks
	floor       owdFloor
	floorFrom   time.Duration // the floor taken from then on (after an outage)
}

func newLayerReceiver(send func([]byte)) *layerReceiver {
	return &layerReceiver{start: time.Now(), send: send, rQueue: -1, qNow: -1, lastArrival: -time.Hour}
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
	if r.lastArrival >= 0 && now-r.lastArrival >= rateSilent {
		// After an outage: maybe another path, its own floor, taken anew
		// once it settles (its delay may still be climbing for a moment).
		r.floor, r.floorFrom = owdFloor{}, now+rateFloorSettle
	}
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
	q := time.Duration(0)
	if now >= r.floorFrom {
		r.floor.note(now, owd)
		q = owd - r.floor.get(now)
	}
	if r.rQueue < 0 || q < r.rQueue {
		r.rQueue = q
	}
	if sendU := int64(now/mediaTickDuration) - r.relU; r.qNow < 0 || sendU >= r.qNowU {
		r.qNow, r.qNowU = q, sendU
	}
	r.newestU = max(r.newestU, int64(now/mediaTickDuration)-r.relU)
	r.pending = append(r.pending, layerArrival{head: h, frame: frame, index: index, at: now})
	return nil
}

// rxOf is the band a datagram is for (nil: none).
func (r *layerReceiver) rxOf(h layerHead) *layerRx {
	if r.haveGen && h.gen == r.gen && int(h.layer) < len(r.hi) {
		return r.hi[h.layer]
	}
	return nil
}

// drain decodes what is due by at: each band's datagrams in order of its own
// counter, a missing one waited for up to videoReorderWait. It reports
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
	// A newer grid: its bands start afresh.
	if !r.haveGen || h.gen != r.gen && byte(h.gen-r.gen) < 128 {
		r.haveGen, r.gen, r.rows = true, h.gen, h.rows
		clear(r.acks)
		old := r.hi
		r.hi = make([]*layerRx, h.bands)
		for i := range r.hi {
			r.hi[i] = newLayerRx()
			if _, smooth := r.hi[i].dec.(*smoothRx); smooth && len(old) == len(r.hi) {
				// The sender's smooth coder goes on across grids: so do the
				// band's decoder and its references.
				r.hi[i].dec = old[i].dec
			}
		}
	}
	if h.gen == r.gen && int(h.layer) < len(r.hi) {
		return r.take(r.hi[h.layer], h.layer, h.gen, h.seq, p.frame, now)
	}
	return false
}

// take decodes a band's frame, as video was received before (protocol 6).
func (r *layerReceiver) take(rx *layerRx, band, gen, seq byte, data []byte, now time.Duration) bool {
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
		wasBroken := rx.broken
		rx.broken, rx.lastLoss = true, now
		if now-rx.lastRequest >= videoKeyframeRetry {
			rx.lastRequest = now
			if id, ok := rx.dec.LastGood(); ok {
				r.send(ctlMessage(ctlRecover, band, gen, id))
			} else {
				r.send(ctlMessage(ctlKeyframe, band, gen, 0))
			}
		}
		// The band turns to noise; in the window a TV's snow, drawn afresh
		// with each frame that does not decode (it moves).
		return !wasBroken || videoSmooth && ui.PixelMode()
	}
	rx.broken, rx.tf = false, tf
	if now-rx.lastLoss < videoAckWindow {
		if id, ok := rx.dec.LastGood(); ok {
			if r.acks == nil {
				r.acks = map[byte]uint16{}
			}
			r.acks[band] = id // sent with the others every videoAckFlush
		}
	}
	return true
}

// smoothReport appends a report's smooth part (layerSender.smoothReport).
func (r *layerReceiver) smoothReport(msg []byte) []byte {
	q := uint16(0xffff)
	if r.qNow >= 0 {
		q = uint16(min(r.qNow, 65534*time.Millisecond) / time.Millisecond)
		ui.UpdateVideoLag(r.qNow) // the window's hologram rolls by it
	}
	msg = binary.LittleEndian.AppendUint16(msg, q)
	b3 := 0
	for j := 1; j <= 3; j++ {
		b3 += r.lossWin[(r.lossK-j+len(r.lossWin))%len(r.lossWin)][2]
	}
	msg = binary.LittleEndian.AppendUint32(msg, uint32(b3))
	type marker interface{ LastMark() (uint16, bool) }
	var bands []byte
	n := 0
	for b, rx := range r.hi {
		g, ok := rx.dec.LastGood()
		if !ok || b > 255 {
			continue
		}
		m := g
		if mk, isM := rx.dec.(marker); isM {
			if v, ok := mk.LastMark(); ok {
				m = v
			}
		}
		bands = binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(append(bands, byte(b)), g), m)
		n++
	}
	return append(append(msg, r.gen, byte(min(n, 255))), bands...)
}

func ctlMessage(kind, band, gen byte, id uint16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{kind, band, gen}, id)
}

// report sends the sender the acknowledgements every videoAckFlush, and a
// report every rateReportEvery.
func (r *layerReceiver) report(at time.Time) {
	now := at.Sub(r.start)
	if len(r.acks) > 0 && now-r.ackedAt >= videoAckFlush {
		r.ackedAt = now
		msg := []byte{ctlAcks, r.gen}
		for band, id := range r.acks {
			msg = binary.LittleEndian.AppendUint16(append(msg, band), id)
		}
		clear(r.acks)
		r.send(msg)
	}
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
			if videoSmooth {
				msg = r.smoothReport(msg)
			}
			r.send(msg)
		}
		r.rBytes, r.rExpected, r.rGot, r.rQueue, r.qNow = 0, 0, 0, -1, -1
		r.nextReport += rateReportEvery
	}
}

// picture is what the receiver shows: each band as decoded; a band that
// hung (a frame of it could not be decoded: one was lost) shows the
// no-signal noise until a frame of it decodes again; a band of a new grid
// not here yet shows the picture before (noise before there was one).
// Nil: nothing decoded yet.
func (r *layerReceiver) picture() *codec.TextFrame {
	var cols int
	for _, rx := range r.hi {
		if rx.tf != nil {
			cols = rx.tf.Cols
			break
		}
	}
	if r.rows == 0 || cols == 0 {
		return nil
	}
	rows := r.rows
	out := &codec.TextFrame{Cols: cols, Rows: rows, Width: cols << 2, Height: rows << 3,
		Glyphs: make([]byte, cols*rows), FgYCbCr: make([]byte, 3*cols*rows), BgYCbCr: make([]byte, 3*cols*rows)}
	for i, b := range bandRanges(rows) {
		var rx *layerRx
		if i < len(r.hi) {
			rx = r.hi[i]
		}
		switch {
		case rx != nil && rx.broken:
			ui.NoiseRows(out, b[0], b[1])
		case rx != nil && rx.tf != nil && rx.tf.Cols == cols && rx.tf.Rows == b[1]-b[0]:
			o := b[0] * cols
			copy(out.Glyphs[o:], rx.tf.Glyphs)
			copy(out.FgYCbCr[3*o:], rx.tf.FgYCbCr)
			copy(out.BgYCbCr[3*o:], rx.tf.BgYCbCr)
		case r.last != nil:
			before := r.lastInGrid(cols, rows)
			o, e := b[0]*cols, b[1]*cols
			copy(out.Glyphs[o:e], before.Glyphs[o:e])
			copy(out.FgYCbCr[3*o:3*e], before.FgYCbCr[3*o:3*e])
			copy(out.BgYCbCr[3*o:3*e], before.BgYCbCr[3*o:3*e])
		default:
			ui.NoiseRows(out, b[0], b[1])
		}
	}
	out.Pixels = r.pixels(cols, rows)
	r.last = out
	return out
}

// pixels is the bands' pixels put together (smooth video in the window,
// cols x rows cells), nil unless every band has its own: then the window
// draws the cells. A band that hung (a frame of it lost on a bad path)
// shows a TV's snow (ui.StaticPixels), the window's no-signal noise, not
// the cells' glyphs.
func (r *layerReceiver) pixels(cols, rows int) *codec.Pixels {
	if len(r.hi) == 0 {
		return nil
	}
	snow := videoSmooth && ui.PixelMode()
	if len(r.hi) == 1 {
		switch rx := r.hi[0]; {
		case rx.broken && snow:
			return ui.StaticPixels(cols*4, rows*8, time.Now())
		case rx.broken:
			return nil
		case rx.tf != nil:
			return rx.tf.Pixels
		case r.last != nil:
			return r.last.Pixels // a new grid's band not here yet: the picture before
		}
		return nil
	}
	// The bands' width: a decoded band's; the snow of a broken one as tall
	// as its rows at that width.
	w := 0
	for _, rx := range r.hi {
		if !rx.broken && rx.tf != nil && rx.tf.Pixels != nil {
			w = rx.tf.Pixels.W
			break
		}
	}
	if w == 0 {
		w = cols * 4
	}
	ranges := bandRanges(rows)
	parts := make([]*codec.Pixels, len(r.hi))
	h := 0
	for i, rx := range r.hi {
		switch {
		case rx.broken && snow && i < len(ranges):
			parts[i] = ui.StaticPixels(w, (ranges[i][1]-ranges[i][0])*8*w/max(1, cols*4), time.Now())
		case rx.broken || rx.tf == nil || rx.tf.Pixels == nil || rx.tf.Pixels.W != w:
			return nil
		default:
			parts[i] = rx.tf.Pixels
		}
		h += parts[i].H
	}
	out := &codec.Pixels{W: w, H: h, Y: make([]byte, 0, w*h), Cb: make([]byte, 0, w*h), Cr: make([]byte, 0, w*h)}
	for _, p := range parts {
		out.Y, out.Cb, out.Cr = append(out.Y, p.Y...), append(out.Cb, p.Cb...), append(out.Cr, p.Cr...)
	}
	return out
}

// lastInGrid is the picture shown before in a grid of cols x rows: the
// same picture when the grid is the same, else each cell's 4x8 pixels
// sampled from it, the glyph that matches them best (or its inverse), the
// colours of the cell under its middle. Kept while the picture is the same.
func (r *layerReceiver) lastInGrid(cols, rows int) *codec.TextFrame {
	src := r.last
	if src.Cols == cols && src.Rows == rows {
		return src
	}
	if r.lastOf == src && r.lastIn != nil && r.lastIn.Cols == cols && r.lastIn.Rows == rows {
		return r.lastIn
	}
	pats := glyphPatterns()
	n := len(codec.GlyphCodepoints())
	up := &codec.TextFrame{Cols: cols, Rows: rows, Width: cols << 2, Height: rows << 3,
		Glyphs: make([]byte, cols*rows), FgYCbCr: make([]byte, 3*cols*rows), BgYCbCr: make([]byte, 3*cols*rows)}
	sw, sh := src.Cols*4, src.Rows*8
	w, h := cols*4, rows*8
	for y := range rows {
		for x := range cols {
			var want uint32
			for k := range 32 {
				sx, sy := (x*4+k%4)*sw/w, (y*8+k/4)*sh/h
				c := (sy/8)*src.Cols + sx/4
				if pats[src.Glyphs[c]]>>(31-((sy%8)*4+sx%4))&1 != 0 {
					want |= 1 << (31 - k)
				}
			}
			best, bestD, inv := 0, 33, false
			for g, p := range pats[:n] {
				if d := bits.OnesCount32(p ^ want); d < bestD {
					best, bestD, inv = g, d, false
				}
				if d := bits.OnesCount32(^p ^ want); d < bestD {
					best, bestD, inv = g, d, true
				}
			}
			mx, my := (x*4+2)*sw/w, (y*8+4)*sh/h
			c := (my/8)*src.Cols + mx/4
			i := y*cols + x
			up.Glyphs[i] = byte(best)
			fg, bg := src.FgYCbCr[3*c:3*c+3], src.BgYCbCr[3*c:3*c+3]
			if inv {
				fg, bg = bg, fg
			}
			copy(up.FgYCbCr[3*i:], fg)
			copy(up.BgYCbCr[3*i:], bg)
		}
	}
	r.lastIn, r.lastOf = up, src
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
