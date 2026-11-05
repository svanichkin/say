package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"runtime"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
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
	var fg, bg [3]int
	fgCount := bits.OnesCount32(pattern)
	mask := uint32(0x80000000)
	for _, px := range block {
		y, cb, cr := int(px>>16&0xff), int(px>>8&0xff), int(px&0xff)
		if pattern&mask != 0 {
			fg[0] += y
			fg[1] += cb
			fg[2] += cr
		} else {
			bg[0] += y
			bg[1] += cb
			bg[2] += cr
		}
		mask >>= 1
	}
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

// findCharDataBlock picks the glyph and colours that best represent a cell.
// When two colours cover most of the cell they are used directly; otherwise
// the cell is split along its widest channel and the halves are averaged.
//
// A camera cell has only a few distinct colours (2-4 in most cells), so the
// work is done per colour: the channel ranges and each colour's side of the
// split are computed once per colour, not once per pixel.
func findCharDataBlock(block *cellBlock, flags int) CharData {
	minC := [3]int{255, 255, 255}
	maxC := [3]int{0, 0, 0}

	// Distinct colours in order of first appearance, found through a small
	// open-addressing table; pixColor maps each pixel to its colour.
	var colors [32]uint32
	var counts [32]int
	var pixColor [32]uint8
	var slots [64]int8 // index+1 into colors; 0 is empty
	nColors := 0
	last, lastIdx := uint32(1<<31), 0 // never a 24-bit colour
	for i, px := range block {
		color := px & 0xffffff
		if color != last {
			h := (color * 2654435761) >> 26 // 6 bits
			for {
				slot := slots[h]
				if slot == 0 {
					lastIdx = nColors
					colors[nColors], counts[nColors] = color, 0
					nColors++
					slots[h] = int8(nColors)
					y, cb, cr := int(color>>16), int(color>>8&0xff), int(color&0xff)
					minC[0], maxC[0] = min(minC[0], y), max(maxC[0], y)
					minC[1], maxC[1] = min(minC[1], cb), max(maxC[1], cb)
					minC[2], maxC[2] = min(minC[2], cr), max(maxC[2], cr)
					break
				}
				if colors[slot-1] == color {
					lastIdx = int(slot - 1)
					break
				}
				h = (h + 1) & 63
			}
			last = color
		}
		counts[lastIdx]++
		pixColor[i] = uint8(lastIdx)
	}

	var maxColor1, maxColor2 uint32
	count2 := 0
	best1, best2 := -1, -1
	idx1, idx2 := -1, -1
	for i := 0; i < nColors; i++ {
		c := counts[i]
		if c > best1 {
			best2, idx2 = best1, idx1
			best1, idx1 = c, i
		} else if c > best2 {
			best2, idx2 = c, i
		}
	}
	if idx1 >= 0 {
		maxColor1 = colors[idx1]
		maxColor2 = maxColor1
		count2 = counts[idx1]
	}
	if idx2 >= 0 {
		maxColor2 = colors[idx2]
		count2 += counts[idx2]
	}

	direct := count2 > (8*4)/2

	// colorBit is each colour's bit in the cell's pattern.
	var colorBit [32]uint32
	if direct {
		y1, cb1, cr1 := int(maxColor1>>16&0xff), int(maxColor1>>8&0xff), int(maxColor1&0xff)
		y2, cb2, cr2 := int(maxColor2>>16&0xff), int(maxColor2>>8&0xff), int(maxColor2&0xff)
		for i := 0; i < nColors; i++ {
			c := colors[i]
			y, cb, cr := int(c>>16), int(c>>8&0xff), int(c&0xff)
			d1 := (y1-y)*(y1-y) + (cb1-cb)*(cb1-cb) + (cr1-cr)*(cr1-cr)
			d2 := (y2-y)*(y2-y) + (cb2-cb)*(cb2-cb) + (cr2-cr)*(cr2-cr)
			if d1 > d2 {
				colorBit[i] = 1
			}
		}
	} else {
		splitIndex := 0
		bestSplit := 0
		for i := 0; i < 3; i++ {
			if maxC[i]-minC[i] > bestSplit {
				bestSplit = maxC[i] - minC[i]
				splitIndex = i
			}
		}
		splitValue := minC[splitIndex] + bestSplit/2
		shift := uint(16 - 8*splitIndex)
		for i := 0; i < nColors; i++ {
			if int(colors[i]>>shift&0xff) > splitValue {
				colorBit[i] = 1
			}
		}
	}
	var bitsVal uint32
	for _, ci := range pixColor {
		bitsVal = bitsVal<<1 | colorBit[ci]
	}

	bestDiff := 32
	bestPattern := uint32(0x0000ffff)
	codePoint := 0x2584
	inverted := false

	// Each glyph is tried as is and inverted (32 minus the difference), in
	// that order; an exact match cannot be beaten.
	if flags == 0 {
		patterns, codes := glyphCandidates()
		for i, pattern := range patterns {
			diff := bits.OnesCount32(pattern ^ bitsVal)
			if diff < bestDiff {
				bestDiff, bestPattern, codePoint, inverted = diff, pattern, codes[i], false
			}
			if 32-diff < bestDiff {
				bestDiff, bestPattern, codePoint, inverted = 32-diff, pattern, codes[i], true
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
				bestDiff, bestPattern, codePoint, inverted = diff, bm.pattern, bm.codePoint, false
			}
			if 32-diff < bestDiff {
				bestDiff, bestPattern, codePoint, inverted = 32-diff, bm.pattern, bm.codePoint, true
			}
			if bestDiff == 0 {
				break
			}
		}
	}

	if direct {
		var res CharData
		if inverted {
			maxColor1, maxColor2 = maxColor2, maxColor1
		}
		for i := 0; i < 3; i++ {
			shift := uint(16 - 8*i)
			res.FgColor[i] = int((maxColor2 >> shift) & 0xff)
			res.BgColor[i] = int((maxColor1 >> shift) & 0xff)
		}
		res.CodePoint = codePoint
		return res
	}

	return createCharDataBlock(block, codePoint, bestPattern)
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

// packGlyphs6 packs glyph indices (each 0..63) into a bitstream
// using 6 bits per glyph.
func packGlyphs6(src []byte) []byte {
	n := len(src)
	if n == 0 {
		return nil
	}
	totalBits := n * 6
	outLen := (totalBits + 7) / 8
	out := make([]byte, outLen)
	bitPos := 0
	for _, v := range src {
		val := int(v) & 0x3f
		byteIndex := bitPos / 8
		bitOffset := bitPos % 8

		// write lower bits into current byte
		out[byteIndex] |= byte(val << bitOffset)

		// if it crosses byte boundary, spill the remaining bits
		if bitOffset > 2 {
			out[byteIndex+1] |= byte(val >> (8 - bitOffset))
		}

		bitPos += 6
	}
	return out
}

// unpackGlyphs6 unpacks glyph indices from a 6-bit-per-glyph bitstream.
func unpackGlyphs6(data []byte, count int) ([]byte, error) {
	if count == 0 {
		return nil, nil
	}
	totalBits := len(data) * 8
	neededBits := count * 6
	if neededBits > totalBits {
		return nil, fmt.Errorf("glyphs6: not enough data: need %d bits, have %d", neededBits, totalBits)
	}

	out := make([]byte, count)
	bitPos := 0
	for i := 0; i < count; i++ {
		byteIndex := bitPos / 8
		bitOffset := bitPos % 8

		val := int(data[byteIndex] >> bitOffset)
		bitsUsed := 8 - bitOffset
		if bitsUsed < 6 {
			val |= int(data[byteIndex+1]) << bitsUsed
		}

		out[i] = byte(val & 0x3f)
		bitPos += 6
	}
	return out, nil
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

var (
	zstdEncoderLevel = zstd.SpeedBetterCompression

	sharedZstdEncoder persistentZstdEncoder
	sharedZstdDecoder persistentZstdDecoder
)

type persistentZstdEncoder struct {
	once sync.Once
	mu   sync.Mutex
	enc  *zstd.Encoder
	err  error
}

func (p *persistentZstdEncoder) use(fn func(*zstd.Encoder) error) error {
	p.once.Do(func() {
		p.enc, p.err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstdEncoderLevel))
	})
	if p.err != nil {
		return p.err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return fn(p.enc)
}

// Resolution limits for one video frame, in terminal cells. They bound the
// memory a peer can make us allocate and keep every encoded frame within a
// single UDP datagram (MaxPayloadBytes). 160×80 cells is a 640×640 px source,
// far above what a pane in a typical terminal needs.
const (
	MaxFrameCols  = 160
	MaxFrameRows  = 80
	MaxFrameCells = MaxFrameCols * MaxFrameRows
)

// MaxPayloadBytes is the largest decompressed payload a valid frame can have:
// header, 6-bit glyph plane, a full palette and two one-byte index planes.
const MaxPayloadBytes = len(PayloadMagic) + 1 + 2 + 2 + 4*4 +
	(MaxFrameCells*6+7)/8 + maxPaletteColors*2 + 2*MaxFrameCells

type persistentZstdDecoder struct {
	once sync.Once
	mu   sync.Mutex
	dec  *zstd.Decoder
	err  error
}

func (p *persistentZstdDecoder) use(fn func(*zstd.Decoder) error) error {
	p.once.Do(func() {
		p.dec, p.err = zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(uint64(MaxPayloadBytes)),
			zstd.WithDecoderMaxWindow(uint64(MaxPayloadBytes)))
	})
	if p.err != nil {
		return p.err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return fn(p.dec)
}

type paletteColor [3]byte

type Encoder struct {
	glyphs   []byte
	fgIdx    []byte
	bgIdx    []byte
	fgColors []paletteColor
	bgColors []paletteColor
	xMap     []int
	yMap     []int
	xOff     []int // byte offset of each sampled column in a row
	rowOff   []int // byte offset of each sampled row
	cellMemo cellMemo

	// match finds the nearest entry of lastPalette and remembers the answers
	// across frames until the palette changes.
	match paletteMatcher

	payloadBuf bytes.Buffer

	lastPalette         []paletteColor
	lastPaletteExpanded [][3]int
	reuseFgIdx          []byte
	reuseBgIdx          []byte

	// Cell hold (see holdShapes): holdBits is the per-pixel threshold.
	holdBits   int
	holdDebug  bool // tint held cells lilac (-video-hold-debug)
	held       []bool
	prevGlyphs []byte
	prevFg     []paletteColor
	prevBg     []paletteColor
	prevCells  int

	// Last frame sent, as palette indices, for change-only frames.
	sentGlyphs []byte
	sentFg     []byte
	sentBg     []byte
	sentCells  int

	framesSinceLastKey int
	keyframeInterval   int
	forceBW            bool
	maxFPS             int
	mirror             bool
}

var defaultEncoder = NewEncoder(false, defaultKeyframeInterval, 0, false)

func (e *Encoder) shouldForcePeriodicKeyframe() bool {
	if e == nil || e.keyframeInterval <= 0 {
		return false
	}
	return e.framesSinceLastKey >= e.keyframeInterval
}

func (e *Encoder) updateKeyframeCounter(keyframe byte) {
	if e == nil || e.keyframeInterval <= 0 {
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
}

// Frame types (the byte after the magic).
const (
	frameTypeFull      byte = 0 // all cells, palette of the last keyframe
	frameTypeKey       byte = 1 // all cells and a palette
	frameTypeDelta     byte = 2 // only the cells that changed since the previous frame
	deltaMaxChangedNum      = 6 // a change frame is sent while at most 6/10…
	deltaMaxChangedDen      = 10
)

// ErrNoBase reports a change-only frame whose previous frame is missing
// (lost or not yet received); frames are shown again from the next keyframe.
var ErrNoBase = errors.New("change frame without its previous frame")

// ResetBase forgets the previous frame, e.g. after a lost packet, so change
// frames are refused until the next full frame instead of drawing garbage.
func (d *Decoder) ResetBase() {
	d.mu.Lock()
	d.baseValid = false
	d.mu.Unlock()
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

// Encode encodes a frame for the network: the payload compressed with zstd.
func (e *Encoder) Encode(frame Frame, cols, rows int) ([]byte, error) {
	return e.encode(frame, cols, rows, true)
}

// EncodePlain encodes a frame that stays in this process (the self view):
// the same payload without compression, for Decoder.DecodePlain.
func (e *Encoder) EncodePlain(frame Frame, cols, rows int) ([]byte, error) {
	return e.encode(frame, cols, rows, false)
}

func (e *Encoder) encode(frame Frame, cols, rows int, compress bool) ([]byte, error) {
	if len(frame.Data) == 0 || frame.Width <= 0 || frame.Height <= 0 {
		return nil, fmt.Errorf("invalid frame data")
	}
	if len(frame.Data) != frame.Width*frame.Height*3 {
		return nil, fmt.Errorf("frame data size mismatch: got %d, want %d", len(frame.Data), frame.Width*frame.Height*3)
	}
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("cols and rows must be > 0")
	}

	if cols > MaxFrameCols || rows > MaxFrameRows {
		return nil, fmt.Errorf("cols/rows too large: cols=%d (max %d), rows=%d (max %d)", cols, MaxFrameCols, rows, MaxFrameRows)
	}

	glyphs, fgColors, bgColors, frameW, frameH, err := e.buildCellData(frame, cols, rows, e.forceBW)
	if err != nil {
		return nil, err
	}
	e.holdShapes(glyphs, fgColors, bgColors)

	if frameW > int(math.MaxUint16) || frameH > int(math.MaxUint16) {
		return nil, fmt.Errorf("frame dimensions too large: %dx%d (max %dx%d)", frameW, frameH, math.MaxUint16, math.MaxUint16)
	}

	totalCells := cols * rows

	keyframe := byte(1)
	var palBytes []byte
	var finalFgIdx []byte
	var finalBgIdx []byte

	if len(e.lastPalette) > 0 && len(e.lastPalette) == len(e.lastPaletteExpanded) && totalCells > 0 {
		reuseFg := ensureByteSlice(&e.reuseFgIdx, totalCells)
		reuseBg := ensureByteSlice(&e.reuseBgIdx, totalCells)
		e.match.reserve(2 * totalCells)
		mismatches := assignPaletteIndices(fgColors, e.lastPalette, &e.match, reuseFg, paletteReuseColorErrorTolerance)
		mismatches += assignPaletteIndices(bgColors, e.lastPalette, &e.match, reuseBg, paletteReuseColorErrorTolerance)

		totalSamples := totalCells * 2
		if totalSamples > 0 {
			mismatchRatio := float64(mismatches) / float64(totalSamples)
			if mismatchRatio <= paletteReuseMaxMismatchRatio {
				keyframe = 0
				finalFgIdx = reuseFg
				finalBgIdx = reuseBg
			}
		}
	}

	if keyframe == 0 && e.shouldForcePeriodicKeyframe() {
		// A periodic keyframe only lets a receiver that lost the last one
		// recover. The current palette still fits, so resend it with the
		// reused indices: rebuilding it from this frame's slightly noisy
		// colours made a static picture shift colours at every keyframe.
		keyframe = 1
		palBytes = flattenPalette(e.lastPalette)
	} else if keyframe == 1 {
		palette := buildPaletteFromColors(fgColors, bgColors, totalCells)
		palBytes = flattenPalette(palette)
		e.storePalette(palette) // also resets e.match to the new palette

		fgIdx := ensureByteSlice(&e.fgIdx, totalCells)
		bgIdx := ensureByteSlice(&e.bgIdx, totalCells)

		e.match.reserve(2 * totalCells)
		assignPaletteIndices(fgColors, e.lastPalette, &e.match, fgIdx, -1)
		assignPaletteIndices(bgColors, e.lastPalette, &e.match, bgIdx, -1)

		finalFgIdx = fgIdx
		finalBgIdx = bgIdx
	}

	// Change-only frame: with the palette unchanged, send just the cells that
	// differ from the previous frame. Keyframes stay full so a receiver that
	// lost a frame recovers at the next one.
	frameType := keyframe
	var changeMask, changedGlyphs, changedFg, changedBg []byte
	if keyframe == 0 && e.sentCells == totalCells {
		changeMask = make([]byte, (totalCells+7)/8)
		for i := 0; i < totalCells; i++ {
			if glyphs[i] != e.sentGlyphs[i] || finalFgIdx[i] != e.sentFg[i] || finalBgIdx[i] != e.sentBg[i] {
				changeMask[i/8] |= 1 << (i % 8)
				changedGlyphs = append(changedGlyphs, glyphs[i])
				changedFg = append(changedFg, finalFgIdx[i])
				changedBg = append(changedBg, finalBgIdx[i])
			}
		}
		if len(changedGlyphs)*deltaMaxChangedDen <= totalCells*deltaMaxChangedNum {
			frameType = frameTypeDelta
		}
	}
	e.sentGlyphs = append(e.sentGlyphs[:0], glyphs...)
	e.sentFg = append(e.sentFg[:0], finalFgIdx...)
	e.sentBg = append(e.sentBg[:0], finalBgIdx...)
	e.sentCells = totalCells

	e.payloadBuf.Reset()
	payload := &e.payloadBuf

	payload.WriteString(PayloadMagic)
	payload.WriteByte(frameType)
	if err := binary.Write(payload, binary.LittleEndian, uint16(cols)); err != nil {
		return nil, err
	}
	if err := binary.Write(payload, binary.LittleEndian, uint16(rows)); err != nil {
		return nil, err
	}

	if frameType == frameTypeDelta {
		for _, field := range [][]byte{changeMask, packGlyphs6(changedGlyphs), changedFg, changedBg} {
			if err := writeField(payload, field); err != nil {
				return nil, fmt.Errorf("write change frame: %w", err)
			}
		}
		e.updateKeyframeCounter(keyframe)
		if !compress {
			return bytes.Clone(payload.Bytes()), nil
		}
		compressed, err := compressZstd(payload.Bytes())
		if err != nil {
			return nil, fmt.Errorf("zstd encode: %w", err)
		}
		return compressed, nil
	}

	// write per-cell planes: glyph indices (packed as 6 bits) and palette-based color indices
	glyphsPacked := packGlyphs6(glyphs)
	if err := writeField(payload, glyphsPacked); err != nil {
		return nil, fmt.Errorf("write glyphs: %w", err)
	}
	if keyframe == 1 {
		if err := writeField(payload, palBytes); err != nil {
			return nil, fmt.Errorf("write palette: %w", err)
		}
	}
	if err := writeField(payload, finalFgIdx); err != nil {
		return nil, fmt.Errorf("write fgIdx: %w", err)
	}
	if err := writeField(payload, finalBgIdx); err != nil {
		return nil, fmt.Errorf("write bgIdx: %w", err)
	}

	e.updateKeyframeCounter(keyframe)

	if !compress {
		return bytes.Clone(payload.Bytes()), nil
	}
	compressed, err := compressZstd(payload.Bytes())
	if err != nil {
		return nil, fmt.Errorf("zstd encode: %w", err)
	}
	return compressed, nil
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
}

func Decode(data []byte) (*TextFrame, error) {
	return defaultDecoder.Decode(data)
}

func (d *Decoder) Decode(data []byte) (*TextFrame, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty payload")
	}
	payload, err := decompressZstd(data)
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}
	return d.DecodePlain(payload)
}

