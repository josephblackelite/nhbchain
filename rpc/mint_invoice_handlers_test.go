package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
)

// mint_getInvoiceStatus says whether a mint invoice identifier is recorded as settled in the committed
// state. It exists because mint_with_sig refuses a repeated identifier with the same error whether the
// first mint has been executed in a block or is only waiting in this node's mempool; the read tells an
// operator which of the two it is. Operator credential only.

func invoiceStatusRequest(t *testing.T, token string, params ...interface{}) *http.Request {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(params))
	for _, p := range params {
		raw = append(raw, marshalParam(t, p))
	}
	body := buildRPCRequestBody(t, 7, "mint_getInvoiceStatus", raw)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

type invoiceStatusAnswer struct {
	InvoiceID string `json:"invoiceId"`
	Settled   bool   `json:"settled"`
	Height    uint64 `json:"height"`
}

func readInvoiceStatus(t *testing.T, env *testEnv, invoiceID string) invoiceStatusAnswer {
	t.Helper()
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, invoiceStatusRequest(t, signEnvJWTWithRole(t, env.now, "operator"), invoiceID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status of %q: expected 200, got %d: %s", invoiceID, rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("status of %q: unexpected rpc error %+v", invoiceID, rpcErr)
	}
	var out invoiceStatusAnswer
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode result %s: %v", result, err)
	}
	return out
}

// submitMintOverRPC sends a real mint_with_sig request and returns the HTTP status and rpc error.
func submitMintOverRPC(t *testing.T, env *testEnv, minterKey *crypto.PrivateKey, invoiceID string) (int, *RPCError) {
	t.Helper()
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	voucher := core.MintVoucher{
		InvoiceID: invoiceID,
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "100",
		ChainID:   core.MintChainID,
		Expiry:    time.Now().Add(time.Hour).Unix(),
	}
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(mintWithSigRequestBody(t, 1, voucher, minterKey))))
	_, rpcErr := decodeRPCResponse(t, rec)
	return rec.Code, rpcErr
}

func newInvoiceStatusEnv(t *testing.T) (*testEnv, *crypto.PrivateKey) {
	t.Helper()
	env := newTestEnv(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter key: %v", err)
	}
	assignMinterRole(t, env, "MINTER_NHB", minterKey)
	return env, minterKey
}

func commitWaiting(t *testing.T, env *testEnv) {
	t.Helper()
	block, err := env.node.CreateBlock(env.node.GetMempool())
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := env.node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
}

func TestMintInvoiceStatusIsOperatorOnly(t *testing.T) {
	if !IsOperatorOnlyMethod("mint_getInvoiceStatus") {
		t.Fatalf("mint_getInvoiceStatus is not in the operator-only table")
	}
	// mint_with_sig is exempt from the operator gate because the voucher's own signature authorizes it. The
	// read carries no signature of its own, so none of the gate's exemptions may cover it.
	if isSelfAuthenticatedMintMethod("mint_getInvoiceStatus") || isSignatureThresholdAuthorizedMethod("mint_getInvoiceStatus") || isRetiredMethod("mint_getInvoiceStatus") {
		t.Fatalf("mint_getInvoiceStatus is exempt from the operator gate")
	}
}

func TestMintInvoiceStatusRefusesACallWithNoCredential(t *testing.T) {
	env, minterKey := newInvoiceStatusEnv(t)
	if code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-no-credential"); code != http.StatusOK || rpcErr != nil {
		t.Fatalf("mint submission: %d %+v", code, rpcErr)
	}
	commitWaiting(t, env)

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, invoiceStatusRequest(t, "", "inv-no-credential"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no credential, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil || rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected codeUnauthorized, got %+v", rpcErr)
	}
	if len(result) != 0 && string(result) != "null" {
		t.Fatalf("a refused call still carries a result: %s", result)
	}
}

func TestMintInvoiceStatusRefusesACredentialThatIsNotAnOperators(t *testing.T) {
	env, minterKey := newInvoiceStatusEnv(t)
	if code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-not-operator"); code != http.StatusOK || rpcErr != nil {
		t.Fatalf("mint submission: %d %+v", code, rpcErr)
	}
	commitWaiting(t, env)

	for _, token := range []string{env.token, signEnvJWTWithRole(t, env.now, "reader"), signEnvJWTWithRole(t, env.now, "operators")} {
		rec := httptest.NewRecorder()
		env.server.ServeHTTP(rec, invoiceStatusRequest(t, token, "inv-not-operator"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for a valid credential without the operator role, got %d: %s", rec.Code, rec.Body.String())
		}
		result, rpcErr := decodeRPCResponse(t, rec)
		if rpcErr == nil || rpcErr.Code != codeOperatorOnly {
			t.Fatalf("expected codeOperatorOnly, got %+v", rpcErr)
		}
		if len(result) != 0 && string(result) != "null" {
			t.Fatalf("a refused call still carries a result: %s", result)
		}
	}
}

