package ui

// Video control messages (protocol 7) travel from the video receiver (UDP)
// to the peer over the control connection (TCP), and from there to the
// peer's video sender: recovery requests, acknowledgements and keyframe
// requests for a layer, and the receiver's reports on what arrived
// (network/udp/video_layers.go). They are opaque here.

var (
	videoCtlOut = make(chan []byte, 256)
	videoCtlIn  = make(chan []byte, 256)
)

// SendVideoCtl queues a message for the peer's video sender. A full queue
// drops it: the receiver repeats what matters.
func SendVideoCtl(msg []byte) {
	select {
	case videoCtlOut <- msg:
	default:
	}
}

// VideoCtlOut delivers SendVideoCtl messages to the control connection.
func VideoCtlOut() <-chan []byte { return videoCtlOut }

// NotePeerVideoCtl hands a message from the peer's video receiver to this
// side's video sender. A full queue drops its oldest.
func NotePeerVideoCtl(msg []byte) {
	for {
		select {
		case videoCtlIn <- msg:
			return
		default:
		}
		select {
		case <-videoCtlIn:
		default:
		}
	}
}

// PeerVideoCtl delivers the peer's video control messages to the sender.
func PeerVideoCtl() <-chan []byte { return videoCtlIn }
