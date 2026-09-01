package rpc

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/core/identity"
	"nhbchain/crypto"
)

// TestHandleClaimableClaim_RejectsUnauthorizedAliasClaim is an independent
// regression test for an externally-reported finding (bug bounty submission
// NHB-TRIAGE-C6, not formally submitted for a bounty but present in the
// same triage file as NHB-TRIAGE-C4/C7): Node.ClaimableClaim (reached via
// the generic claimable_claim RPC) used to call straight into
// Manager.ClaimableClaim with zero identity checks -- strictly weaker than
// identity_claim's own (also-broken) check, since it applied to EVERY
// claimable regardless of how it was created. Combined with
// identity_createClaimable deriving the hashlock from
// identity.DeriveAliasID(alias) -- a value anyone can compute from nothing
// more than a target's public username -- any authenticated caller could
// drain a claimable meant for someone else's username, whether or not that
// username was registered yet, using only the target's name.
//
// This reproduces the exploit end to end through the real, currently
// registered RPC handlers (handleIdentityCreateClaimable,
// handleClaimableClaim), not a direct manager-level call, and covers the
// case the pre-existing TestHandleIdentityClaimableAliasAuthorization test
// did NOT: an alias that isn't registered yet at claim time -- the
// platform's own documented headline use case for claimables, and the
// specific gap identity_claim's pre-fix logic silently fell through on.
func TestHandleClaimableClaim_RejectsUnauthorizedAliasClaim(t *testing.T) {
	env := newTestEnv(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	var payer [20]byte
	copy(payer[:], payerAddr.Bytes())
	fundAccountNHB(t, env.node, payer, 1_000)

	const alias = "unclaimedusername"
	aliasID := identity.DeriveAliasID(alias)
	aliasHex := "0x" + hex.EncodeToString(aliasID[:])
	deadline := time.Now().Add(30 * time.Minute).Unix()

	createPayload := map[string]interface{}{
		"payer":     payerAddr.String(),
		"recipient": alias,
		"token":     "NHB",
		"amount":    "250",
		"deadline":  deadline,
	}
	createReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, createPayload)}}
	createRec := httptest.NewRecorder()
	env.server.handleIdentityCreateClaimable(createRec, env.newRequest(), createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("unexpected create status: %d", createRec.Code)
	}
	createResult, createErr := decodeRPCResponse(t, createRec)
	if createErr != nil {
		t.Fatalf("create claimable error: %+v", createErr)
	}
	var createResp identityCreateClaimableResult
	if err := json.Unmarshal(createResult, &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// alias is deliberately never registered -- the attacker only knows the
	// target's username, exactly as the original report described.
	attackerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate attacker: %v", err)
	}
	attackerAddr := attackerKey.PubKey().Address().String()

	attack := map[string]interface{}{
		"id":       createResp.ClaimID,
		"payee":    attackerAddr,
		"preimage": aliasHex,
	}
	attackReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, attack)}}
	attackRec := httptest.NewRecorder()
	env.server.handleClaimableClaim(attackRec, env.newRequest(), attackReq)
	if attackRec.Code == http.StatusOK {
		t.Fatalf("SECURITY: claimable_claim paid out an alias-addressed claimable to an attacker who only knew the public username, before the alias was even registered")
	}
	if attackRec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden status for the unauthorized claim, got %d", attackRec.Code)
	}

	// Now the rightful owner registers the alias and claims through the
	// SAME generic RPC the attacker used -- proving the fix authorizes the
	// legitimate case, not just rejects everything.
	rightfulKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rightful key: %v", err)
	}
	rightfulAddr := rightfulKey.PubKey().Address().String()
	setAliasReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, rightfulAddr), marshalParam(t, alias)}}
	env.server.handleIdentitySetAlias(httptest.NewRecorder(), env.newRequest(), setAliasReq)

	rightfulClaim := map[string]interface{}{
		"id":       createResp.ClaimID,
		"payee":    rightfulAddr,
		"preimage": aliasHex,
	}
	rightfulReq := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, rightfulClaim)}}
	rightfulRec := httptest.NewRecorder()
	env.server.handleClaimableClaim(rightfulRec, env.newRequest(), rightfulReq)
	if rightfulRec.Code != http.StatusOK {
		t.Fatalf("expected the rightful, now-registered owner's claim via claimable_claim to succeed, got %d", rightfulRec.Code)
	}
}