// DecodePlain decodes an uncompressed payload from Encoder.EncodePlain.
func (d *Decoder) DecodePlain(payload []byte) (*TextFrame, error) {
	offset := 0
	// magic + keyframe + cols + rows is the fixed-size prefix.
	if len(payload) < len(PayloadMagic)+1+2+2 {
		return nil, fmt.Errorf("payload too short")
	}
	if string(payload[:len(PayloadMagic)]) != PayloadMagic {
		return nil, fmt.Errorf("unexpected magic %q", string(payload[:len(PayloadMagic)]))
	}
	offset += len(PayloadMagic)

	keyframe := payload[offset]
	offset++

	if len(payload)-offset < 4 {
		return nil, fmt.Errorf("payload too short for cols/rows")
	}
	cols := binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	rows := binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	if cols == 0 || rows == 0 || int(cols) > MaxFrameCols || int(rows) > MaxFrameRows {
		return nil, fmt.Errorf("frame size %dx%d outside 1..%dx%d", cols, rows, MaxFrameCols, MaxFrameRows)
	}

	readField := func(label string) ([]byte, error) {
		if len(payload)-offset < 4 {
			return nil, fmt.Errorf("%s length header truncated", label)
		}
		length := binary.LittleEndian.Uint32(payload[offset : offset+4])
		offset += 4
		if len(payload)-offset < int(length) {
			return nil, fmt.Errorf("%s length %d exceeds remaining %d", label, length, len(payload)-offset)
		}
		data := payload[offset : offset+int(length)]
		offset += int(length)
		return data, nil
	}

	totalCells := int(cols) * int(rows)
	var glyphs, fgIdx, bgIdx []byte
	var paletteBytes []byte

	if keyframe == frameTypeDelta {
		mask, err := readField("mask")
		if err != nil {
			return nil, err
		}
		changedGlyphsEnc, err := readField("glyphs")
		if err != nil {
			return nil, err
		}
		changedFg, err := readField("fgIdx")
		if err != nil {
			return nil, err
		}
		changedBg, err := readField("bgIdx")
		if err != nil {
			return nil, err
		}
		if len(mask) != (totalCells+7)/8 {
			return nil, fmt.Errorf("change mask length %d for %d cells", len(mask), totalCells)
		}
		changed := 0
		for _, m := range mask {
			changed += bits.OnesCount8(m)
		}
		changedGlyphs, err := unpackGlyphs6(changedGlyphsEnc, changed)
		if err != nil {
			return nil, err
		}
		if len(changedFg) != changed || len(changedBg) != changed {
			return nil, fmt.Errorf("change frame: %d cells but %d/%d colour indices", changed, len(changedFg), len(changedBg))
		}
		d.mu.Lock()
		valid := d.baseValid && d.baseCols == int(cols) && d.baseRows == int(rows)
		if valid {
			glyphs = append([]byte(nil), d.baseGlyphs...)
			fgIdx = append([]byte(nil), d.baseFg...)
			bgIdx = append([]byte(nil), d.baseBg...)
		}
		d.mu.Unlock()
		if !valid {
			return nil, ErrNoBase
		}
		k := 0
		for i := 0; i < totalCells; i++ {
			if mask[i/8]&(1<<(i%8)) != 0 {
				glyphs[i], fgIdx[i], bgIdx[i] = changedGlyphs[k], changedFg[k], changedBg[k]
				k++
			}
		}
	} else {
		glyphsEnc, err := readField("glyphs")
		if err != nil {
			return nil, err
		}
		if keyframe == frameTypeKey {
			paletteBytes, err = readField("palette")
			if err != nil {
				return nil, err
			}
		}
		fgIdxEnc, err := readField("fgIdx")
		if err != nil {
			return nil, err
		}
		bgIdxEnc, err := readField("bgIdx")
		if err != nil {
			return nil, err
		}
		glyphs, err = unpackGlyphs6(glyphsEnc, totalCells)
		if err != nil {
			return nil, err
		}
		fgIdx, bgIdx = fgIdxEnc, bgIdxEnc
		if len(fgIdx) != totalCells {
			return nil, fmt.Errorf("fgIdx length %d does not match total cells %d", len(fgIdx), totalCells)
		}
		if len(bgIdx) != totalCells {
			return nil, fmt.Errorf("bgIdx length %d does not match total cells %d", len(bgIdx), totalCells)
		}
	}
	var palette []paletteColor
	if keyframe == frameTypeKey {
		parsedPalette, err := decodePalettePayload(paletteBytes)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.lastPalette = parsedPalette
		palette = d.lastPalette
		d.mu.Unlock()
	} else {
		d.mu.Lock()
		if len(d.lastPalette) == 0 {
			d.baseValid = false
			d.mu.Unlock()
			return nil, fmt.Errorf("delta frame received before any keyframe palette")
		}
		palette = d.lastPalette
		d.mu.Unlock()
	}

	palCount := len(palette)

	fgYCbCr := make([]byte, totalCells*3)
	bgYCbCr := make([]byte, totalCells*3)
	for i := 0; i < totalCells; i++ {
		idx := int(fgIdx[i])
		if idx < 0 || idx >= palCount {
			d.ResetBase()
			return nil, fmt.Errorf("fgIdx[%d]=%d out of range (palette size %d)", i, idx, palCount)
		}
		copy(fgYCbCr[i*3:], palette[idx][:])

		idx2 := int(bgIdx[i])
		if idx2 < 0 || idx2 >= palCount {
			d.ResetBase()
			return nil, fmt.Errorf("bgIdx[%d]=%d out of range (palette size %d)", i, idx2, palCount)
		}
		copy(bgYCbCr[i*3:], palette[idx2][:])
	}

	// This frame is the base for the next change-only frame.
	d.mu.Lock()
	d.baseGlyphs = append(d.baseGlyphs[:0], glyphs...)
	d.baseFg = append(d.baseFg[:0], fgIdx...)
	d.baseBg = append(d.baseBg[:0], bgIdx...)
	d.baseCols, d.baseRows, d.baseValid = int(cols), int(rows), true
	d.mu.Unlock()

	return &TextFrame{
		Cols:    int(cols),
		Rows:    int(rows),
		Width:   int(cols) << 2,
		Height:  int(rows) << 3,
		Glyphs:  glyphs,
		FgYCbCr: fgYCbCr,
		BgYCbCr: bgYCbCr,
	}, nil
}

