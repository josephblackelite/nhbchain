package p2p

import "time"

// PexRequestPayload asks a peer for recently seen addresses.
type PexRequestPayload struct {
	Limit int    `json:"limit"`
	Token string `json:"token"`
}

// PexAddress captures a gossipable peer endpoint.
type PexAddress struct {
	Addr     string    `json:"addr"`
	NodeID   string    `json:"nodeID"`
	LastSeen time.Time `json:"lastSeen"`
}

// PexAddressesPayload contains the set of addresses returned for a request.
type PexAddressesPayload struct {
	Token     string       `json:"token"`
	Addresses []PexAddress `json:"addresses"`
}
