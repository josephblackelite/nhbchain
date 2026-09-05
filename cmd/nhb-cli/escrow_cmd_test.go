package main

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestEscrowCommandArgValidation(t *testing.T) {
	originalNow := escrowNow
	escrowNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	defer func() { escrowNow = originalNow }()

	originalCall := escrowRPCCall
	escrowRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
		t.Fatalf("unexpected RPC call for method %s", method)
		return nil, nil, nil
	}
	defer func() { escrowRPCCall = originalCall }()

	cases := []struct {
		name     string
		args     []string
		wantFile string
		wantExit int
	}{
		{
			name:     "usage",
			args:     nil,
			wantFile: "escrow_usage.golden",
			wantExit: 1,
		},
		{
			name:     "unknown_subcommand",
			args:     []string{"unknown"},
			wantFile: "escrow_unknown.golden",
			wantExit: 1,
		},
		{
			// create no longer takes --payer -- the signing key's own
			// address is the payer -- so this now exercises the (new)
			// --key requirement instead of the (removed) --payer one.
			name: "create_missing_key",
			args: []string{
				"create",
				"--payee", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"--token", "NHB",
				"--amount", "100",
				"--fee-bps", "10",
				"--deadline", "+72h",
				"--nonce", "1",
			},
			wantFile: "escrow_create_missing_key.golden",
			wantExit: 1,
		},
		{
			name: "create_invalid_amount",
			args: []string{
				"create",
				"--payee", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"--token", "NHB",
				"--amount", "1.23e-1",
				"--fee-bps", "10",
				"--deadline", "+72h",
				"--nonce", "1",
			},
			wantFile: "escrow_create_invalid_amount.golden",
			wantExit: 1,
		},
		{
			name: "get_invalid_id",
			args: []string{
				"get",
				"--id", "0x1234",
			},
			wantFile: "escrow_get_invalid_id.golden",
			wantExit: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exitCode := runEscrowCommand(tc.args, stdout, stderr)
			if exitCode != tc.wantExit {
				t.Fatalf("unexpected exit code: got %d, want %d", exitCode, tc.wantExit)
			}
			if stdout.Len() != 0 {
				t.Fatalf("expected empty stdout, got %q", stdout.String())
			}
			got := stderr.String()
			want := readGolden(t, tc.wantFile)
			if got != want {
				t.Fatalf("stderr mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
			}
		})
	}
}

