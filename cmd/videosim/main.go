// Command videosim plays recorded camera frames (cmd/recordframes) through
// the video codec and a simulated path, the way a call does, and reports
// what the receiver saw: bytes, keyframes, delay, late and frozen frames
// and the damage against the same stream over a clean path.
//
// The path: a link of -bw kbit/s (with -bwdrop spells of less) whose queue
// drops what would wait longer than -queue, -delay one way plus up to
// -jitter more (so frames may arrive out of order), and loss of IP
// fragments (-frag bytes each; -loss, -burst, -outage). A frame is one
// datagram, lost when any of its fragments is.
//
// The sender skips unchanged frames (a heartbeat every 200 ms). The
// receiver, as network/udp: a frame older than the last is dropped; without
// references a gap drops the change frames and asks for a keyframe; frames
// that cannot be decoded ask again, at most every 300 ms; with references
// it asks to recover from the newest frame decoded, and with -ack it also
// acknowledges every frame decoded. Its messages reach the sender a path
// delay later. Frames are shown -playout after their capture; one decoded
// after that is not shown (late), but stays the base of the next.
//
//	go run ./cmd/videosim -in call.frames -loss 0.02 -burst 3 -jitter 40ms
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/svanichkin/say/codec"
)

// request is the receiver's message to the sender: a keyframe request,
// or with references a loss report (recover from id) or an
// acknowledgement of frame id.
type request struct {
	at      time.Duration
	id      uint16
	recover bool // with references: recover from (or acknowledge) id
	loss    bool // a loss report, not an acknowledgement
}

// arrival is a frame that reached the receiver.
type arrival struct {
	at    time.Duration // when its last fragment arrived
	index int           // the frame's capture number
	seq   byte
	data  []byte
}

