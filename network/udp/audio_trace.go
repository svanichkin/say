package udp

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"sync/atomic"
)

// Optional three-channel PCM recording: raw microphone, the exact reference
// passed to Cancel, and processed microphone. File I/O never blocks capture.
// Opt in with SAY_AUDIO_TRACE=directory; files are private and unique per call.
type audioTrace struct {
	frames  chan []byte
	done    chan struct{}
	dropped atomic.Uint64
}

func newAudioTrace(dir string) (*audioTrace, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "say-audio-*.wav")
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(wavHeader(0)); err != nil {
		f.Close()
		return nil, err
	}
	t := &audioTrace{frames: make(chan []byte, 64), done: make(chan struct{})}
	log.Printf("[audio/trace] recording mic/reference/processed: %s", f.Name())
	go func() {
		defer close(t.done)
		defer f.Close()
		var size uint32
		var failure error
		for data := range t.frames {
			if failure != nil {
				continue
			}
			// Keep a valid RIFF size even if the diagnostic is left on for days.
			if uint64(size)+uint64(len(data))+36 > uint64(^uint32(0)) {
				failure = fmt.Errorf("WAV size limit reached")
				continue
			}
			n, err := f.Write(data)
			size += uint32(n)
			if err != nil {
				failure = err
			}
		}
		if _, err := f.WriteAt(wavHeader(size), 0); err != nil && failure == nil {
			failure = err
		}
		if failure != nil {
			log.Printf("[audio/trace] %s: %v", f.Name(), failure)
		}
		log.Printf("[audio/trace] saved %s (%d diagnostic frames dropped)", f.Name(), t.dropped.Load())
	}()
	return t, nil
}

func (t *audioTrace) record(mic, ref, out []int16) {
	if t == nil {
		return
	}
	n := min(len(mic), min(len(ref), len(out)))
	data := make([]byte, n*6)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(data[i*6:], uint16(mic[i]))
		binary.LittleEndian.PutUint16(data[i*6+2:], uint16(ref[i]))
		binary.LittleEndian.PutUint16(data[i*6+4:], uint16(out[i]))
	}
	select {
	case t.frames <- data:
	default:
		t.dropped.Add(1)
	}
}
func (t *audioTrace) close() { close(t.frames); <-t.done }
func wavHeader(size uint32) []byte {
	b := make([]byte, 44)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 36+size)
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 3)
	binary.LittleEndian.PutUint32(b[24:], audioSampleRate)
	binary.LittleEndian.PutUint32(b[28:], audioSampleRate*6)
	binary.LittleEndian.PutUint16(b[32:], 6)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], size)
	return b
}
