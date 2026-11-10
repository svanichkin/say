package udp

import (
	"encoding/binary"
	"errors"
	"sync"
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
// in DTX silence frames come alone and go out after that wait. A video
// datagram leaving meanwhile takes the waiting frames along (see
// appendVideoVoice), which saves the voice datagram altogether.

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

// add appends a frame with media index index. It returns the pending
// bundle (header + payload) to send first if the frame does not continue it
// or would make it longer than audioBundleFrames. A full bundle still waits
// for its deadline: a video datagram may take it along meanwhile.
func (b *audioBundler) add(index uint16, frame []byte, now time.Time) (flush [][]byte) {
	if len(b.frames) > 0 && (len(b.frames) >= audioBundleFrames ||
		index != advanceMediaIndex(b.first, int32(len(b.frames))*audioFrameTicks)) {
		flush = append(flush, b.take())
	}
	if len(b.frames) == 0 {
		b.first = index
		b.deadline = now.Add(audioBundleWait)
	}
	b.frames = append(b.frames, frame)
	return flush
}

// takeFrames empties the bundler and returns its frames and the first one's
// media index (ok false: nothing pending).
func (b *audioBundler) takeFrames() (first uint16, frames [][]byte, ok bool) {
	if len(b.frames) == 0 {
		return 0, nil, false
	}
	first, frames = b.first, append([][]byte(nil), b.frames...)
	b.frames = b.frames[:0]
	return first, frames, true
}

// voiceMux is the voice bundler shared by a session's voice sender, which
// fills it and sends due bundles, and its video sender, which takes the
// waiting frames along in its datagrams.
type voiceMux struct {
	mu sync.Mutex
	b  audioBundler
}

func (m *voiceMux) add(index uint16, frame []byte, now time.Time) [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.add(index, frame, now)
}

func (m *voiceMux) due(now time.Time) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.due(now)
}

func (m *voiceMux) take() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.take()
}

func (m *voiceMux) pendingDeadline() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.pendingDeadline()
}

// takeFrames hands the waiting frames to a video datagram.
func (m *voiceMux) takeFrames() (first uint16, frames [][]byte, ok bool) {
	if m == nil {
		return 0, nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.takeFrames()
}

// Video datagrams (protocol 4), after the media header:
//
//	seq    1 byte, the frame counter (loss detection)
//	flags  1 byte; videoFlagVoice: voice frames follow
//	voice  if videoFlagVoice: the first voice frame's media index (2 bytes,
//	       big endian), the bundle's length (uvarint), the bundle (as in a
//	       voice datagram)
//	video  the rest: the encoded frame
const (
	videoFlagsBytes = 1
	videoFlagVoice  = 1
)

// appendVideoHead appends a video datagram's seq, flags and voice part.
func appendVideoHead(dst []byte, seq byte, voiceFirst uint16, voice [][]byte) []byte {
	if len(voice) == 0 {
		return append(dst, seq, 0)
	}
	dst = append(dst, seq, videoFlagVoice, byte(voiceFirst>>8), byte(voiceFirst))
	bundle := appendAudioBundle(nil, voice)
	dst = binary.AppendUvarint(dst, uint64(len(bundle)))
	return append(dst, bundle...)
}

var errVideoHead = errors.New("malformed video datagram")

// splitVideoHead parses what follows a video datagram's media header: the
// frame counter, the voice frames it carries (if any) and the video payload.
// Everything returned aliases p.
func splitVideoHead(p []byte) (seq byte, voiceFirst uint16, voice [][]byte, video []byte, err error) {
	if len(p) < 1+videoFlagsBytes {
		return 0, 0, nil, nil, errVideoHead
	}
	seq, flags, p := p[0], p[1], p[2:]
	if flags&^videoFlagVoice != 0 {
		return 0, 0, nil, nil, errVideoHead
	}
	if flags&videoFlagVoice != 0 {
		if len(p) < 2 {
			return 0, 0, nil, nil, errVideoHead
		}
		voiceFirst, p = uint16(p[0])<<8|uint16(p[1]), p[2:]
		n, w := binary.Uvarint(p)
		if w <= 0 || n > uint64(len(p)-w) {
			return 0, 0, nil, nil, errVideoHead
		}
		if voice, err = splitAudioBundle(p[w : w+int(n)]); err != nil {
			return 0, 0, nil, nil, err
		}
		p = p[w+int(n):]
	}
	return seq, voiceFirst & mediaIndexMask, voice, p, nil
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
