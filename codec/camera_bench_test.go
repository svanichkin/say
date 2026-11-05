package codec

import (
	"encoding/binary"
	"os"
	"testing"
)

// loadCameraFrames reads frames recorded by device.TestRecordCameraFrames
// from SAY_CAM_FRAMES.
func loadCameraFrames(b *testing.B) []Frame {
	path := os.Getenv("SAY_CAM_FRAMES")
	if path == "" {
		b.Skip("set SAY_CAM_FRAMES to frames recorded with SAY_RECORD_FRAMES (device package)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	var frames []Frame
	for len(data) >= 4 {
		w, h := int(binary.LittleEndian.Uint16(data)), int(binary.LittleEndian.Uint16(data[2:]))
		n := w * h * 3
		frames = append(frames, Frame{Data: data[4 : 4+n], Width: w, Height: h})
		data = data[4+n:]
	}
	return frames
}

// BenchmarkEncodeCamera encodes recorded camera frames in order, as the call
// does: the network encoder (no mirror) and the self-preview one (mirror).
func BenchmarkEncodeCamera(b *testing.B) {
	frames := loadCameraFrames(b)
	for _, bc := range []struct {
		name       string
		mirror     bool
		cols, rows int
	}{
		{"net_88x36", false, 88, 36},
		{"preview_88x36", true, 88, 36},
		{"net_160x66", false, 160, 66},
	} {
		b.Run(bc.name, func(b *testing.B) {
			enc := NewEncoder(false, 60, 30, bc.mirror)
			enc.SetShapeHold(10)
			b.ResetTimer()
			total := 0
			for i := 0; i < b.N; i++ {
				out, err := enc.Encode(frames[i%len(frames)], bc.cols, bc.rows)
				if err != nil {
					b.Fatal(err)
				}
				total += len(out)
			}
			b.ReportMetric(float64(total)/float64(b.N), "B/frame")
		})
	}
}
