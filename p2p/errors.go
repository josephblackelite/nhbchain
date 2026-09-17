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
