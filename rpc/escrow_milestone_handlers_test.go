package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/crypto"
)

// The milestone escrow mutators (create/fund/release/cancel/subscription
// update) wrote validator-local state outside the block pipeline, like the
// escrow_* methods disabled before them (see milestoneRPCDisabledMessage).
// Each must return codeMethodDisabled with HTTP 410 for any input, even a
// well-formed authenticated request, and escrow_milestoneGet must stay live.

func TestEscrowMilestoneMutatorsDisabled(t *testing.T) {
	env := newTestEnv(t)
	payerKey, _ := crypto.GeneratePrivateKey()
	payeeKey, _ := crypto.GeneratePrivateKey()
	id := "0x" + strings.Repeat("00", 32)
	sig := "0x" + strings.Repeat("11", 65)

	create := map[string]interface{}{
		"payer": payerKey.PubKey().Address().String(),
		"payee": payeeKey.PubKey().Address().String(),
		"legs": []map[string]interface{}{
			{"id": 1, "type": "deliverable", "title": "t", "token": "NHB", "amount": "1", "deadline": 4102444800},
		},
		"signature": sig,
	}
	legAction := map[string]interface{}{"id": id, "legId": 1, "signature": sig}
	subscription := map[string]interface{}{"id": id, "active": false, "signature": sig}

	cases := []struct {
		name    string
		params  interface{}
		handler func(http.ResponseWriter, *http.Request, *RPCRequest)
	}{
		{"create", create, env.server.handleEscrowMilestoneCreate},
		{"fund", legAction, env.server.handleEscrowMilestoneFund},
		{"release", legAction, env.server.handleEscrowMilestoneRelease},
		{"cancel", legAction, env.server.handleEscrowMilestoneCancel},
		{"subscriptionUpdate", subscription, env.server.handleEscrowMilestoneSubscriptionUpdate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, tc.params)}}
			recorder := httptest.NewRecorder()
			tc.handler(recorder, env.newRequest(), req)
			if recorder.Code != http.StatusGone {
				t.Fatalf("expected HTTP %d, got %d", http.StatusGone, recorder.Code)
			}
			_, rpcErr := decodeRPCResponse(t, recorder)
			if rpcErr == nil {
				t.Fatalf("expected disabled error")
			}
			if rpcErr.Code != codeMethodDisabled {
				t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
			}
			if !strings.Contains(rpcErr.Message, "disabled") {
				t.Fatalf("expected a disabled message, got %q", rpcErr.Message)
			}
		})
	}
}

// TestEscrowMilestoneGetStaysLive proves the read-only method is untouched:
// an unknown project is an ordinary not-found error, not the disabled error.
func TestEscrowMilestoneGetStaysLive(t *testing.T) {
	env := newTestEnv(t)
	params := map[string]string{"id": "0x" + strings.Repeat("00", 32)}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, params)}}
	recorder := httptest.NewRecorder()
	env.server.handleEscrowMilestoneGet(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected a not-found error for an unknown project")
	}
	if rpcErr.Code == codeMethodDisabled {
		t.Fatalf("escrow_milestoneGet must not be disabled")
	}
}
