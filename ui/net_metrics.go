package ui

import (
	"sync"
	"time"
)

var netRateMu sync.RWMutex

type netRates struct {
	txAudio float64
	txVideo float64
	rxAudio float64
	rxVideo float64
}

var (
	currentRates         netRates
	peerLatency          time.Duration
	rxAudioLoss          float64
	rxAudioLossUpdatedAt time.Time
)

func UpdateTxAudioRate(kBps float64) {
	netRateMu.Lock()
	currentRates.txAudio = kBps
	netRateMu.Unlock()
}

func UpdateTxVideoRate(kBps float64) {
	netRateMu.Lock()
	currentRates.txVideo = kBps
	netRateMu.Unlock()
}

func UpdateRxAudioRate(kBps float64) {
	netRateMu.Lock()
	currentRates.rxAudio = kBps
	netRateMu.Unlock()
}

func UpdateRxVideoRate(kBps float64) {
	netRateMu.Lock()
	currentRates.rxVideo = kBps
	netRateMu.Unlock()
}

func NetRateSnapshot() (txAudio, txVideo, rxAudio, rxVideo float64) {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return currentRates.txAudio, currentRates.txVideo, currentRates.rxAudio, currentRates.rxVideo
}

func UpdatePeerLatency(latency time.Duration) {
	netRateMu.Lock()
	peerLatency = latency
	netRateMu.Unlock()
}

func PeerLatencySnapshot() time.Duration {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return peerLatency
}

// UpdateRxAudioLoss records the estimated fraction of missing incoming audio frames.
func UpdateRxAudioLoss(loss float64) {
	if loss < 0 {
		loss = 0
	}
	if loss > 1 {
		loss = 1
	}
	netRateMu.Lock()
	rxAudioLoss = loss
	rxAudioLossUpdatedAt = time.Now()
	netRateMu.Unlock()
}

// RxAudioLossSnapshot returns the latest incoming audio loss estimate and its sample time.
func RxAudioLossSnapshot() (loss float64, updatedAt time.Time) {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return rxAudioLoss, rxAudioLossUpdatedAt
}
