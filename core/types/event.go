package types

// Event represents a typed event emitted during state transitions.
type Event struct {
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
}
