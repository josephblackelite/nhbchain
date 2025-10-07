package events

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
)

const (
	// TypeFeeApplied marks a transaction where a fee was assessed and routed.
	TypeFeeApplied = "fees.applied"
)

// FeeApplied records the outcome of a fee evaluation for analytics pipelines.
type FeeApplied struct {
	Payer         [20]byte
	Domain        string
	Gross         *big.Int
	Fee           *big.Int
	Net           *big.Int
	PolicyVersion uint64
	RouteWallet   [20]byte
}

// EventType satisfies the events.Event interface.
func (FeeApplied) EventType() string { return TypeFeeApplied }

// Event converts the structured payload into a broadcastable event.
func (e FeeApplied) Event() *types.Event {
	attrs := map[string]string{}
	if !zeroBytes(e.Payer[:]) {
		attrs["payer"] = hex.EncodeToString(e.Payer[:])
	}
	if domain := strings.TrimSpace(e.Domain); domain != "" {
		attrs["domain"] = domain
	}
	if e.Gross != nil {
		attrs["grossWei"] = e.Gross.String()
	}
	if e.Fee != nil {
		attrs["feeWei"] = e.Fee.String()
	}
	if e.Net != nil {
		attrs["netWei"] = e.Net.String()
	}
	if e.PolicyVersion > 0 {
		attrs["policyVersion"] = strconv.FormatUint(e.PolicyVersion, 10)
	}
	if !zeroBytes(e.RouteWallet[:]) {
		attrs["routeWallet"] = hex.EncodeToString(e.RouteWallet[:])
	}
	return &types.Event{Type: TypeFeeApplied, Attributes: attrs}
}
