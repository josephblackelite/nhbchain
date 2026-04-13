package events

import (
	"encoding/hex"
	"fmt"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	TypePotsoEvidenceAccepted = "potso.evidence.accepted"
	TypePotsoEvidenceRejected = "potso.evidence.rejected"
)

type PotsoEvidenceAccepted struct {
	Hash         [32]byte
	EvidenceType string
	Offender     [20]byte
	Height       uint64
	Reporter     [20]byte
}

func (e PotsoEvidenceAccepted) Event() *types.Event {
	offender := crypto.MustNewAddress(crypto.NHBPrefix, e.Offender[:])
	reporter := crypto.MustNewAddress(crypto.NHBPrefix, e.Reporter[:])
	attrs := map[string]string{
		"hash":     "0x" + hex.EncodeToString(e.Hash[:]),
		"type":     e.EvidenceType,
		"offender": offender.String(),
		"height":   fmt.Sprintf("%d", e.Height),
		"reporter": reporter.String(),
	}
	return &types.Event{Type: TypePotsoEvidenceAccepted, Attributes: attrs}
}

type PotsoEvidenceRejected struct {
	Reporter [20]byte
	Reason   string
}

func (e PotsoEvidenceRejected) Event() *types.Event {
	reporter := crypto.MustNewAddress(crypto.NHBPrefix, e.Reporter[:])
	attrs := map[string]string{
		"reporter": reporter.String(),
	}
	if e.Reason != "" {
		attrs["reason"] = e.Reason
	}
	return &types.Event{Type: TypePotsoEvidenceRejected, Attributes: attrs}
}
