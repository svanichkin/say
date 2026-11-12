package tcp

import (
	"testing"
	"time"
)

// probe feeds one probe after the pacer held the sender back (the rate was
// the limit) during the last span.
func probe(p *adaptiveFilePacer, now time.Time, span time.Duration, rtt time.Duration, loss float64) {
	p.mu.Lock()
	p.limited = true
	p.mu.Unlock()
	p.observeAt(rtt, loss, now)
}

func TestFilePacerReachesFastLinksQuickly(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	now := time.Now()
	const base = 30 * time.Millisecond
	elapsed := time.Duration(0)
	for p.rate < 37<<20 { // 300 Mbit/s
		now = now.Add(fileProbeInterval)
		elapsed += fileProbeInterval
		probe(p, now, fileProbeInterval, base, 0)
		if elapsed > 10*time.Second {
			t.Fatalf("only %.1f MB/s after %v on a clean path", p.rate/(1<<20), elapsed)
		}
	}
	t.Logf("37 MB/s after %v of clean probes", elapsed)
}

func TestFilePacerBacksOffOnQueueAndLoss(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	now := time.Now()
	probe(p, now, fileProbeInterval, 30*time.Millisecond, 0) // base RTT
	start := p.rate
	now = now.Add(fileProbeInterval)
	probe(p, now, fileProbeInterval, 30*time.Millisecond+fileQueueTarget+time.Millisecond, 0)
	if p.rate != start*fileRateDecrease {
		t.Fatalf("queue over target: rate %.0f, want %.0f", p.rate, start*fileRateDecrease)
	}
	before := p.rate
	now = now.Add(time.Second)                                  // past the hold of the last decrease
	probe(p, now, fileProbeInterval, 30*time.Millisecond, 0.03) // media loss
	if p.rate != before*fileRateDecrease {
		t.Fatalf("media loss: rate %.0f, want %.0f", p.rate, before*fileRateDecrease)
	}
	before = p.rate
	now = now.Add(time.Second)
	probe(p, now, fileProbeInterval, 30*time.Millisecond+fileQueueSevere+time.Millisecond, 0)
	if p.rate != max(before*0.5, fileRateMin) || !p.pauseUntil.After(now) {
		t.Fatalf("severe queue: rate %.0f (want %.0f), pause %v", p.rate, before*0.5, p.pauseUntil.Sub(now))
	}
	for i := 0; i < 50; i++ {
		now = now.Add(3 * time.Second)
		probe(p, now, fileProbeInterval, time.Second, 0.5)
	}
	if p.rate != fileRateMin {
		t.Fatalf("floor: %.0f, want %d", p.rate, fileRateMin)
	}
}

func TestFilePacerDoesNotGrowWhenTheRateIsUnused(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	now := time.Now()
	for i := 0; i < 20; i++ {
		now = now.Add(fileProbeInterval)
		p.observeAt(30*time.Millisecond, 0, now) // the sender never waited: a slow disk
	}
	if p.rate != fileRateStart {
		t.Fatalf("rate grew to %.0f while the sender was not held back", p.rate)
	}
}

func TestFilePacerBaseRTTIgnoresItsOwnQueue(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	now := time.Now()
	probe(p, now, fileProbeInterval, 30*time.Millisecond, 0)
	// Minutes of a transfer with a standing queue: the baseline stays.
	for i := 0; i < int(4*time.Minute/fileProbeInterval); i++ {
		now = now.Add(fileProbeInterval)
		probe(p, now, fileProbeInterval, 300*time.Millisecond, 0)
	}
	if base := p.baseRTT(); base != 30*time.Millisecond {
		t.Fatalf("base RTT drifted to %v", base)
	}
}

func TestFilePacerTokenBucketHoldsTheRate(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.rate = 20 << 20 // 20 MB/s
	start := time.Now()
	sent := 0
	for sent < 4<<20 {
		p.waitAfterChunk(fileChunkBytes)
		sent += fileChunkBytes
	}
	elapsed := time.Since(start)
	want := time.Duration(float64(sent) / p.rate * float64(time.Second))
	if elapsed < want-fileBurstCredit || elapsed > want*3/2+20*time.Millisecond {
		t.Fatalf("4 MB at 20 MB/s took %v, want about %v", elapsed, want)
	}
}

func TestFilePacerFastProbesOnlyDuringTransfers(t *testing.T) {
	p := newAdaptiveFilePacer()
	if p.active() {
		t.Fatal("active without a file")
	}
	p.beginFile()
	if !p.active() {
		t.Fatal("not active while sending")
	}
	p.endFile()
	if p.active() {
		t.Fatal("active after the file")
	}
}

func TestFilePacerDecreasesOncePerRoundTrip(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	now := time.Now()
	probe(p, now, fileProbeInterval, 30*time.Millisecond, 0)
	start := p.rate
	// A queue of 600 ms takes a while to drain; probes keep showing it.
	for i := 0; i < 2; i++ {
		now = now.Add(fileProbeInterval)
		probe(p, now, fileProbeInterval, 630*time.Millisecond, 0)
	}
	if p.rate != start*0.5 {
		t.Fatalf("rate %.0f after two probes within one round trip, want one halving to %.0f", p.rate, start*0.5)
	}
	// Once the queue is gone the rate grows again.
	now = now.Add(time.Second)
	probe(p, now, fileProbeInterval, 30*time.Millisecond, 0)
	if p.rate <= start*0.5 {
		t.Fatalf("no recovery: %.0f", p.rate)
	}
}

func TestFilePacerRecoversFromTheFloor(t *testing.T) {
	p := newAdaptiveFilePacer()
	p.beginFile()
	p.rate = fileRateMin
	p.limited = false
	now := time.Now()
	p.observeAt(30*time.Millisecond, 0, now)
	// At the floor a 60 kB chunk takes about a second; the sender waits
	// for tokens, so probes in between still let the rate grow.
	p.waitAfterChunk(fileChunkBytes)
	p.waitAfterChunk(fileChunkBytes)
	now = now.Add(fileProbeInterval)
	p.observeAt(30*time.Millisecond, 0, now)
	if p.rate <= fileRateMin {
		t.Fatalf("stuck at the floor: %.0f", p.rate)
	}
}
