package device

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// The crop and the resize as two passes, as before cropResizeRGB.

// centerCropToAspect crops the input RGB24 frame to match the target aspect ratio (targetW/targetH).
func centerCropToAspectReference(f CameraFrame, targetW, targetH int) CameraFrame {
	inW, inH := f.Width, f.Height
	if inW <= 0 || inH <= 0 {
		return f
	}
	ta := float64(targetW) / float64(targetH)
	ia := float64(inW) / float64(inH)
	cropW, cropH := inW, inH
	if ia > ta {
		cropW = int(float64(inH) * ta)
	} else if ia < ta {
		cropH = int(float64(inW) / ta)
	}
	x0 := (inW - cropW) / 2
	y0 := (inH - cropH) / 2
	out := CameraFrame{Width: cropW, Height: cropH, Data: make([]byte, cropW*cropH*3)}
	for y := 0; y < cropH; y++ {
		srcY := y0 + y
		copy(out.Data[y*cropW*3:(y+1)*cropW*3], f.Data[(srcY*inW+x0)*3:(srcY*inW+x0+cropW)*3])
	}
	return out
}

// resizeFrameRGB rescales an RGB24 frame to the specified output dimensions using nearest-neighbor sampling.
func resizeFrameRGBReference(f CameraFrame, outW, outH int) (CameraFrame, error) {
	if f.Width <= 0 || f.Height <= 0 || len(f.Data) < f.Width*f.Height*3 {
		return CameraFrame{}, fmt.Errorf("bad frame")
	}
	if outW <= 0 || outH <= 0 {
		return CameraFrame{}, fmt.Errorf("bad out size")
	}
	out := CameraFrame{Width: outW, Height: outH, Data: make([]byte, outW*outH*3)}
	wIn, hIn := float64(f.Width), float64(f.Height)
	for y := 0; y < outH; y++ {
		sy := int(float64(y) * hIn / float64(outH))
		if sy >= f.Height {
			sy = f.Height - 1
		}
		for x := 0; x < outW; x++ {
			sx := int(float64(x) * wIn / float64(outW))
			if sx >= f.Width {
				sx = f.Width - 1
			}
			srcIdx := (sy*f.Width + sx) * 3
			dstIdx := (y*outW + x) * 3
			copy(out.Data[dstIdx:dstIdx+3], f.Data[srcIdx:srcIdx+3])
		}
	}
	return out, nil
}

func TestCropResizeMatchesTwoPasses(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for _, sz := range [][2]int{{1280, 720}, {1920, 1080}, {640, 480}, {352, 288}, {1552, 1552}, {100, 300}, {353, 289}} {
		f := CameraFrame{Width: sz[0], Height: sz[1], Data: make([]byte, sz[0]*sz[1]*3)}
		rng.Read(f.Data)
		got, err := cropResizeRGB(f, 352, 288)
		if err != nil {
			t.Fatal(err)
		}
		want, err := resizeFrameRGBReference(centerCropToAspectReference(f, 352, 288), 352, 288)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Data, want.Data) || got.Width != want.Width || got.Height != want.Height {
			t.Fatalf("%v: one-pass crop+resize differs", sz)
		}
	}
	if _, err := cropResizeRGB(CameraFrame{Width: 10, Height: 10, Data: make([]byte, 5)}, 352, 288); err == nil {
		t.Fatal("a short frame must be rejected")
	}
}

func BenchmarkCropResize720p(b *testing.B) {
	f := CameraFrame{Width: 1280, Height: 720, Data: make([]byte, 1280*720*3)}
	for i := 0; i < b.N; i++ {
		cropResizeRGB(f, 352, 288)
	}
}

func BenchmarkCropResize720pTwoPasses(b *testing.B) {
	f := CameraFrame{Width: 1280, Height: 720, Data: make([]byte, 1280*720*3)}
	for i := 0; i < b.N; i++ {
		resizeFrameRGBReference(centerCropToAspectReference(f, 352, 288), 352, 288)
	}
}
