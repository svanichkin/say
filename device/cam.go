package device

import (
	"context"
	"fmt"

	gocam "github.com/svanichkin/Gocam"
)

// CameraFrame is the raw RGB frame type produced by the camera backend.
type CameraFrame = gocam.Frame

// StartCameraStream starts camera capture using gocam and returns a frame channel.
// Frames are center-cropped to CIF aspect and resized to 352x288 before being sent to consumers.
func StartCameraStream(ctx context.Context) (<-chan CameraFrame, error) {
	src, err := gocam.StartStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("camera start: %w", err)
	}

	out := make(chan CameraFrame, 8)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-src:
				if !ok {
					return
				}
				// 1) center-crop to CIF aspect, 2) resize to 352x288
				cropped := centerCropToAspect(f, 352, 288)
				rf, err := resizeFrameRGB(cropped, 352, 288)
				if err != nil {
					continue
				}
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
	return out, nil
}

// centerCropToAspect crops the input RGB24 frame to match the target aspect ratio (targetW/targetH).
func centerCropToAspect(f CameraFrame, targetW, targetH int) CameraFrame {
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
func resizeFrameRGB(f CameraFrame, outW, outH int) (CameraFrame, error) {
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
