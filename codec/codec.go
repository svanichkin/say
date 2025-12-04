package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
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
	var res CharData
	res.CodePoint = codePoint

	fgCount, bgCount := 0, 0
	mask := uint32(0x80000000)

	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			var avg *[3]int
			if pattern&mask != 0 {
				avg = &res.FgColor
				fgCount++
			} else {
				avg = &res.BgColor
				bgCount++
			}

			rgb := getPixel(x0+x, y0+y)
			for i := 0; i < 3; i++ {
				(*avg)[i] += getChannel(rgb, i)
			}
			mask >>= 1
		}
	}

	if bgCount > 0 {
		for i := range res.BgColor {
			res.BgColor[i] /= bgCount
		}
	}
	if fgCount > 0 {
		for i := range res.FgColor {
			res.FgColor[i] /= fgCount
		}
	}
	return res
}

func findCharData(getPixel func(int, int) uint32, x0, y0, flags int) CharData {
	minC := [3]int{255, 255, 255}
	maxC := [3]int{0, 0, 0}

	type colorCount struct {
		color uint32
		count int
	}

	var colors [32]colorCount
	nColors := 0

	// Cache the 4x8 block to avoid calling getPixel twice for each cell.
	var block [32]uint32
	idx := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 4; x++ {
			rgb := getPixel(x0+x, y0+y)
			block[idx] = rgb
			idx++

			var color uint32
			for i := 0; i < 3; i++ {
				d := getChannel(rgb, i)
				if d < minC[i] {
					minC[i] = d
				}
				if d > maxC[i] {
					maxC[i] = d
				}
				color = (color << 8) | uint32(d)
			}

			found := false
			for i := 0; i < nColors; i++ {
				if colors[i].color == color {
					colors[i].count++
					found = true
					break
				}
			}
			if !found && nColors < len(colors) {
				colors[nColors] = colorCount{color: color, count: 1}
				nColors++
			}
		}
	}

	var maxColor1, maxColor2 uint32
	count2 := 0

	if nColors > 0 {
		best1, best2 := -1, -1
		idx1, idx2 := -1, -1
		for i := 0; i < nColors; i++ {
			c := colors[i].count
			if c > best1 {
				best2, idx2 = best1, idx1
				best1, idx1 = c, i
			} else if c > best2 {
				best2, idx2 = c, i
			}
		}
		if idx1 >= 0 {
			maxColor1 = colors[idx1].color
			maxColor2 = maxColor1
			count2 = colors[idx1].count
		}
		if idx2 >= 0 {
			maxColor2 = colors[idx2].color
			count2 += colors[idx2].count
		}
	}

	var bitsVal uint32
	direct := count2 > (8*4)/2

	if direct {
		idx = 0
		for y := 0; y < 8; y++ {
			for x := 0; x < 4; x++ {
				bitsVal <<= 1
				rgb := block[idx]
				idx++
				d1, d2 := 0, 0
				for i := 0; i < 3; i++ {
					shift := uint(16 - 8*i)
					c1 := int((maxColor1 >> shift) & 0xff)
					c2 := int((maxColor2 >> shift) & 0xff)
					c := getChannel(rgb, i)
					d1 += (c1 - c) * (c1 - c)
					d2 += (c2 - c) * (c2 - c)
				}
				if d1 > d2 {
					bitsVal |= 1
				}
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

		idx = 0
		for y := 0; y < 8; y++ {
			for x := 0; x < 4; x++ {
				bitsVal <<= 1
				if getChannel(block[idx], splitIndex) > splitValue {
					bitsVal |= 1
				}
				idx++
			}
		}
	}

	bestDiff := 32
	bestPattern := uint32(0x0000ffff)
	codePoint := 0x2584
	inverted := false

	for _, bm := range bitmaps {
		if bm.flags&flags != bm.flags {
			continue
		}
		pattern := bm.pattern
		for i := 0; i < 2; i++ {
			diff := bits.OnesCount32(pattern ^ bitsVal)
			if diff < bestDiff {
				bestDiff = diff
				bestPattern = bm.pattern
				codePoint = bm.codePoint
				inverted = bestPattern != pattern
			}
			pattern = ^pattern
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

	return createCharData(getPixel, x0, y0, codePoint, bestPattern)
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

type persistentZstdDecoder struct {
	once sync.Once
	mu   sync.Mutex
	dec  *zstd.Decoder
	err  error
}

func (p *persistentZstdDecoder) use(fn func(*zstd.Decoder) error) error {
	p.once.Do(func() {
		p.dec, p.err = zstd.NewReader(nil)
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

	// Cache mapping from exact paletteColor to its palette index to avoid
	// recalculating nearest palette entry for the same color.
	colorToIndex map[paletteColor]byte

	payloadBuf bytes.Buffer

	lastPalette         []paletteColor
	lastPaletteExpanded [][3]int
	reuseFgIdx          []byte
	reuseBgIdx          []byte

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

func mirrorFrameHorizontal(frame Frame) Frame {
	if frame.Width <= 0 || frame.Height <= 0 {
		return frame
	}
	rowStride := frame.Width * 3
	required := rowStride * frame.Height
	if len(frame.Data) < required {
		return frame
	}
	mirrored := make([]byte, required)
	for y := 0; y < frame.Height; y++ {
		rowOffset := y * rowStride
		for x := 0; x < frame.Width; x++ {
			src := rowOffset + x*3
			dst := rowOffset + (frame.Width-1-x)*3
			copy(mirrored[dst:dst+3], frame.Data[src:src+3])
		}
	}
	frame.Data = mirrored
	return frame
}

func (e *Encoder) Encode(frame Frame, cols, rows int) ([]byte, error) {
	if len(frame.Data) == 0 || frame.Width <= 0 || frame.Height <= 0 {
		return nil, fmt.Errorf("invalid frame data")
	}
	if len(frame.Data) != frame.Width*frame.Height*3 {
		return nil, fmt.Errorf("frame data size mismatch: got %d, want %d", len(frame.Data), frame.Width*frame.Height*3)
	}
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("cols and rows must be > 0")
	}

	if e.mirror {
		frame = mirrorFrameHorizontal(frame)
	}

	maxCols := int(math.MaxUint16) / 4
	maxRows := int(math.MaxUint16) / 8
	if cols > maxCols || rows > maxRows {
		return nil, fmt.Errorf("cols/rows too large: cols=%d (max %d), rows=%d (max %d)", cols, maxCols, rows, maxRows)
	}

	glyphs, fgColors, bgColors, frameW, frameH, err := e.buildCellData(frame, cols, rows, e.forceBW)
	if err != nil {
		return nil, err
	}

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
		reuseCache := make(map[paletteColor]byte, len(e.lastPalette))

		mismatches := assignPaletteIndices(fgColors, e.lastPalette, e.lastPaletteExpanded, reuseCache, reuseFg, paletteReuseColorErrorTolerance)
		mismatches += assignPaletteIndices(bgColors, e.lastPalette, e.lastPaletteExpanded, reuseCache, reuseBg, paletteReuseColorErrorTolerance)

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
		keyframe = 1
	}

	if keyframe == 1 {
		palette, paletteExpanded := buildPaletteFromColors(fgColors, bgColors, totalCells)
		palBytes = flattenPalette(palette)

		fgIdx := ensureByteSlice(&e.fgIdx, totalCells)
		bgIdx := ensureByteSlice(&e.bgIdx, totalCells)

		e.colorToIndex = make(map[paletteColor]byte, len(palette))
		assignPaletteIndices(fgColors, palette, paletteExpanded, e.colorToIndex, fgIdx, -1)
		assignPaletteIndices(bgColors, palette, paletteExpanded, e.colorToIndex, bgIdx, -1)

		finalFgIdx = fgIdx
		finalBgIdx = bgIdx

		e.storePalette(palette)
	}

	e.payloadBuf.Reset()
	payload := &e.payloadBuf

	payload.WriteString(PayloadMagic)
	payload.WriteByte(keyframe)
	if err := binary.Write(payload, binary.LittleEndian, uint16(cols)); err != nil {
		return nil, err
	}
	if err := binary.Write(payload, binary.LittleEndian, uint16(rows)); err != nil {
		return nil, err
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

	glyphsEnc, err := readField("glyphs")
	if err != nil {
		return nil, err
	}

	var paletteBytes []byte
	if keyframe != 0 {
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

	totalCells := int(cols) * int(rows)

	glyphs, err := unpackGlyphs6(glyphsEnc, totalCells)
	if err != nil {
		return nil, err
	}

	// For experiment: RLE disabled, use raw planes for fg/bg indices.
	fgIdx := fgIdxEnc
	bgIdx := bgIdxEnc

	if len(fgIdx) != totalCells {
		return nil, fmt.Errorf("fgIdx length %d does not match total cells %d", len(fgIdx), totalCells)
	}
	if len(bgIdx) != totalCells {
		return nil, fmt.Errorf("bgIdx length %d does not match total cells %d", len(bgIdx), totalCells)
	}
	var palette []paletteColor
	if keyframe != 0 {
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
			return nil, fmt.Errorf("fgIdx[%d]=%d out of range (palette size %d)", i, idx, palCount)
		}
		copy(fgYCbCr[i*3:], palette[idx][:])

		idx2 := int(bgIdx[i])
		if idx2 < 0 || idx2 >= palCount {
			return nil, fmt.Errorf("bgIdx[%d]=%d out of range (palette size %d)", i, idx2, palCount)
		}
		copy(bgYCbCr[i*3:], palette[idx2][:])
	}

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

	totalCells := cols * rows
	glyphMap := glyphIndexMap()

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

	getPixel := func(x, y int) uint32 {
		if x < 0 || y < 0 || x >= w || y >= h {
			return 0
		}
		sx := xMap[x]
		sy := yMap[y]
		off := (sy*srcW + sx) * 3
		if off < 0 || off+2 >= len(frame.Data) {
			return 0
		}
		yVal := frame.Data[off]
		cb := frame.Data[off+1]
		cr := frame.Data[off+2]
		if bw {
			cb = 128
			cr = 128
		}
		return (uint32(yVal) << 16) | (uint32(cb) << 8) | uint32(cr)
	}

	// Parallel pass: compute glyphs and per-cell fg/bg colors.
	workers := runtime.GOMAXPROCS(0)
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
			for by := start; by < rows; by += workers {
				for bx := 0; bx < cols; bx++ {
					cell := by*cols + bx
					cd := findCharData(getPixel, bx*4, by*8, 0)
					idx, ok := glyphMap[cd.CodePoint]
					if !ok {
						idx = 0
					}
					glyphs[cell] = idx

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
				}
			}
		}(startRow)
	}
	wg.Wait()

	return glyphs, fgColors, bgColors, w, h, nil
}

func buildPaletteFromColors(fgColors, bgColors []paletteColor, totalCells int) ([]paletteColor, [][3]int) {
	paletteMap := make(map[uint16]struct{}, 256)
	packColor := func(y6, cb5, cr5 byte) uint16 {
		return (uint16(y6) << 10) | (uint16(cb5) << 5) | uint16(cr5)
	}

	for i := 0; i < totalCells; i++ {
		fc := fgColors[i]
		y6 := fc[0] >> 2
		cb5 := fc[1] >> 3
		cr5 := fc[2] >> 3
		paletteMap[packColor(y6, cb5, cr5)] = struct{}{}

		bc := bgColors[i]
		y6b := bc[0] >> 2
		cb5b := bc[1] >> 3
		cr5b := bc[2] >> 3
		paletteMap[packColor(y6b, cb5b, cr5b)] = struct{}{}

		if len(paletteMap) >= 256 {
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
	paletteExpanded := make([][3]int, len(paletteCodes))
	for i, code := range paletteCodes {
		y6 := byte((code >> 10) & 0x3f)
		cb5 := byte((code >> 5) & 0x1f)
		cr5 := byte(code & 0x1f)
		palette[i][0] = y6
		palette[i][1] = cb5
		palette[i][2] = cr5
		paletteExpanded[i] = expandPaletteEntry(palette[i])
	}

	return palette, paletteExpanded
}

func expandPaletteEntry(p paletteColor) [3]int {
	return [3]int{
		int((p[0] << 2) | (p[0] >> 4)),
		int((p[1] << 3) | (p[1] >> 2)),
		int((p[2] << 3) | (p[2] >> 2)),
	}
}

func assignPaletteIndices(colors []paletteColor, palette []paletteColor, paletteExpanded [][3]int, cache map[paletteColor]byte, dst []byte, mismatchThreshold int) int {
	if len(colors) != len(dst) {
		return 0
	}
	if len(palette) == 0 {
		for i := range dst {
			dst[i] = 0
		}
		return len(colors)
	}
	mismatches := 0
	for i, color := range colors {
		idx, ok := cache[color]
		var dist int
		if !ok {
			bestIdx := 0
			bestDist := math.MaxInt
			for j := 0; j < len(palette); j++ {
				d := colorDistance(paletteExpanded[j], color)
				if d < bestDist {
					bestDist = d
					bestIdx = j
				}
			}
			idx = byte(bestIdx)
			cache[color] = idx
			dist = bestDist
		} else {
			dist = colorDistance(paletteExpanded[int(idx)], color)
		}
		dst[i] = idx
		if mismatchThreshold >= 0 && dist > mismatchThreshold {
			mismatches++
		}
	}
	return mismatches
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

	var buf bytes.Buffer

	if err := sharedZstdEncoder.use(func(enc *zstd.Encoder) error {
		enc.Reset(&buf)

		if _, err := enc.Write(data); err != nil {
			_ = enc.Close()
			return err
		}
		return enc.Close()
	}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func decompressZstd(data []byte) ([]byte, error) {
	var out bytes.Buffer

	if err := sharedZstdDecoder.use(func(dec *zstd.Decoder) error {
		if err := dec.Reset(bytes.NewReader(data)); err != nil {
			return err
		}
		_, err := out.ReadFrom(dec)
		return err
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