func main() {
	in := flag.String("in", "", "recorded frames (cmd/recordframes)")
	cols := flag.Int("cols", 80, "cells across")
	rows := flag.Int("rows", 40, "cells down")
	fps := flag.Float64("fps", 30, "frames a second (of the recording)")
	loss := flag.Float64("loss", 0, "packet (fragment) loss rate")
	burst := flag.Float64("burst", 1, "mean length of a loss burst in packets (Gilbert model; 1: independent)")
	outage := flag.String("outage", "", "outages: start:seconds[,start:seconds...]")
	frag := flag.Int("frag", 1400, "IP fragment size in bytes (0: a frame is one packet)")
	delay := flag.Duration("delay", 40*time.Millisecond, "one-way path delay")
	jitter := flag.Duration("jitter", 0, "extra one-way delay, uniform up to this (frames may arrive out of order)")
	bw := flag.Float64("bw", 0, "link rate in kbit/s (0: unlimited)")
	bwdrop := flag.String("bwdrop", "", "spells of a lower link rate: start:seconds:kbit/s[,...]")
	queue := flag.Duration("queue", 500*time.Millisecond, "the link queue drops a packet that would wait longer")
	playout := flag.Duration("playout", 150*time.Millisecond, "a frame is shown this long after its capture")
	hold := flag.Int("hold", 10, "cell hold threshold (-video-hold)")
	seed := flag.Int64("seed", 1, "random seed")
	ack := flag.Bool("ack", false, "with -refs, the receiver acknowledges every frame decoded and the sender codes against the newest acknowledged one: a lost frame loses only its own update")
	hybrid := flag.Duration("hybrid", 0, "with -ack: code against acknowledged frames only this long after the last loss reported, else against the frame before")
	reorder := flag.Duration("reorder", 0, "the receiver holds frames up to this long to take them in order of their numbers (0: as they arrive, the older dropped)")
	withRefs := flag.Bool("refs", false, "frames with numbers and references: a lost frame asks to recover from the newest frame decoded instead of for a keyframe")
	flag.Parse()
	if *ack {
		*withRefs = true
	}

	frames, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spells := func(s string, n int) [][]float64 {
		var out [][]float64
		if s == "" {
			return out
		}
		for _, o := range strings.Split(s, ",") {
			var v []float64
			for _, p := range strings.SplitN(o, ":", n) {
				f, _ := strconv.ParseFloat(p, 64)
				v = append(v, f)
			}
			for len(v) < n {
				v = append(v, 0)
			}
			out = append(out, v)
		}
		return out
	}
	outages := spells(*outage, 2)
	drops := spells(*bwdrop, 3)
	rng := rand.New(rand.NewSource(*seed))
	// Gilbert model: the bad state loses every packet; its mean stay is
	// burst packets and the long-run loss rate is loss.
	var pGoodBad, pBadGood float64
	if *loss > 0 {
		pBadGood = 1 / max(1, *burst)
		pGoodBad = *loss * pBadGood / (1 - *loss)
	}
	bad := false
	lostPacket := func() bool {
		if bad {
			bad = rng.Float64() >= pBadGood
		} else {
			bad = rng.Float64() < pGoodBad
		}
		return bad
	}
	rate := func(t time.Duration) float64 { // kbit/s, 0 unlimited
		s := t.Seconds()
		for _, d := range drops {
			if s >= d[0] && s < d[0]+d[1] {
				return d[2]
			}
		}
		return *bw
	}
	inOutage := func(t time.Duration) bool {
		s := t.Seconds()
		for _, o := range outages {
			if s >= o[0] && s < o[0]+o[1] {
				return true
			}
		}
		return false
	}
	pathDelay := func() time.Duration {
		d := *delay
		if *jitter > 0 {
			d += time.Duration(rng.Int63n(int64(*jitter)))
		}
		return d
	}

	enc := codec.NewEncoder(false, 0, 0, false)
	enc.SetShapeHold(*hold)
	enc.SetRefs(*withRefs)
	dec := codec.NewDecoder()
	// The same stream over a clean path: what the receiver should show.
	refEnc := codec.NewEncoder(false, 0, 0, false)
	refEnc.SetShapeHold(*hold)
	refDec := codec.NewDecoder()

	tick := time.Duration(float64(time.Second) / *fps)
	playoutTicks := int(*playout / tick)
	var (
		lastSent           time.Duration = -time.Hour
		seq                byte
		linkFree           time.Duration
		inFlight           []arrival     // frames on their way, by arrival
		requests           []request     // the receiver's messages on their way
		lastLoss           time.Duration = -time.Hour
		acked              uint16
		haveAck            bool
		haveSeq            bool
		lastSeq            byte
		lastRequest        time.Duration = -time.Hour
		waiting            bool
		decoded            = map[int]*codec.TextFrame{} // decoded frames by capture number, until shown
		refFrames          = map[int]*codec.TextFrame{} // the clean path's
		refLast            *codec.TextFrame
		shown              *codec.TextFrame
		shownIndex         = -1
		bytesSent, packets int
		framesSent, keys   int
		keyBytes, maxFrame int
		lostFrames, late   int
		queueDrops         int
		delays             []float64
		frozen, stalled    int
		shownTicks         int
		sumPSNR            float64
		cellsWrong, cells  int
		glyphsWrong        int
		encTime            time.Duration
		requestsMade       int
	)
	// receive handles the frames that have arrived by now, in order of
	// arrival, as network/udp does.
	// process decodes one frame, as network/udp does (at, the moment it is
	// taken: on arrival, or out of the reorder buffer).
	process := func(a arrival, at time.Duration) {
		if haveSeq {
			gap := a.seq - lastSeq
			if gap == 0 || gap > 128 {
				return // a duplicate or older than the last: its turn has passed
			}
		}
		ask := func() {
			if at-lastRequest < 300*time.Millisecond {
				return
			}
			lastRequest = at
			requestsMade++
			r := request{at: at + pathDelay(), loss: true}
			if *withRefs {
				r.id, r.recover = dec.LastGood()
			}
			requests = append(requests, r)
		}
		if haveSeq && a.seq-lastSeq != 1 && !*withRefs {
			dec.ResetBase() // without references the change frames have no base until a keyframe
			ask()
		}
		haveSeq, lastSeq = true, a.seq
		tv, err := dec.Decode(a.data)
		if err != nil {
			if !errors.Is(err, codec.ErrNoBase) {
				fmt.Fprintln(os.Stderr, "decode:", err)
			}
			ask()
			waiting = true
			return
		}
		waiting = false
		delays = append(delays, (at-time.Duration(a.index)*tick).Seconds()*1000)
		if at > time.Duration(a.index)*tick+*playout {
			late++ // decoded, the base of the next, but its time to show has passed
		} else {
			decoded[a.index] = tv
		}
		if *ack {
			id, _ := dec.LastGood()
			requests = append(requests, request{at: at + pathDelay(), id: id, recover: true})
		}
	}
	// receive takes the frames that have arrived by now. With -reorder they
	// wait in a buffer and go in order of their numbers; a missing one is
	// given up after -reorder.
	var pending []arrival
	receive := func(now time.Duration) {
		for len(inFlight) > 0 && inFlight[0].at <= now {
			a := inFlight[0]
			inFlight = inFlight[1:]
			if *reorder == 0 {
				process(a, a.at)
				continue
			}
			pending = append(pending, a)
		}
		for len(pending) > 0 {
			// The next by number after the last taken.
			sort.Slice(pending, func(x, y int) bool {
				return byte(pending[x].seq-lastSeq-1) < byte(pending[y].seq-lastSeq-1)
			})
			next := pending[0]
			if haveSeq && next.seq-lastSeq > 128 {
				pending = pending[1:] // older than the last taken
				continue
			}
			oldest := next.at
			for _, p := range pending {
				oldest = min(oldest, p.at)
			}
			if haveSeq && next.seq != lastSeq+1 && now-oldest < *reorder {
				break // wait a little for the missing one
			}
			pending = pending[1:]
			process(next, max(now, next.at))
		}
	}
	for i, fr := range frames {
		now := time.Duration(i) * tick
		// The receiver's messages that reached the sender.
		sort.Slice(requests, func(a, b int) bool { return requests[a].at < requests[b].at })
		for len(requests) > 0 && requests[0].at <= now {
			r := requests[0]
			requests = requests[1:]
			switch {
			case !r.recover:
				enc.ForceKeyframe()
			case r.loss:
				enc.RecoverFrom(r.id) // the newest frame the peer holds
				lastLoss = now
			default:
				acked, haveAck = r.id, true // an acknowledgement
			}
		}
		if *ack && haveAck && (*hybrid == 0 || now-lastLoss < *hybrid) {
			enc.CodeAgainst(acked) // never on a frame not acknowledged; gone: on from the frame before
		}

		// Send.
		enc.SetSkipEmpty(now-lastSent < 200*time.Millisecond)
		refEnc.SetSkipEmpty(now-lastSent < 200*time.Millisecond)
		t0 := time.Now()
		data, err := enc.Encode(fr, *cols, *rows)
		encTime += time.Since(t0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "encode:", err)
			os.Exit(1)
		}
		if refData, _ := refEnc.Encode(fr, *cols, *rows); refData != nil {
			if tv, err := refDec.Decode(refData); err == nil {
				refLast = tv
			}
		}
		refFrames[i] = refLast
		if data != nil {
			lastSent = now
			framesSent++
			bytesSent += len(data)
			maxFrame = max(maxFrame, len(data))
			if len(data) > 4 && (data[4] == 1 || data[4] == 5) { // a keyframe (plain or with references)
				keys++
				keyBytes += len(data)
			}
			n, size := 1, len(data)+28
			if *frag > 0 {
				n = (size + *frag - 1) / *frag
			}
			packets += n
			lost, arrive := inOutage(now), time.Duration(0)
			for k := range n {
				part := size
				if *frag > 0 {
					part = min(*frag, size-k**frag)
				}
				depart := now
				if r := rate(now); r > 0 {
					start := max(now, linkFree)
					if start-now > *queue {
						lost = true // the queue is full: dropped
						queueDrops++
						continue
					}
					depart = start + time.Duration(float64(part*8)/r*float64(time.Millisecond))
					linkFree = depart
				}
				if lostPacket() {
					lost = true
				}
				arrive = max(arrive, depart+pathDelay())
			}
			s := seq
			seq++
			if lost {
				lostFrames++
			} else {
				inFlight = append(inFlight, arrival{at: arrive, index: i, seq: s, data: data})
				sort.Slice(inFlight, func(a, b int) bool { return inFlight[a].at < inFlight[b].at })
			}
		}

		// Receive what arrived by now, then show the frame due: captured
		// playout ago, or the newest decoded before it.
		receive(now)
		if waiting {
			stalled++
		}
		due := i - playoutTicks
		if due < 0 {
			continue
		}
		for k, tv := range decoded {
			if k <= due && k > shownIndex {
				shown, shownIndex = tv, k
			}
			if k <= due {
				delete(decoded, k)
			}
		}
		ref := refFrames[due]
		delete(refFrames, due)
		if shown == nil || ref == nil {
			continue
		}
		shownTicks++
		if time.Duration(due-shownIndex)*tick >= 200*time.Millisecond {
			frozen++
		}
		p, w, g := compare(shown, ref)
		sumPSNR += p
		cellsWrong += w
		glyphsWrong += g
		cells += ref.Cols * ref.Rows
	}
	sort.Float64s(delays)
	pct := func(p float64) float64 {
		if len(delays) == 0 {
			return 0
		}
		return delays[min(len(delays)-1, int(p*float64(len(delays))))]
	}
	secs := float64(len(frames)) / *fps
	mode := "plain"
	switch {
	case *ack && *hybrid > 0:
		mode = "refs, ack for " + hybrid.String() + " after a loss"
	case *ack:
		mode = "refs, ack"
	case *withRefs:
		mode = "refs"
	}
	fmt.Printf("%d frames (%.1f s), %dx%d cells, %s\n", len(frames), secs, *cols, *rows, mode)
	fmt.Printf("  path    loss %.1f%% burst %.0f, outages %q, delay %v + jitter %v, link %.0f kbit/s (drops %q), queue %v, playout %v, reorder %v\n", 100**loss, *burst, *outage, *delay, *jitter, *bw, *bwdrop, *queue, *playout, *reorder)
	fmt.Printf("  sent    %d frames, %.1f kbit/s, mean %d B, max %d B, %d packets\n", framesSent, float64(bytesSent)*8/secs/1000, bytesSent/max(1, framesSent), maxFrame, packets)
	fmt.Printf("  keys    %d (%d B, %.0f%% of the bytes), requests %d\n", keys, keyBytes, 100*float64(keyBytes)/float64(max(1, bytesSent)), requestsMade)
	fmt.Printf("  lost    %d frames (%d packets dropped by the queue), late %d; delay mean %.0f ms, 95%% %.0f ms\n", lostFrames, queueDrops, late, mean(delays), pct(0.95))
	fmt.Printf("  shown   frozen %.1f%% of the time (200 ms or more behind), stalled %.1f%% (frames arrive but cannot be decoded)\n", 100*float64(frozen)/float64(max(1, shownTicks)), 100*float64(stalled)/float64(len(frames)))
	fmt.Printf("  damage  %.2f%% of cells wrong (%.2f%% by glyph), Y PSNR %.2f dB against the clean path\n", 100*float64(cellsWrong)/float64(max(1, cells)), 100*float64(glyphsWrong)/float64(max(1, cells)), sumPSNR/float64(max(1, shownTicks)))
	fmt.Printf("  encode  %.3f ms a frame\n", encTime.Seconds()*1000/float64(len(frames)))
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// load reads cmd/recordframes' file.
func load(path string) ([]codec.Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var frames []codec.Frame
	for len(data) >= 4 {
		w, h := int(binary.LittleEndian.Uint16(data)), int(binary.LittleEndian.Uint16(data[2:]))
		n := w * h * 3
		if len(data) < 4+n {
			break
		}
		frames = append(frames, codec.Frame{Data: data[4 : 4+n], Width: w, Height: h})
		data = data[4+n:]
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("%s: no frames", path)
	}
	return frames, nil
}

