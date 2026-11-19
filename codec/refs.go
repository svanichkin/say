package codec

import "sync/atomic"

// Frames with numbers and references (Encoder.SetRefs): a lost frame no
// longer costs a keyframe. Each frame carries its number; every frame but
// a keyframe also the number of the frame it is coded against, its
// reference: normally the frame before. Both sides keep the last refRing
// frames they sent or decoded (the cells as palette indices, the palette,
// the change frames' models). A receiver that lost a frame asks the sender
// to recover from the newest frame it decoded (RecoverFrom); the sender
// then codes its next frame against that one, the palette patched from
// that frame's palette, and goes on from there. A keyframe only when that
// frame is no longer kept.
//
// The frame types with references are the plain ones plus frameTypeRefs;
// after the header (cols, rows) the frame's number and, but for a keyframe,
// its reference, each uint16 LE: they come round only after half an hour,
// so a receiver that decoded nothing for a while never names a frame the
// sender has since replaced with another of that number.
const (
	frameTypeRefs = 4
	refRing       = 64 // frames kept: two seconds at 30 a second
)

// refFrame is a frame as both sides hold it after coding or decoding it.
type refFrame struct {
	valid      bool
	id         uint16
	cols, rows int
	glyphs     []byte
	fg, bg     []byte
	palette    []paletteColor
	rc         rcChange
	patched    bool // the encoder's patchedSinceKey after it
}

// save stores a frame's state in r (its buffers reused).
func (r *refFrame) save(id uint16, cols, rows int, glyphs, fg, bg []byte, palette []paletteColor, rc *rcChange, patched bool) {
	r.valid, r.id, r.cols, r.rows, r.patched = true, id, cols, rows, patched
	r.glyphs = append(r.glyphs[:0], glyphs...)
	r.fg = append(r.fg[:0], fg...)
	r.bg = append(r.bg[:0], bg...)
	r.palette = append(r.palette[:0], palette...)
	prev := append(r.rc.prev[:0], rc.prev...)
	r.rc = *rc
	r.rc.prev = prev
}

// model is a copy of the frame's change model.
func (r *refFrame) model(dst *rcChange) {
	prev := append(dst.prev[:0], r.rc.prev...)
	*dst = r.rc
	dst.prev = prev
}

// refs is one side's frames by number.
type refs struct {
	ring [refRing]refFrame
}

func (s *refs) at(id uint16) *refFrame {
	r := &s.ring[int(id)%refRing]
	if !r.valid || r.id != id {
		return nil
	}
	return r
}

func (s *refs) slot(id uint16) *refFrame { return &s.ring[int(id)%refRing] }

func (s *refs) clear() {
	for i := range s.ring {
		s.ring[i].valid = false
	}
}

// encoderRefs is the encoder's side.
type encoderRefs struct {
	on      bool
	frames  refs
	next    uint16       // the next frame's number
	last    uint16       // the frame coded against (the last one, or the one recovered from)
	sent    uint16       // the last frame sent
	any     bool         // a frame was sent
	recover atomic.Int64 // RecoverFrom: the frame to code against, -1 none
	acked   atomic.Int64 // Acknowledged: a frame to code against if kept, -1 none
}

// showsSame reports whether the receiver already shows these cells: an
// empty change frame is skipped only then. Coded against an older frame
// (RecoverFrom), the last frame sent may differ from it.
func (r *encoderRefs) showsSame(glyphs, fg, bg []byte) bool {
	if !r.on || !r.any || r.last == r.sent {
		return true
	}
	f := r.frames.at(r.sent)
	return f != nil && string(f.glyphs) == string(glyphs) && string(f.fg) == string(fg) && string(f.bg) == string(bg)
}

// SetRefs makes frames carry numbers and references (frame types 4-7), so
// a peer that lost one asks to recover from a frame it holds (RecoverFrom)
// instead of for a keyframe. Both sides must support it.
func (e *Encoder) SetRefs(on bool) {
	e.refs.on = on
	e.refs.recover.Store(-1)
	e.refs.acked.Store(-1)
}

// CodeAgainst makes the next frame code against frame id, which the peer
// acknowledged, while it is kept (else against the frame before, as
// usual): a frame lost on the way then costs only its own update. Safe to
// call from another goroutine.
func (e *Encoder) CodeAgainst(id uint16) {
	e.refs.acked.Store(int64(id))
}

// RecoverFrom makes the next frame code against frame id, which the peer
// holds (it lost a later one), or a keyframe when that frame is no longer
// kept. Safe to call from another goroutine.
func (e *Encoder) RecoverFrom(id uint16) {
	e.refs.recover.Store(int64(id))
}

// rewind sets the encoder's view of the receiver back to frame id: the
// cells sent, the palette, the change model. It reports false when that
// frame is not kept (or has another size).
func (e *Encoder) rewind(id uint16, cols, rows int) bool {
	r := e.refs.frames.at(id)
	if r == nil || r.cols != cols || r.rows != rows {
		return false
	}
	e.sentGlyphs = append(e.sentGlyphs[:0], r.glyphs...)
	e.sentFg = append(e.sentFg[:0], r.fg...)
	e.sentBg = append(e.sentBg[:0], r.bg...)
	e.sentCells = cols * rows
	e.storePalette(r.palette)
	r.model(&e.rc)
	e.patchedSinceKey = r.patched
	e.refs.last = id
	return true
}

// decoderRefs is the decoder's side.
type decoderRefs struct {
	frames refs
	last   uint16 // the last frame decoded
	any    bool
}

// LastGood is the number of the newest frame decoded with references (for
// Encoder.RecoverFrom on the sender), ok false before any.
func (d *Decoder) LastGood() (id uint16, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refs.last, d.refs.any
}

// rewindLocked makes frame id the decoder's base. It reports false when the
// frame is not kept.
func (d *Decoder) rewindLocked(id uint16, cols, rows int) bool {
	if d.refs.any && d.refs.last == id && d.baseValid && d.baseCols == cols && d.baseRows == rows {
		return true // the frame just decoded
	}
	r := d.refs.frames.at(id)
	if r == nil || r.cols != cols || r.rows != rows {
		return false
	}
	d.baseGlyphs = append(d.baseGlyphs[:0], r.glyphs...)
	d.baseFg = append(d.baseFg[:0], r.fg...)
	d.baseBg = append(d.baseBg[:0], r.bg...)
	d.baseCols, d.baseRows, d.baseValid = cols, rows, true
	d.lastPalette = append(d.lastPalette[:0], r.palette...)
	r.model(&d.rc)
	return true
}
