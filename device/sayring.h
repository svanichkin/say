// sayring.h: the hand-off between a real-time audio thread and Go.
//
// The audio thread never enters Go: a Go call can wait for the Go
// scheduler, whose threads have ordinary priority and, on a loaded machine,
// wait for the CPU for hundreds of ms. Blocked there, the device drops
// microphone input and plays nothing. The audio thread only moves samples
// between the device and two lock-free rings (one writer and one reader
// each); goroutines empty and fill them at their own pace (ring.go).
//
// Each cgo file that includes this gets its own copy (all static).

#ifndef SAY_RING_H
#define SAY_RING_H

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#include <windows.h>
#elif defined(__APPLE__)
#include <time.h>
#else
#include <time.h>
#endif

#define SAY_MIC_SLOTS 128   // ~1.3 s of ~10 ms blocks
#define SAY_MIC_SLOT 2048   // samples per slot; larger blocks take several
#define SAY_OUT_CAP 32768   // samples (4 s at 8 kHz); a power of two
#define SAY_DECLICK 40      // ramp after running dry (5 ms at 8 kHz)

typedef struct {
	uint64_t ns; // sayRingNow when the slot's last sample was captured
	uint32_t n;
	int16_t s[SAY_MIC_SLOT];
} sayMicSlot;

typedef struct {
	double rate; // frames per second
	int channels;

	sayMicSlot *slots;
	uint64_t micW, micR;   // slots written, read
	uint64_t micDropped;   // samples dropped: the ring was full

	int16_t *out;
	uint64_t outW, outR;   // samples written, handed to the device
	uint64_t mute;         // nonzero: play silence, take nothing
	uint64_t cbStartPos, cbNs; // outR and the time at the start of the latest callback

	// Counted by the audio thread, read by Go.
	uint64_t callbacks, asked, dryCallbacks, drySamples;

	// The audio thread's own.
	int16_t lastOut;
	int rampIn;
} sayRing;

#define SAY_LOAD(p) __atomic_load_n(p, __ATOMIC_ACQUIRE)
#define SAY_STORE(p, v) __atomic_store_n(p, v, __ATOMIC_RELEASE)
#define SAY_ADD(p, v) __atomic_fetch_add(p, v, __ATOMIC_RELAXED)

// sayRingNow is a monotonic clock in ns.
static uint64_t sayRingNow(void) {
#if defined(_WIN32)
	LARGE_INTEGER c, f;
	QueryPerformanceCounter(&c);
	QueryPerformanceFrequency(&f);
	return (uint64_t)((double)c.QuadPart * 1e9 / (double)f.QuadPart);
#elif defined(__APPLE__)
	return clock_gettime_nsec_np(CLOCK_UPTIME_RAW);
#else
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
#endif
}

static sayRing *sayRingNew(double rate, int channels) {
	sayRing *r = calloc(1, sizeof(sayRing));
	if (!r) return NULL;
	r->rate = rate;
	r->channels = channels > 0 ? channels : 1;
	r->slots = calloc(SAY_MIC_SLOTS, sizeof(sayMicSlot));
	r->out = calloc(SAY_OUT_CAP, sizeof(int16_t));
	if (!r->slots || !r->out) {
		free(r->slots);
		free(r->out);
		free(r);
		return NULL;
	}
	return r;
}

static void sayRingFree(sayRing *r) {
	if (!r) return;
	free(r->slots);
	free(r->out);
	free(r);
}

// sayRingMicPut keeps a microphone block of n samples (interleaved) whose
// last sample was captured at now. Audio thread.
static void sayRingMicPut(sayRing *r, const int16_t *p, uint32_t n, uint64_t now) {
	while (n > 0) {
		uint32_t k = n < SAY_MIC_SLOT ? n : SAY_MIC_SLOT;
		k -= k % r->channels;
		if (k == 0) return;
		n -= k;
		uint64_t w = r->micW;
		if (w - SAY_LOAD(&r->micR) >= SAY_MIC_SLOTS) {
			SAY_ADD(&r->micDropped, k);
		} else {
			sayMicSlot *slot = &r->slots[w % SAY_MIC_SLOTS];
			slot->ns = now - (uint64_t)((double)(n / r->channels) * 1e9 / r->rate);
			slot->n = k;
			memcpy(slot->s, p, k * 2);
			SAY_STORE(&r->micW, w + 1);
		}
		p += k;
	}
}

