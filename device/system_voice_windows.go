//go:build windows

package device

/*
#cgo LDFLAGS: -lavrt -lole32 -lmsdmo -lstrmiids -ldmoguids -lwmcodecdspuuid -luuid -lpropsys
#define COBJMACROS
#include <windows.h>
#include <objbase.h>
#include <mediaobj.h>
#include <dmort.h>
#include <uuids.h>
#include <mmreg.h>
#include <propsys.h>
#include <wmcodecdsp.h>
#include <mmdeviceapi.h>
#include <wchar.h>
#include <string.h>
#include <avrt.h>
#include "sayring.h"

// Property keys of the Voice Capture DSP (wmcodecdsp.h in the Windows SDK;
// missing from mingw-w64). Same GUID, different property ids.
static const PROPERTYKEY sayPK_SystemMode = {{0x6f52c567, 0x0360, 0x4bd2, {0x96, 0x17, 0xcc, 0xbf, 0x14, 0x21, 0xc9, 0x39}}, 2};
static const PROPERTYKEY sayPK_DeviceIndexes = {{0x6f52c567, 0x0360, 0x4bd2, {0x96, 0x17, 0xcc, 0xbf, 0x14, 0x21, 0xc9, 0x39}}, 4};

static const CLSID sayCLSID_MMDeviceEnumerator = {0xbcde0395, 0xe52f, 0x467c, {0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}};
static const IID sayIID_IMMDeviceEnumerator = {0xa95664d2, 0x9614, 0x4f35, {0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}};

// sayCallEndpoint is the index, among the active endpoints of flow (the
// DSP's device indexes), of the default device for calls
// (eCommunications); -1 if none.
static int sayCallEndpoint(IMMDeviceEnumerator *en, EDataFlow flow) {
	IMMDevice *def = NULL;
	LPWSTR defID = NULL;
	int idx = -1;
	if (FAILED(IMMDeviceEnumerator_GetDefaultAudioEndpoint(en, flow, eCommunications, &def))) {
		return -1;
	}
	if (SUCCEEDED(IMMDevice_GetId(def, &defID))) {
		IMMDeviceCollection *col = NULL;
		if (SUCCEEDED(IMMDeviceEnumerator_EnumAudioEndpoints(en, flow, DEVICE_STATE_ACTIVE, &col))) {
			UINT n = 0;
			IMMDeviceCollection_GetCount(col, &n);
			for (UINT i = 0; i < n && idx < 0; i++) {
				IMMDevice *d = NULL;
				LPWSTR id = NULL;
				if (SUCCEEDED(IMMDeviceCollection_Item(col, i, &d))) {
					if (SUCCEEDED(IMMDevice_GetId(d, &id))) {
						if (wcscmp(id, defID) == 0) idx = (int)i;
						CoTaskMemFree(id);
					}
					IMMDevice_Release(d);
				}
			}
			IMMDeviceCollection_Release(col);
		}
		CoTaskMemFree(defID);
	}
	IMMDevice_Release(def);
	return idx;
}

// sayBuffer is a minimal IMediaBuffer over memory owned by sayAEC.
typedef struct {
	IMediaBufferVtbl *lpVtbl;
	LONG ref;
	BYTE *data;
	DWORD len;
	DWORD max;
} sayBuffer;

static HRESULT STDMETHODCALLTYPE sayBufQI(IMediaBuffer *This, REFIID riid, void **ppv) {
	if (IsEqualIID(riid, &IID_IUnknown) || IsEqualIID(riid, &IID_IMediaBuffer)) {
		*ppv = This;
		This->lpVtbl->AddRef(This);
		return S_OK;
	}
	*ppv = NULL;
	return E_NOINTERFACE;
}
static ULONG STDMETHODCALLTYPE sayBufAddRef(IMediaBuffer *This) { return InterlockedIncrement(&((sayBuffer *)This)->ref); }
// The buffer lives as long as sayAEC; reference counts are only reported.
static ULONG STDMETHODCALLTYPE sayBufRelease(IMediaBuffer *This) { return InterlockedDecrement(&((sayBuffer *)This)->ref); }
static HRESULT STDMETHODCALLTYPE sayBufSetLength(IMediaBuffer *This, DWORD n) {
	sayBuffer *b = (sayBuffer *)This;
	if (n > b->max) return E_INVALIDARG;
	b->len = n;
	return S_OK;
}
static HRESULT STDMETHODCALLTYPE sayBufGetMaxLength(IMediaBuffer *This, DWORD *p) {
	*p = ((sayBuffer *)This)->max;
	return S_OK;
}
static HRESULT STDMETHODCALLTYPE sayBufGetBufferAndLength(IMediaBuffer *This, BYTE **pp, DWORD *pn) {
	sayBuffer *b = (sayBuffer *)This;
	if (pp) *pp = b->data;
	if (pn) *pn = b->len;
	return S_OK;
}
static IMediaBufferVtbl sayBufVtbl = {
	sayBufQI, sayBufAddRef, sayBufRelease, sayBufSetLength, sayBufGetMaxLength, sayBufGetBufferAndLength,
};

typedef struct {
	IMediaObject *dmo;
	IPropertyStore *props;
	sayBuffer buf;
	int comInit; // CoInitializeEx succeeded and must be undone
	int render, capture; // the devices for calls it uses (-1: the default)
} sayAEC;

// sayAECOpen creates the Voice Capture DSP in source mode (it opens the
// default microphone and takes the default speaker's output as the echo
// reference itself) with echo cancellation, and mono 16-bit output at rate.
// It returns the failing HRESULT and the step that failed.
static HRESULT sayAECOpen(sayAEC *a, int rate, int *step) {
	memset(a, 0, sizeof(*a));
	*step = 1;
	HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
	if (SUCCEEDED(hr)) {
		a->comInit = 1;
	} else if (hr != RPC_E_CHANGED_MODE) {
		return hr;
	}
	*step = 2;
	hr = CoCreateInstance(&CLSID_CWMAudioAEC, NULL, CLSCTX_INPROC_SERVER, &IID_IMediaObject, (void **)&a->dmo);
	if (FAILED(hr)) return hr;
	*step = 3;
	hr = IMediaObject_QueryInterface(a->dmo, &IID_IPropertyStore, (void **)&a->props);
	if (FAILED(hr)) return hr;

	PROPVARIANT pv;
	PropVariantInit(&pv);
	pv.vt = VT_I4;
	pv.lVal = 0; // AEC_SYSTEM_MODE SINGLE_CHANNEL_AEC (missing from mingw-w64)
	*step = 4;
	hr = IPropertyStore_SetValue(a->props, &sayPK_SystemMode, &pv);
	if (FAILED(hr)) return hr;

	// The devices for calls, the speaker's too (miniaudio plays on the
	// default device for calls): the DSP cancels the echo of what plays on
	// its render device. Where they cannot be found the DSP takes its
	// defaults.
	a->render = a->capture = -1;
	IMMDeviceEnumerator *en = NULL;
	if (SUCCEEDED(CoCreateInstance(&sayCLSID_MMDeviceEnumerator, NULL, CLSCTX_ALL, &sayIID_IMMDeviceEnumerator, (void **)&en))) {
		a->render = sayCallEndpoint(en, eRender);
		a->capture = sayCallEndpoint(en, eCapture);
		IMMDeviceEnumerator_Release(en);
	}
	if (a->render >= 0 && a->capture >= 0) {
		PropVariantInit(&pv);
		pv.vt = VT_I4;
		pv.lVal = (LONG)(((unsigned long)a->render << 16) | ((unsigned long)a->capture & 0xffff));
		if (FAILED(IPropertyStore_SetValue(a->props, &sayPK_DeviceIndexes, &pv))) {
			a->render = a->capture = -1;
		}
	}

	DMO_MEDIA_TYPE mt;
	memset(&mt, 0, sizeof(mt));
	*step = 5;
	hr = MoInitMediaType(&mt, sizeof(WAVEFORMATEX));
	if (FAILED(hr)) return hr;
	mt.majortype = MEDIATYPE_Audio;
	mt.subtype = MEDIASUBTYPE_PCM;
	mt.lSampleSize = 0;
	mt.bFixedSizeSamples = TRUE;
	mt.bTemporalCompression = FALSE;
	mt.formattype = FORMAT_WaveFormatEx;
	WAVEFORMATEX *wav = (WAVEFORMATEX *)mt.pbFormat;
	wav->wFormatTag = WAVE_FORMAT_PCM;
	wav->nChannels = 1;
	wav->nSamplesPerSec = rate;
	wav->nAvgBytesPerSec = rate * 2;
	wav->nBlockAlign = 2;
	wav->wBitsPerSample = 16;
	wav->cbSize = 0;
	*step = 6;
	hr = IMediaObject_SetOutputType(a->dmo, 0, &mt, 0);
	MoFreeMediaType(&mt);
	if (FAILED(hr)) return hr;

	*step = 7;
	hr = IMediaObject_AllocateStreamingResources(a->dmo);
	if (FAILED(hr)) return hr;

	a->buf.lpVtbl = &sayBufVtbl;
	a->buf.ref = 1;
	a->buf.max = rate * 2; // one second of 16-bit mono
	a->buf.data = (BYTE *)CoTaskMemAlloc(a->buf.max);
	*step = 8;
	if (a->buf.data == NULL) return E_OUTOFMEMORY;
	return S_OK;
}

// sayAECRead takes the processed microphone audio the DSP has ready into
// out (at most max samples). It returns the number of samples, sets *more
// when the DSP has more, and *hr to the result of ProcessOutput.
static int sayAECRead(sayAEC *a, short *out, int max, int *more, HRESULT *hrOut) {
	DMO_OUTPUT_DATA_BUFFER ob;
	memset(&ob, 0, sizeof(ob));
	a->buf.len = 0;
	ob.pBuffer = (IMediaBuffer *)&a->buf;
	DWORD status = 0;
	HRESULT hr = IMediaObject_ProcessOutput(a->dmo, 0, 1, &ob, &status);
	*hrOut = hr;
	*more = (ob.dwStatus & DMO_OUTPUT_DATA_BUFFERF_INCOMPLETE) != 0;
	if (hr != S_OK) {
		return 0;
	}
	int n = (int)(a->buf.len / 2);
	if (n > max) n = max;
	memcpy(out, a->buf.data, n * 2);
	return n;
}

static void sayAECClose(sayAEC *a) {
	if (a->props) {
		IPropertyStore_Release(a->props);
		a->props = NULL;
	}
	if (a->dmo) {
		IMediaObject_FreeStreamingResources(a->dmo);
		IMediaObject_Release(a->dmo);
		a->dmo = NULL;
	}
	if (a->buf.data) {
		CoTaskMemFree(a->buf.data);
		a->buf.data = NULL;
	}
	if (a->comInit) {
		CoUninitialize();
		a->comInit = 0;
	}
}

// The DSP is read on a thread of its own, in C, at the priority Windows
// gives audio (MMCSS "Audio"), into a sayRing: a Go thread can be held up
// by the Go scheduler, and the DSP drops what is not read in time. COM
// objects stay on the thread that created them, so it opens the DSP too.
typedef struct {
	sayRing *ring;
	int rate;
	volatile LONG quit;
	HANDLE thread, ready;
	HRESULT openHR;
	int openStep;
	int render, capture; // the DSP's devices (sayAEC)
	volatile LONG lastErr, errors; // ProcessOutput failures, for the log
} sayAECThread;

static DWORD WINAPI sayAECLoop(LPVOID p) {
	sayAECThread *t = (sayAECThread *)p;
	sayAEC a;
	int step = 0;
	HRESULT hr = sayAECOpen(&a, t->rate, &step);
	t->openHR = hr;
	t->openStep = step;
	t->render = a.render;
	t->capture = a.capture;
	SetEvent(t->ready);
	if (hr != 0) {
		sayAECClose(&a);
		return 0;
	}
	DWORD task = 0;
	HANDLE mm = AvSetMmThreadCharacteristicsW(L"Audio", &task);
	short *buf = (short *)malloc(t->rate * sizeof(short)); // up to one second per read
	while (buf && !InterlockedCompareExchange(&t->quit, 0, 0)) {
		Sleep(10); // the DSP works in 10 ms frames
		for (;;) {
			int more = 0;
			HRESULT h = 0;
			int n = sayAECRead(&a, buf, t->rate, &more, &h);
			if (h != 0 && h != 1) { // 1: S_FALSE, no data yet
				InterlockedExchange(&t->lastErr, (LONG)h);
				InterlockedIncrement(&t->errors);
			}
			if (n > 0) {
				sayRingMicPut(t->ring, buf, (uint32_t)n, sayRingNow());
			}
			if (!more) break;
		}
	}
	free(buf);
	if (mm) AvRevertMmThreadCharacteristics(mm);
	sayAECClose(&a);
	return 0;
}

// sayAECStart opens the DSP on its thread; it returns the HRESULT and the
// step of sayAECOpen.
static HRESULT sayAECStart(sayAECThread *t, int *step) {
	t->ready = CreateEventW(NULL, TRUE, FALSE, NULL);
	t->thread = CreateThread(NULL, 0, sayAECLoop, t, 0, NULL);
	if (!t->thread) {
		*step = 0;
		CloseHandle(t->ready);
		return HRESULT_FROM_WIN32(GetLastError());
	}
	WaitForSingleObject(t->ready, INFINITE);
	*step = t->openStep;
	if (t->openHR != 0) {
		WaitForSingleObject(t->thread, INFINITE);
		CloseHandle(t->thread);
		CloseHandle(t->ready);
	}
	return t->openHR;
}

static void sayAECStop(sayAECThread *t) {
	InterlockedExchange(&t->quit, 1);
	WaitForSingleObject(t->thread, INFINITE);
	CloseHandle(t->thread);
	CloseHandle(t->ready);
}

static LONG sayAECErrors(sayAECThread *t, LONG *last) {
	*last = InterlockedCompareExchange(&t->lastErr, 0, 0);
	return InterlockedCompareExchange(&t->errors, 0, 0);
}
*/
import "C"

