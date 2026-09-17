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
