package p2p

// Message is the generic structure for any data sent between nodes.
type Message struct {
	Type    byte
	Payload []byte
}

// Broadcaster defines any component that can broadcast messages to the network.
type Broadcaster interface {
	Broadcast(msg *Message) error
}

// MessageHandler defines any component that can process a raw message from the network.
type MessageHandler interface {
	HandleMessage(msg *Message) error
}

// PeerSender is the send path to a single connected peer. Enqueue never blocks:
// when the peer's queue is full the message is dropped and an error returned.
type PeerSender interface {
	ID() string
	Enqueue(msg *Message) error
}

// PeerMessageHandler is an optional extension of MessageHandler. When the
// server's handler implements it, every message is delivered together with the
// peer it arrived from, so that a request can be answered to that peer alone
// instead of being broadcast to everybody.
type PeerMessageHandler interface {
	HandlePeerMessage(from PeerSender, msg *Message) error
}
