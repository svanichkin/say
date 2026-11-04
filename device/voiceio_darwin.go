//go:build darwin

package device

/*
#cgo LDFLAGS: -framework AudioToolbox -framework CoreAudio
#include <AudioToolbox/AudioToolbox.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

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

// sayVoiceLatency is one direction's latency as CoreAudio reports it.
typedef struct {
	AudioDeviceID device;
	int fromUnit;          // the device was reported by the unit (else: system default)
	Float64 rate;          // the device's nominal sample rate
	UInt32 bufferFrames;   // IO buffer (callback) size
	UInt32 deviceLatency;  // frames
	UInt32 safetyOffset;   // frames
	UInt32 streamLatency;  // frames, first stream
	Float64 unitLatency;   // seconds, the voice unit's own processing
} sayVoiceLatency;

static UInt32 sayGetU32(AudioObjectID obj, AudioObjectPropertySelector sel, AudioObjectPropertyScope scope) {
	AudioObjectPropertyAddress a = {sel, scope, kAudioObjectPropertyElementMain};
	UInt32 v = 0, size = sizeof(v);
	if (AudioObjectGetPropertyData(obj, &a, 0, NULL, &size, &v) != noErr) {
		return 0;
	}
	return v;
}

// sayDefaultDevice returns the system default output (input == 0) or
// input device, or 0.
static AudioDeviceID sayDefaultDevice(int input) {
	AudioObjectPropertyAddress a = {input ? kAudioHardwarePropertyDefaultInputDevice : kAudioHardwarePropertyDefaultOutputDevice,
		kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioDeviceID dev = 0;
	UInt32 size = sizeof(dev);
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &size, &dev) != noErr) {
		return 0;
	}
	return dev;
}

// sayDeviceLatency fills out the latency of dev in one direction. The IO
// buffer size is the one this process uses.
static void sayDeviceLatency(AudioDeviceID dev, int input, sayVoiceLatency *out) {
	AudioObjectPropertyScope scope = input ? kAudioObjectPropertyScopeInput : kAudioObjectPropertyScopeOutput;
	out->device = dev;
	if (dev == 0) {
		return;
	}
	AudioObjectPropertyAddress ra = {kAudioDevicePropertyNominalSampleRate, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	UInt32 size = sizeof(out->rate);
	AudioObjectGetPropertyData(dev, &ra, 0, NULL, &size, &out->rate);
	out->bufferFrames = sayGetU32(dev, kAudioDevicePropertyBufferFrameSize, scope);
	out->deviceLatency = sayGetU32(dev, kAudioDevicePropertyLatency, scope);
	out->safetyOffset = sayGetU32(dev, kAudioDevicePropertySafetyOffset, scope);
	AudioObjectPropertyAddress sa = {kAudioDevicePropertyStreams, scope, kAudioObjectPropertyElementMain};
	AudioStreamID streams[8];
	size = sizeof(streams);
	if (AudioObjectGetPropertyData(dev, &sa, 0, NULL, &size, streams) == noErr && size >= sizeof(AudioStreamID)) {
		out->streamLatency = sayGetU32(streams[0], kAudioStreamPropertyLatency, kAudioObjectPropertyScopeGlobal);
	}
}

// sayDefaultLatency fills out the latency of the default device.
static void sayDefaultLatency(int input, sayVoiceLatency *out) {
	memset(out, 0, sizeof(*out));
	sayDeviceLatency(sayDefaultDevice(input), input, out);
}

// sayVoiceQuery fills out the latency of the output (input == 0) or the
// input device of the running unit.
static void sayVoiceQuery(sayVoiceIO *v, int input, sayVoiceLatency *out) {
	memset(out, 0, sizeof(*out));
	AudioDeviceID dev = 0;
	UInt32 size = sizeof(dev);
	if (AudioUnitGetProperty(v->unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, input ? 1 : 0, &dev, &size) == noErr && dev != 0) {
		out->fromUnit = 1;
	} else {
		dev = sayDefaultDevice(input);
	}
	sayDeviceLatency(dev, input, out);
	size = sizeof(out->unitLatency);
	AudioUnitGetProperty(v->unit, kAudioUnitProperty_Latency, kAudioUnitScope_Global, 0, &out->unitLatency, &size);
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
	"time"
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

// VoiceLatency is one direction's latency of the system voice unit, as
// CoreAudio reports it.
type VoiceLatency struct {
	FromUnit      bool    // the device came from the unit (else: the system default)
	Default       bool    // queried as the system default device (Say's own audio)
	Rate          float64 // device sample rate, Hz
	BufferFrames  int     // IO buffer (callback) size, frames
	DeviceLatency int     // frames
	SafetyOffset  int     // frames
	StreamLatency int     // frames
	UnitLatency   time.Duration
}

// Total is the time between a sample's IO cycle and its sound (output) or
// between sound and its delivery (input): the device's latency, safety
// offset and stream latency, one IO buffer, and the unit's processing.
func (l VoiceLatency) Total() time.Duration {
	if l.Rate <= 0 {
		return l.UnitLatency
	}
	frames := l.DeviceLatency + l.SafetyOffset + l.StreamLatency + l.BufferFrames
	return time.Duration(float64(frames)/l.Rate*float64(time.Second)) + l.UnitLatency
}

func (l VoiceLatency) String() string {
	src := "unit device"
	if !l.FromUnit {
		src = "default device"
	}
	if l.Default {
		src = "device"
	}
	text := fmt.Sprintf("%.1f ms (%s %.0f Hz: buffer %d + latency %d + safety %d + stream %d frames",
		float64(l.Total())/float64(time.Millisecond), src, l.Rate, l.BufferFrames, l.DeviceLatency, l.SafetyOffset, l.StreamLatency)
	if !l.Default {
		text += fmt.Sprintf(", unit %.1f ms", float64(l.UnitLatency)/float64(time.Millisecond))
	}
	return text + ")"
}

// Latency reports the running unit's output (input false) or input latency.
func (vio *VoiceIO) Latency(input bool) VoiceLatency {
	var c C.sayVoiceLatency
	C.sayVoiceQuery(vio.v, cBool(input), &c)
	return voiceLatencyFromC(&c)
}

// DefaultDeviceLatency reports the latency of the system default output
// (input false) or input device, which Say's own audio (malgo) uses. The IO
// buffer size is this process's: the period the device was opened with.
func DefaultDeviceLatency(input bool) (VoiceLatency, bool) {
	var c C.sayVoiceLatency
	C.sayDefaultLatency(cBool(input), &c)
	l := voiceLatencyFromC(&c)
	l.Default = true
	return l, c.device != 0
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func voiceLatencyFromC(c *C.sayVoiceLatency) VoiceLatency {
	return VoiceLatency{
		FromUnit:      c.fromUnit != 0,
		Rate:          float64(c.rate),
		BufferFrames:  int(c.bufferFrames),
		DeviceLatency: int(c.deviceLatency),
		SafetyOffset:  int(c.safetyOffset),
		StreamLatency: int(c.streamLatency),
		UnitLatency:   time.Duration(float64(c.unitLatency) * float64(time.Second)),
	}
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
