package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPotsoSubmitEvidenceIsNotAnRPCMethod: evidence used to be accepted from
// whoever called potso_submitEvidence -- no authentication, no signer -- and the
// node then injected a transaction on the caller's behalf. Reports are now a
// signed TxTypeSubmitEvidence sent through nhb_sendTransaction like every other
// native transaction, so the method is gone, with or without credentials and
// whatever the parameters.
func TestPotsoSubmitEvidenceIsNotAnRPCMethod(t *testing.T) {
	fullPayload := json.RawMessage(`{"type":"DOWNTIME","offender":"nhb1x","heights":[1],` +
		`"reporter":"nhb1y","reporterSig":"0x00","timestamp":1}`)
	cases := []struct {
		name       string
		params     []json.RawMessage
		authorized bool
	}{
		{"no params, anonymous", nil, false},
		{"full payload, anonymous", []json.RawMessage{fullPayload}, false},
		{"full payload, authenticated", []json.RawMessage{fullPayload}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// One server per case: the per-source rate limiter would otherwise
			// answer 429 before the method dispatch is ever reached.
			env := newTestEnv(t)
			body := buildRPCRequestBody(t, 1, "potso_submitEvidence", tc.params)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer "+env.token)
			}
			rec := httptest.NewRecorder()
			env.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeMethodNotFound {
				t.Fatalf("expected method-not-found, got %+v", rpcErr)
			}
		})
	}
}

// TestPotsoEvidenceReadMethodsRemain: the read-only queries are unaffected.
func TestPotsoEvidenceReadMethodsRemain(t *testing.T) {
	env := newTestEnv(t)
	body := buildRPCRequestBody(t, 1, "potso_listEvidence", nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var listed struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(listed.Records) != 0 {
		t.Fatalf("expected no evidence on a fresh node, got %d", len(listed.Records))
	}
}
