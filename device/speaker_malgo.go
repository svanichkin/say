//go:build !plan9

package device

import (
	"log"
	"sync"
	"time"

	malgo "github.com/gen2brain/malgo"
)

// speakerBackend is the miniaudio device behind a Speaker. Its audio
// thread runs only C: it plays from ring, which a goroutine keeps filled
// (ring.go).
type speakerBackend struct {
	dev  *malgo.Device
	ctx  *malgo.AllocatedContext
	ring *nativeRing
	w    *ringRenderer
	quit chan struct{}
	pump sync.WaitGroup

	// mu guards dev against the stop watcher while stopDevice takes it
	// down; closing: we stop it ourselves.
	mu      sync.Mutex
	closing bool
}

// speakerRestartWait is how long a stopped device may take to start again
// by itself: on a change of the default device (Windows: the device for
// calls) miniaudio stops it, opens the new one and starts it, in about
// half a second.
const speakerRestartWait = 3 * time.Second

// watchStop runs when the device has stopped. If it starts again by itself
// (a change of device) the call goes on; if not, onStop ends it.
func (b *speakerBackend) watchStop(onStop func()) {
	deadline := time.Now().Add(speakerRestartWait)
	for {
		b.mu.Lock()
		closing, started := b.closing, b.dev != nil && b.dev.IsStarted()
		b.mu.Unlock()
		if closing {
			return
		}
		if started {
			log.Printf("[audio] the speaker stopped and started again (the system changed the device)")
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[audio] the speaker stopped and did not start again in %v: ending the call's media", speakerRestartWait)
			onStop()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopDevice stops the device and the goroutine filling its ring; no
// callback runs after it returns.
func (b *speakerBackend) stopDevice() {
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()
	if b.dev != nil {
		_ = b.dev.Stop()
	}
	if b.quit != nil {
		close(b.quit)
		b.pump.Wait()
		b.quit = nil
	}
	if b.w != nil {
		b.w.final = b.ring.State()
	}
	if b.dev != nil {
		b.mu.Lock()
		b.dev.Uninit()
		b.dev = nil
		b.mu.Unlock()
	}
}

// free releases the miniaudio context and the ring.
func (b *speakerBackend) free() {
	if b.ctx != nil {
		b.ctx.Uninit()
		b.ctx.Free()
	}
	b.ring.free()
}

// StartSpeaker initializes a playback device that consumes PCM frames from speakerIn.
// It reports every played sample back to speakerIn so its latency accounting stays exact.
// stopCh is observed to output silence when the session stops, and onStop is invoked when
// the underlying device stops unexpectedly. onRender, if set, gets what the device played
// (silence included), for the software echo canceller's reference.
func StartSpeaker(sampleRate, channels, periodSamples, periods, samplesPerMediaTick int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, onRender func([]int16), onStop func()) (*Speaker, error) {
	callStreams()
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Printf("[malgo] %s", message)
	})
	if err != nil {
		return nil, err
	}
	ring, err := newNativeRing(sampleRate, channels)
	if err != nil {
		ctx.Uninit()
		ctx.Free()
		return nil, err
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = uint32(channels)
	deviceConfig.SampleRate = uint32(sampleRate)
	// The connection profile supplies the device period and count. The period
	// goes in milliseconds: a size in frames is passed to the device as is,
	// in the device's own frames (48 kHz), so 160 frames meant to be 20 ms at
	// 8 kHz became 3.3 ms. Milliseconds are converted at the device's rate.
	deviceConfig.PeriodSizeInMilliseconds = uint32(max(1, (periodSamples*1000+sampleRate/2)/sampleRate))
	deviceConfig.Periods = uint32(max(periods, 2))

	// periodSamples is the period at our sample rate (the device's own frames
	// differ); the latency model counts in our samples.
	speaker := &Speaker{latencySamples: periodSamples * int(deviceConfig.Periods)}
	if samplesPerMediaTick > 0 {
		// The callback cursor is ahead of audible playback by approximately the
		// configured device buffer. Account for that when scheduling video.
		speaker.playbackLatencyTicks = uint32(periodSamples * int(deviceConfig.Periods) / samplesPerMediaTick)
		speaker.periodTicks = uint32(max(1, periodSamples/samplesPerMediaTick))
		speaker.tickDur = time.Duration(samplesPerMediaTick) * time.Second / time.Duration(sampleRate)
	}
	w := &ringRenderer{
		speaker: speaker, in: speakerIn, ring: ring, onRender: onRender,
		samplesPerMediaTick: samplesPerMediaTick, channels: channels,
		ahead: sampleRate * channels * ringAheadMs / 1000,
	}
	speaker.release = w.release

	// The device stops on a change of device too, and starts again by
	// itself: that does not end the call (watchStop).
	var stopped malgo.StopProc
	if onStop != nil {
		stopped = func() { go speaker.backend.watchStop(onStop) }
	}
	dev, err := malgo.InitDeviceRaw(ctx.Context, deviceConfig, deviceDataProc(), ring.ptr(), stopped)
	if err != nil {
		ring.free()
		ctx.Uninit()
		ctx.Free()
		return nil, err
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		ring.free()
		ctx.Uninit()
		ctx.Free()
		return nil, err
	}

	speaker.backend.mu.Lock()
	speaker.backend.dev = dev
	speaker.backend.mu.Unlock()
	speaker.backend.ctx, speaker.backend.ring, speaker.backend.w = ctx, ring, w
	speaker.backend.quit = make(chan struct{})
	speaker.backend.pump.Add(1)
	go func() {
		defer speaker.backend.pump.Done()
		pumpSpeaker(w, stopCh, speaker.backend.quit)
	}()
	return speaker, nil
}
