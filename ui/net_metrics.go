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
	currentRates netRates
	peerLatency  time.Duration
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
