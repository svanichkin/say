package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"runtime"
	"sort"
	"sync"
)

const defaultKeyframeInterval = 100

// DefaultKeyframeInterval экспортируется, чтобы потребители могли
// использовать ту же периодичность ключевых кадров, что и встроенный энкодер.
const DefaultKeyframeInterval = defaultKeyframeInterval

// Frame represents a raw YCbCr 4:4:4 image (packed as [Y Cb Cr]).
type Frame struct {
	Data          []byte
	Width, Height int
}

// TextFrame represents a terminal-oriented image using glyph/palette planes.
type TextFrame struct {
	Cols    int
	Rows    int
	Width   int
	Height  int
	Glyphs  []byte
	FgYCbCr []byte
	BgYCbCr []byte
	// Pixels, when set, is the picture itself (video in pixels, Babe's
	// smooth coder, in the window): the window draws it in place of the
	// cells, which the terminal draws.
	Pixels *Pixels
}

// Pixels is a picture of W x H pixels, its Y, Cb and Cr planes (4:4:4).
type Pixels struct {
	W, H      int
	Y, Cb, Cr []byte
}

// CharData describes a single terminal character cell and its colors.
type CharData struct {
	FgColor   [3]int
	BgColor   [3]int
	CodePoint int
}

type bitmapEntry struct {
	pattern   uint32
	codePoint int
	flags     int
}

var (
	glyphOnce       sync.Once
	glyphIndexTable map[int]byte
	glyphCodepoints []int
	glyphPatterns   []uint32 // 4x8 shape of each glyph index
)

var bitmaps = []bitmapEntry{
	{0x00000000, 0x00a0, 0},

	// Block graphics
	{0x0000000f, 0x2581, 0},
	{0x000000ff, 0x2582, 0},
	{0x00000fff, 0x2583, 0},
	{0x0000ffff, 0x2584, 0},
	{0x000fffff, 0x2585, 0},
	{0x00ffffff, 0x2586, 0},
	{0x0fffffff, 0x2587, 0},

	{0xeeeeeeee, 0x258a, 0},
	{0xcccccccc, 0x258c, 0},
	{0x88888888, 0x258e, 0},

	{0x0000cccc, 0x2596, 0},
	{0x00003333, 0x2597, 0},
	{0xcccc0000, 0x2598, 0},
	{0xcccc3333, 0x259a, 0},
	{0x33330000, 0x259d, 0},

	// Heavy lines
	{0x000ff000, 0x2501, 0},
	{0x66666666, 0x2503, 0},

	{0x00077666, 0x250f, 0},
	{0x000ee666, 0x2513, 0},
	{0x66677000, 0x2517, 0},
	{0x666ee000, 0x251b, 0},

	{0x66677666, 0x2523, 0},
	{0x666ee666, 0x252b, 0},
	{0x000ff666, 0x2533, 0},
	{0x666ff000, 0x253b, 0},
	{0x666ff666, 0x254b, 0},

	{0x000cc000, 0x2578, 0},
	{0x00066000, 0x2579, 0},
	{0x00033000, 0x257a, 0},
	{0x00066000, 0x257b, 0},

	{0x06600660, 0x254f, 0},

	// Light lines
	{0x000f0000, 0x2500, 0},
	{0x0000f000, 0x2500, 0},
	{0x44444444, 0x2502, 0},
	{0x22222222, 0x2502, 0},

	{0x000e0000, 0x2574, 0},
	{0x0000e000, 0x2574, 0},
	{0x44440000, 0x2575, 0},
	{0x22220000, 0x2575, 0},
	{0x00030000, 0x2576, 0},
	{0x00003000, 0x2576, 0},
	{0x00004444, 0x2577, 0},
	{0x00002222, 0x2577, 0},

	// Misc technical
	{0x44444444, 0x23a2, 0},
	{0x22222222, 0x23a5, 0},

	{0x0f000000, 0x23ba, 0},
	{0x00f00000, 0x23bb, 0},
	{0x00000f00, 0x23bc, 0},
	{0x000000f0, 0x23bd, 0},

	// Shapes
	{0x00066000, 0x25aa, 0},
}

func initGlyphTables() {
	glyphOnce.Do(func() {
		glyphIndexTable = make(map[int]byte, len(bitmaps))
		seen := make(map[int]struct{}, len(bitmaps))
		for _, bm := range bitmaps {
			if _, ok := seen[bm.codePoint]; ok {
				continue
			}
			glyphIndexTable[bm.codePoint] = byte(len(glyphCodepoints))
			glyphCodepoints = append(glyphCodepoints, bm.codePoint)
			glyphPatterns = append(glyphPatterns, bm.pattern)
			seen[bm.codePoint] = struct{}{}
			if len(glyphCodepoints) >= 64 {
				break
			}
		}
	})
}

// GlyphPattern returns the 4x8 shape of a glyph (bit 31 is the top left
// pixel, rows of 4 follow each other), for drawing the cells as pixels.
func GlyphPattern(codePoint int) (pattern uint32, ok bool) {
	for _, bm := range bitmaps {
		if bm.codePoint == codePoint {
			return bm.pattern, true
		}
	}
	return 0, false
}

func GlyphCodepoints() []int {
	initGlyphTables()
	return glyphCodepoints
}

func CreateCharData(getPixel func(int, int) uint32, x0, y0, codePoint int, pattern uint32) CharData {
	return createCharData(getPixel, x0, y0, codePoint, pattern)
}

func FindCharData(getPixel func(int, int) uint32, x0, y0, flags int) CharData {
	return findCharData(getPixel, x0, y0, flags)
}

func createCharData(getPixel func(int, int) uint32, x0, y0, codePoint int, pattern uint32) CharData {
	block := readCellBlock(getPixel, x0, y0)
	return createCharDataBlock(&block, codePoint, pattern)
}

func findCharData(getPixel func(int, int) uint32, x0, y0, flags int) CharData {
	block := readCellBlock(getPixel, x0, y0)
	return findCharDataBlock(&block, flags)
}

// cellBlock holds the 4x8 pixels of one cell, row by row, each packed as
// Y<<16 | Cb<<8 | Cr.
type cellBlock [32]uint32

func readCellBlock(getPixel func(int, int) uint32, x0, y0 int) cellBlock {
	var block cellBlock
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			block[y*4+x] = getPixel(x0+x, y0+y)
		}
	}
	return block
}

// createCharDataBlock averages the pixels under the set and the clear bits
// of pattern into the foreground and background colours.
func createCharDataBlock(block *cellBlock, codePoint int, pattern uint32) CharData {
	var sums [6]uint32
	cellSums(block, pattern, &sums)
	fg := [3]int{int(sums[0]), int(sums[1]), int(sums[2])}
	bg := [3]int{int(sums[3]) - fg[0], int(sums[4]) - fg[1], int(sums[5]) - fg[2]}
	fgCount := bits.OnesCount32(pattern)
	if bgCount := 32 - fgCount; bgCount > 0 {
		bg[0], bg[1], bg[2] = bg[0]/bgCount, bg[1]/bgCount, bg[2]/bgCount
	}
	if fgCount > 0 {
		fg[0], fg[1], fg[2] = fg[0]/fgCount, fg[1]/fgCount, fg[2]/fgCount
	}
	return CharData{CodePoint: codePoint, FgColor: fg, BgColor: bg}
}

// glyphCandidates are the glyphs tried for a cell without flags, in the
// order of bitmaps, without the entries that can never win: a pattern equal
// to an earlier one, or to an earlier one inverted, scores the same and the
// earlier entry keeps a tie.
var (
	glyphCandOnce     sync.Once
	glyphCandPatterns []uint32
	glyphCandCodes    []int
)

func glyphCandidates() ([]uint32, []int) {
	glyphCandOnce.Do(func() {
		seen := make(map[uint32]bool)
		for _, bm := range bitmaps {
			if bm.flags != 0 || seen[bm.pattern] || seen[^bm.pattern] {
				continue
			}
			seen[bm.pattern] = true
			glyphCandPatterns = append(glyphCandPatterns, bm.pattern)
			glyphCandCodes = append(glyphCandCodes, bm.codePoint)
		}
	})
	return glyphCandPatterns, glyphCandCodes
}

// findCharDataBlock picks the glyph and colours that best represent a cell:
// the cell is split along its widest channel at the middle of its range, the
// glyph closest to that split is chosen, and the pixels under its set and
// clear bits are averaged into the colours.
//
// Everything is done per pixel in one pass. Finding the cell's distinct
// colours first (a camera cell has 12-24) to catch the case of two colours
// covering most of it, which held in about one cell in nine and was then drawn
// in those two exact colours, cost a third of the encoder's time.
func findCharDataBlock(block *cellBlock, flags int) CharData {
	pattern, codePoint := nearestGlyph(cellMask(block), flags)
	return createCharDataBlock(block, codePoint, pattern)
}

// cellSumsGo is cellSums in Go.
func cellSumsGo(block *cellBlock, pattern uint32, sums *[6]uint32) {
	*sums = [6]uint32{}
	mask := uint32(0x80000000)
	for _, px := range block {
		y, cb, cr := px>>16&0xff, px>>8&0xff, px&0xff
		if pattern&mask != 0 {
			sums[0], sums[1], sums[2] = sums[0]+y, sums[1]+cb, sums[2]+cr
		}
		sums[3], sums[4], sums[5] = sums[3]+y, sums[4]+cb, sums[5]+cr
		mask >>= 1
	}
}

