package events

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/types"
)

const (
	// TypeFeeApplied marks a transaction where a fee was assessed and routed.
	TypeFeeApplied = "fees.applied"
)

// FeeApplied records the outcome of a fee evaluation for analytics pipelines.
type FeeApplied struct {
	Payer             [20]byte
	Domain            string
	Asset             string
	Gross             *big.Int
	Fee               *big.Int
	Net               *big.Int
	PolicyVersion     uint64
	OwnerWallet       [20]byte
	FreeTierApplied   bool
	FreeTierLimit     uint64
	FreeTierRemaining uint64
	UsageCount        uint64
	WindowStart       time.Time
	FeeBasisPoints    uint32
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
	if asset := strings.TrimSpace(e.Asset); asset != "" {
		attrs["asset"] = strings.ToUpper(asset)
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
	if !zeroBytes(e.OwnerWallet[:]) {
		attrs["ownerWallet"] = hex.EncodeToString(e.OwnerWallet[:])
	}
	attrs["freeTierApplied"] = strconv.FormatBool(e.FreeTierApplied)
	if e.FreeTierLimit > 0 {
		attrs["freeTierLimit"] = strconv.FormatUint(e.FreeTierLimit, 10)
	}
	attrs["freeTierRemaining"] = strconv.FormatUint(e.FreeTierRemaining, 10)
	if e.UsageCount > 0 {
		attrs["usageCount"] = strconv.FormatUint(e.UsageCount, 10)
	}
	if !e.WindowStart.IsZero() {
		attrs["windowStartUnix"] = strconv.FormatInt(e.WindowStart.UTC().Unix(), 10)
	}
	if e.FeeBasisPoints > 0 {
		attrs["feeBps"] = strconv.FormatUint(uint64(e.FeeBasisPoints), 10)
	}
	return &types.Event{Type: TypeFeeApplied, Attributes: attrs}
}