// sayRingFill fills dst (n samples, interleaved) from the output ring; when
// it runs dry the last sample ramps down to silence, and the audio ramps
// back in when it returns. Audio thread.
static void sayRingFill(sayRing *r, int16_t *dst, uint32_t n) {
	SAY_ADD(&r->callbacks, 1);
	SAY_ADD(&r->asked, n);
	uint64_t rd = r->outR;
	SAY_STORE(&r->cbNs, sayRingNow());
	SAY_STORE(&r->cbStartPos, rd);
	if (SAY_LOAD(&r->mute)) {
		memset(dst, 0, n * 2);
		return;
	}
	uint64_t avail = SAY_LOAD(&r->outW) - rd;
	uint32_t k = avail < n ? (uint32_t)avail : n;
	for (uint32_t i = 0; i < k; i++) {
		int16_t s = r->out[(rd + i) & (SAY_OUT_CAP - 1)];
		if (r->rampIn > 0) {
			s = (int16_t)((int32_t)s * (SAY_DECLICK + 1 - r->rampIn) / (SAY_DECLICK + 1));
			r->rampIn--;
		}
		dst[i] = s;
	}
	if (k > 0) {
		SAY_STORE(&r->outR, rd + k);
		r->lastOut = dst[k - 1];
	}
	if (k < n) {
		for (uint32_t i = k; i < n; i++) {
			uint32_t j = i - k;
			dst[i] = j < SAY_DECLICK ? (int16_t)((int32_t)r->lastOut * (int32_t)(SAY_DECLICK - j) / (SAY_DECLICK + 1)) : 0;
		}
		r->lastOut = 0;
		r->rampIn = SAY_DECLICK;
		SAY_ADD(&r->dryCallbacks, 1);
		SAY_ADD(&r->drySamples, n - k);
	}
}

// sayRingMicRead takes the oldest microphone block into dst (at most cap
// samples) and the time its last sample was captured into ns. 0: none.
static int sayRingMicRead(sayRing *r, int16_t *dst, int cap, uint64_t *ns) {
	uint64_t rd = r->micR;
	if (rd == SAY_LOAD(&r->micW)) {
		return 0;
	}
	sayMicSlot *slot = &r->slots[rd % SAY_MIC_SLOTS];
	int n = slot->n < (uint32_t)cap ? (int)slot->n : cap;
	memcpy(dst, slot->s, n * 2);
	*ns = slot->ns;
	SAY_STORE(&r->micR, rd + 1);
	return n;
}

// sayRingWrite queues samples to play; it returns how many fit.
static int sayRingWrite(sayRing *r, const int16_t *src, int n) {
	uint64_t w = r->outW;
	uint64_t room = SAY_OUT_CAP - (w - SAY_LOAD(&r->outR));
	if ((uint64_t)n > room) {
		n = (int)room;
	}
	for (int i = 0; i < n; i++) {
		r->out[(w + i) & (SAY_OUT_CAP - 1)] = src[i];
	}
	SAY_STORE(&r->outW, w + n);
	return n;
}

typedef struct {
	uint64_t written, played, cbStartPos, cbNs, now;
	uint64_t callbacks, asked, dryCallbacks, drySamples, micDropped;
} sayRingState;

static void sayRingGetState(sayRing *r, sayRingState *st) {
	st->written = r->outW;
	st->played = SAY_LOAD(&r->outR);
	st->cbStartPos = SAY_LOAD(&r->cbStartPos);
	st->cbNs = SAY_LOAD(&r->cbNs);
	st->now = sayRingNow();
	st->callbacks = SAY_LOAD(&r->callbacks);
	st->asked = SAY_LOAD(&r->asked);
	st->dryCallbacks = SAY_LOAD(&r->dryCallbacks);
	st->drySamples = SAY_LOAD(&r->drySamples);
	st->micDropped = SAY_LOAD(&r->micDropped);
}

static void sayRingMute(sayRing *r) { SAY_STORE(&r->mute, 1); }

#endif