// TestHandleIdentityCreateClaimable_OmitsSecretHintFromEvent is an
// independent regression test for the second half of NHB-TRIAGE-C6:
// identity_createClaimable used to broadcast RecipientHint in cleartext in
// the claimable.created event unconditionally -- for an alias-derived hint
// that's harmless (it's public by construction), but for a raw, opaque
// hint the caller supplied as a genuine off-chain-shared secret (the
// documented "email" flow, distinct from the alias flow), broadcasting it
// on-chain before the real recipient ever sees it lets anyone watching the
// event log front-run the claim.
func TestHandleIdentityCreateClaimable_OmitsSecretHintFromEvent(t *testing.T) {
	env := newTestEnv(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	var payer [20]byte
	copy(payer[:], payerAddr.Bytes())
	fundAccountNHB(t, env.node, payer, 2_000)
	deadline := time.Now().Add(30 * time.Minute).Unix()

	// Opaque secret case: caller supplies a raw 32-byte hex value, not an
	// alias string.
	secretHintBytes := make([]byte, 32)
	for i := range secretHintBytes {
		secretHintBytes[i] = byte(i + 1)
	}
	secretHex := "0x" + hex.EncodeToString(secretHintBytes)
	secretPayload := map[string]interface{}{
		"payer":     payerAddr.String(),
		"recipient": secretHex,
		"token":     "NHB",
		"amount":    "100",
		"deadline":  deadline,
	}
	secretReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, secretPayload)}}
	secretRec := httptest.NewRecorder()
	env.server.handleIdentityCreateClaimable(secretRec, env.newRequest(), secretReq)
	if secretRec.Code != http.StatusOK {
		t.Fatalf("unexpected create status for opaque secret: %d", secretRec.Code)
	}

	events := env.node.Events()
	if len(events) == 0 {
		t.Fatalf("expected at least one event after creating the opaque-secret claimable")
	}
	last := events[len(events)-1]
	if last.Type != "claimable.created" {
		t.Fatalf("expected claimable.created event, got %s", last.Type)
	}
	zeroHex := hex.EncodeToString(make([]byte, 32))
	if got := last.Attributes["recipientHint"]; got != zeroHex {
		t.Fatalf("SECURITY: opaque secret hint was broadcast in cleartext in claimable.created: %s", got)
	}

	// Alias case, for contrast: the hint IS public (anyone can compute
	// DeriveAliasID(alias) themselves), so including it is fine.
	const alias = "publicaliasname"
	aliasID := identity.DeriveAliasID(alias)
	aliasHex := hex.EncodeToString(aliasID[:])
	aliasPayload := map[string]interface{}{
		"payer":     payerAddr.String(),
		"recipient": alias,
		"token":     "NHB",
		"amount":    "100",
		"deadline":  deadline,
	}
	aliasReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, aliasPayload)}}
	aliasRec := httptest.NewRecorder()
	env.server.handleIdentityCreateClaimable(aliasRec, env.newRequest(), aliasReq)
	if aliasRec.Code != http.StatusOK {
		t.Fatalf("unexpected create status for alias case: %d", aliasRec.Code)
	}
	events = env.node.Events()
	last = events[len(events)-1]
	if last.Type != "claimable.created" {
		t.Fatalf("expected claimable.created event, got %s", last.Type)
	}
	if got := last.Attributes["recipientHint"]; got != aliasHex {
		t.Fatalf("expected the public alias-derived hint to still be published, got %s want %s", got, aliasHex)
	}
}
