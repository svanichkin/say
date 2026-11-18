package codec

import (
	"strings"
	"testing"
)

func TestDecodeRejectsOversizedPayloads(t *testing.T) {
	// A header claiming more cells than allowed must fail before allocating planes.
	hdr := []byte(PayloadMagic)
	hdr = append(hdr, frameTypeKey, 0xff, 0xff, 0xff, 0xff)
	if _, err := NewDecoder().Decode(hdr); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("65535x65535 frame: err = %v, want a size limit error", err)
	}
	// An unknown frame type is refused.
	hdr = append([]byte(PayloadMagic), 9, 1, 0, 1, 0)
	if _, err := NewDecoder().Decode(hdr); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("frame type 9: err = %v, want an unknown type error", err)
	}
}

func TestEncodeLargestFrameFitsDatagram(t *testing.T) {
	const w, h = 640, 640
	data := make([]byte, w*h*3)
	for i := range data {
		data[i] = byte(i*7919 + i/3) // high-entropy content compresses poorly
	}
	enc := NewEncoder(false, 0, 25, false)
	out, err := enc.Encode(Frame{Data: data, Width: w, Height: h}, MaxFrameCols, MaxFrameRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxMediaTestDatagram {
		t.Fatalf("largest frame encodes to %d bytes, over the %d datagram limit", len(out), maxMediaTestDatagram)
	}
	if _, err := enc.Encode(Frame{Data: data, Width: w, Height: h}, MaxFrameCols+1, MaxFrameRows); err == nil {
		t.Fatal("encoding above MaxFrameCols succeeded")
	}
}

// Mirrors network/udp maxMediaDatagram minus its 2-byte media header.
const maxMediaTestDatagram = 60000 - 2
