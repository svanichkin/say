package device

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	gocam "github.com/svanichkin/gocam"
)

// A video file as the camera (-camera clip.y4m): where there is no camera
// (a virtual machine, Plan 9 without a USB one) or for a repeatable picture.
// The clip plays in a loop at its own rate and goes the way the camera's
// frames go. Two formats, both uncompressed, read without libraries:
//
//   - .y4m, YUV4MPEG2 (4:2:0, 4:2:2, 4:4:4 or mono), what ffmpeg writes
//     (ffmpeg -i clip.mp4 -vf scale=352:288 -pix_fmt yuv420p clip.y4m) and
//     what video test sets come in;
//   - .frames, what cmd/recordframes records from a camera: per frame its
//     width and height (uint16 LE), then its pixels as the camera gives them.
//
// Frames are packed Y, Cb, Cr per pixel, as gocam delivers them.

var cameraFile atomic.Pointer[string]

// SetCameraFile makes a video file the camera ("": the camera again).
func SetCameraFile(path string) { cameraFile.Store(&path) }

// IsCameraFile reports whether path names a video file Say plays as a camera.
func IsCameraFile(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".y4m") || strings.HasSuffix(p, ".frames")
}

// fileCameraFPS is the rate of a .frames recording (the camera's).
const fileCameraFPS = 30

func startFileCamera(ctx context.Context, path string) (<-chan gocam.Frame, error) {
	open := func() (frameReader, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(strings.ToLower(path), ".y4m") {
			return newY4MReader(f)
		}
		return &framesReader{f: f, r: bufio.NewReaderSize(f, 1<<20)}, nil
	}
	r, err := open()
	if err != nil {
		return nil, fmt.Errorf("camera file: %w", err)
	}
	out := make(chan gocam.Frame, 1)
	go func() {
		defer close(out)
		defer func() { r.close() }()
		tick := time.NewTicker(r.interval())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			f, err := r.next()
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// Again from the start: a loop.
				r.close()
				if r, err = open(); err != nil {
					return
				}
				if f, err = r.next(); err != nil {
					return
				}
			} else if err != nil {
				return
			}
			f.CapturedAt = time.Now()
			select {
			case out <- f:
			default: // the reader lags: this frame goes
			}
		}
	}()
	return out, nil
}

type frameReader interface {
	next() (gocam.Frame, error)
	interval() time.Duration
	close()
}

type framesReader struct {
	f *os.File
	r *bufio.Reader
}

func (fr *framesReader) next() (gocam.Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(fr.r, hdr[:]); err != nil {
		return gocam.Frame{}, err
	}
	w, h := int(binary.LittleEndian.Uint16(hdr[:2])), int(binary.LittleEndian.Uint16(hdr[2:]))
	if w == 0 || h == 0 {
		return gocam.Frame{}, fmt.Errorf("frames: a %dx%d frame", w, h)
	}
	data := make([]byte, w*h*3)
	if _, err := io.ReadFull(fr.r, data); err != nil {
		return gocam.Frame{}, err
	}
	return gocam.Frame{Data: data, Width: w, Height: h}, nil
}

func (fr *framesReader) interval() time.Duration { return time.Second / fileCameraFPS }
func (fr *framesReader) close()                  { fr.f.Close() }

// y4mReader reads YUV4MPEG2: a header line, then frames, each a FRAME line
// and the Y, Cb and Cr planes.
type y4mReader struct {
	f         *os.File
	r         *bufio.Reader
	w, h      int
	cw, chh   int // the chroma planes' size
	mono      bool
	rate      time.Duration
	y, cb, cr []byte
}

func newY4MReader(f *os.File) (*y4mReader, error) {
	y := &y4mReader{f: f, r: bufio.NewReaderSize(f, 1<<20), rate: time.Second / 25}
	line, err := y.r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "YUV4MPEG2 ") {
		f.Close()
		return nil, fmt.Errorf("y4m: not a YUV4MPEG2 file")
	}
	colour := "420"
	for _, p := range strings.Fields(line)[1:] {
		v := p[1:]
		switch p[0] {
		case 'W':
			y.w, _ = strconv.Atoi(v)
		case 'H':
			y.h, _ = strconv.Atoi(v)
		case 'F':
			if n, d, ok := strings.Cut(v, ":"); ok {
				num, _ := strconv.Atoi(n)
				den, _ := strconv.Atoi(d)
				if num > 0 && den > 0 {
					y.rate = time.Duration(float64(time.Second) * float64(den) / float64(num))
				}
			}
		case 'C':
			colour = v
		}
	}
	switch {
	case strings.HasPrefix(colour, "420"):
		y.cw, y.chh = (y.w+1)/2, (y.h+1)/2
	case strings.HasPrefix(colour, "422"):
		y.cw, y.chh = (y.w+1)/2, y.h
	case strings.HasPrefix(colour, "444"):
		y.cw, y.chh = y.w, y.h
	case strings.HasPrefix(colour, "mono"):
		y.mono = true
	default:
		f.Close()
		return nil, fmt.Errorf("y4m: colour %q not read", colour)
	}
	if y.w <= 0 || y.h <= 0 {
		f.Close()
		return nil, fmt.Errorf("y4m: no size")
	}
	y.y = make([]byte, y.w*y.h)
	y.cb = make([]byte, y.cw*y.chh)
	y.cr = make([]byte, y.cw*y.chh)
	return y, nil
}

func (y *y4mReader) next() (gocam.Frame, error) {
	line, err := y.r.ReadString('\n')
	if err != nil {
		return gocam.Frame{}, err
	}
	if !strings.HasPrefix(line, "FRAME") {
		return gocam.Frame{}, fmt.Errorf("y4m: %q where a frame begins", strings.TrimSpace(line))
	}
	if _, err := io.ReadFull(y.r, y.y); err != nil {
		return gocam.Frame{}, err
	}
	if !y.mono {
		if _, err := io.ReadFull(y.r, y.cb); err != nil {
			return gocam.Frame{}, err
		}
		if _, err := io.ReadFull(y.r, y.cr); err != nil {
			return gocam.Frame{}, err
		}
	}
	data := make([]byte, y.w*y.h*3)
	for row := 0; row < y.h; row++ {
		crow := row * y.chh / y.h
		for col := 0; col < y.w; col++ {
			o := (row*y.w + col) * 3
			data[o] = y.y[row*y.w+col]
			if y.mono {
				data[o+1], data[o+2] = 128, 128
				continue
			}
			ci := crow*y.cw + col*y.cw/y.w
			data[o+1], data[o+2] = y.cb[ci], y.cr[ci]
		}
	}
	return gocam.Frame{Data: data, Width: y.w, Height: y.h}, nil
}

func (y *y4mReader) interval() time.Duration { return y.rate }
func (y *y4mReader) close()                  { y.f.Close() }
