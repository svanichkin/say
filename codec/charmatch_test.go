package codec

import (
	"math/rand"
	"testing"
)

// blockGetPixel serves a cell block through the old getPixel interface.
func blockGetPixel(b *cellBlock) func(int, int) uint32 {
	return func(x, y int) uint32 { return b[y*4+x] }
}

func randomBlock(rng *rand.Rand, kind int) cellBlock {
	var b cellBlock
	pick := func() uint32 { return rng.Uint32() & 0xffffff }
	switch kind {
	case 0: // camera noise: nearly every pixel distinct
		for i := range b {
			b[i] = pick()
		}
	case 1: // two dominant colours plus a few others
		c := [4]uint32{pick(), pick(), pick(), pick()}
		for i := range b {
			b[i] = c[min(rng.Intn(10), 3)%4]
			if rng.Intn(10) < 7 {
				b[i] = c[rng.Intn(2)]
			}
		}
	case 2: // smooth gradient with small noise
		base := [3]int{rng.Intn(200), rng.Intn(200), rng.Intn(200)}
		for i := range b {
			v := [3]int{}
			for ch := range v {
				v[ch] = min(255, base[ch]+i*rng.Intn(3)+rng.Intn(4))
			}
			b[i] = uint32(v[0])<<16 | uint32(v[1])<<8 | uint32(v[2])
		}
	default: // flat with one or two odd pixels, ties between counts
		c := pick()
		for i := range b {
			b[i] = c
		}
		for k := rng.Intn(3); k > 0; k-- {
			b[rng.Intn(32)] = pick()
		}
	}
	return b
}

func TestFindCharDataBlockMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 200000; n++ {
		b := randomBlock(rng, n%4)
		for _, flags := range []int{0, 1, 2, 3} {
			got := findCharDataBlock(&b, flags)
			want := findCharDataReference(blockGetPixel(&b), 0, 0, flags)
			if got != want {
				t.Fatalf("block %d kind %d flags %d: got %+v, want %+v (%x)", n, n%4, flags, got, want, b)
			}
		}
		pattern := rng.Uint32()
		if got, want := createCharDataBlock(&b, 0x2584, pattern), createCharDataReference(blockGetPixel(&b), 0, 0, 0x2584, pattern); got != want {
			t.Fatalf("createCharData: got %+v, want %+v", got, want)
		}
	}
}

// referenceCells computes the encoder's cells the way it did before: a
// getPixel closure over the scaled, cropped source and a map lookup.
func referenceCells(frame Frame, cols, rows int, bw bool) ([]byte, []paletteColor, []paletteColor) {
	var e Encoder
	_, _, _, w, h, _ := e.buildCellData(frame, cols, rows, bw) // fills e.xMap / e.yMap
	xMap, yMap := e.xMap[:w], e.yMap[:h]
	getPixel := func(x, y int) uint32 {
		off := (yMap[y]*frame.Width + xMap[x]) * 3
		if off < 0 || off+2 >= len(frame.Data) {
			return 0
		}
		cb, cr := frame.Data[off+1], frame.Data[off+2]
		if bw {
			cb, cr = 128, 128
		}
		return uint32(frame.Data[off])<<16 | uint32(cb)<<8 | uint32(cr)
	}
	glyphMap := glyphIndexMap()
	glyphs := make([]byte, cols*rows)
	fg := make([]paletteColor, cols*rows)
	bg := make([]paletteColor, cols*rows)
	for by := 0; by < rows; by++ {
		for bx := 0; bx < cols; bx++ {
			cell := by*cols + bx
			cd := findCharDataReference(getPixel, bx*4, by*8, 0)
			glyphs[cell] = glyphMap[cd.CodePoint]
			fg[cell] = paletteColor{byte(clampByte(cd.FgColor[0])), byte(clampByte(cd.FgColor[1])), byte(clampByte(cd.FgColor[2]))}
			bg[cell] = paletteColor{byte(clampByte(cd.BgColor[0])), byte(clampByte(cd.BgColor[1])), byte(clampByte(cd.BgColor[2]))}
		}
	}
	return glyphs, fg, bg
}

func TestEncoderCellsMatchReference(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for _, tc := range []struct{ w, h, cols, rows int }{{640, 480, 98, 58}, {333, 251, 37, 23}, {1280, 720, 40, 40}, {64, 64, 30, 10}} {
		frame := Frame{Width: tc.w, Height: tc.h, Data: make([]byte, tc.w*tc.h*3)}
		for y := 0; y < tc.h; y++ {
			for x := 0; x < tc.w; x++ {
				off := (y*tc.w + x) * 3
				frame.Data[off] = byte((x*255/tc.w + rng.Intn(12)) & 0xff)
				frame.Data[off+1] = byte(100 + y*50/tc.h + rng.Intn(3))
				frame.Data[off+2] = byte(140 - (x/40)*3)
			}
		}
		for _, bw := range []bool{false, true} {
			var e Encoder
			glyphs, fg, bg, _, _, err := e.buildCellData(frame, tc.cols, tc.rows, bw)
			if err != nil {
				t.Fatal(err)
			}
			wantG, wantFg, wantBg := referenceCells(frame, tc.cols, tc.rows, bw)
			for i := range wantG {
				if glyphs[i] != wantG[i] || fg[i] != wantFg[i] || bg[i] != wantBg[i] {
					t.Fatalf("%dx%d->%dx%d bw=%v cell %d: got %d %v %v, want %d %v %v", tc.w, tc.h, tc.cols, tc.rows, bw, i, glyphs[i], fg[i], bg[i], wantG[i], wantFg[i], wantBg[i])
				}
			}
		}
	}
}

func BenchmarkBuildCellData(b *testing.B) {
	rng := rand.New(rand.NewSource(3))
	frame := Frame{Width: 640, Height: 480, Data: make([]byte, 640*480*3)}
	rng.Read(frame.Data)
	var e Encoder
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, _, _, _, err := e.buildCellData(frame, 98, 58, false); err != nil {
			b.Fatal(err)
		}
	}
}
