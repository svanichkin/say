//go:build plan9

package device

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

// Plan 9 audio, without miniaudio (there is no cgo on Plan 9): the kernel's
// audio(3) device and 9front's mixfs.
//
// Playback writes /dev/audio: mixfs when it runs (several programs can play
// at once), otherwise the device itself. Capture reads #A/audio, always the
// device itself, since mixfs serves output only; the microphone is picked
// with "inpin N" in /dev/audioctl and its level with "recgain N" in
// /dev/volume. Both carry 16-bit little-endian stereo at the rate the
// "speed" line of the matching volume file gives (44.1 kHz by default). Say's
// mono stream is converted to and from it here.

const (
	plan9PlaybackPath   = "/dev/audio"
	plan9PlaybackVolume = "/dev/volume"
	plan9CapturePath    = "#A/audio"
	plan9CaptureVolume  = "#A/volume"
)

// speakerBackend: the Plan 9 speaker runs through Speaker.closeDevice.
type speakerBackend struct{}

func (b *speakerBackend) stopDevice() {}
func (b *speakerBackend) free()       {}

func plan9DeviceRate(volumePath string) int {
	b, err := os.ReadFile(volumePath)
	if err != nil {
		return defaultDeviceRate
	}
	return parseAudioSpeed(string(b))
}

// StartSpeaker starts playback of the PCM frames in speakerIn. A writer
// goroutine renders one period at a time and writes it to /dev/audio; the
// write blocks while the device buffer is full, so the device's clock paces
// it. Otherwise it behaves as the miniaudio speaker (speaker_malgo.go):
// silence once stopCh closes, onStop when the device fails.
func StartSpeaker(sampleRate, channels, periodSamples, periods, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, onRender func([]int16), onStop func()) (*Speaker, error) {
	if channels != 1 {
		return nil, fmt.Errorf("plan9 audio: %d channels, only mono is supported", channels)
	}
	if periodSamples <= 0 {
		return nil, fmt.Errorf("plan9 audio: period of %d samples", periodSamples)
	}
	f, err := os.OpenFile(plan9PlaybackPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	devRate := plan9DeviceRate(plan9PlaybackVolume)
	log.Printf("[audio/speaker] %s: %d Hz stereo, from %d Hz mono", plan9PlaybackPath, devRate, sampleRate)

	periods = max(periods, 2)
	speaker := &Speaker{latencySamples: periodSamples * periods}
	if samplesPerMediaTick > 0 {
		speaker.playbackLatencyTicks = uint32(periodSamples * periods / samplesPerMediaTick)
		speaker.periodTicks = uint32(max(1, periodSamples/samplesPerMediaTick))
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
	}
	r := &queueRenderer{speaker: speaker, in: speakerIn, channels: channels, samplesPerMediaTick: samplesPerMediaTick}
	speaker.release = r.release

	conv := newRateConverter(sampleRate, devRate)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		out := make([]int16, periodSamples)
		var in, converted []float32
		var buf []byte
		for {
			select {
			case <-done:
				return
			default:
			}
			rendered := false
			select {
			case <-stopCh:
				clear(out)
			default:
				if speakerIn != nil {
					r.render(out)
					rendered = true
				} else {
					clear(out)
				}
			}
			if rendered && onRender != nil {
				onRender(out)
			}
			in = in[:0]
			for _, v := range out {
				in = append(in, float32(v))
			}
			converted = conv.convert(converted[:0], in)
			buf = appendStereoS16(buf[:0], converted)
			if _, err := f.Write(buf); err != nil {
				select {
				case <-done:
				default:
					log.Printf("[audio/speaker] %s: %v", plan9PlaybackPath, err)
					if onStop != nil {
						onStop()
					}
				}
				return
			}
		}
	}()
	// Close: the writer finishes its period (render must not run once
	// release has), then the device is closed.
	speaker.closeDevice = func() {
		close(done)
		<-exited
		f.Close()
	}
	return speaker, nil
}

// StartMicrophoneStream captures #A/audio in 20 ms blocks, converted to
// sampleRate mono. Blocks carry the same timing as the miniaudio capture
// (mic_malgo.go), and the oldest block is dropped when downstream is late.
func StartMicrophoneStream(ctx context.Context, sampleRate, channels int) (<-chan AudioCaptureFrame, error) {
	if channels != 1 {
		return nil, fmt.Errorf("plan9 audio: %d channels, only mono is supported", channels)
	}
	f, err := os.Open(plan9CapturePath)
	if err != nil {
		return nil, err
	}
	devRate := plan9DeviceRate(plan9CaptureVolume)
	log.Printf("[audio/mic] %s: %d Hz stereo, to %d Hz mono", plan9CapturePath, devRate, sampleRate)

	out := make(chan AudioCaptureFrame, 8)
	go func() {
		<-ctx.Done()
		f.Close() // the blocked read returns once the device delivers
	}()
	go func() {
		defer close(out)
		conv := newRateConverter(devRate, sampleRate)
		buf := make([]byte, devRate/50*4) // 20 ms of stereo frames
		var pending []byte                // a partial frame left by a read
		var mono, converted []float32
		var firstSample uint64
		var captureEpoch time.Time
		for {
			n, err := f.Read(buf)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("[audio/mic] %s: %v", plan9CapturePath, err)
				return
			}
			data := append(pending, buf[:n]...)
			whole := len(data) &^ 3
			mono = appendMonoFromStereoS16(mono[:0], data[:whole])
			pending = append(pending[:0], data[whole:]...)
			converted = conv.convert(converted[:0], mono)
			if len(converted) == 0 {
				continue
			}
			samples := make([]int16, len(converted))
			for i, v := range converted {
				samples[i] = clampInt16(v)
			}
			// The read returns once the block is complete; its first sample
			// was captured one block duration earlier.
			blockFrames := len(samples)
			if captureEpoch.IsZero() {
				captureEpoch = time.Now().Add(-time.Duration(blockFrames) * time.Second / time.Duration(sampleRate))
			}
			capturedAt := captureEpoch.Add(time.Duration(firstSample) * time.Second / time.Duration(sampleRate))
			frame := AudioCaptureFrame{Samples: samples, CapturedAt: capturedAt, FirstSample: firstSample}
			firstSample += uint64(blockFrames)
			select {
			case out <- frame:
			default:
				select {
				case <-out:
				default:
				}
				select {
				case out <- frame:
				default:
				}
			}
		}
	}()
	return out, nil
}
