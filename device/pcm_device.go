package device

import (
	"strconv"
	"strings"
)

// PCM helpers for backends that talk to the sound device directly
// (audio_plan9.go); free of build tags so they are tested everywhere.

// defaultDeviceRate is audio(3)'s default sampling rate.
const defaultDeviceRate = 44100

// parseAudioSpeed finds the "speed N" line of an audio(3) or mixfs volume
// file; defaultDeviceRate if there is none.
func parseAudioSpeed(volume string) int {
	for _, line := range strings.Split(volume, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "speed" {
			if n, err := strconv.Atoi(f[len(f)-1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return defaultDeviceRate
}

// appendStereoS16 appends mono samples as 16-bit little-endian stereo
// frames, the same sample on both channels.
func appendStereoS16(dst []byte, mono []float32) []byte {
	for _, v := range mono {
		s := clampInt16(v)
		lo, hi := byte(s), byte(s>>8)
		dst = append(dst, lo, hi, lo, hi)
	}
	return dst
}

// appendMonoFromStereoS16 appends the average of each 16-bit little-endian
// stereo frame of src; a trailing partial frame is ignored.
func appendMonoFromStereoS16(dst []float32, src []byte) []float32 {
	for i := 0; i+4 <= len(src); i += 4 {
		l := int16(src[i]) | int16(src[i+1])<<8
		r := int16(src[i+2]) | int16(src[i+3])<<8
		dst = append(dst, (float32(l)+float32(r))/2)
	}
	return dst
}
