package device

import (
	"context"
	"fmt"
	"time"

	gocam "github.com/svanichkin/gocam"
)

// CameraFrame is an RGB frame with the monotonic time at which camera capture
// delivered it to this process.
type CameraFrame struct {
	Data          []byte
	Width, Height int
	CapturedAt    time.Time
}

// StartCameraStream starts camera capture using gocam and returns a frame channel.
// Frames are center-cropped to CIF aspect and resized to 352x288 before being sent to consumers.
func StartCameraStream(ctx context.Context) (<-chan CameraFrame, error) {
	src, err := gocam.StartStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("camera start: %w", err)
	}
	return cameraFrames(ctx, src), nil
}

// cameraFrames closes its output only after the native source closes.
func cameraFrames(ctx context.Context, src <-chan gocam.Frame) <-chan CameraFrame {
	out := make(chan CameraFrame, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				// gocam closes src only after stopping the native capture session.
				// Keep our channel open until that completes, so reconnect can join it.
				for range src {
				}
				return
			case f, ok := <-src:
				if !ok {
					return
				}
				capturedAt := time.Now()
				frame := CameraFrame{Data: f.Data, Width: f.Width, Height: f.Height, CapturedAt: capturedAt}
				// Center-crop to CIF aspect and resize to 352x288; gocam
				// usually delivers exactly that already (its own buffer).
				rf := frame
				if frame.Width != 352 || frame.Height != 288 {
					var err error
					if rf, err = cropResizeRGB(frame, 352, 288); err != nil {
						continue
					}
				}
				rf.CapturedAt = capturedAt
				select {
				case out <- rf:
				default:
					// if the consumer is lagging behind, drop the oldest frame and overwrite with the newest one
					select {
					case <-out:
					default:
					}
					out <- rf
				}
			}
		}
	}()
	return out
}

// cropResizeRGB center-crops an RGB24 frame to the aspect of outW x outH
// and scales it to that size by nearest-neighbour sampling, in one pass:
// only the sampled pixels are read, without an intermediate cropped copy
// (a 720p frame is 2.7 MB). The sampling positions are those of a crop
// followed by a resize, computed once per row and column.
func cropResizeRGB(f CameraFrame, outW, outH int) (CameraFrame, error) {
	inW, inH := f.Width, f.Height
	if inW <= 0 || inH <= 0 || len(f.Data) < inW*inH*3 {
		return CameraFrame{}, fmt.Errorf("bad frame")
	}
	if outW <= 0 || outH <= 0 {
		return CameraFrame{}, fmt.Errorf("bad out size")
	}
	ta := float64(outW) / float64(outH)
	ia := float64(inW) / float64(inH)
	cropW, cropH := inW, inH
	if ia > ta {
		cropW = int(float64(inH) * ta)
	} else if ia < ta {
		cropH = int(float64(inW) / ta)
	}
	if cropW <= 0 || cropH <= 0 {
		return CameraFrame{}, fmt.Errorf("bad frame")
	}
	x0 := (inW - cropW) / 2
	y0 := (inH - cropH) / 2

	colOff := make([]int, outW)
	for x := range colOff {
		sx := min(int(float64(x)*float64(cropW)/float64(outW)), cropW-1)
		colOff[x] = (x0 + sx) * 3
	}
	out := CameraFrame{Width: outW, Height: outH, Data: make([]byte, outW*outH*3)}
	dst := out.Data
	for y := 0; y < outH; y++ {
		sy := min(int(float64(y)*float64(cropH)/float64(outH)), cropH-1)
		row := f.Data[(y0+sy)*inW*3:]
		d := dst[y*outW*3 : (y+1)*outW*3]
		for x, o := range colOff {
			d[x*3], d[x*3+1], d[x*3+2] = row[o], row[o+1], row[o+2]
		}
	}
	return out, nil
}