// cellMaskGo is cellMask in Go: the cell is split along its widest channel
// (the first wins a tie) at the middle of its range, the pixels above it
// set, raster order from the top bit.
func cellMaskGo(block *cellBlock) uint32 {
	minY, minCb, minCr := uint32(255), uint32(255), uint32(255)
	maxY, maxCb, maxCr := uint32(0), uint32(0), uint32(0)
	for _, px := range block {
		y, cb, cr := px>>16&0xff, px>>8&0xff, px&0xff
		minY, maxY = min(minY, y), max(maxY, y)
		minCb, maxCb = min(minCb, cb), max(maxCb, cb)
		minCr, maxCr = min(minCr, cr), max(maxCr, cr)
	}
	shift, lo, span := uint(16), minY, maxY-minY
	if maxCb-minCb > span {
		shift, lo, span = 8, minCb, maxCb-minCb
	}
	if maxCr-minCr > span {
		shift, lo, span = 0, minCr, maxCr-minCr
	}
	splitValue := lo + span/2
	var bitsVal uint32
	for _, px := range block {
		var bit uint32
		if px>>shift&0xff > splitValue {
			bit = 1
		}
		bitsVal = bitsVal<<1 | bit
	}
	return bitsVal
}

// nearestGlyph is the glyph closest to a cell's mask: the fewest pixels
// differing, as it is or inverted (32 minus the difference), in that order;
// the first wins a tie and an exact match cannot be beaten. The colours are
// the averages under the glyph's set and clear bits, so an inverted match
// needs nothing more.
func nearestGlyph(bitsVal uint32, flags int) (uint32, int) {
	bestDiff := 32
	bestPattern := uint32(0x0000ffff)
	codePoint := 0x2584
	if flags == 0 {
		patterns, codes := glyphCandidates()
		for i, pattern := range patterns {
			diff := bits.OnesCount32(pattern ^ bitsVal)
			if diff < bestDiff {
				bestDiff, bestPattern, codePoint = diff, pattern, codes[i]
			}
			if 32-diff < bestDiff {
				bestDiff, bestPattern, codePoint = 32-diff, pattern, codes[i]
			}
			if bestDiff == 0 {
				break
			}
		}
	} else {
		for _, bm := range bitmaps {
			if bm.flags&flags != bm.flags {
				continue
			}
			diff := bits.OnesCount32(bm.pattern ^ bitsVal)
			if diff < bestDiff {
				bestDiff, bestPattern, codePoint = diff, bm.pattern, bm.codePoint
			}
			if 32-diff < bestDiff {
				bestDiff, bestPattern, codePoint = 32-diff, bm.pattern, bm.codePoint
			}
			if bestDiff == 0 {
				break
			}
		}
	}
	return bestPattern, codePoint
}

// glyphCache remembers nearestGlyph's answers (flags 0) by mask, a
// direct-mapped table: a camera's cells repeat their masks (flat cells,
// the same edges), and an answer depends on the mask alone.
type glyphCache struct {
	key     [glyphCacheSize]uint64 // 1<<32 | mask; 0: empty
	pattern [glyphCacheSize]uint32
	code    [glyphCacheSize]int32
}

const glyphCacheSize = 1 << 12

func (c *glyphCache) nearest(mask uint32) (uint32, int) {
	h := (mask * 2654435761) >> (32 - 12)
	k := 1<<32 | uint64(mask)
	if c.key[h] == k {
		return c.pattern[h], int(c.code[h])
	}
	p, cp := nearestGlyph(mask, 0)
	c.key[h], c.pattern[h], c.code[h] = k, p, int32(cp)
	return p, cp
}

var (
	glyphLookupOnce  sync.Once
	glyphLookupTable []byte // codepoint -> glyph index + 1 (0: not a glyph)
)

// glyphIndexLookup is glyphIndexMap as a table indexed by codepoint: a map
// lookup per cell showed up in encoder profiles.
func glyphIndexLookup() []byte {
	glyphLookupOnce.Do(func() {
		m := glyphIndexMap()
		maxCP := 0
		for cp := range m {
			maxCP = max(maxCP, cp)
		}
		glyphLookupTable = make([]byte, maxCP+1)
		for cp, idx := range m {
			glyphLookupTable[cp] = idx + 1
		}
	})
	return glyphLookupTable
}

// glyphIndexOf returns the glyph index of a codepoint, or 0 if unknown.
func glyphIndexOf(table []byte, cp int) byte {
	if cp < 0 || cp >= len(table) || table[cp] == 0 {
		return 0
	}
	return table[cp] - 1
}

func glyphIndexMap() map[int]byte {
	initGlyphTables()
	return glyphIndexTable
}

func getChannel(rgb uint32, index int) int {
	// rgb here actually stores Y, Cb, Cr in 3 bytes.
	switch index {
	case 0:
		// Y component in the high byte
		return int((rgb >> 16) & 0xff)
	case 1:
		// Cb component in the middle byte
		return int((rgb >> 8) & 0xff)
	case 2:
		// Cr component in the low byte
		return int(rgb & 0xff)
	default:
		return 0
	}
}

const (
	PayloadMagic                    = "TEXT"
	paletteReuseMaxMismatchRatio    = 0.05
	paletteReuseColorErrorTolerance = 30
)

// Resolution limits for one video frame, in terminal cells. They bound the
// memory a peer can make us allocate and the size of an encoded frame.
// 160×80 cells is a 640×640 px source, far above what a pane in a typical
// terminal needs.
const (
	MaxFrameCols  = 160
	MaxFrameRows  = 80
	MaxFrameCells = MaxFrameCols * MaxFrameRows
)

type paletteColor [3]byte

type Encoder struct {
	glyphs   []byte
	fgIdx    []byte
	bgIdx    []byte
	fgColors []paletteColor
	bgColors []paletteColor
	xMap     []int
	yMap     []int
	xOff     []int    // byte offset of each sampled column in a row
	rowOff   []int    // byte offset of each sampled row
	box      []byte   // the frame averaged down to the cells' pixels (boxScale)
	boxSum   []uint16 // a row of sums of the frame's bytes down its rows
	boxX     []int    // the first source column of each of its pixels (and the end)
	boxY     []int    // the first source row of each of its rows (and the end)
	boxTabs  [3]struct {
		ny, gen int
		tab     []boxTarget
	} // boxTargets for the pixels' heights
	boxGen, boxNext int
	cellMemo        cellMemo

	// match finds the nearest entry of lastPalette and remembers the answers
	// across frames until the palette changes.
	match paletteMatcher

	payloadBuf []byte

	lastPalette         []paletteColor
	lastPaletteExpanded [][3]int
	reuseFgIdx          []byte
	reuseBgIdx          []byte

	// Palette patches (frameTypeDeltaPatch): a colour the palette misses
	// takes a free entry instead of rebuilding the palette, which made every
	// third frame of a large picture a full keyframe. patchedSinceKey: the
	// palette differs from the one the last keyframe carried, so a frame
	// without a palette must not be sent (a receiver that lost a patch
	// would draw it in the old colours). paletteMissRatio: the share of
	// colours a freshly built palette missed (more colours than it holds).
	patchedSinceKey  bool
	paletteMissRatio float64
	paletteUsed      [maxPaletteColors]uint32 // frame number of each entry's last use
	paletteFrame     uint32
	paletteMissed    []int32
	palettePatch     []byte

	skipEmpty bool // SetSkipEmpty

	// The range coder and the frames' models (frames.go).
	rcEnc     rcEncoder
	rcFrame   rcFrame
	rc        rcChange
	rcChanged []bool
	rcPrevG   []byte // the cells sent before this frame
	rcPrevFg  []byte
	rcPrevBg  []byte
	emptyFg   []paletteColor // the foregrounds, the empty glyph's its background

	// Cell hold (see holdShapes): holdBits is the per-pixel threshold.
	holdBits   int
	holdDebug  bool // tint held cells lilac (-video-hold-debug)
	held       []bool
	holdAge    []uint16 // frames each cell has been held for
	prevGlyphs []byte
	prevFg     []paletteColor
	prevBg     []paletteColor
	prevCells  int

	// Last frame sent, as palette indices, for change-only frames.
	sentGlyphs []byte
	sentFg     []byte
	sentBg     []byte
	sentCells  int

	// Scratch for building a payload, reused across frames.
	changeMask []byte

	framesSinceLastKey int
	keyframeInterval   int
	forceKey           bool // ForceKeyframe: the next frame is a keyframe
	forceBW            bool
	maxFPS             int
	mirror             bool

	// codeIdx holds a colour code's index in the palette (codeIndices),
	// codeGen<<8 | index; an entry of another generation is empty, so a new
	// palette clears the table by advancing codeGen.
	codeIdx []uint32
	codeGen uint32

	glyphCache *[2]glyphCache // per worker of buildCellData, kept by Restart

	refs encoderRefs // frames with numbers and references (refs.go)
}

var defaultEncoder = NewEncoder(false, defaultKeyframeInterval, 0, false)

func (e *Encoder) shouldForcePeriodicKeyframe() bool {
	if e == nil {
		return false
	}
	if e.forceKey {
		return true
	}
	if e.keyframeInterval <= 0 {
		return false
	}
	return e.framesSinceLastKey >= e.keyframeInterval
}

// ForceKeyframe makes the next frame a keyframe (the peer lost a frame and
// asked for one).
func (e *Encoder) ForceKeyframe() {
	e.forceKey = true
}

func (e *Encoder) updateKeyframeCounter(keyframe byte) {
	if e == nil {
		return
	}
	if keyframe == 1 {
		e.forceKey = false
	}
	if e.keyframeInterval <= 0 {
		return
	}
	if keyframe == 1 {
		e.framesSinceLastKey = 0
		return
	}
	e.framesSinceLastKey++
}

type Decoder struct {
	mu          sync.Mutex
	lastPalette []paletteColor

	// Last decoded frame as palette indices: the base of change-only frames.
	baseValid  bool
	baseCols   int
	baseRows   int
	baseGlyphs []byte
	baseFg     []byte
	baseBg     []byte

	// The range decoder and the frames' models follow the encoder's.
	rcDec     rcDecoder
	rcFrame   rcFrame
	rc        rcChange
	rcChanged []bool

	refs decoderRefs // frames with numbers and references (refs.go)
}