func TestMintInvoiceStatusOfAnInvoiceTheChainNeverSaw(t *testing.T) {
	env, _ := newInvoiceStatusEnv(t)
	got := readInvoiceStatus(t, env, "inv-unknown")
	if got.Settled {
		t.Fatalf("an invoice no mint ever named reads settled")
	}
	if got.InvoiceID != "inv-unknown" {
		t.Fatalf("the answer names %q, want the invoice asked about", got.InvoiceID)
	}
	if want := env.node.GetHeight() + 1; got.Height != want {
		t.Fatalf("height %d, want the next block's height %d", got.Height, want)
	}
}

// The case the read exists for, over the wire: the first mint is accepted and waits, a second submission
// is refused with the duplicate code, and the read says the invoice is NOT settled. After the block the
// second submission is refused with the same code, and the read says settled.
func TestMintInvoiceStatusTellsAWaitingMintFromALandedOne(t *testing.T) {
	env, minterKey := newInvoiceStatusEnv(t)

	if code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-two-answers"); code != http.StatusOK || rpcErr != nil {
		t.Fatalf("first submission: %d %+v", code, rpcErr)
	}
	code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-two-answers")
	if code != http.StatusConflict || rpcErr == nil || rpcErr.Code != codeDuplicateTx {
		t.Fatalf("second submission while the first waits: expected 409 with the duplicate code, got %d %+v", code, rpcErr)
	}
	waiting := readInvoiceStatus(t, env, "inv-two-answers")
	if waiting.Settled {
		t.Fatalf("a mint that is only waiting reads settled")
	}

	commitWaiting(t, env)

	code, rpcErr = submitMintOverRPC(t, env, minterKey, "inv-two-answers")
	if code != http.StatusConflict || rpcErr == nil || rpcErr.Code != codeDuplicateTx {
		t.Fatalf("second submission after the block: expected 409 with the duplicate code, got %d %+v", code, rpcErr)
	}
	landed := readInvoiceStatus(t, env, "inv-two-answers")
	if !landed.Settled {
		t.Fatalf("the mint is in a committed block and the invoice does not read settled")
	}
	if landed.Height != waiting.Height+1 {
		t.Fatalf("height went from %d to %d over one block, want one more", waiting.Height, landed.Height)
	}
	if other := readInvoiceStatus(t, env, "inv-two-answers-2"); other.Settled {
		t.Fatalf("another invoice reads settled")
	}
}

func TestMintInvoiceStatusAnswerShape(t *testing.T) {
	env, minterKey := newInvoiceStatusEnv(t)
	if code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-shape"); code != http.StatusOK || rpcErr != nil {
		t.Fatalf("mint submission: %d %+v", code, rpcErr)
	}
	commitWaiting(t, env)

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, invoiceStatusRequest(t, signEnvJWTWithRole(t, env.now, "operator"), "  inv-shape \n"))
	result, rpcErr := decodeRPCResponse(t, rec)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("expected 200, got %d %+v", rec.Code, rpcErr)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result, &fields); err != nil {
		t.Fatalf("decode result %s: %v", result, err)
	}
	if len(fields) != 3 {
		t.Fatalf("the answer has %d fields, want exactly invoiceId, settled and height: %s", len(fields), result)
	}
	if got := string(fields["invoiceId"]); got != `"inv-shape"` {
		t.Fatalf("invoiceId is %s, want the trimmed identifier as a string", got)
	}
	if got := string(fields["settled"]); got != "true" {
		t.Fatalf("settled is %s, want the boolean true", got)
	}
	var height uint64
	if err := json.Unmarshal(fields["height"], &height); err != nil {
		t.Fatalf("height %s is not a number: %v", fields["height"], err)
	}
	if want := env.node.GetHeight() + 1; height != want {
		t.Fatalf("height %d, want the next block's height %d", height, want)
	}
}

func TestMintInvoiceStatusRefusesABadIdentifier(t *testing.T) {
	env, minterKey := newInvoiceStatusEnv(t)
	if code, rpcErr := submitMintOverRPC(t, env, minterKey, "inv-bad-params"); code != http.StatusOK || rpcErr != nil {
		t.Fatalf("mint submission: %d %+v", code, rpcErr)
	}
	commitWaiting(t, env)
	operator := signEnvJWTWithRole(t, env.now, "operator")

	cases := []struct {
		name   string
		params []interface{}
	}{
		{"no parameter", nil},
		{"two parameters", []interface{}{"inv-bad-params", "inv-bad-params"}},
		{"an empty string", []interface{}{""}},
		{"only spaces", []interface{}{" \t\n "}},
		{"a number", []interface{}{42}},
		{"a boolean", []interface{}{true}},
		{"null", []interface{}{nil}},
		{"an object", []interface{}{map[string]string{"invoiceId": "inv-bad-params"}}},
		{"a list", []interface{}{[]string{"inv-bad-params"}}},
	}
	for i, tc := range cases {
		rec := httptest.NewRecorder()
		req := invoiceStatusRequest(t, operator, tc.params...)
		// Each case comes from its own address, so the server's per-address rate limit is not what answers.
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:4000", i+1)
		env.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		result, rpcErr := decodeRPCResponse(t, rec)
		if rpcErr == nil || rpcErr.Code != codeInvalidParams {
			t.Fatalf("%s: expected codeInvalidParams, got %+v", tc.name, rpcErr)
		}
		if len(result) != 0 && string(result) != "null" {
			t.Fatalf("%s: a refused call still carries a result: %s", tc.name, result)
		}
	}
}
