package udp

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestAudioTraceStoresSynchronousChannels(t *testing.T) {
	dir := t.TempDir()
	trace, err := newAudioTrace(dir)
	if err != nil {
		t.Fatal(err)
	}
	trace.record([]int16{1, -2}, []int16{3, -4}, []int16{5, -6})
	trace.close()
	files, err := filepath.Glob(filepath.Join(dir, "*.wav"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %v, %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 56 || string(b[:4]) != "RIFF" || binary.LittleEndian.Uint32(b[4:]) != 48 || binary.LittleEndian.Uint32(b[40:]) != 12 {
		t.Fatal("invalid WAV sizes")
	}
	if binary.LittleEndian.Uint16(b[22:]) != 3 || binary.LittleEndian.Uint32(b[24:]) != 8000 {
		t.Fatal("wrong format")
	}
	for i, want := range []int16{1, 3, 5, -2, -4, -6} {
		if got := int16(binary.LittleEndian.Uint16(b[44+i*2:])); got != want {
			t.Fatalf("sample %d=%d, want %d", i, got, want)
		}
	}
	if trace, err := newAudioTrace(""); trace != nil || err != nil {
		t.Fatal("recording enabled without opt-in")
	}
}