// Frame types (the byte after the magic); after the header (cols, rows) a
// frame is one range-coded stream (frames.go).
const (
	frameTypeFull       byte = 0 // all cells, on the palette the receiver holds
	frameTypeKey        byte = 1 // a palette and all cells
	frameTypeDelta      byte = 2 // the cells that changed since the previous frame
	frameTypeDeltaPatch byte = 3 // palette entries replaced, then as frameTypeDelta

	deltaMaxChangedNum = 6 // a change frame is sent while at most 6/10…
	deltaMaxChangedDen = 10
)

// ErrNoBase reports a change-only frame whose previous frame is missing
// (lost or not yet received); frames are shown again from the next keyframe.
var ErrNoBase = errors.New("change frame without its previous frame")

// applyPatchLocked applies a palette patch (index, colour code lo, hi) in
// order, on the palette of the frame before: a frame lost before this one
// invalidated the base. It reports false for an index past the palette.
func (d *Decoder) applyPatchLocked(patch []byte) bool {
	for k := 0; k+3 <= len(patch); k += 3 {
		slot := int(patch[k])
		v := uint16(patch[k+1]) | uint16(patch[k+2])<<8
		c := paletteColor{byte((v >> 10) & 0x3f), byte((v >> 5) & 0x1f), byte(v & 0x1f)}
		e := expandPaletteEntry(c)
		entry := paletteColor{byte(e[0]), byte(e[1]), byte(e[2])}
		if slot > len(d.lastPalette) {
			return false
		}
		if slot == len(d.lastPalette) {
			d.lastPalette = append(d.lastPalette, entry)
		} else {
			d.lastPalette[slot] = entry
		}
	}
	return true
}

// ResetBase forgets the previous frame, e.g. after a lost packet, so change
// frames are refused until the next full frame instead of drawing garbage.
func (d *Decoder) ResetBase() {
	d.mu.Lock()
	d.resetBaseLocked()
	d.mu.Unlock()
}

func (d *Decoder) resetBaseLocked() {
	d.baseValid = false
	d.rc.valid = false
}

var defaultDecoder Decoder

func NewDecoder() *Decoder {
	return &Decoder{}
}

func NewEncoder(forceBW bool, keyframeInterval int, maxFPS int, mirror bool) *Encoder {
	if keyframeInterval < 0 {
		keyframeInterval = 0
	}
	if maxFPS < 0 {
		maxFPS = 0
	}
	return &Encoder{
		keyframeInterval: keyframeInterval,
		forceBW:          forceBW,
		maxFPS:           maxFPS,
		mirror:           mirror,
	}
}

func Encode(frame Frame, cols, rows int) ([]byte, error) {
	return defaultEncoder.Encode(frame, cols, rows)
}

// Encode encodes a frame for the network (nil: nothing changed, with
// SetSkipEmpty).
func (e *Encoder) Encode(frame Frame, cols, rows int) ([]byte, error) {
	glyphs, fgColors, bgColors, err := e.cells(frame, cols, rows)
	if err != nil {
		return nil, err
	}
	return e.encodeCells(glyphs, fgColors, bgColors, cols, rows)
}

// EncodeWithSelfView encodes frame for the network, as Encode does, and makes
// the self view from the same cells instead of computing them a second time:
// mirrored left to right, as an Encoder made with mirror would. Both views have
// the size cols x rows. The self view keeps this encoder's cell hold; self only
// lends its buffers.
func (e *Encoder) EncodeWithSelfView(frame Frame, cols, rows int, self *Encoder) (net []byte, selfView *TextFrame, err error) {
	glyphs, fgColors, bgColors, err := e.cells(frame, cols, rows)
	if err != nil {
		return nil, nil, err
	}
	net, err = e.encodeCells(glyphs, fgColors, bgColors, cols, rows)
	if err != nil {
		return nil, nil, err
	}
	mg, mfg, mbg := self.mirrorCells(glyphs, fgColors, bgColors, cols, rows)
	return net, newTextFrame(mg, mfg, mbg, cols, rows), nil
}

// SelfView draws a frame for this process only (the self view): the cells in
// their own colours, with no palette, payload or compression in between.
func (e *Encoder) SelfView(frame Frame, cols, rows int) (*TextFrame, error) {
	glyphs, fgColors, bgColors, err := e.cells(frame, cols, rows)
	if err != nil {
		return nil, err
	}
	return newTextFrame(glyphs, fgColors, bgColors, cols, rows), nil
}

// newTextFrame copies cells into a new TextFrame, which the caller may keep.
func newTextFrame(glyphs []byte, fgColors, bgColors []paletteColor, cols, rows int) *TextFrame {
	total := cols * rows
	buf := make([]byte, total*7) // one allocation: glyphs, then fg, then bg
	tf := &TextFrame{
		Cols:    cols,
		Rows:    rows,
		Width:   cols << 2,
		Height:  rows << 3,
		Glyphs:  buf[:total:total],
		FgYCbCr: buf[total : total*4 : total*4],
		BgYCbCr: buf[total*4:],
	}
	copy(tf.Glyphs, glyphs[:total])
	for i := 0; i < total; i++ {
		copy(tf.FgYCbCr[i*3:i*3+3], fgColors[i][:])
		copy(tf.BgYCbCr[i*3:i*3+3], bgColors[i][:])
	}
	return tf
}

// cells turns a frame into glyphs and fg/bg colours, after the cell hold.
// The slices are the encoder's own buffers, valid until its next frame.
func (e *Encoder) cells(frame Frame, cols, rows int) ([]byte, []paletteColor, []paletteColor, error) {
	if len(frame.Data) == 0 || frame.Width <= 0 || frame.Height <= 0 {
		return nil, nil, nil, fmt.Errorf("invalid frame data")
	}
	if len(frame.Data) != frame.Width*frame.Height*3 {
		return nil, nil, nil, fmt.Errorf("frame data size mismatch: got %d, want %d", len(frame.Data), frame.Width*frame.Height*3)
	}
	if cols <= 0 || rows <= 0 {
		return nil, nil, nil, fmt.Errorf("cols and rows must be > 0")
	}

	if cols > MaxFrameCols || rows > MaxFrameRows {
		return nil, nil, nil, fmt.Errorf("cols/rows too large: cols=%d (max %d), rows=%d (max %d)", cols, MaxFrameCols, rows, MaxFrameRows)
	}

	glyphs, fgColors, bgColors, frameW, frameH, err := e.buildCellData(frame, cols, rows, e.forceBW)
	if err != nil {
		return nil, nil, nil, err
	}
	e.holdShapes(glyphs, fgColors, bgColors)

	if frameW > int(math.MaxUint16) || frameH > int(math.MaxUint16) {
		return nil, nil, nil, fmt.Errorf("frame dimensions too large: %dx%d (max %dx%d)", frameW, frameH, math.MaxUint16, math.MaxUint16)
	}
	return glyphs, fgColors, bgColors, nil
}

// mirrorCells writes the left-right mirror image of a cell grid into e's
// buffers: the columns in reverse order, each glyph replaced by its mirror
// (see glyphMirrors).
func (e *Encoder) mirrorCells(glyphs []byte, fgColors, bgColors []paletteColor, cols, rows int) ([]byte, []paletteColor, []paletteColor) {
	total := cols * rows
	mg := ensureByteSlice(&e.glyphs, total)
	if cap(e.fgColors) < total {
		e.fgColors = make([]paletteColor, total)
	}
	if cap(e.bgColors) < total {
		e.bgColors = make([]paletteColor, total)
	}
	mfg, mbg := e.fgColors[:total], e.bgColors[:total]
	mirror, swap := glyphMirrors()
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			src, dst := r*cols+c, r*cols+cols-1-c
			g, fg, bg := glyphs[src], fgColors[src], bgColors[src]
			if int(g) < len(mirror) {
				if swap[g] {
					fg, bg = bg, fg
				}
				g = mirror[g]
			}
			mg[dst], mfg[dst], mbg[dst] = g, fg, bg
		}
	}
	return mg, mfg, mbg
}

var (
	glyphMirrorOnce sync.Once
	glyphMirror     []byte // glyph index of each glyph's left-right mirror image
	glyphMirrorSwap []bool // the mirror image is that glyph with fg and bg swapped
)

// mirrorPattern flips a 4x8 pattern left to right.
func mirrorPattern(p uint32) uint32 {
	var m uint32
	for y := 0; y < 8; y++ {
		shift := uint(28 - 4*y)
		row := p >> shift & 0xf
		m |= (row&1<<3 | row&2<<1 | row&4>>1 | row&8>>3) << shift
	}
	return m
}

// glyphMirrors maps each glyph to the one that draws its mirror image: the
// same shape, or the inverse shape with the colours swapped (the left half
// block mirrored is the right half, which is the left half inverted). A
// mirror image no glyph draws exactly gets the nearest one.
func glyphMirrors() ([]byte, []bool) {
	glyphMirrorOnce.Do(func() {
		initGlyphTables()
		glyphMirror = make([]byte, len(glyphPatterns))
		glyphMirrorSwap = make([]bool, len(glyphPatterns))
		for i, p := range glyphPatterns {
			m := mirrorPattern(p)
			best, bestDiff, swap := i, 33, false
			for j, q := range glyphPatterns {
				if d := bits.OnesCount32(q ^ m); d < bestDiff {
					best, bestDiff, swap = j, d, false
				}
				if d := 32 - bits.OnesCount32(q^m); d < bestDiff {
					best, bestDiff, swap = j, d, true
				}
			}
			glyphMirror[i], glyphMirrorSwap[i] = byte(best), swap
		}
	})
	return glyphMirror, glyphMirrorSwap
}

