package device

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A y4m clip (4:2:0) plays as the camera, frames packed Y, Cb, Cr, the
// chroma spread over its pixels, in a loop.
func TestFileCameraY4M(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.y4m")
	w, h := 8, 4
	data := []byte(fmt.Sprintf("YUV4MPEG2 W%d H%d F100:1 Ip A1:1 C420jpeg\n", w, h))
	for f := 0; f < 2; f++ {
		data = append(data, "FRAME\n"...)
		for i := 0; i < w*h; i++ {
			data = append(data, byte(10*f+i))
		}
		for i := 0; i < w*h/4; i++ {
			data = append(data, byte(100+i))
		}
		for i := 0; i < w*h/4; i++ {
			data = append(data, byte(200+i))
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := startFileCamera(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var firsts []byte
	for i := 0; i < 3; i++ { // the third is the first again
		select {
		case f := <-src:
			if f.Width != w || f.Height != h || len(f.Data) != w*h*3 {
				t.Fatalf("frame %d: %dx%d, %d bytes", i, f.Width, f.Height, len(f.Data))
			}
			// The pixel at (3, 2): Y 19, the chroma of (1, 1) of its plane.
			o := (2*w + 3) * 3
			if f.Data[o+1] != 105 || f.Data[o+2] != 205 {
				t.Fatalf("frame %d: chroma %d,%d, want 105,205", i, f.Data[o+1], f.Data[o+2])
			}
			firsts = append(firsts, f.Data[0])
		case <-time.After(2 * time.Second):
			t.Fatal("no frame")
		}
	}
	if firsts[0] != 0 || firsts[1] != 10 || firsts[2] != 0 {
		t.Fatalf("frames' first pixels %v, want 0, 10, then 0 again", firsts)
	}
}

// A recording of cmd/recordframes plays as recorded.
func TestFileCameraFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.frames")
	var data []byte
	data = binary.LittleEndian.AppendUint16(data, 4)
	data = binary.LittleEndian.AppendUint16(data, 2)
	for i := 0; i < 4*2*3; i++ {
		data = append(data, byte(i))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := startFileCamera(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-src:
		if f.Width != 4 || f.Height != 2 || f.Data[5] != 5 {
			t.Fatalf("%dx%d %v", f.Width, f.Height, f.Data[:6])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame")
	}
	if !IsCameraFile("/tmp/Clip.Y4M") || IsCameraFile("/dev/video0") {
		t.Fatal("IsCameraFile")
	}
}
