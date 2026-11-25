package tcp

import (
	"log"
	"net"
	"sync"
)

// A call's two sides send each other their own cards right after the
// handshake (frameCard: a vCard of the names and the devices, what a
// contact needs); what is done with one is the address book's
// (contacts.Store.Received).

// maxCard is the largest card sent: a frame's length is 16 bits.
const maxCard = 32 << 10

var cards struct {
	sync.Mutex
	mine     func() []byte                  // our card (nil: none)
	received func(host string, card []byte) // a peer's card, and the address it came from
}

// SetCards is where our card comes from and where a peer's goes.
func SetCards(mine func() []byte, received func(host string, card []byte)) {
	cards.Lock()
	cards.mine, cards.received = mine, received
	cards.Unlock()
}

// sendCard sends our card, if we have one.
func sendCard(conn net.Conn) {
	cards.Lock()
	mine := cards.mine
	cards.Unlock()
	if mine == nil {
		return
	}
	data := mine()
	if len(data) == 0 || len(data) > maxCard {
		return
	}
	if err := writeFrameSafe(conn, frameCard, data); err != nil {
		log.Printf("[p2p] our card not sent: %v", err)
	}
}

// receiveCard hands a peer's card on, aside (writing it is the book's);
// one a call is taken, none larger than maxCard (serveControlFrames).
func receiveCard(host string, data []byte) {
	if len(data) > maxCard {
		return
	}
	cards.Lock()
	received := cards.received
	cards.Unlock()
	if received != nil {
		go received(host, append([]byte(nil), data...))
	}
}