// encodeCells codes cells (glyph indices and fg/bg colours) into a frame:
// a key frame (palette and cells), a full frame (cells on the palette the
// receiver holds) or a change frame (the cells that changed, first the
// palette entries replaced).
func (e *Encoder) encodeCells(glyphs []byte, fgColors, bgColors []paletteColor, cols, rows int) ([]byte, error) {
	totalCells := cols * rows

	// The empty glyph shows no foreground: none in the palette, no patch
	// and no change for it; it takes its background's index (frames.go).
	e.emptyFg = append(e.emptyFg[:0], fgColors[:totalCells]...)
	for i, g := range glyphs[:totalCells] {
		if g == 0 {
			e.emptyFg[i] = bgColors[i]
		}
	}
	fgColors = e.emptyFg

	// The peer lost a frame: code against the newest one it holds, or a
	// keyframe when that one is gone (refs.go).
	if e.refs.on {
		if id := e.refs.recover.Swap(-1); id >= 0 {
			if !e.rewind(uint16(id), cols, rows) {
				e.forceKey = true
			}
		} else if id := e.refs.acked.Swap(-1); id >= 0 {
			e.rewind(uint16(id), cols, rows) // gone: on from the frame before
		}
	}

	keyframe := byte(1)
	var finalFgIdx []byte
	var finalBgIdx []byte

	if len(e.lastPalette) > 0 && len(e.lastPalette) == len(e.lastPaletteExpanded) && totalCells > 0 {
		reuseFg := ensureByteSlice(&e.reuseFgIdx, totalCells)
		reuseBg := ensureByteSlice(&e.reuseBgIdx, totalCells)
		e.match.reserve(2 * totalCells)
		mismatches := e.patchPalette(fgColors, bgColors, reuseFg, reuseBg)
		// What is still missed found no free entry: a fresh palette would
		// miss as much, so rebuilding it only costs a keyframe.
		limit := max(paletteReuseMaxMismatchRatio, e.paletteMissRatio)
		if float64(mismatches)/float64(2*totalCells) <= limit && len(e.palettePatch) <= 3*maxPaletteColors {
			keyframe = 0
			finalFgIdx = reuseFg
			finalBgIdx = reuseBg
		}
	}
	patch := e.palettePatch
	if keyframe != 0 {
		patch = nil // a keyframe carries the whole palette
	}
	if len(patch) > 0 {
		e.patchedSinceKey = true
	}

	if keyframe == 0 && (e.shouldForcePeriodicKeyframe() || e.patchedSinceKey && !e.deltaFits(glyphs, finalFgIdx, finalBgIdx, totalCells)) {
		// A periodic keyframe only lets a receiver that lost the last one
		// recover. The current palette still fits, so resend it with the
		// reused indices: rebuilding it from this frame's slightly noisy
		// colours made a static picture shift colours at every keyframe.
		keyframe = 1
	} else if keyframe == 1 {
		palette := buildPaletteFromColors(fgColors, bgColors, totalCells)
		e.storePalette(palette) // also resets e.match to the new palette

		fgIdx := ensureByteSlice(&e.fgIdx, totalCells)
		bgIdx := ensureByteSlice(&e.bgIdx, totalCells)

		e.match.reserve(2 * totalCells)
		misses := e.codeIndices(fgColors, fgIdx, paletteReuseColorErrorTolerance)
		misses += e.codeIndices(bgColors, bgIdx, paletteReuseColorErrorTolerance)
		e.paletteMissRatio = 0
		if totalCells > 0 {
			e.paletteMissRatio = float64(misses) / float64(2*totalCells)
		}

		finalFgIdx = fgIdx
		finalBgIdx = bgIdx
	}
	if len(e.lastPalette) == 0 || finalFgIdx == nil {
		return nil, fmt.Errorf("no palette for %d cells", totalCells)
	}
	for i, g := range glyphs[:totalCells] {
		if g == 0 {
			finalFgIdx[i] = finalBgIdx[i]
		}
	}

	// Change-only frame: with the palette unchanged, send just the cells that
	// differ from the previous frame. Keyframes stay full so a receiver that
	// lost a frame recovers at the next one.
	frameType := frameTypeFull
	if keyframe == 1 {
		frameType = frameTypeKey
	}
	var changeMask []byte
	if keyframe == 0 && e.sentCells == totalCells {
		changeMask = ensureByteSlice(&e.changeMask, (totalCells+7)/8)
		clear(changeMask)
		changed := 0
		for i := 0; i < totalCells; i++ {
			if glyphs[i] != e.sentGlyphs[i] || finalFgIdx[i] != e.sentFg[i] || finalBgIdx[i] != e.sentBg[i] {
				changeMask[i/8] |= 1 << (i % 8)
				changed++
			}
		}
		if changed*deltaMaxChangedDen <= totalCells*deltaMaxChangedNum {
			frameType = frameTypeDelta
			if len(patch) > 0 {
				frameType = frameTypeDeltaPatch
			}
		}
		if e.skipEmpty && frameType == frameTypeDelta && changed == 0 && e.refs.showsSame(glyphs[:totalCells], finalFgIdx, finalBgIdx) {
			// Nothing changed: nothing to send. Returned before any state the
			// receiver follows (the range coder's models above all) moves.
			return nil, nil
		}
	}
	if keyframe == 1 {
		e.patchedSinceKey = false
	}
	e.rcPrevG = append(e.rcPrevG[:0], e.sentGlyphs...)
	e.rcPrevFg = append(e.rcPrevFg[:0], e.sentFg...)
	e.rcPrevBg = append(e.rcPrevBg[:0], e.sentBg...)
	e.sentGlyphs = append(e.sentGlyphs[:0], glyphs[:totalCells]...)
	e.sentFg = append(e.sentFg[:0], finalFgIdx...)
	e.sentBg = append(e.sentBg[:0], finalBgIdx...)
	e.sentCells = totalCells

	id, ref := e.refs.next, e.refs.last
	var out []byte
	if gap := id - ref; frameType == frameTypeKey || !e.refs.on || gap >= 1 && gap <= 255 {
		out = appendCompactHeader(e.payloadBuf[:0], frameType, cols, rows, e.refs.on, id, ref)
	} else {
		out = append(e.payloadBuf[:0], PayloadMagic...)
		if e.refs.on {
			out = append(out, frameType+frameTypeRefs)
		} else {
			out = append(out, frameType)
		}
		out = binary.LittleEndian.AppendUint16(out, uint16(cols))
		out = binary.LittleEndian.AppendUint16(out, uint16(rows))
		if e.refs.on {
			out = binary.LittleEndian.AppendUint16(out, id)
			if frameType != frameTypeKey {
				out = binary.LittleEndian.AppendUint16(out, ref)
			}
		}
	}
	e.rcEnc.reset(e.rcEnc.out)
	c := rcCoder{e: &e.rcEnc}
	switch frameType {
	case frameTypeDelta, frameTypeDeltaPatch:
		if !e.rc.valid {
			e.rc.restart(totalCells)
		}
		if frameType == frameTypeDeltaPatch {
			e.rc.patch(c, patch)
		}
		if cap(e.rcChanged) < totalCells {
			e.rcChanged = make([]bool, totalCells)
		}
		e.rc.cells(c, glyphs[:totalCells], finalFgIdx, finalBgIdx, e.rcPrevG, e.rcPrevFg, e.rcPrevBg, changeMask, cols, e.rcChanged[:totalCells])
	default:
		e.rc.valid = false // a key or full frame restarts the change frames' model
		e.rcFrame.restart()
		if frameType == frameTypeKey {
			e.rcFrame.palette(c, paletteCodes(e.lastPalette))
		}
		e.rcFrame.encodeCells(&e.rcEnc, glyphs[:totalCells], finalFgIdx, finalBgIdx, cols)
	}
	out = append(out, e.rcEnc.finish()...)
	e.payloadBuf = out
	e.updateKeyframeCounter(keyframe)
	if e.refs.on {
		e.refs.frames.slot(id).save(id, cols, rows, e.sentGlyphs, e.sentFg, e.sentBg, e.lastPalette, &e.rc, e.patchedSinceKey)
		e.refs.last, e.refs.sent, e.refs.next, e.refs.any = id, id, id+1, true
	}
	return bytes.Clone(out), nil
}

// patchPalette assigns palette indices to the frame's colours. A colour
// farther than paletteReuseColorErrorTolerance from every entry gets one of
// its own: an entry no cell of this frame uses (so no cell on the receiver's
// screen does either), least recently used first, or a new one while the
// palette has room. The entries changed are recorded in e.palettePatch as
// (index, colour code lo, hi). It returns how many colours are still missed.
func (e *Encoder) patchPalette(fgColors, bgColors []paletteColor, fgIdx, bgIdx []byte) int {
	const tol = paletteReuseColorErrorTolerance
	e.paletteFrame++
	frame := e.paletteFrame
	var used [maxPaletteColors]bool
	missed := e.paletteMissed[:0]
	n := len(fgColors)
	assign := func(colors []paletteColor, dst []byte, base int) {
		for i, c := range colors {
			idx, d := e.match.index(c)
			if d > tol {
				idx, d = e.match.refresh(c) // the remembered entry may have changed
			}
			dst[i] = idx
			if d > tol {
				missed = append(missed, int32(base+i))
				continue
			}
			used[idx] = true
		}
	}
	assign(fgColors, fgIdx, 0)
	assign(bgColors, bgIdx, n)
	for i, u := range used[:len(e.lastPalette)] {
		if u {
			e.paletteUsed[i] = frame
		}
	}
	e.paletteMissed = missed
	e.palettePatch = e.palettePatch[:0]
	misses := 0
	for _, pos := range missed {
		c, dst, i := fgColors, fgIdx, int(pos)
		if i >= n {
			c, dst, i = bgColors, bgIdx, i-n
		}
		// An entry added for an earlier miss of this frame may fit.
		idx, d := e.match.refresh(c[i])
		if d > tol {
			slot, ok := e.freePaletteEntry(&used)
			if !ok {
				dst[i] = idx
				used[idx] = true
				misses++
				continue
			}
			code := paletteColor{c[i][0] >> 2, quantChroma5(c[i][1]), quantChroma5(c[i][2])}
			if int(slot) == len(e.lastPalette) {
				e.lastPalette = append(e.lastPalette, code)
				e.lastPaletteExpanded = append(e.lastPaletteExpanded, expandPaletteEntry(code))
			} else {
				e.lastPalette[slot] = code
				e.lastPaletteExpanded[slot] = expandPaletteEntry(code)
			}
			e.match.set(e.lastPaletteExpanded, slot)
			v := uint16(code[0])<<10 | uint16(code[1])<<5 | uint16(code[2])
			e.palettePatch = append(e.palettePatch, slot, byte(v), byte(v>>8))
			idx, _ = e.match.refresh(c[i])
		}
		dst[i] = idx
		used[idx] = true
		e.paletteUsed[idx] = frame
	}
	return misses
}