func TestEscrowRPCErrors(t *testing.T) {
	originalNow := escrowNow
	escrowNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	defer func() { escrowNow = originalNow }()

	// escrow_get is the one escrow subcommand still backed by an RPC call
	// (it's read-only and was never disabled) -- this is the only case
	// escrowRPCCall's swappable-function mock still applies to.
	originalCall := escrowRPCCall
	escrowRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
		if method != "escrow_get" {
			t.Fatalf("unexpected method: %s", method)
		}
		return nil, &rpcError{Code: -32022, Message: "not_found"}, nil
	}
	defer func() { escrowRPCCall = originalCall }()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	args := []string{"get", "--id", "0x" + strings.Repeat("0", 64)}
	exitCode := runEscrowCommand(args, stdout, stderr)
	if exitCode != 1 {
		t.Fatalf("unexpected exit code: got %d, want 1", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	want := "RPC error -32022: not_found\n"
	if stderr.String() != want {
		t.Fatalf("unexpected stderr: got %q, want %q", stderr.String(), want)
	}
}

// TestEscrowCreateSignsAndSendsRealTransaction proves runEscrowCreate
// actually builds, signs, and broadcasts a TxTypeCreateEscrow transaction --
// unlike the old escrow_create RPC call this replaced, there is no
// swappable mock function for this path (it goes through
// loadPrivateKey/fetchAccount/sendTransaction, real HTTP), so this test runs
// a real httptest.Server standing in for the node's JSON-RPC endpoint and
// inspects the actual transaction it receives: correct type, correctly
// recovers to the signing key's own address (proving a real signature was
// produced, not a placeholder), and a payload that decodes back to exactly
// what was requested on the command line.
func TestEscrowCreateSignsAndSendsRealTransaction(t *testing.T) {
	originalNow := escrowNow
	escrowNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	defer func() { escrowNow = originalNow }()

	privKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "payer.key")
	if err := os.WriteFile(keyPath, privKey.Bytes(), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	payerAddr := privKey.PubKey().Address()

	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}
	payeeAddr := payeeKey.PubKey().Address()

	var capturedTx types.Transaction
	var sawGetBalance, sawSendTransaction bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int               `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "nhb_getBalance":
			sawGetBalance = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"address":"","nonce":0}}`))
		case "nhb_sendTransaction":
			sawSendTransaction = true
			if len(req.Params) != 1 {
				t.Fatalf("expected exactly one param, got %d", len(req.Params))
			}
			if err := json.Unmarshal(req.Params[0], &capturedTx); err != nil {
				t.Fatalf("decode transaction param: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("ab", 32) + `"}`))
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	originalEndpoint := rpcEndpoint
	rpcEndpoint = server.URL
	defer func() { rpcEndpoint = originalEndpoint }()

	originalToken := rpcAuthToken
	rpcAuthToken = "test-token"
	defer func() { rpcAuthToken = originalToken }()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	args := []string{
		"create",
		"--payee", payeeAddr.String(),
		"--token", "NHB",
		"--amount", "100e18",
		"--fee-bps", "10",
		"--deadline", "+1h",
		"--nonce", "42",
		"--key", keyPath,
	}
	exitCode := runEscrowCommand(args, stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: got %d, want 0, stderr: %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	if !sawGetBalance {
		t.Fatalf("expected nhb_getBalance to be called for the nonce")
	}
	if !sawSendTransaction {
		t.Fatalf("expected nhb_sendTransaction to be called")
	}
	if !strings.Contains(stdout.String(), "Escrow creation transaction sent") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}

	if capturedTx.Type != types.TxTypeCreateEscrow {
		t.Fatalf("unexpected tx type: got %d, want %d", capturedTx.Type, types.TxTypeCreateEscrow)
	}
	sender, err := capturedTx.From()
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if !bytes.Equal(sender, payerAddr.Bytes()) {
		t.Fatalf("recovered sender does not match signing key: got %x want %x", sender, payerAddr.Bytes())
	}

	var payload struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}
	if err := json.Unmarshal(capturedTx.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !bytes.Equal(payload.Payee, payeeAddr.Bytes()) {
		t.Fatalf("payload payee mismatch: got %x want %x", payload.Payee, payeeAddr.Bytes())
	}
	if payload.Token != "NHB" {
		t.Fatalf("unexpected token: %s", payload.Token)
	}
	if payload.Amount == nil || payload.Amount.String() != "100000000000000000000" {
		t.Fatalf("unexpected amount: %v", payload.Amount)
	}
	if payload.FeeBps != 10 {
		t.Fatalf("unexpected feeBps: %d", payload.FeeBps)
	}
	if payload.Nonce != 42 {
		t.Fatalf("unexpected escrow nonce: %d", payload.Nonce)
	}
}

// TestEscrowCreateRealmSignsAndSendsRealTransaction proves runEscrowCreateRealm
// builds a TxTypeEscrowCreateRealm transaction whose payload matches exactly
// what core/state_transition.go's decodeEscrowRealmPayload expects -- this
// command was entirely missing before (native/escrow/engine.go's CreateRealm
// existed but was unreachable from this CLI).
func TestEscrowCreateRealmSignsAndSendsRealTransaction(t *testing.T) {
	privKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "realm-admin.key")
	if err := os.WriteFile(keyPath, privKey.Bytes(), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	adminAddr := privKey.PubKey().Address()

	memberKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate member key: %v", err)
	}
	memberAddr := memberKey.PubKey().Address()

	var capturedTx types.Transaction
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "nhb_getBalance":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"address":"","nonce":0}}`))
		case "nhb_sendTransaction":
			if err := json.Unmarshal(req.Params[0], &capturedTx); err != nil {
				t.Fatalf("decode transaction param: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("cd", 32) + `"}`))
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	originalEndpoint := rpcEndpoint
	rpcEndpoint = server.URL
	defer func() { rpcEndpoint = originalEndpoint }()

	originalToken := rpcAuthToken
	rpcAuthToken = "test-token"
	defer func() { rpcAuthToken = originalToken }()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	args := []string{
		"create-realm",
		"--id", "core-arbitration",
		"--threshold", "1",
		"--scheme", "single",
		"--members", memberAddr.String(),
		"--scope", "platform",
		"--provider-profile", "core-team",
		"--key", keyPath,
	}
	exitCode := runEscrowCommand(args, stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: got %d, want 0, stderr: %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `realm "core-arbitration"`) {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}

	if capturedTx.Type != types.TxTypeEscrowCreateRealm {
		t.Fatalf("unexpected tx type: got %d, want %d", capturedTx.Type, types.TxTypeEscrowCreateRealm)
	}
	sender, err := capturedTx.From()
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if !bytes.Equal(sender, adminAddr.Bytes()) {
		t.Fatalf("recovered sender does not match signing key: got %x want %x", sender, adminAddr.Bytes())
	}

	var payload struct {
		ID              string   `json:"id"`
		Threshold       uint32   `json:"threshold"`
		Scheme          uint8    `json:"scheme"`
		Members         []string `json:"members"`
		Scope           uint8    `json:"scope"`
		ProviderProfile string   `json:"providerProfile"`
	}
	if err := json.Unmarshal(capturedTx.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ID != "core-arbitration" {
		t.Fatalf("unexpected id: %s", payload.ID)
	}
	if payload.Threshold != 1 {
		t.Fatalf("unexpected threshold: %d", payload.Threshold)
	}
	if payload.Scheme != 1 {
		t.Fatalf("unexpected scheme: %d, want 1 (single)", payload.Scheme)
	}
	if len(payload.Members) != 1 || payload.Members[0] != memberAddr.String() {
		t.Fatalf("unexpected members: %v", payload.Members)
	}
	if payload.Scope != 1 {
		t.Fatalf("unexpected scope: %d, want 1 (platform)", payload.Scope)
	}
	if payload.ProviderProfile != "core-team" {
		t.Fatalf("unexpected providerProfile: %s", payload.ProviderProfile)
	}
}

func TestEscrowCreateRealmArgValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing_id",
			args:    []string{"create-realm", "--members", "nhb1x", "--provider-profile", "x", "--key", "k"},
			wantErr: "Error: --id is required\n",
		},
		{
			name:    "missing_members",
			args:    []string{"create-realm", "--id", "r", "--provider-profile", "x", "--key", "k"},
			wantErr: "Error: --members is required (comma-separated bech32 addresses)\n",
		},
		{
			name:    "threshold_exceeds_members",
			args:    []string{"create-realm", "--id", "r", "--members", "nhb1x", "--threshold", "2", "--provider-profile", "x", "--key", "k"},
			wantErr: "Error: --threshold must be between 1 and the number of --members\n",
		},
		{
			name:    "invalid_scheme",
			args:    []string{"create-realm", "--id", "r", "--members", "nhb1x", "--scheme", "bogus", "--provider-profile", "x", "--key", "k"},
			wantErr: "Error: --scheme must be single or committee\n",
		},
		{
			name:    "invalid_scope",
			args:    []string{"create-realm", "--id", "r", "--members", "nhb1x", "--scope", "bogus", "--provider-profile", "x", "--key", "k"},
			wantErr: "Error: --scope must be platform or marketplace\n",
		},
		{
			name:    "missing_provider_profile",
			args:    []string{"create-realm", "--id", "r", "--members", "nhb1x", "--key", "k"},
			wantErr: "Error: --provider-profile is required\n",
		},
		{
			name:    "missing_key",
			args:    []string{"create-realm", "--id", "r", "--members", "nhb1x", "--provider-profile", "x"},
			wantErr: "Error: --key is required (path to a private key file holding ROLE_ESCROW_REALM_ADMIN)\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exitCode := runEscrowCommand(tc.args, stdout, stderr)
			if exitCode != 1 {
				t.Fatalf("unexpected exit code: got %d, want 1", exitCode)
			}
			if stderr.String() != tc.wantErr {
				t.Fatalf("unexpected stderr: got %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestNormalizeEscrowAmount(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "100", want: "100"},
		{input: "00100", want: "100"},
		{input: "100e18", want: "100000000000000000000"},
		{input: "0.5e18", want: "500000000000000000"},
		{input: "1.0", want: "1"},
		{input: "1.23e-1", wantErr: true},
		{input: "-10", wantErr: true},
		{input: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeEscrowAmount(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected result: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseEscrowDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "relative_hours", input: "+2h", want: now.Add(2 * time.Hour).Unix()},
		{name: "relative_days", input: "+1.5d", want: now.Add(time.Duration(36) * time.Hour).Unix()},
		{name: "absolute", input: "2024-01-01T00:00:00Z", want: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()},
		{name: "invalid", input: "soon", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEscrowDeadline(tc.input, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("unexpected deadline: got %d, want %d", got, tc.want)
			}
		})
	}
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read golden file %s: %v", name, err)
	}
	return string(data)
}

// diffParams is shared with identity_cmd_test.go and p2p_cmd_test.go.
func diffParams(actual interface{}, expected map[string]interface{}) string {
	actualMap, ok := actual.(map[string]interface{})
	if !ok {
		return "actual params are not an object"
	}
	for key, want := range expected {
		got, exists := actualMap[key]
		if !exists {
			return "missing key " + key
		}
		switch wantTyped := want.(type) {
		case string:
			gotStr, ok := got.(string)
			if !ok || gotStr != wantTyped {
				return "value mismatch for " + key
			}
		case uint64:
			switch g := got.(type) {
			case uint64:
				if g != wantTyped {
					return "value mismatch for " + key
				}
			case float64:
				if uint64(g) != wantTyped {
					return "value mismatch for " + key
				}
			default:
				return "value mismatch for " + key
			}
		case int64:
			switch g := got.(type) {
			case int64:
				if g != wantTyped {
					return "value mismatch for " + key
				}
			case float64:
				if int64(g) != wantTyped {
					return "value mismatch for " + key
				}
			default:
				return "value mismatch for " + key
			}
		default:
			return "unsupported expected type"
		}
	}
	return ""
}
