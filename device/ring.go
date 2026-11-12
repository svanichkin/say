//go:build !plan9

package device

/*
#include "sayring.h"

// sayRingDeviceData is a miniaudio data callback (InitDeviceRaw): the
// device's audio thread moves samples through the ring and nothing else.
static void sayRingDeviceData(void *user, void *out, const void *in, uint32_t frames) {
	sayRing *r = (sayRing *)user;
	uint32_t n = frames * (uint32_t)r->channels;
	if (in) {
		sayRingMicPut(r, (const int16_t *)in, n, sayRingNow());
	}
	if (out) {
		sayRingFill(r, (int16_t *)out, n);
	}
}

static void *sayRingDeviceDataProc(void) { return (void *)sayRingDeviceData; }
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

// nativeRing is the hand-off between a device's real-time audio thread and
// Go (sayring.h): microphone blocks one way, samples to play the other.
// The audio thread runs only C; goroutines call these methods.
type nativeRing struct {
	r *C.sayRing
}

func newNativeRing(sampleRate, channels int) (*nativeRing, error) {
	r := C.sayRingNew(C.double(sampleRate), C.int(channels))
	if r == nil {
		return nil, errors.New("out of memory for the audio ring")
	}
	return &nativeRing{r: r}, nil
}

// free releases the ring; no audio thread may use it any more.
func (n *nativeRing) free() {
	if n != nil && n.r != nil {
		C.sayRingFree(n.r)
		n.r = nil
	}
}

// ptr is the ring for C code (the device's user data).
func (n *nativeRing) ptr() unsafe.Pointer { return unsafe.Pointer(n.r) }

// deviceDataProc is the miniaudio data callback that serves a ring.
func deviceDataProc() unsafe.Pointer { return C.sayRingDeviceDataProc() }

// ReadMic takes the oldest microphone block into buf: n samples, the last
// of them captured at at. n is 0 when there is none.
func (n *nativeRing) ReadMic(buf []int16) (int, time.Time) {
	if len(buf) == 0 {
		return 0, time.Time{}
	}
	var ns C.uint64_t
	got := int(C.sayRingMicRead(n.r, (*C.int16_t)(unsafe.Pointer(&buf[0])), C.int(len(buf)), &ns))
	if got == 0 {
		return 0, time.Time{}
	}
	return got, time.Now().Add(-time.Duration(uint64(C.sayRingNow()) - uint64(ns)))
}

// Write queues samples to play and returns how many fit.
func (n *nativeRing) Write(samples []int16) int {
	if len(samples) == 0 {
		return 0
	}
	return int(C.sayRingWrite(n.r, (*C.int16_t)(unsafe.Pointer(&samples[0])), C.int(len(samples))))
}

// Mute makes the device play silence from now on.
func (n *nativeRing) Mute() { C.sayRingMute(n.r) }

// RingState is a snapshot of the rings and the audio thread's counters.
type RingState struct {
	Written, Played uint64 // samples queued and handed to the device, in total
	// CallbackPos is Played at the start of the latest output callback,
	// CallbackAt when it ran.
	CallbackPos uint64
	CallbackAt  time.Time
	// Callbacks and Asked: output callbacks and the samples they asked
	// for; DryCallbacks and DrySamples: those that ran out, and the
	// silence they played. MicDropped: microphone samples lost to a full
	// ring.
	Callbacks, Asked, DryCallbacks, DrySamples, MicDropped uint64
}

// State reads the rings' positions and the counters.
func (n *nativeRing) State() RingState {
	var c C.sayRingState
	C.sayRingGetState(n.r, &c)
	st := RingState{
		Written: uint64(c.written), Played: uint64(c.played), CallbackPos: uint64(c.cbStartPos),
		Callbacks: uint64(c.callbacks), Asked: uint64(c.asked), DryCallbacks: uint64(c.dryCallbacks),
		DrySamples: uint64(c.drySamples), MicDropped: uint64(c.micDropped),
	}
	if c.cbNs != 0 {
		st.CallbackAt = time.Now().Add(-time.Duration(uint64(c.now) - uint64(c.cbNs)))
	}
	return st
}

// devicePlay and deviceCapture stand in for the audio thread (tests).
func (n *nativeRing) devicePlay(dst []int16) {
	C.sayRingFill(n.r, (*C.int16_t)(unsafe.Pointer(&dst[0])), C.uint32_t(len(dst)))
}

func (n *nativeRing) deviceCapture(samples []int16, ago time.Duration) {
	C.sayRingMicPut(n.r, (*C.int16_t)(unsafe.Pointer(&samples[0])), C.uint32_t(len(samples)), C.sayRingNow()-C.uint64_t(ago))
}

// RingMarginMs is how much audio the receiver keeps queued beyond its own
// low point, on top of the device's period: the device plays it from the
// ring while the goroutines filling the ring wait for the CPU. Without it
// the ring holds only what falls due (one period, 10-20 ms) and runs dry
// whenever Go is a little late.
const RingMarginMs = 40

// ringPoll is how often the goroutines look at a ring; ringAheadMs is how
// much audio the output ring is kept filled with. Audio in the ring still
// counts as queued (the playout's target covers it), so the ring adds no
// delay; it plays on while Go is held up.
const (
	ringPoll    = 5 * time.Millisecond
	ringAheadMs = 300
)

// pumpMic hands the ring's microphone blocks to capture, each with the time
// it was captured (not when this goroutine got to it), until quit closes.
func pumpMic(n *nativeRing, capture *systemVoiceCapture, quit <-chan struct{}) {
	AudioThread()
	var blockAt time.Time
	capture.clock = func() time.Time { return blockAt }
	buf := make([]int16, C.SAY_MIC_SLOT)
	tick := time.NewTicker(ringPoll)
	defer tick.Stop()
	for {
		for {
			got, at := n.ReadMic(buf)
			if got == 0 {
				break
			}
			blockAt = at
			capture.write(buf[:got])
		}
		select {
		case <-quit:
			return
		case <-tick.C:
		}
	}
}

// pumpSpeaker keeps the ring filled from w's queue until quit closes; once
// stopCh closes the device plays silence.
func pumpSpeaker(w *ringRenderer, stopCh <-chan struct{}, quit <-chan struct{}) {
	AudioThread()
	tick := time.NewTicker(ringPoll)
	defer tick.Stop()
	for {
		select {
		case <-quit:
			return
		case <-stopCh:
			w.ring.Mute()
			<-quit
			return
		case <-tick.C:
		}
		w.step()
	}
}