var patterns = func() (p [256]uint32) {
	for i, cp := range codec.GlyphCodepoints() {
		p[i], _ = codec.GlyphPattern(cp)
	}
	return p
}()

// compare draws both frames' luma (4x8 pixels a cell) and returns the PSNR
// of a against b (99 when equal), how many cells differ and how many of
// them by glyph (colours aside: another palette rounds them differently).
func compare(a, b *codec.TextFrame) (float64, int, int) {
	if a.Cols != b.Cols || a.Rows != b.Rows {
		return 0, b.Cols * b.Rows, b.Cols * b.Rows
	}
	var se float64
	wrong, glyphs := 0, 0
	for i := range b.Cols * b.Rows {
		pa, pb := patterns[a.Glyphs[i]], patterns[b.Glyphs[i]]
		fa, ba := float64(a.FgYCbCr[3*i]), float64(a.BgYCbCr[3*i])
		fb, bb := float64(b.FgYCbCr[3*i]), float64(b.BgYCbCr[3*i])
		if pa != pb || a.FgYCbCr[3*i] != b.FgYCbCr[3*i] || a.BgYCbCr[3*i] != b.BgYCbCr[3*i] {
			wrong++
		}
		if pa != pb {
			glyphs++
		}
		for k := 31; k >= 0; k-- {
			va, vb := ba, bb
			if pa>>k&1 != 0 {
				va = fa
			}
			if pb>>k&1 != 0 {
				vb = fb
			}
			se += (va - vb) * (va - vb)
		}
	}
	mse := se / float64(32*b.Cols*b.Rows)
	if mse == 0 {
		return 99, wrong, glyphs
	}
	return 10 * math.Log10(255*255/mse), wrong, glyphs
}
