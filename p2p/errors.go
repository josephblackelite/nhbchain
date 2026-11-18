package p2p

import "errors"

// ErrInvalidPayload indicates that a peer supplied a syntactically correct message with invalid contents.
var ErrInvalidPayload = errors.New("p2p: invalid payload")

// ErrPeerAlreadyConnected indicates that the requested peer session already exists.
var ErrPeerAlreadyConnected = errors.New("p2p: peer already connected")

// IsInvalidPayload reports whether the error originated from a malformed or invalid payload.
func IsInvalidPayload(err error) bool {
	return errors.Is(err, ErrInvalidPayload)
}

// ErrPeerIDMismatch indicates that the node reached at an address proved, in its handshake, an ID other
// than the one the dial expected (a seed whose address is now answered by somebody else).
var ErrPeerIDMismatch = errors.New("p2p: peer ID does not match the expected ID")
