//go:build windows

package device

/*
#cgo LDFLAGS: -lole32 -lmsdmo -lstrmiids -ldmoguids -lwmcodecdspuuid -luuid -lpropsys
#define COBJMACROS
#include <windows.h>
#include <objbase.h>
#include <mediaobj.h>
#include <dmort.h>
#include <uuids.h>
#include <mmreg.h>
#include <propsys.h>
#include <wmcodecdsp.h>
#include <string.h>

// Property keys of the Voice Capture DSP (wmcodecdsp.h in the Windows SDK;
// missing from mingw-w64). Same GUID, different property ids.
static const PROPERTYKEY sayPK_SystemMode = {{0x6f52c567, 0x0360, 0x4bd2, {0x96, 0x17, 0xcc, 0xbf, 0x14, 0x21, 0xc9, 0x39}}, 2};

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
*/
import "C"

import (
	"fmt"
	"log"
	"runtime"
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

	micOut := make(chan AudioCaptureFrame, 48)
	ready := make(chan error, 1)
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(micOut)
		// COM objects stay on the thread that created them.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		a := (*C.sayAEC)(C.calloc(1, C.size_t(unsafe.Sizeof(C.sayAEC{}))))
		defer C.free(unsafe.Pointer(a))
		var step C.int
		if hr := C.sayAECOpen(a, C.int(sampleRate), &step); hr != 0 {
			C.sayAECClose(a)
			ready <- fmt.Errorf("voice capture DSP: step %d failed with HRESULT 0x%08x", int(step), uint32(hr))
			return
		}
		defer C.sayAECClose(a)
		ready <- nil

		buf := make([]int16, sampleRate) // up to one second per read
		var firstSample uint64
		var captureEpoch time.Time
		var lastErr C.HRESULT
		tick := time.NewTicker(10 * time.Millisecond) // the DSP works in 10 ms frames
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
			}
			for {
				var more C.int
				var hr C.HRESULT
				n := int(C.sayAECRead(a, (*C.short)(unsafe.Pointer(&buf[0])), C.int(len(buf)), &more, &hr))
				if hr != 0 && hr != 1 && hr != lastErr { // 1: S_FALSE, no data yet
					log.Printf("[audio] voice capture DSP: ProcessOutput HRESULT 0x%08x", uint32(hr))
				}
				lastErr = hr
				if n > 0 {
					if captureEpoch.IsZero() {
						captureEpoch = time.Now().Add(-time.Duration(n) * time.Second / time.Duration(sampleRate))
					}
					frame := AudioCaptureFrame{
						Samples:     append([]int16(nil), buf[:n]...),
						CapturedAt:  captureEpoch.Add(time.Duration(firstSample) * time.Second / time.Duration(sampleRate)),
						FirstSample: firstSample,
					}
					firstSample += uint64(n)
					select {
					case micOut <- frame:
					default:
						select {
						case <-micOut:
						default:
						}
						select {
						case micOut <- frame:
						default:
						}
					}
				}
				if more == 0 {
					break
				}
			}
		}
	}()
	if err := <-ready; err != nil {
		<-done
		speaker.Close()
		return nil, nil, err
	}
	log.Printf("[audio] system voice: Windows Voice Capture DSP (echo cancellation, noise suppression, gain control) on the default microphone; speaker %d ms x %d",
		periodSamples*1000/sampleRate, periods)
	speaker.closeDevice = func() {
		close(quit)
		<-done
	}
	return speaker, micOut, nil
}
