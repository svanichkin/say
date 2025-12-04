package codec

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

// -----------------------------------------------------------------------------
// Benchmarks for comparing Go's JPEG encoder with the custom TIV encoder.
//
// To use these, copy this code into a separate file named, for example,
// `codec_benchmark_test.go` in the same package, and adjust EncodeTIV import
///references.
//
// The benchmarks assume there is a PNG image `test.png` in the project root.
// -----------------------------------------------------------------------------

func loadTestImage(b *testing.B) image.Image {
	f, err := os.Open("test.png")
	if err != nil {
		b.Fatalf("failed to open test.png: %v", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		b.Fatalf("failed to decode test.png: %v", err)
	}
	return img
}

func BenchmarkJPEG(b *testing.B) {
	img := loadTestImage(b)

	buf := &bytes.Buffer{}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf.Reset()
		if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 80}); err != nil {
			b.Fatalf("jpeg encode failed: %v", err)
		}
	}
}

func BenchmarkTextFrame(b *testing.B) {
	img := loadTestImage(b)
	frame := imageToFrameYCbCr(img)

	bounds := img.Bounds()
	cols := bounds.Dx() / 4
	rows := bounds.Dy() / 8

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := Encode(frame, cols, rows); err != nil {
			b.Fatalf("textFrame encode failed: %v", err)
		}
	}
}

func imageToFrameYCbCr(img image.Image) Frame {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	data := make([]byte, w*h*3)
	idx := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.YCbCrModel.Convert(img.At(x, y)).(color.YCbCr)
			data[idx] = c.Y
			data[idx+1] = c.Cb
			data[idx+2] = c.Cr
			idx += 3
		}
	}
	return Frame{
		Data:   data,
		Width:  w,
		Height: h,
	}
}
