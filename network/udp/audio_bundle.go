package udp

import (
	"encoding/binary"
	"errors"
	"time"
)

// Voice bundles (protocol 4). Over Yggdrasil every datagram costs about 300
// bytes on the wire whatever it carries, and a 20 ms speech frame is ~13
// bytes: one frame per datagram spent 95% of the voice traffic on headers.
// Consecutive frames now share a datagram. Its payload, after the media
// header (which carries the first frame's media index), is:
//
//	count  1 byte, the number of frames (1..audioBundleMax)
//	lens   count-1 uvarints, the length of every frame but the last
//	frames the frames; frame k has media index first + k*audioFrameTicks
//
// A sender holds a frame for at most audioBundleWait waiting for the next:
// in DTX silence frames come alone and go out after that wait.

const (
	// audioBundleFrames is how many frames a bundle holds: 2 is 40 ms of
	// speech per datagram.
	audioBundleFrames = 2
	// audioBundleWait is how long the first frame waits for the rest: the
	// frames in between plus half a frame, since the next one arrives about
	// a frame later, a few ms either way. 30 ms for 2 frames.
	audioBundleWait = ((audioBundleFrames-1)*audioFrameMillis + audioFrameMillis/2) * time.Millisecond
	// audioBundleMax is the most frames a receiver accepts in one datagram.
	audioBundleMax = 8
)

// appendAudioBundle appends the bundle payload of frames (at least one).
func appendAudioBundle(dst []byte, frames [][]byte) []byte {
	dst = append(dst, byte(len(frames)))
	for _, f := range frames[:len(frames)-1] {
		dst = binary.AppendUvarint(dst, uint64(len(f)))
	}
	for _, f := range frames {
		dst = append(dst, f...)
	}
	return dst
}

var errAudioBundle = errors.New("malformed voice bundle")

// splitAudioBundle returns the frames of a bundle payload. They alias p.
func splitAudioBundle(p []byte) ([][]byte, error) {
	if len(p) < 2 {
		return nil, errAudioBundle
	}
	count := int(p[0])
	if count < 1 || count > audioBundleMax {
		return nil, errAudioBundle
	}
	p = p[1:]
	var lens [audioBundleMax]int
	for k := 0; k < count-1; k++ {
		n, w := binary.Uvarint(p)
		if w <= 0 || n == 0 || n > uint64(len(p)) {
			return nil, errAudioBundle
		}
		lens[k] = int(n)
		p = p[w:]
	}
	frames := make([][]byte, 0, count)
	for k := 0; k < count-1; k++ {
		if lens[k] >= len(p) { // the last frame needs at least a byte too
			return nil, errAudioBundle
		}
		frames = append(frames, p[:lens[k]])
		p = p[lens[k]:]
	}
	if len(p) == 0 {
		return nil, errAudioBundle
	}
	return append(frames, p), nil
}

// audioBundler gathers consecutive encoded frames into bundles.
type audioBundler struct {
	frames   [][]byte
	first    uint16 // media index of frames[0]
	deadline time.Time
}

// add appends a frame with media index index. It returns a finished bundle
// (header + payload) to send first if the frame does not continue the
// pending one, and the pending bundle itself once it is full.
func (b *audioBundler) add(index uint16, frame []byte, now time.Time) (flush [][]byte) {
	if len(b.frames) > 0 && index != advanceMediaIndex(b.first, int32(len(b.frames))*audioFrameTicks) {
		flush = append(flush, b.take())
	}
	if len(b.frames) == 0 {
		b.first = index
		b.deadline = now.Add(audioBundleWait)
	}
	b.frames = append(b.frames, frame)
	if len(b.frames) >= audioBundleFrames {
		flush = append(flush, b.take())
	}
	return flush
}

// due returns the pending bundle if it has waited long enough.
func (b *audioBundler) due(now time.Time) []byte {
	if len(b.frames) == 0 || now.Before(b.deadline) {
		return nil
	}
	return b.take()
}

// take returns the pending frames as a datagram and empties the bundler.
func (b *audioBundler) take() []byte {
	if len(b.frames) == 0 {
		return nil
	}
	header := encodeMediaHeader(mediaKindAudio, b.first)
	out := appendAudioBundle(append([]byte(nil), header[:]...), b.frames)
	b.frames = b.frames[:0]
	return out
}

// pendingDeadline reports when the oldest pending frame must go out (zero:
// nothing pending).
func (b *audioBundler) pendingDeadline() time.Time {
	if len(b.frames) == 0 {
		return time.Time{}
	}
	return b.deadline
}
