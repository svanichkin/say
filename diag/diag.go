// Package diag collects temporary audio pipeline counters and logs them once
// per second as a single "[diag]" line. It exists to locate audio glitches and
// is meant to be removed afterwards. All updates are lock-free so the audio
// callbacks never wait on it.
package diag

import (
	"log"
	"math"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Counter accumulates events between reports.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Add(n int64) { c.v.Add(n) }
func (c *Counter) Inc()        { c.v.Add(1) }
func (c *Counter) take() int64 { return c.v.Swap(0) }

// Max keeps the largest value observed between reports.
type Max struct{ v atomic.Int64 }

func (m *Max) Observe(x int64) {
	for {
		cur := m.v.Load()
		if x <= cur || m.v.CompareAndSwap(cur, x) {
			return
		}
	}
}
func (m *Max) take() int64 { return m.v.Swap(0) }

// Min keeps the smallest value observed between reports (-1 if none).
type Min struct{ v atomic.Int64 }

func (m *Min) Observe(x int64) {
	x++ // 0 means "unset"
	for {
		cur := m.v.Load()
		if (cur != 0 && x >= cur) || m.v.CompareAndSwap(cur, x) {
			return
		}
	}
}
func (m *Min) take() int64 { return m.v.Swap(0) - 1 }

// Speaker (device callback).
var (
	SpkCallbacks       Counter
	SpkFrames          Counter // frames taken from the playback queue
	SpkIdle            Counter // callbacks with no audio at all while starved
	SpkUnderruns       Counter // callbacks that ran out of audio mid-stream
	SpkUnderrunSamples Counter
	SpkGaps            Counter // crossfades over discarded audio
	SpkCallbackMaxUs   Max     // time spent inside the callback
	SpkIntervalMaxUs   Max     // time between callbacks
	SpkIntervalMinUs   Min
	SpkFrameCount      Max // largest frameCount requested
)

// Microphone (device callback).
var (
	MicCallbacks     Counter
	MicSamples       Counter
	MicDrops         Counter // blocks dropped because the sender was late
	MicClipped       Counter // samples at or near full scale
	MicPeak          Max
	MicIntervalMaxUs Max
	MicIntervalMinUs Min
	MicBlockMin      Min
	MicBlockMax      Max
)

// Sender (mic → codec → UDP).
var (
	TxFrames      Counter
	TxPackets     Counter
	TxDTX         Counter // frames the codec chose not to send
	TxResyncs     Counter // capture timeline restarts after lost samples
	TxSendErrors  Counter
	TxEncodeMaxUs Max
	TxLagMaxMs    Max // capture time to encode time
	TxDeltaMaxUs  Max // |capture time - expected timeline position|
)

// Receiver (UDP → decoder → playback queue).
var (
	RxPackets      Counter
	RxDecoded      Counter
	RxConcealGap   Counter // missing slots concealed before a waiting packet
	RxConcealTick  Counter // slots concealed because nothing arrived in time
	RxLate         Counter // packets dropped after their slot was concealed
	RxReordered    Counter
	RxDuplicates   Counter
	RxResets       Counter // long gaps: restart at the live edge
	RxRebuffers    Counter
	RxTrimmed      Counter // frames dropped to bound latency
	RxDecodeErrors Counter
	RxBudgetOut    Counter // concealment budget exhausted
	RxQueueMinMs   Min
	RxQueueMaxMs   Max
	RxPendingMax   Max // reorder buffer depth
	RxLeadMs       Max // adaptive jitter buffer lead
	RxExpanded     Counter
	RxShrunk       Counter
)

// Start logs the counters once per second until stop is closed.
func Start(stop <-chan struct{}) {
	go func() {
		pid := os.Getpid()
		var gc debug.GCStats
		debug.ReadGCStats(&gc)
		lastGC, lastPause := gc.NumGC, gc.PauseTotal
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			debug.ReadGCStats(&gc)
			var maxPause time.Duration
			for i := 0; i < int(gc.NumGC-lastGC) && i < len(gc.Pause); i++ {
				maxPause = max(maxPause, gc.Pause[i])
			}
			gcN, gcPause := gc.NumGC-lastGC, gc.PauseTotal-lastPause
			lastGC, lastPause = gc.NumGC, gc.PauseTotal

			log.Printf("[diag] pid=%d "+
				"SPK cb=%d frames=%d idle=%d under=%d/%dsmp gaps=%d cbMax=%dus ivl=%d..%dus fc=%d | "+
				"MIC cb=%d smp=%d drop=%d clip=%d peak=%d ivl=%d..%dus blk=%d..%d | "+
				"TX fr=%d pkt=%d dtx=%d resync=%d sendErr=%d encMax=%dus lagMax=%dms tsDeltaMax=%dus | "+
				"RX pkt=%d dec=%d concGap=%d concTick=%d late=%d reord=%d dup=%d reset=%d rebuf=%d trim=%d decErr=%d budget=%d q=%d..%dms pend=%d lead=%dms exp=%d shrink=%d | "+
				"GC n=%d pause=%s max=%s",
				pid,
				SpkCallbacks.take(), SpkFrames.take(), SpkIdle.take(), SpkUnderruns.take(), SpkUnderrunSamples.take(), SpkGaps.take(),
				SpkCallbackMaxUs.take(), SpkIntervalMinUs.take(), SpkIntervalMaxUs.take(), SpkFrameCount.take(),
				MicCallbacks.take(), MicSamples.take(), MicDrops.take(), MicClipped.take(), MicPeak.take(),
				MicIntervalMinUs.take(), MicIntervalMaxUs.take(), MicBlockMin.take(), MicBlockMax.take(),
				TxFrames.take(), TxPackets.take(), TxDTX.take(), TxResyncs.take(), TxSendErrors.take(),
				TxEncodeMaxUs.take(), TxLagMaxMs.take(), TxDeltaMaxUs.take(),
				RxPackets.take(), RxDecoded.take(), RxConcealGap.take(), RxConcealTick.take(), RxLate.take(),
				RxReordered.take(), RxDuplicates.take(), RxResets.take(), RxRebuffers.take(), RxTrimmed.take(),
				RxDecodeErrors.take(), RxBudgetOut.take(), RxQueueMinMs.take(), RxQueueMaxMs.take(), RxPendingMax.take(),
				RxLeadMs.take(), RxExpanded.take(), RxShrunk.take(),
				gcN, gcPause, maxPause)
		}
	}()
}

// Abs16 returns |v| as int64 without overflowing on -32768.
func Abs16(v int16) int64 { return int64(math.Abs(float64(v))) }
