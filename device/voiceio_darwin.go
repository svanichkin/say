//go:build darwin

package device

/*
#cgo LDFLAGS: -framework AudioToolbox -framework CoreAudio
#include <AudioToolbox/AudioToolbox.h>
#include <stdlib.h>
#include <stdint.h>

extern void goVoiceInput(uintptr_t handle, int16_t *samples, UInt32 frames);
extern void goVoiceOutput(uintptr_t handle, int16_t *samples, UInt32 frames);

typedef struct {
	AudioUnit unit;
	uintptr_t handle;
	int16_t *inBuf;
	UInt32 inCap;
} sayVoiceIO;

static OSStatus sayVoiceInputCB(void *ref, AudioUnitRenderActionFlags *flags,
	const AudioTimeStamp *ts, UInt32 bus, UInt32 frames, AudioBufferList *unused) {
	sayVoiceIO *v = (sayVoiceIO *)ref;
	if (frames > v->inCap) {
		frames = v->inCap;
	}
	AudioBufferList bl;
	bl.mNumberBuffers = 1;
	bl.mBuffers[0].mNumberChannels = 1;
	bl.mBuffers[0].mDataByteSize = frames * 2;
	bl.mBuffers[0].mData = v->inBuf;
	OSStatus st = AudioUnitRender(v->unit, flags, ts, 1, frames, &bl);
	if (st != noErr) {
		return st;
	}
	goVoiceInput(v->handle, v->inBuf, bl.mBuffers[0].mDataByteSize / 2);
	return noErr;
}

static OSStatus sayVoiceRenderCB(void *ref, AudioUnitRenderActionFlags *flags,
	const AudioTimeStamp *ts, UInt32 bus, UInt32 frames, AudioBufferList *io) {
	sayVoiceIO *v = (sayVoiceIO *)ref;
	for (UInt32 i = 0; i < io->mNumberBuffers; i++) {
		goVoiceOutput(v->handle, (int16_t *)io->mBuffers[i].mData, io->mBuffers[i].mDataByteSize / 2);
	}
	return noErr;
}

// sayVoiceOpen creates and starts a VoiceProcessingIO unit: microphone on
// bus 1 and speaker on bus 0, both as mono 16-bit PCM at rate. It returns
// the failing OSStatus and the step that failed.
static OSStatus sayVoiceOpen(sayVoiceIO *v, double rate, int *step) {
	AudioComponentDescription d = {0};
	d.componentType = kAudioUnitType_Output;
	d.componentSubType = kAudioUnitSubType_VoiceProcessingIO;
	d.componentManufacturer = kAudioUnitManufacturer_Apple;
	*step = 1;
	AudioComponent c = AudioComponentFindNext(NULL, &d);
	if (c == NULL) {
		return -1;
	}
	*step = 2;
	OSStatus st = AudioComponentInstanceNew(c, &v->unit);
	if (st) return st;

	UInt32 one = 1;
	*step = 3;
	st = AudioUnitSetProperty(v->unit, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &one, sizeof(one));
	if (st) return st;
	*step = 4;
	st = AudioUnitSetProperty(v->unit, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Output, 0, &one, sizeof(one));
	if (st) return st;

	AudioStreamBasicDescription f = {0};
	f.mSampleRate = rate;
	f.mFormatID = kAudioFormatLinearPCM;
	f.mFormatFlags = kAudioFormatFlagIsSignedInteger | kAudioFormatFlagIsPacked;
	f.mBitsPerChannel = 16;
	f.mChannelsPerFrame = 1;
	f.mFramesPerPacket = 1;
	f.mBytesPerFrame = 2;
	f.mBytesPerPacket = 2;
	*step = 5;
	st = AudioUnitSetProperty(v->unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, &f, sizeof(f));
	if (st) return st;
	*step = 6;
	st = AudioUnitSetProperty(v->unit, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &f, sizeof(f));
	if (st) return st;

	AURenderCallbackStruct in = {sayVoiceInputCB, v};
	*step = 7;
	st = AudioUnitSetProperty(v->unit, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 1, &in, sizeof(in));
	if (st) return st;
	AURenderCallbackStruct out = {sayVoiceRenderCB, v};
	*step = 8;
	st = AudioUnitSetProperty(v->unit, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &out, sizeof(out));
	if (st) return st;

	v->inCap = 8192;
	v->inBuf = calloc(v->inCap, sizeof(int16_t));
	*step = 9;
	st = AudioUnitInitialize(v->unit);
	if (st) return st;
	*step = 10;
	return AudioOutputUnitStart(v->unit);
}

static void sayVoiceClose(sayVoiceIO *v) {
	if (v->unit) {
		AudioOutputUnitStop(v->unit);
		AudioUnitUninitialize(v->unit);
		AudioComponentInstanceDispose(v->unit);
		v->unit = NULL;
	}
	free(v->inBuf);
	v->inBuf = NULL;
}
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
	"unsafe"
)

// VoiceIO is the system's voice processing audio unit (VoiceProcessingIO on
// macOS, as used by FaceTime): one device for microphone and speaker, with
// the system's echo cancellation, noise suppression and gain control.
type VoiceIO struct {
	v        *C.sayVoiceIO
	handle   cgo.Handle
	onInput  func(samples []int16)
	onOutput func(out []int16)
	once     sync.Once
}

// VoiceIOSupported reports whether this platform has a system voice unit.
const VoiceIOSupported = true

// StartVoiceIO opens the system voice unit at sampleRate (mono, 16-bit).
// onInput receives processed microphone blocks (the slice is reused after
// the call returns); onOutput must fill out with the samples to play. Both
// run on the system's real-time audio thread.
func StartVoiceIO(sampleRate int, onInput func(samples []int16), onOutput func(out []int16)) (*VoiceIO, error) {
	vio := &VoiceIO{onInput: onInput, onOutput: onOutput}
	vio.v = (*C.sayVoiceIO)(C.calloc(1, C.size_t(unsafe.Sizeof(C.sayVoiceIO{}))))
	vio.handle = cgo.NewHandle(vio)
	vio.v.handle = C.uintptr_t(vio.handle)
	var step C.int
	if st := C.sayVoiceOpen(vio.v, C.double(sampleRate), &step); st != 0 {
		vio.Close()
		return nil, fmt.Errorf("voice processing unit: step %d failed with OSStatus %d", int(step), int32(st))
	}
	return vio, nil
}

// Close stops the unit and releases it.
func (vio *VoiceIO) Close() {
	if vio == nil {
		return
	}
	vio.once.Do(func() {
		C.sayVoiceClose(vio.v)
		C.free(unsafe.Pointer(vio.v))
		vio.handle.Delete()
	})
}

//export goVoiceInput
func goVoiceInput(handle C.uintptr_t, samples *C.int16_t, frames C.UInt32) {
	vio := cgo.Handle(handle).Value().(*VoiceIO)
	if vio.onInput != nil && frames > 0 {
		vio.onInput(unsafe.Slice((*int16)(unsafe.Pointer(samples)), int(frames)))
	}
}

//export goVoiceOutput
func goVoiceOutput(handle C.uintptr_t, samples *C.int16_t, frames C.UInt32) {
	out := unsafe.Slice((*int16)(unsafe.Pointer(samples)), int(frames))
	vio := cgo.Handle(handle).Value().(*VoiceIO)
	if vio.onOutput == nil {
		clear(out)
		return
	}
	vio.onOutput(out)
}