func decodePalettePayload(paletteBytes []byte) ([]paletteColor, error) {
	if len(paletteBytes)%2 != 0 {
		return nil, fmt.Errorf("palette length %d is not a multiple of 2", len(paletteBytes))
	}

	paletteCount := len(paletteBytes) / 2
	palette := make([]paletteColor, paletteCount)

	for i := 0; i < paletteCount; i++ {
		lo := uint16(paletteBytes[2*i])
		hi := uint16(paletteBytes[2*i+1])
		v := lo | (hi << 8)

		y6 := (v >> 10) & 0x3f
		cb5 := (v >> 5) & 0x1f
		cr5 := v & 0x1f

		palette[i][0] = byte((y6 << 2) | (y6 >> 4))
		palette[i][1] = byte((cb5 << 3) | (cb5 >> 2))
		palette[i][2] = byte((cr5 << 3) | (cr5 >> 2))
	}

	return palette, nil
}

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
			var block cellBlock
			for by := start; by < rows; by += workers {
				for bx := 0; bx < cols; bx++ {
					cell := by*cols + bx
					// Read the cell's pixels straight from the frame (what
					// getPixel does, without a call per pixel).
					xo := xOff[bx*4 : bx*4+4 : bx*4+4]
					for y := 0; y < 8; y++ {
						row := frame.Data[rowOff[by*8+y]:]
						b := block[y*4 : y*4+4 : y*4+4]
						if bw {
							for x, o := range xo {
								b[x] = uint32(row[o])<<16 | 128<<8 | 128
							}
						} else {
							for x, o := range xo {
								b[x] = uint32(row[o])<<16 | uint32(row[o+1])<<8 | uint32(row[o+2])
							}
						}
					}
					// A cell whose pixels did not change since the last frame
					// (three in four on a camera) gets the same result again.
					if memo.valid[cell] && memo.blocks[cell] == block {
						glyphs[cell], fgColors[cell], bgColors[cell] = memo.glyph[cell], memo.fg[cell], memo.bg[cell]
						continue
					}
					cd := findCharDataBlock(&block, 0)
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
		code := packColor(c[0]>>2, c[1]>>3, c[2]>>3)
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
		int((p[1] << 3) | (p[1] >> 2)),
		int((p[2] << 3) | (p[2] >> 2)),
	}
}