// freePaletteEntry picks an entry for a new colour: a new one while the
// palette has room, else the least recently used one no colour of this
// frame uses.
func (e *Encoder) freePaletteEntry(used *[maxPaletteColors]bool) (byte, bool) {
	if len(e.lastPalette) < maxPaletteColors {
		return byte(len(e.lastPalette)), true
	}
	best, found := 0, false
	for i := range e.lastPalette {
		if used[i] {
			continue
		}
		if !found || e.paletteUsed[i] < e.paletteUsed[best] {
			best, found = i, true
		}
	}
	return byte(best), found
}

// deltaFits reports whether the frame will go out as a change frame (see
// encodeCells): only then may it follow palette patches without the palette.
func (e *Encoder) deltaFits(glyphs, fg, bg []byte, totalCells int) bool {
	if e.sentCells != totalCells {
		return false
	}
	changed := 0
	for i := 0; i < totalCells; i++ {
		if glyphs[i] != e.sentGlyphs[i] || fg[i] != e.sentFg[i] || bg[i] != e.sentBg[i] {
			changed++
		}
	}
	return changed*deltaMaxChangedDen <= totalCells*deltaMaxChangedNum
}

func (e *Encoder) storePalette(palette []paletteColor) {
	if len(palette) == 0 {
		e.lastPalette = e.lastPalette[:0]
		e.lastPaletteExpanded = e.lastPaletteExpanded[:0]
		e.match.reset(nil)
		return
	}

	if cap(e.lastPalette) < len(palette) {
		e.lastPalette = make([]paletteColor, len(palette))
	}
	e.lastPalette = e.lastPalette[:len(palette)]
	copy(e.lastPalette, palette)

	if cap(e.lastPaletteExpanded) < len(palette) {
		e.lastPaletteExpanded = make([][3]int, len(palette))
	}
	e.lastPaletteExpanded = e.lastPaletteExpanded[:len(palette)]
	for i, c := range e.lastPalette {
		e.lastPaletteExpanded[i] = expandPaletteEntry(c)
	}
	e.match.reset(e.lastPaletteExpanded)
	e.codeGen++ // codeIdx forgets the palette before
}

func Decode(data []byte) (*TextFrame, error) {
	return defaultDecoder.Decode(data)
}

// Decode decodes a frame of Encoder.Encode. A change frame without its
// previous frame returns ErrNoBase.
func (d *Decoder) Decode(data []byte) (*TextFrame, error) {
	initGlyphTables() // the glyphs' range is checked below, a decoder may come first
	if len(data) >= 1 && data[0]&^compactHeaderBits == compactHeaderTag {
		return d.decodeCompact(data)
	}
	if len(data) < len(PayloadMagic)+1+2+2 {
		return nil, fmt.Errorf("payload too short")
	}
	if string(data[:len(PayloadMagic)]) != PayloadMagic {
		return nil, fmt.Errorf("unexpected magic %q", string(data[:len(PayloadMagic)]))
	}
	offset := len(PayloadMagic)
	frameType := data[offset]
	cols := int(binary.LittleEndian.Uint16(data[offset+1:]))
	rows := int(binary.LittleEndian.Uint16(data[offset+3:]))
	offset += 5
	if cols == 0 || rows == 0 || cols > MaxFrameCols || rows > MaxFrameRows {
		return nil, fmt.Errorf("frame size %dx%d outside 1..%dx%d", cols, rows, MaxFrameCols, MaxFrameRows)
	}
	if frameType > frameTypeDeltaPatch+frameTypeRefs {
		return nil, fmt.Errorf("unknown frame type %d", frameType)
	}
	// A frame with a number and, but for a keyframe, a reference (refs.go).
	withRefs := frameType >= frameTypeRefs
	var id, ref uint16
	if withRefs {
		frameType -= frameTypeRefs
		n := 4
		if frameType == frameTypeKey {
			n = 2
		}
		if len(data) < offset+n {
			return nil, fmt.Errorf("payload too short")
		}
		id, ref = binary.LittleEndian.Uint16(data[offset:]), binary.LittleEndian.Uint16(data[offset+n-2:])
		offset += n
	}
	if withRefs {
		frameType += frameTypeRefs
	}
	return d.decodeBody(data[offset:], frameType, cols, rows, id, ref)
}

// decodeBody decodes a frame's range-coded body: frameType (frameTypeRefs
// added when the frame has a number id and, but for a keyframe, its
// reference ref), cols x rows cells.
func (d *Decoder) decodeBody(body []byte, frameType byte, cols, rows int, id, ref uint16) (*TextFrame, error) {
	withRefs := frameType >= frameTypeRefs
	if withRefs {
		frameType -= frameTypeRefs
	}
	totalCells := cols * rows
	c := rcCoder{d: &d.rcDec}

	d.mu.Lock()
	defer d.mu.Unlock()
	if withRefs && frameType != frameTypeKey && !d.rewindLocked(ref, cols, rows) {
		return nil, ErrNoBase
	}
	var glyphs, fgIdx, bgIdx []byte
	d.rcDec.reset(body)
	switch frameType {
	case frameTypeDelta, frameTypeDeltaPatch:
		if !d.baseValid || d.baseCols != cols || d.baseRows != rows || len(d.lastPalette) == 0 {
			return nil, ErrNoBase
		}
		glyphs = append([]byte(nil), d.baseGlyphs...)
		fgIdx = append([]byte(nil), d.baseFg...)
		bgIdx = append([]byte(nil), d.baseBg...)
		if !d.rc.valid {
			d.rc.restart(totalCells)
		}
		if frameType == frameTypeDeltaPatch && !d.applyPatchLocked(d.rc.patch(c, nil)) {
			d.resetBaseLocked()
			return nil, ErrNoBase
		}
		if cap(d.rcChanged) < totalCells {
			d.rcChanged = make([]bool, totalCells)
		}
		d.rc.cells(c, glyphs, fgIdx, bgIdx, nil, nil, nil, nil, cols, d.rcChanged[:totalCells])
	default:
		buf := make([]byte, totalCells*3)
		glyphs, fgIdx, bgIdx = buf[:totalCells:totalCells], buf[totalCells:2*totalCells:2*totalCells], buf[2*totalCells:]
		d.rcFrame.restart()
		if frameType == frameTypeKey {
			codes := d.rcFrame.palette(c, nil)
			palette := make([]paletteColor, len(codes))
			for i, v := range codes {
				palette[i] = expandCode(v)
			}
			d.lastPalette = palette
		} else if len(d.lastPalette) == 0 {
			return nil, fmt.Errorf("full frame received before any keyframe palette")
		}
		d.rcFrame.decodeCells(&d.rcDec, glyphs, fgIdx, bgIdx, cols)
		d.rc.valid = false // the encoder restarts its change model here too
	}

	palette := d.lastPalette
	colours := make([]byte, totalCells*6) // one allocation for both planes
	fgYCbCr := colours[: totalCells*3 : totalCells*3]
	bgYCbCr := colours[totalCells*3:]
	for i := 0; i < totalCells; i++ {
		if int(glyphs[i]) >= len(glyphPatterns) || int(fgIdx[i]) >= len(palette) || int(bgIdx[i]) >= len(palette) {
			d.resetBaseLocked()
			return nil, fmt.Errorf("cell %d: glyph %d, colours %d, %d out of range (palette size %d)", i, glyphs[i], fgIdx[i], bgIdx[i], len(palette))
		}
		copy(fgYCbCr[i*3:], palette[fgIdx[i]][:])
		copy(bgYCbCr[i*3:], palette[bgIdx[i]][:])
	}

	// This frame is the base for the next change-only frame.
	d.baseGlyphs = append(d.baseGlyphs[:0], glyphs...)
	d.baseFg = append(d.baseFg[:0], fgIdx...)
	d.baseBg = append(d.baseBg[:0], bgIdx...)
	d.baseCols, d.baseRows, d.baseValid = cols, rows, true
	if withRefs {
		d.refs.frames.slot(id).save(id, cols, rows, glyphs, fgIdx, bgIdx, d.lastPalette, &d.rc, false)
		d.refs.last, d.refs.any = id, true
	}

	return &TextFrame{
		Cols:    cols,
		Rows:    rows,
		Width:   cols << 2,
		Height:  rows << 3,
		Glyphs:  glyphs,
		FgYCbCr: fgYCbCr,
		BgYCbCr: bgYCbCr,
	}, nil
}

