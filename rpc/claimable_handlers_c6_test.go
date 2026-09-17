package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/crypto"
)

// TestHandleClaimableClaim_RejectsUnauthorizedAliasClaim and
// TestHandleIdentityCreateClaimable_OmitsSecretHintFromEvent used to
// reproduce NHB-TRIAGE-C6 end to end through handleIdentityCreateClaimable
// and handleClaimableClaim. Both handlers are now disabled (see
// claimableRPCDisabledMessage in claimable_handlers.go and
// identityRPCDisabledMessage in identity_handlers.go) -- they mutated the
// live state trie directly outside the block pipeline, guaranteeing a
// consensus fork/halt on this 2-validator zero-quorum-slack chain. With
// claimable creation and claiming unreachable through RPC at all, the C6
// exploit path (and its fix) is moot: there is no longer a live claim
// authorization gap for these RPCs to have. Replaced with a direct check
// that handleClaimableClaim returns the disabled error regardless of input.
func TestHandleClaimableClaimDisabled(t *testing.T) {
	env := newTestEnv(t)
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee: %v", err)
	}
	payload := map[string]interface{}{
		"id":       "0x" + strings.Repeat("00", 32),
		"payee":    payeeKey.PubKey().Address().String(),
		"preimage": "0x" + strings.Repeat("00", 32),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleClaimableClaim(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
}