import (
	"fmt"
	"log"
	"time"
	"unsafe"
)

// StartSystemVoice plays speakerIn through Say's own speaker (miniaudio, on
// the default output device) and, with mic set, captures the microphone
// through the Windows Voice Capture DSP in source mode: the DSP opens the
// default microphone and takes the default speaker's output as the echo
// reference itself, then cancels echo, suppresses noise and controls gain.
// Closing the returned Speaker stops both and closes the microphone channel.
func StartSystemVoice(sampleRate, samplesPerMediaTick, periodSamples, periods int, speakerIn *PlaybackQueue, stopCh <-chan struct{}, mic bool, onStop func()) (*Speaker, <-chan AudioCaptureFrame, error) {
	// The DSP needs an active stream on the output device: start it first.
	speaker, err := StartSpeaker(sampleRate, 1, periodSamples, periods, samplesPerMediaTick, speakerIn, stopCh, nil, onStop)
	if err != nil {
		return nil, nil, err
	}
	if !mic {
		return speaker, nil, nil
	}

	ring, err := newNativeRing(sampleRate, 1)
	if err != nil {
		speaker.Close()
		return nil, nil, err
	}
	t := (*C.sayAECThread)(C.calloc(1, C.size_t(unsafe.Sizeof(C.sayAECThread{}))))
	t.ring = (*C.sayRing)(ring.ptr())
	t.rate = C.int(sampleRate)
	var step C.int
	if hr := C.sayAECStart(t, &step); hr != 0 {
		C.free(unsafe.Pointer(t))
		ring.free()
		speaker.Close()
		return nil, nil, fmt.Errorf("voice capture DSP: step %d failed with HRESULT 0x%08x", int(step), uint32(hr))
	}

	micOut := make(chan AudioCaptureFrame, 48)
	capture := &systemVoiceCapture{sampleRate: sampleRate, frameSize: max(1, sampleRate/50), out: micOut}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		pumpMic(ring, capture, quit)
	}()
	go func() { // the DSP's failures, as they change
		var seen C.LONG
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
			}
			var last C.LONG
			if n := C.sayAECErrors(t, &last); n != seen {
				log.Printf("[audio] voice capture DSP: ProcessOutput HRESULT 0x%08x (%d failures)", uint32(last), int(n))
				seen = n
			}
		}
	}()
	devices := "the default devices"
	if t.render >= 0 {
		devices = fmt.Sprintf("the devices for calls (render #%d, capture #%d)", int(t.render), int(t.capture))
	}
	log.Printf("[audio] system voice: Windows Voice Capture DSP (echo cancellation, noise suppression, gain control) on %s; speaker %d ms x %d, a call's stream on the default device for calls",
		devices, periodSamples*1000/sampleRate, periods)
	speaker.closeDevice = func() {
		C.sayAECStop(t)
		close(quit)
		<-done
		C.free(unsafe.Pointer(t))
		ring.free()
		close(micOut)
	}
	return speaker, micOut, nil
}
