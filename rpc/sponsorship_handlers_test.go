package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/rpc/modules"
)

// tx_setSponsorshipEnabled was disabled (see
// sponsorshipToggleRPCDisabledMessage in http.go): it took an unsigned
// "caller" address on trust and flipped a validator-local flag that decides
// whether sponsored transactions are valid. It now answers like the other
// retired mutators, whatever the request carries.

func TestTxSetSponsorshipEnabledDisabled(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := map[string]interface{}{
		"caller":  key.PubKey().Address().String(),
		"enabled": false,
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	rec := httptest.NewRecorder()
	env.server.handleTxSetSponsorshipEnabled(rec, env.newRequest(), req)

	if rec.Code != http.StatusGone {
		t.Fatalf("expected HTTP %d (gone), got %d", http.StatusGone, rec.Code)
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected disabled error")
	}
	if rpcErr.Code != codeMethodDisabled {
		t.Fatalf("expected code %d (disabled) got %d", codeMethodDisabled, rpcErr.Code)
	}
	if !strings.Contains(rpcErr.Message, "disabled") {
		t.Fatalf("expected the disabled message, got %q", rpcErr.Message)
	}
}

// TestTxSetSponsorshipEnabledCannotFlipTheFlag drives the real dispatch: even a
// caller naming a genuine ROLE_PAYMASTER_ADMIN holder, with or without
// credentials, gets the disabled answer and the flag never moves.
func TestTxSetSponsorshipEnabledCannotFlipTheFlag(t *testing.T) {
	env := newTestEnv(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	admin := adminKey.PubKey().Address()
	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		return manager.SetRole("ROLE_PAYMASTER_ADMIN", admin.Bytes())
	}); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	if !env.node.PaymasterModuleEnabled() {
		t.Fatalf("sponsorship should start enabled")
	}

	validParams := []json.RawMessage{marshalParam(t, map[string]interface{}{"caller": admin.String(), "enabled": false})}
	tests := []struct {
		name   string
		params []json.RawMessage
		auth   string
	}{
		{"authorised admin caller", validParams, "Bearer " + env.token},
		{"admin caller without credentials", validParams, ""},
		{"admin caller with a bad token", validParams, "Bearer not-a-token"},
		{"no parameters", nil, "Bearer " + env.token},
		{"two parameters", append(append([]json.RawMessage{}, validParams...), validParams...), "Bearer " + env.token},
		{"unknown caller", []json.RawMessage{marshalParam(t, map[string]interface{}{"caller": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0", "enabled": false})}, "Bearer " + env.token},
	}
	for i, tt := range tests {
		body := buildRPCRequestBody(t, i+1, "tx_setSponsorshipEnabled", tt.params)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		// A distinct source per request keeps the per-IP rate limiter out of it.
		req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", i+1)
		if tt.auth != "" {
			req.Header.Set("Authorization", tt.auth)
		}
		rec := httptest.NewRecorder()
		env.server.ServeHTTP(rec, req)

		if rec.Code != http.StatusGone {
			t.Fatalf("%s: expected HTTP %d, got %d: %s", tt.name, http.StatusGone, rec.Code, rec.Body.String())
		}
		if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeMethodDisabled {
			t.Fatalf("%s: expected the disabled error, got %+v", tt.name, rpcErr)
		}
		if !env.node.PaymasterModuleEnabled() {
			t.Fatalf("SECURITY: %s flipped the per-node sponsorship flag", tt.name)
		}
	}

	// The read-only view stays live and still reports sponsorship enabled.
	body := buildRPCRequestBody(t, 99, "tx_getSponsorshipConfig", nil)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("tx_getSponsorshipConfig: %+v", rpcErr)
	}
	var cfg struct {
		Enabled   bool   `json:"enabled"`
		AdminRole string `json:"adminRole"`
	}
	if err := json.Unmarshal(result, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.Enabled || cfg.AdminRole != "ROLE_PAYMASTER_ADMIN" {
		t.Fatalf("unexpected sponsorship config: %+v", cfg)
	}
}

// TestNothingElseCanFlipTheSponsorshipFlag guards the removal: the only code
// that could change the per-node flag at runtime took an unsigned caller
// address. If either setter is ever reintroduced this test must be revisited.
func TestNothingElseCanFlipTheSponsorshipFlag(t *testing.T) {
	if _, ok := reflect.TypeOf(&core.Node{}).MethodByName("SetPaymasterModuleEnabled"); ok {
		t.Fatalf("core.Node must not expose a runtime setter for the sponsorship flag")
	}
	if _, ok := reflect.TypeOf(&modules.TransactionsModule{}).MethodByName("SetSponsorshipEnabled"); ok {
		t.Fatalf("TransactionsModule must not expose a sponsorship setter")
	}
}