// Palette chroma has 5 bits. Values are rounded to the nearest of 32 levels
// eight apart and come back as level*8, so neutral 128 stays 128 exactly. It
// was truncated and expanded as (q<<3)|(q>>2), which turned 128 into 132 and
// 127 into 123: grey drawn lilac, and a noisy grey flickering between lilac
// and green. A peer still on that expansion sees a level up to 4 higher.
func quantChroma5(c byte) byte { return byte(min(31, (int(c)+4)>>3)) }

func expandChroma5(q byte) byte { return q << 3 }

func (e *Encoder) buildCellData(frame Frame, cols, rows int, bw bool) ([]byte, []paletteColor, []paletteColor, int, int, error) {
	if cols <= 0 || rows <= 0 {
		return nil, nil, nil, 0, 0, fmt.Errorf("cols and rows must be > 0")
	}
	w := cols << 2
	h := rows << 3

	srcW := frame.Width
	srcH := frame.Height
	if srcW <= 0 || srcH <= 0 {
		return nil, nil, nil, 0, 0, fmt.Errorf("source image has invalid size %dx%d", srcW, srcH)
	}

	// Compute cover scaling from source (srcW x srcH) to target (w x h),
	// preserving aspect ratio and cropping as in scaleImageCover.
	scaleX := float64(w) / float64(srcW)
	scaleY := float64(h) / float64(srcH)
	scale := math.Max(scaleX, scaleY)
	if scale <= 0 {
		scale = 1
	}

	scaledW := int(float64(srcW) * scale)
	scaledH := int(float64(srcH) * scale)
	if scaledW <= 0 {
		scaledW = 1
	}
	if scaledH <= 0 {
		scaledH = 1
	}

	xOffset := maxInt(0, (scaledW-w)/2)
	yOffset := maxInt(0, (scaledH-h)/2)

	if cap(e.xMap) < w {
		e.xMap = make([]int, w)
	}
	if cap(e.yMap) < h {
		e.yMap = make([]int, h)
	}
	xMap := e.xMap[:w]
	yMap := e.yMap[:h]

	for x := 0; x < w; x++ {
		sxScaled := x + xOffset
		sx := sxScaled * srcW / scaledW
		if sx >= srcW {
			sx = srcW - 1
		}
		if e.mirror {
			sx = srcW - 1 - sx // the self view is shown mirrored
		}
		xMap[x] = sx
	}
	for y := 0; y < h; y++ {
		syScaled := y + yOffset
		sy := syScaled * srcH / scaledH
		if sy >= srcH {
			sy = srcH - 1
		}
		yMap[y] = sy
	}

	// Byte offsets of the sampled rows and columns: every one lies inside
	// the frame (xMap < srcW, yMap < srcH), checked once here instead of
	// per pixel.
	if len(frame.Data) < srcW*srcH*3 {
		return nil, nil, nil, 0, 0, fmt.Errorf("frame data size %d, want %d", len(frame.Data), srcW*srcH*3)
	}
	if cap(e.xOff) < w {
		e.xOff = make([]int, w)
	}
	if cap(e.rowOff) < h {
		e.rowOff = make([]int, h)
	}
	xOff, rowOff := e.xOff[:w], e.rowOff[:h]
	for x, sx := range xMap {
		xOff[x] = sx * 3
	}
	for y, sy := range yMap {
		rowOff[y] = sy * srcW * 3
	}
	data := frame.Data
	if BoxScale && scale < 1 && (srcW/w+2)*(srcH/h+2) <= 266 {
		data = e.boxScale(frame.Data, srcW, w, h, scaledW, scaledH, srcH, xOffset, yOffset)
		for x := range xOff {
			sx := x
			if e.mirror {
				sx = w - 1 - x
			}
			xOff[x] = sx * 3
		}
		for y := range rowOff {
			rowOff[y] = y * w * 3
		}
	}

	totalCells := cols * rows
	glyphLookup := glyphIndexLookup()

	if cap(e.glyphs) < totalCells {
		e.glyphs = make([]byte, totalCells)
	}
	if cap(e.fgIdx) < totalCells {
		e.fgIdx = make([]byte, totalCells)
	}
	if cap(e.bgIdx) < totalCells {
		e.bgIdx = make([]byte, totalCells)
	}
	if cap(e.fgColors) < totalCells {
		e.fgColors = make([]paletteColor, totalCells)
	}
	if cap(e.bgColors) < totalCells {
		e.bgColors = make([]paletteColor, totalCells)
	}

	glyphs := e.glyphs[:totalCells]
	fgColors := e.fgColors[:totalCells]
	bgColors := e.bgColors[:totalCells]

	memo := &e.cellMemo
	memo.resize(totalCells)
	if e.glyphCache == nil {
		e.glyphCache = new([2]glyphCache)
	}

	// Parallel pass: compute glyphs and per-cell fg/bg colors. Two workers
	// halve the time of a frame for the same CPU; more only add CPU (+15 %
	// with 4 or 8, measured on camera frames) for little gain, since most
	// cells come from cellMemo.
	workers := min(runtime.GOMAXPROCS(0), 2)
	if workers > rows {
		workers = rows
	}
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for wIdx := 0; wIdx < workers; wIdx++ {
		startRow := wIdx
		go func(start int) {
			defer wg.Done()
			gc := &e.glyphCache[start]
			var block cellBlock
			for by := start; by < rows; by += workers {
				for bx := 0; bx < cols; bx++ {
					cell := by*cols + bx
					// Read the cell's pixels straight from the frame (what
					// getPixel does, without a call per pixel). Each pixel is
					// a three-byte subslice: one bounds check instead of
					// three, which halved this loop.
					x0, x1, x2, x3 := xOff[bx*4], xOff[bx*4+1], xOff[bx*4+2], xOff[bx*4+3]
					for y := 0; y < 8; y++ {
						r := rowOff[by*8+y]
						b := block[y*4 : y*4+4 : y*4+4]
						p0 := data[r+x0 : r+x0+3 : r+x0+3]
						p1 := data[r+x1 : r+x1+3 : r+x1+3]
						p2 := data[r+x2 : r+x2+3 : r+x2+3]
						p3 := data[r+x3 : r+x3+3 : r+x3+3]
						if bw {
							b[0] = uint32(p0[0])<<16 | 128<<8 | 128
							b[1] = uint32(p1[0])<<16 | 128<<8 | 128
							b[2] = uint32(p2[0])<<16 | 128<<8 | 128
							b[3] = uint32(p3[0])<<16 | 128<<8 | 128
						} else {
							b[0] = uint32(p0[0])<<16 | uint32(p0[1])<<8 | uint32(p0[2])
							b[1] = uint32(p1[0])<<16 | uint32(p1[1])<<8 | uint32(p1[2])
							b[2] = uint32(p2[0])<<16 | uint32(p2[1])<<8 | uint32(p2[2])
							b[3] = uint32(p3[0])<<16 | uint32(p3[1])<<8 | uint32(p3[2])
						}
					}
					// A cell whose pixels did not change since the last frame
					// (three in four on a camera) gets the same result again.
					if memo.valid[cell] && memo.blocks[cell] == block {
						glyphs[cell], fgColors[cell], bgColors[cell] = memo.glyph[cell], memo.fg[cell], memo.bg[cell]
						continue
					}
					pattern, codePoint := gc.nearest(cellMask(&block))
					cd := createCharDataBlock(&block, codePoint, pattern)
					glyphs[cell] = glyphIndexOf(glyphLookup, cd.CodePoint)

					fgColor := paletteColor{
						byte(clampByte(cd.FgColor[0])),
						byte(clampByte(cd.FgColor[1])),
						byte(clampByte(cd.FgColor[2])),
					}

					bgColor := paletteColor{
						byte(clampByte(cd.BgColor[0])),
						byte(clampByte(cd.BgColor[1])),
						byte(clampByte(cd.BgColor[2])),
					}

					fgColors[cell] = fgColor
					bgColors[cell] = bgColor
					memo.blocks[cell], memo.valid[cell] = block, true
					memo.glyph[cell], memo.fg[cell], memo.bg[cell] = glyphs[cell], fgColor, bgColor
				}
			}
		}(startRow)
	}
	wg.Wait()

	return glyphs, fgColors, bgColors, w, h, nil
}

// cellMemo keeps each cell's pixels and the result computed from them.
// The result depends on the pixels only, so a cell with the same pixels in
// the next frame reuses it. holdShapes later edits the frame's own slices,
// not these.
type cellMemo struct {
	blocks []cellBlock
	valid  []bool
	glyph  []byte
	fg, bg []paletteColor
}

func (m *cellMemo) resize(cells int) {
	if len(m.blocks) == cells {
		return
	}
	m.blocks = make([]cellBlock, cells)
	m.valid = make([]bool, cells)
	m.glyph = make([]byte, cells)
	m.fg = make([]paletteColor, cells)
	m.bg = make([]paletteColor, cells)
}

// maxPaletteColors is the number of entries addressable by a one-byte index.
const maxPaletteColors = 256

func buildPaletteFromColors(fgColors, bgColors []paletteColor, totalCells int) []paletteColor {
	paletteMap := make(map[uint16]struct{}, 256)
	packColor := func(y6, cb5, cr5 byte) uint16 {
		return (uint16(y6) << 10) | (uint16(cb5) << 5) | uint16(cr5)
	}

	// Palette indices are a single byte, so the palette may hold at most
	// maxPaletteColors entries. Check before every insertion: each cell adds
	// two colors, and checking once per cell could overshoot to 257.
	add := func(c paletteColor) bool {
		code := packColor(c[0]>>2, quantChroma5(c[1]), quantChroma5(c[2]))
		if _, ok := paletteMap[code]; ok {
			return true
		}
		if len(paletteMap) >= maxPaletteColors {
			return false
		}
		paletteMap[code] = struct{}{}
		return true
	}
	for i := 0; i < totalCells; i++ {
		if !add(fgColors[i]) || !add(bgColors[i]) {
			break
		}
	}

	paletteCodes := make([]uint16, 0, len(paletteMap))
	for code := range paletteMap {
		paletteCodes = append(paletteCodes, code)
	}

	sort.Slice(paletteCodes, func(i, j int) bool {
		return paletteCodes[i] < paletteCodes[j]
	})

	palette := make([]paletteColor, len(paletteCodes))
	for i, code := range paletteCodes {
		y6 := byte((code >> 10) & 0x3f)
		cb5 := byte((code >> 5) & 0x1f)
		cr5 := byte(code & 0x1f)
		palette[i][0] = y6
		palette[i][1] = cb5
		palette[i][2] = cr5
	}

	return palette
}

