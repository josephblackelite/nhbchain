package escrow_test

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"reflect"
	"strconv"
	"testing"

	"nhbchain/core/types"
	escrowpkg "nhbchain/native/escrow"
)

func TestEscrowEventsHaveDeterministicPayload(t *testing.T) {
	var id [32]byte
	copy(id[:], bytes.Repeat([]byte{0xAA}, 32))
	var payer [20]byte
	copy(payer[:], bytes.Repeat([]byte{0xBB}, 20))
	var payee [20]byte
	copy(payee[:], bytes.Repeat([]byte{0xCC}, 20))
	var mediator [20]byte
	copy(mediator[:], bytes.Repeat([]byte{0xDD}, 20))

	escrowDef := &escrowpkg.Escrow{
		ID:        id,
		Payer:     payer,
		Payee:     payee,
		Mediator:  mediator,
		Token:     "nhb",
		Amount:    big.NewInt(42_000),
		FeeBps:    125,
		CreatedAt: 1_700_000_123,
		Status:    escrowpkg.EscrowFunded,
	}
	expected := map[string]string{
		"id":        hex.EncodeToString(id[:]),
		"payer":     hex.EncodeToString(payer[:]),
		"payee":     hex.EncodeToString(payee[:]),
		"mediator":  hex.EncodeToString(mediator[:]),
		"token":     "NHB",
		"amount":    escrowDef.Amount.String(),
		"feeBps":    strconv.FormatUint(uint64(escrowDef.FeeBps), 10),
		"createdAt": strconv.FormatInt(escrowDef.CreatedAt, 10),
	}
	cases := []struct {
		name string
		fn   func(*escrowpkg.Escrow) *types.Event
		typ  string
	}{
		{"created", escrowpkg.NewCreatedEvent, escrowpkg.EventTypeEscrowCreated},
		{"funded", escrowpkg.NewFundedEvent, escrowpkg.EventTypeEscrowFunded},
		{"released", escrowpkg.NewReleasedEvent, escrowpkg.EventTypeEscrowReleased},
		{"refunded", escrowpkg.NewRefundedEvent, escrowpkg.EventTypeEscrowRefunded},
		{"expired", escrowpkg.NewExpiredEvent, escrowpkg.EventTypeEscrowExpired},
		{"disputed", escrowpkg.NewDisputedEvent, escrowpkg.EventTypeEscrowDisputed},
		{"resolved", func(e *escrowpkg.Escrow) *types.Event {
			return escrowpkg.NewResolvedEvent(e, escrowpkg.DecisionOutcomeUnknown, [32]byte{}, nil)
		}, escrowpkg.EventTypeEscrowResolved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt := tc.fn(escrowDef)
			if evt == nil {
				t.Fatalf("event function returned nil")
			}
			if evt.Type != tc.typ {
				t.Fatalf("unexpected event type: %s", evt.Type)
			}
			if !reflect.DeepEqual(evt.Attributes, expected) {
				t.Fatalf("unexpected attributes: %#v", evt.Attributes)
			}
		})
	}
}

func TestEscrowEventOmitsMediatorWhenZero(t *testing.T) {
	var id [32]byte
	copy(id[:], bytes.Repeat([]byte{0x11}, 32))
	var payer [20]byte
	copy(payer[:], bytes.Repeat([]byte{0x22}, 20))
	var payee [20]byte
	copy(payee[:], bytes.Repeat([]byte{0x33}, 20))

	escrowDef := &escrowpkg.Escrow{
		ID:        id,
		Payer:     payer,
		Payee:     payee,
		Token:     "znhb",
		Amount:    big.NewInt(1),
		FeeBps:    0,
		CreatedAt: 12345,
		Status:    escrowpkg.EscrowInit,
	}
	evt := escrowpkg.NewCreatedEvent(escrowDef)
	if _, ok := evt.Attributes["mediator"]; ok {
		t.Fatalf("mediator attribute should be omitted when address is zero")
	}
	if evt.Attributes["token"] != "ZNHB" {
		t.Fatalf("expected token to be normalised to ZNHB")
	}
}
