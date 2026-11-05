package device

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

// TestRecordCameraFrames writes about two seconds of camera frames, as the
// video encoder receives them, to SAY_RECORD_FRAMES (for codec benchmarks
// on real sensor noise). Each frame: width and height as uint16 LE, then
// width*height*3 bytes.
func TestRecordCameraFrames(t *testing.T) {
	path := os.Getenv("SAY_RECORD_FRAMES")
	if path == "" {
		t.Skip("set SAY_RECORD_FRAMES=<file> to record camera frames")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames, err := StartCameraStream(ctx)
	if err != nil {
		t.Skipf("camera unavailable: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	skip := time.Now().Add(time.Second) // let exposure settle
	n := 0
	for n < 60 {
		fr, ok := <-frames
		if !ok {
			t.Fatal("camera stream closed")
		}
		if time.Now().Before(skip) {
			continue
		}
		var hdr [4]byte
		binary.LittleEndian.PutUint16(hdr[:2], uint16(fr.Width))
		binary.LittleEndian.PutUint16(hdr[2:], uint16(fr.Height))
		f.Write(hdr[:])
		f.Write(fr.Data)
		n++
	}
	t.Logf("recorded %d frames to %s", n, path)
}