func expandPaletteEntry(p paletteColor) [3]int {
	return [3]int{
		int((p[0] << 2) | (p[0] >> 4)),
		int(expandChroma5(p[1])),
		int(expandChroma5(p[2])),
	}
}

// codeIndices writes the palette index of each of a keyframe's colours to
// dst and returns how many colours are farther than mismatchThreshold from
// it (-1: not counted). The palette is made of the colours' codes (Y 6
// bits, Cb and Cr 5): a colour takes its code's entry, or, for a code the
// palette lacks (a frame of more than 256 codes), the entry nearest the
// code's colour, searched once per code and palette (codeIdx). Before, each
// colour searched for the entry nearest itself: on camera frames of 160x65
// cells 0.50 ms against 0.11, the pictures the same within 0.1 dB.
func (e *Encoder) codeIndices(colors []paletteColor, dst []byte, mismatchThreshold int) int {
	if len(e.codeIdx) != 1<<16 {
		e.codeIdx = make([]uint32, 1<<16)
	}
	if e.codeGen == 0 || e.codeGen >= 1<<24 {
		clear(e.codeIdx)
		e.codeGen = 1
	}
	gen, tab := e.codeGen<<8, e.codeIdx
	mismatches := 0
	for i, c := range colors {
		code := uint16(c[0]>>2)<<10 | uint16(quantChroma5(c[1]))<<5 | uint16(quantChroma5(c[2]))
		t := tab[code]
		if t&^0xff != gen {
			idx, _ := e.match.nearest(expandCode(code))
			t = gen | uint32(idx)
			tab[code] = t
		}
		dst[i] = byte(t)
		if mismatchThreshold >= 0 && colorDistance(e.lastPaletteExpanded[byte(t)], c) > mismatchThreshold {
			mismatches++
		}
	}
	return mismatches
}

// paletteMatcher finds the palette entry nearest to a colour (L1 distance,
// the lowest index among equals) and remembers the answers in a hash table
// until the palette changes. A camera picture brings thousands of new
// colours per frame: a Go map rebuilt every frame took most of the
// encoder's time, growing and rehashing.
type paletteMatcher struct {
	palette [][3]int // expanded entries
	// order lists the entries in ascending Y (stable), so the search can
	// stop early; palette patches change entries in place, which breaks
	// the order of the palette itself.
	order []byte

	// keys hold gen<<24 | colour; an entry of another generation is empty,
	// so the table is cleared by advancing gen.
	keys  []uint32
	vals  []byte
	shift uint32
	gen   uint32
	used  int

	// sorted is the palette in order, packed for nearest (made again when
	// stale).
	sorted []sortedEntry
	stale  bool
}

// sortedEntry is a palette entry for the search: Y, Cb, Cr and its index.
type sortedEntry struct {
	y, cb, cr int32
	idx       int32
}

// reset makes m match against palette and forgets earlier answers.
func (m *paletteMatcher) reset(palette [][3]int) {
	m.palette = palette
	m.order = m.order[:0]
	for i := range palette {
		m.order = append(m.order, byte(i))
	}
	sort.SliceStable(m.order, func(a, b int) bool {
		return palette[m.order[a]][0] < palette[m.order[b]][0]
	})
	m.stale = true
	m.forget()
}

// set makes entry idx of palette (which may have grown by it) take its new
// value, keeping the order and the remembered answers: an answer that now
// points to a changed entry returns that entry's new, larger distance, and
// refresh replaces it.
func (m *paletteMatcher) set(palette [][3]int, idx byte) {
	m.palette = palette
	if i := bytes.IndexByte(m.order, idx); i >= 0 {
		m.order = append(m.order[:i], m.order[i+1:]...)
	}
	y := palette[idx][0]
	at := sort.Search(len(m.order), func(i int) bool {
		o := m.order[i]
		return palette[o][0] > y || palette[o][0] == y && o > idx
	})
	m.order = append(m.order, 0)
	copy(m.order[at+1:], m.order[at:])
	m.order[at] = idx
	m.stale = true
}

// refresh finds the nearest entry anew and remembers it for c.
func (m *paletteMatcher) refresh(c paletteColor) (byte, int) {
	idx, dist := m.nearest(c)
	color := uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2])
	key := m.gen<<24 | color
	mask := uint32(len(m.keys) - 1)
	h := (color * 2654435761) >> m.shift
	for {
		k := m.keys[h]
		if k == key || k>>24 != m.gen {
			if k != key {
				m.used++
			}
			m.keys[h], m.vals[h] = key, idx
			return idx, dist
		}
		h = (h + 1) & mask
	}
}

func (m *paletteMatcher) forget() {
	m.gen++
	if m.gen > 0xff {
		clear(m.keys)
		m.gen = 1
	}
	m.used = 0
}

// reserve makes room for n more colours, keeping the table at most half full.
func (m *paletteMatcher) reserve(n int) {
	size := max(1024, len(m.keys))
	for size < 4*n {
		size <<= 1
	}
	if size > len(m.keys) {
		m.keys = make([]uint32, size)
		m.vals = make([]byte, size)
		m.shift = uint32(32 - bits.TrailingZeros(uint(size)))
		m.gen = 1
		m.used = 0
		return
	}
	if m.used+n > size/2 {
		m.forget()
	}
}

// index returns the nearest palette index of c and its distance.
func (m *paletteMatcher) index(c paletteColor) (byte, int) {
	color := uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2])
	key := m.gen<<24 | color
	mask := uint32(len(m.keys) - 1)
	h := (color * 2654435761) >> m.shift
	for {
		k := m.keys[h]
		if k == key {
			idx := m.vals[h]
			return idx, colorDistance(m.palette[idx], c)
		}
		if k>>24 != m.gen {
			idx, dist := m.nearest(c)
			m.keys[h], m.vals[h] = key, idx
			m.used++
			return idx, dist
		}
		h = (h + 1) & mask
	}
}

