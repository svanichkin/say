package device

// Motion-adaptive temporal denoising of camera frames.
//
// On a static scene the sensor still delivers slightly different values
// every frame. The text-mode codec has one glyph and two colours per cell,
// so a pixel block near a decision boundary flips between glyphs or palette
// colours from frame to frame and a still picture shimmers. Pixels whose
// change stays within the noise threshold are moved only part of the way
// towards the new value; larger changes (motion, a light switched on) pass
// through at once, so motion stays sharp.
const (
	denoiseThreshold = 12 // per-channel change (0..255) still treated as noise
	denoiseShift     = 2  // noise moves the pixel by 1/(1<<shift) of the change
)

type temporalDenoiser struct {
	prev          []byte
	width, height int
}

// apply filters an RGB24 frame in place.
func (d *temporalDenoiser) apply(data []byte, width, height int) {
	if len(data) != width*height*3 || len(data) == 0 {
		return
	}
	if d.width != width || d.height != height || len(d.prev) != len(data) {
		d.prev = append(d.prev[:0], data...)
		d.width, d.height = width, height
		return
	}
	prev := d.prev
	for i := 0; i+2 < len(data); i += 3 {
		dr := int(data[i]) - int(prev[i])
		dg := int(data[i+1]) - int(prev[i+1])
		db := int(data[i+2]) - int(prev[i+2])
		if abs(dr) > denoiseThreshold || abs(dg) > denoiseThreshold || abs(db) > denoiseThreshold {
			prev[i], prev[i+1], prev[i+2] = data[i], data[i+1], data[i+2] // motion
			continue
		}
		prev[i] = byte(int(prev[i]) + dr>>denoiseShift)
		prev[i+1] = byte(int(prev[i+1]) + dg>>denoiseShift)
		prev[i+2] = byte(int(prev[i+2]) + db>>denoiseShift)
		data[i], data[i+1], data[i+2] = prev[i], prev[i+1], prev[i+2]
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
