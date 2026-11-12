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

// Routes to the peer, shown next to the ping.
const (
	PathDirect = "P2P" // nothing between us: a direct link or the local network
	PathMesh   = "YGG" // through other Yggdrasil nodes
)

var peerPath string

// UpdatePeerPath sets the route the call takes (PathDirect, PathMesh, or ""
// when there is no call).
func UpdatePeerPath(path string) {
	netRateMu.Lock()
	changed := peerPath != path
	peerPath = path
	netRateMu.Unlock()
	if changed {
		RequestRedraw()
	}
}

// PeerPathSnapshot is the route the call takes ("" when unknown).
func PeerPathSnapshot() string {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return peerPath
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

var (
	rxVideoLoss            float64
	rxVideoLossUpdatedAt   time.Time
	peerVideoLoss          float64
	peerVideoLossUpdatedAt time.Time
)

func clampLoss(loss float64) float64 {
	if loss < 0 {
		return 0
	}
	if loss > 1 {
		return 1
	}
	return loss
}

// UpdateRxVideoLoss records the fraction of incoming video frames that were
// lost or could not be decoded; it is reported to the peer in pongs.
func UpdateRxVideoLoss(loss float64) {
	now := time.Now()
	netRateMu.Lock()
	rxVideoLoss, rxVideoLossUpdatedAt = clampLoss(loss), now
	netRateMu.Unlock()
	noteVideoLoss(clampLoss(loss), now)
}

// RxVideoLossSnapshot returns the latest incoming video loss and its sample time.
func RxVideoLossSnapshot() (loss float64, updatedAt time.Time) {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return rxVideoLoss, rxVideoLossUpdatedAt
}

// UpdatePeerVideoLoss records the loss of our video as reported by the peer.
func UpdatePeerVideoLoss(loss float64) {
	netRateMu.Lock()
	peerVideoLoss, peerVideoLossUpdatedAt = clampLoss(loss), time.Now()
	netRateMu.Unlock()
}

// PeerVideoLossSnapshot returns the peer's latest report on our video.
func PeerVideoLossSnapshot() (loss float64, updatedAt time.Time) {
	netRateMu.RLock()
	defer netRateMu.RUnlock()
	return peerVideoLoss, peerVideoLossUpdatedAt
}