// nearest searches the palette. Sorted by Y, it starts at c's Y and stops
// on each side once the Y difference alone exceeds the best distance.
func (m *paletteMatcher) nearest(c paletteColor) (byte, int) {
	if m.stale {
		m.sorted = m.sorted[:0]
		for _, o := range m.order {
			p := m.palette[o]
			m.sorted = append(m.sorted, sortedEntry{int32(p[0]), int32(p[1]), int32(p[2]), int32(o)})
		}
		m.stale = false
	}
	e := m.sorted
	y, cb, cr := int32(c[0]), int32(c[1]), int32(c[2])
	best, bestIdx := int32(math.MaxInt32), int32(0)
	lo, hi := 0, len(e) // first entry with Y >= y
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if e[mid].y < y {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	abs := func(v int32) int32 {
		if v < 0 {
			return -v
		}
		return v
	}
	// The lowest index wins a tie.
	for j := lo; j < len(e); j++ {
		p := &e[j]
		dy := p.y - y
		if dy > best {
			break
		}
		if d := dy + abs(p.cb-cb) + abs(p.cr-cr); d < best || d == best && p.idx < bestIdx {
			best, bestIdx = d, p.idx
		}
	}
	for j := lo - 1; j >= 0; j-- {
		p := &e[j]
		dy := y - p.y
		if dy > best {
			break
		}
		if d := dy + abs(p.cb-cb) + abs(p.cr-cr); d < best || d == best && p.idx < bestIdx {
			best, bestIdx = d, p.idx
		}
	}
	return byte(bestIdx), int(best)
}

func colorDistance(paletteRGB [3]int, color paletteColor) int {
	dr := paletteRGB[0] - int(color[0])
	if dr < 0 {
		dr = -dr
	}
	dg := paletteRGB[1] - int(color[1])
	if dg < 0 {
		dg = -dg
	}
	db := paletteRGB[2] - int(color[2])
	if db < 0 {
		db = -db
	}
	return dr + dg + db
}

func ensureByteSlice(buf *[]byte, size int) []byte {
	if cap(*buf) < size {
		*buf = make([]byte, size)
	}
	return (*buf)[:size]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// SetShapeHold sets the cell hold threshold: the mean per-pixel colour
// difference (sum over the three channels) below which a cell keeps its
// previous glyph and colours. 0 disables the hold.
func (e *Encoder) SetShapeHold(threshold int) {
	e.holdBits = max(0, threshold) // the held cells stay valid for the new threshold
}

// holdShapes keeps a cell exactly as it was (glyph and both colours) while
// its new appearance differs only a little from the previous one. Camera
// noise makes low-contrast cells jump between glyphs whose pixels look
// almost the same; a real change, even an edge moving by one pixel, changes
// the cell's pixels a lot and passes at once.
func (e *Encoder) holdShapes(glyphs []byte, fg, bg []paletteColor) {
	if e.holdBits <= 0 {
		return
	}
	n := len(glyphs)
	if cap(e.held) < n {
		e.held = make([]bool, n)
	}
	held := e.held[:n]
	clear(held)
	if e.prevCells == n && len(e.prevGlyphs) >= n && len(e.prevFg) >= n && len(e.prevBg) >= n {
		limit := e.holdBits * 32 // threshold is per pixel; a cell has 32
		if len(e.holdAge) != n {
			e.holdAge = make([]uint16, n)
		}
		age := e.holdAge
		for i, g := range glyphs {
			p := e.prevGlyphs[i]
			if int(g) >= len(glyphPatterns) || int(p) >= len(glyphPatterns) {
				age[i] = 0
				continue
			}
			l := limit
			if HoldAge > 0 && int(age[i]) >= HoldAge {
				l /= 2
			}
			if cellDifference(glyphPatterns[p], e.prevFg[i], e.prevBg[i], glyphPatterns[g], fg[i], bg[i]) > l {
				age[i] = 0
				continue
			}
			glyphs[i], fg[i], bg[i] = p, e.prevFg[i], e.prevBg[i]
			held[i] = true
			if age[i] < math.MaxUint16 {
				age[i]++
			}
		}
	}
	e.prevGlyphs = append(e.prevGlyphs[:0], glyphs...)
	e.prevFg = append(e.prevFg[:0], fg...)
	e.prevBg = append(e.prevBg[:0], bg...)
	e.prevCells = n
	if e.holdDebug {
		// Tint after storing the real colours, so the hold itself is unchanged.
		for i, h := range held {
			if h {
				fg[i], bg[i] = tintLilac(fg[i]), tintLilac(bg[i])
			}
		}
	}
}

// holdDebugLilac is lilac (RGB 200,160,230) in the codec's YCbCr.
var holdDebugLilac = paletteColor{180, 156, 142}

// tintLilac mixes a cell colour halfway towards lilac.
func tintLilac(c paletteColor) paletteColor {
	for k := 0; k < 3; k++ {
		c[k] = byte((int(c[k]) + int(holdDebugLilac[k])) / 2)
	}
	return c
}

// SetHoldDebug tints cells kept by the hold lilac, to see how many there are.
func (e *Encoder) SetHoldDebug(on bool) {
	e.holdDebug = on
}

// HoldAge: a cell held for HoldAge frames is kept only while it differs by
// less than half the threshold (0: no such limit), so the stale cells a slow
// change leaves behind get sent in the end. On a recorded call (44x18 cells)
// 5, 8 and 15 frames gave the same quality for the bytes, 30 less; tried
// too and dropped: a per-pixel limit and a lower threshold beside a cell
// that changed (no better for the bytes, the tails beside a moving head
// stayed).
var HoldAge = 8

// cellDifference is the summed colour difference over the 32 pixels of two
// cells, each drawn as fg on its pattern's set bits and bg elsewhere.
func cellDifference(pa uint32, fa, ba paletteColor, pb uint32, fb, bb paletteColor) int {
	both := bits.OnesCount32(pa & pb)
	onlyA := bits.OnesCount32(pa &^ pb)
	onlyB := bits.OnesCount32(pb &^ pa)
	neither := 32 - both - onlyA - onlyB
	return both*cellColorDistance(fa, fb) + onlyA*cellColorDistance(fa, bb) +
		onlyB*cellColorDistance(ba, fb) + neither*cellColorDistance(ba, bb)
}

func cellColorDistance(a, b paletteColor) int {
	d := 0
	for c := 0; c < 3; c++ {
		v := int(a[c]) - int(b[c])
		if v < 0 {
			v = -v
		}
		d += v
	}
	return d
}

// SetKeyframeInterval sets how many frames may pass between full keyframes.
// SetBW switches the encoder to brightness only (neutral colour) or back.
// The next frame starts afresh: a keyframe with a new palette (greys would
// otherwise match the old palette's tinted entries within the tolerance) and
// no held cells (they would keep their old colours).
func (e *Encoder) SetBW(on bool) {
	if on == e.forceBW {
		return
	}
	e.forceBW = on
	e.lastPalette = e.lastPalette[:0]
	e.lastPaletteExpanded = e.lastPaletteExpanded[:0]
	e.prevCells = 0
}

// SetSkipEmpty makes Encode return a nil payload instead of a change frame
// in which no cell changed: nothing to send. The caller still sends one now
// and then, so the receiver knows the stream is alive.
func (e *Encoder) SetSkipEmpty(on bool) {
	e.skipEmpty = on
}

func (e *Encoder) SetKeyframeInterval(frames int) {
	e.keyframeInterval = max(0, frames)
}

// Restart makes the next frame of e the one a new Encoder would make (a
// keyframe with a palette of its own, nothing of the frames before), the
// settings and buffers kept, and the cells' results (cellMemo: what a
// cell's pixels give, a function of them alone): for pictures coded one by
// one, which a new Encoder each costs its allocations.
func (e *Encoder) Restart() {
	*e = Encoder{
		glyphs: e.glyphs, fgIdx: e.fgIdx, bgIdx: e.bgIdx,
		fgColors: e.fgColors, bgColors: e.bgColors,
		xMap: e.xMap, yMap: e.yMap, xOff: e.xOff, rowOff: e.rowOff,
		cellMemo:   e.cellMemo,
		match:      e.match,
		payloadBuf: e.payloadBuf[:0],

		lastPalette:         e.lastPalette[:0],
		lastPaletteExpanded: e.lastPaletteExpanded[:0],
		reuseFgIdx:          e.reuseFgIdx,
		reuseBgIdx:          e.reuseBgIdx,
		paletteMissed:       e.paletteMissed[:0],
		palettePatch:        e.palettePatch[:0],

		skipEmpty: e.skipEmpty,

		rcEnc:     rcEncoder{out: e.rcEnc.out[:0]},
		rcChanged: e.rcChanged,
		rcPrevG:   e.rcPrevG[:0],
		rcPrevFg:  e.rcPrevFg[:0],
		rcPrevBg:  e.rcPrevBg[:0],
		emptyFg:   e.emptyFg[:0],

		holdBits:   e.holdBits,
		holdDebug:  e.holdDebug,
		held:       e.held,
		prevGlyphs: e.prevGlyphs[:0],
		prevFg:     e.prevFg[:0],
		prevBg:     e.prevBg[:0],

		sentGlyphs: e.sentGlyphs[:0],
		sentFg:     e.sentFg[:0],
		sentBg:     e.sentBg[:0],
		changeMask: e.changeMask,

		codeIdx:    e.codeIdx,
		glyphCache: e.glyphCache,
		codeGen:    e.codeGen,

		keyframeInterval: e.keyframeInterval,
		forceBW:          e.forceBW,
		maxFPS:           e.maxFPS,
		mirror:           e.mirror,
	}
}

// The compact header (appendCompactHeader): a byte compactHeaderTag | the
// frame type (frameTypeRefs added with numbers); for a key or full frame
// the cells across and down (a byte each); with numbers the frame's (two
// bytes) and, but for a keyframe, how far back its reference is (a byte,
// 1-255). A change frame's size is its reference's. 1-4 bytes where the
// TEXT header took 9-13.
const (
	compactHeaderTag  byte = 0xA0
	compactHeaderBits byte = 0x07 // the frame type and frameTypeRefs
)

func appendCompactHeader(out []byte, frameType byte, cols, rows int, refs bool, id, ref uint16) []byte {
	t := frameType
	if refs {
		t += frameTypeRefs
	}
	out = append(out, compactHeaderTag|t)
	if frameType == frameTypeKey || frameType == frameTypeFull {
		out = append(out, byte(cols), byte(rows))
	}
	if refs {
		out = binary.LittleEndian.AppendUint16(out, id)
		if frameType != frameTypeKey {
			out = append(out, byte(id-ref))
		}
	}
	return out
}

// decodeCompact decodes a frame with the compact header.
func (d *Decoder) decodeCompact(data []byte) (*TextFrame, error) {
	t := data[0] & compactHeaderBits
	frameType, withRefs := t, t >= frameTypeRefs
	if withRefs {
		frameType -= frameTypeRefs
	}
	pos := 1
	var cols, rows int
	if frameType == frameTypeKey || frameType == frameTypeFull {
		if len(data) < pos+2 {
			return nil, fmt.Errorf("payload too short")
		}
		cols, rows = int(data[pos]), int(data[pos+1])
		pos += 2
	}
	var id, ref uint16
	if withRefs {
		n := 3
		if frameType == frameTypeKey {
			n = 2
		}
		if len(data) < pos+n {
			return nil, fmt.Errorf("payload too short")
		}
		id = binary.LittleEndian.Uint16(data[pos:])
		if frameType != frameTypeKey {
			if data[pos+2] == 0 {
				return nil, fmt.Errorf("a reference of itself")
			}
			ref = id - uint16(data[pos+2])
		}
		pos += n
	}
	if cols == 0 {
		// A change frame: its reference's size.
		d.mu.Lock()
		switch {
		case withRefs && !(d.refs.any && d.refs.last == ref && d.baseValid):
			if r := d.refs.frames.at(ref); r != nil {
				cols, rows = r.cols, r.rows
			}
		case d.baseValid:
			cols, rows = d.baseCols, d.baseRows
		}
		d.mu.Unlock()
		if cols == 0 {
			return nil, ErrNoBase
		}
	}
	if cols > MaxFrameCols || rows > MaxFrameRows || rows == 0 {
		return nil, fmt.Errorf("frame size %dx%d outside 1..%dx%d", cols, rows, MaxFrameCols, MaxFrameRows)
	}
	if withRefs {
		frameType += frameTypeRefs
	}
	return d.decodeBody(data[pos:], frameType, cols, rows, id, ref)
}

// IsKeyframe says whether data (Encoder.Encode's) is a keyframe.
func IsKeyframe(data []byte) bool {
	if len(data) >= 1 && data[0]&^compactHeaderBits == compactHeaderTag {
		return data[0]&compactHeaderBits&^frameTypeRefs == frameTypeKey
	}
	return len(data) > 4 && string(data[:4]) == PayloadMagic && data[4]&^frameTypeRefs == frameTypeKey
}