// assignPaletteIndices writes the index of the nearest palette entry of each
// colour to dst (m must be reset to palette) and returns how many colours
// are farther than mismatchThreshold from it (-1: not counted).
func assignPaletteIndices(colors []paletteColor, palette []paletteColor, m *paletteMatcher, dst []byte, mismatchThreshold int) int {
	if len(colors) != len(dst) {
		return 0
	}
	if len(palette) == 0 {
		clear(dst)
		return len(colors)
	}
	mismatches := 0
	for i, color := range colors {
		idx, dist := m.index(color)
		dst[i] = idx
		if mismatchThreshold >= 0 && dist > mismatchThreshold {
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
	sortedY bool     // palette in ascending Y: the search can stop early

	// keys hold gen<<24 | colour; an entry of another generation is empty,
	// so the table is cleared by advancing gen.
	keys  []uint32
	vals  []byte
	shift uint32
	gen   uint32
	used  int
}

// reset makes m match against palette and forgets earlier answers.
func (m *paletteMatcher) reset(palette [][3]int) {
	m.palette = palette
	m.sortedY = true
	for i := 1; i < len(palette); i++ {
		if palette[i][0] < palette[i-1][0] {
			m.sortedY = false
			break
		}
	}
	m.forget()
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
	p := m.palette
	best, bestIdx := math.MaxInt, 0
	if !m.sortedY {
		for j := range p {
			if d := colorDistance(p[j], c); d < best {
				best, bestIdx = d, j
			}
		}
		return byte(bestIdx), best
	}
	y := int(c[0])
	lo, hi := 0, len(p) // first entry with Y >= y
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if p[mid][0] < y {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	for j := lo; j < len(p) && p[j][0]-y <= best; j++ {
		if d := colorDistance(p[j], c); d < best {
			best, bestIdx = d, j
		}
	}
	for j := lo - 1; j >= 0 && y-p[j][0] <= best; j-- {
		if d := colorDistance(p[j], c); d <= best { // lower index wins a tie
			best, bestIdx = d, j
		}
	}
	return byte(bestIdx), best
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

func compressZstd(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}

	// EncodeAll compresses one block with the encoder's reusable state; the
	// streaming Reset/Write/Close allocated its history for every frame.
	var out []byte
	if err := sharedZstdEncoder.use(func(enc *zstd.Encoder) error {
		out = enc.EncodeAll(data, make([]byte, 0, len(data)/2+64))
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func decompressZstd(data []byte) ([]byte, error) {
	var out bytes.Buffer

	if err := sharedZstdDecoder.use(func(dec *zstd.Decoder) error {
		if err := dec.Reset(bytes.NewReader(data)); err != nil {
			return err
		}
		// Read one byte past the limit to tell "exactly at" from "over".
		n, err := out.ReadFrom(io.LimitReader(dec, int64(MaxPayloadBytes)+1))
		if err != nil {
			return err
		}
		if n > int64(MaxPayloadBytes) {
			return fmt.Errorf("decompressed payload exceeds %d bytes", MaxPayloadBytes)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeField(buf *bytes.Buffer, data []byte) error {
	if err := binary.Write(buf, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := buf.Write(data)
	return err
}

func flattenPalette(palette []paletteColor) []byte {
	if len(palette) == 0 {
		return nil
	}
	k := len(palette)
	out := make([]byte, k*2)

	for i := 0; i < k; i++ {
		// palette[i] хранит коды: Y6, Cb5, Cr5.
		y6 := uint16(palette[i][0]) & 0x3f
		cb5 := uint16(palette[i][1]) & 0x1f
		cr5 := uint16(palette[i][2]) & 0x1f

		v := (y6 << 10) | (cb5 << 5) | cr5

		// Little-endian: [lo, hi]
		out[2*i] = byte(v & 0xff)
		out[2*i+1] = byte(v >> 8)
	}

	return out
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
		for i, g := range glyphs {
			p := e.prevGlyphs[i]
			if int(g) >= len(glyphPatterns) || int(p) >= len(glyphPatterns) {
				continue
			}
			if cellDifference(glyphPatterns[p], e.prevFg[i], e.prevBg[i], glyphPatterns[g], fg[i], bg[i]) <= limit {
				glyphs[i], fg[i], bg[i] = p, e.prevFg[i], e.prevBg[i]
				held[i] = true
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
func (e *Encoder) SetKeyframeInterval(frames int) {
	e.keyframeInterval = max(0, frames)
}
