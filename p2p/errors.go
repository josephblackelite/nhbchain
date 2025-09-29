package p2p

import "errors"

// ErrInvalidPayload indicates that a peer supplied a syntactically correct message with invalid contents.
var ErrInvalidPayload = errors.New("p2p: invalid payload")

// IsInvalidPayload reports whether the error originated from a malformed or invalid payload.
func IsInvalidPayload(err error) bool {
	return errors.Is(err, ErrInvalidPayload)
}
