package udp

import (
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/mediactrl"
	"github.com/svanichkin/say/ui"
)

const audioSendQueueFrames = 8 // at most 160 ms of encoded speech
const audioSendMaxAge = audioSendQueueFrames * audioFrameMillis * time.Millisecond

type audioDatagram struct {
	data     []byte
	addr     *net.UDPAddr
	queuedAt time.Time
}

// Capture/AEC/encoding never wait on the transport. The bounded encoded queue
// discards old speech under backpressure, preserving each packet's media time.
func startAudioSender(wg *sync.WaitGroup, pc net.PacketConn, stop func(),
	stopCh <-chan struct{}, getPeer func() *net.UDPAddr, enc audioEncoder, aec *audioEcho,
	frameSamples int, mediaEpoch time.Time, audioMicIn <-chan device.AudioCaptureFrame,
) {
	if !useVoice || enc == nil || audioMicIn == nil {
		return
	}
	if aec != nil {
		var err error
		aec.trace, err = newAudioTrace(os.Getenv("SAY_AUDIO_TRACE"))
		if err != nil {
			log.Printf("[audio/trace] %v", err)
		}
	}
	packets := make(chan audioDatagram, audioSendQueueFrames)
	var dropped atomic.Uint64
	wg.Add(2)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		last := time.Now()
		var bytes, count uint64
		for {
			select {
			case <-stopCh:
				return
			case <-tick.C:
				rate := float64(bytes) / 1024 / time.Since(last).Seconds()
				logs.LogV("[voice/udp] tx: %d pkts, %.2f kB/s, %d stale/full-queue drops", count, rate, dropped.Swap(0))
				ui.UpdateTxAudioRate(rate)
				bytes, count, last = 0, 0, time.Now()
			case pkt, ok := <-packets:
				if !ok {
					return
				}
				if time.Since(pkt.queuedAt) > audioSendMaxAge {
					dropped.Add(1)
					continue
				}
				select {
				case <-stopCh:
					return
				default:
				}
				n, err := pc.WriteTo(pkt.data, pkt.addr)
				if err != nil {
					if !isFatalSendError(err) {
						sendErrLog.report("voice", err)
						continue
					}
					log.Printf("[voice/udp] write error: %v", err)
					if stop != nil {
						stop()
					}
					return
				}
				count++
				if n > mediaHeaderBytes {
					bytes += uint64(n - mediaHeaderBytes)
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer close(packets)
		if aec != nil && aec.trace != nil {
			defer aec.trace.close()
		}
		var pcmBuf []int16
		var pcmStartAt time.Time
		var expectedSample uint64
		var timelineValid bool
		need := frameSamples * audioChannels
		aecSamples := make([]int16, need)
		encoded := make([]byte, maxAudioPacket)
		for {
			select {
			case <-stopCh:
				return
			case capture, ok := <-audioMicIn:
				if !ok {
					return
				}
				if len(capture.Samples) == 0 {
					continue
				}
				if !mediactrl.CaptureEnabled() {
					if aec != nil {
						aec.setCaptureEnabled(false)
					}
					pcmBuf = pcmBuf[:0]
					timelineValid = false
					continue
				}
				if aec != nil {
					aec.setCaptureEnabled(true)
				}
				if !timelineValid || capture.FirstSample != expectedSample {
					if timelineValid {
						logs.LogV("[voice/udp] capture discontinuity: expected sample %d, got %d", expectedSample, capture.FirstSample)
						if aec != nil {
							// Keep reference and microphone sample counts aligned across a drop.
							skipped := 0
							if capture.FirstSample > expectedSample {
								skipped = int(min(capture.FirstSample-expectedSample, uint64(audioSampleRate*2)))
							}
							aec.skipCapture(skipped + len(pcmBuf)/audioChannels)
						}
					}
					pcmBuf = pcmBuf[:0]
					pcmStartAt = capture.CapturedAt
					timelineValid = true
				}
				expectedSample = capture.FirstSample + uint64(len(capture.Samples)/audioChannels)
				pcmBuf = append(pcmBuf, capture.Samples...)
				for len(pcmBuf) >= need {
					chunk := pcmBuf[:need]
					pcm := chunk
					if aec != nil {
						if err := aec.capture(chunk, aecSamples); err != nil {
							log.Printf("[audio/aec] capture frame: %v", err)
						} else {
							pcm = aecSamples
						}
					}
					addr := getPeer()
					if addr != nil {
						written, err := enc.EncodeFrame(pcm, encoded)
						if err != nil {
							log.Printf("[voice/udp] %s encode error: %v", audioCodec, err)
							if stop != nil {
								stop()
							}
							return
						}
						if written > 0 {
							header := encodeMediaHeader(mediaKindAudio, mediaIndex(mediaEpoch, pcmStartAt))
							data := make([]byte, mediaHeaderBytes+written)
							copy(data, header[:])
							copy(data[mediaHeaderBytes:], encoded[:written])
							pkt := audioDatagram{data: data, addr: addr, queuedAt: time.Now()}
							select {
							case packets <- pkt:
							default:
								select {
								case <-packets:
									dropped.Add(1)
								default:
								}
								select {
								case packets <- pkt:
								default:
									dropped.Add(1)
								}
							}
						}
					}
					pcmBuf = pcmBuf[need:]
					pcmStartAt = pcmStartAt.Add(time.Duration(frameSamples) * time.Second / audioSampleRate)
				}
			}
		}
	}()
}
