package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			name: "create_missing_payer",
			args: []string{
				"create",
				"--payee", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"--token", "NHB",
				"--amount", "100",
				"--fee-bps", "10",
				"--deadline", "+72h",
			},
			wantFile: "escrow_create_missing_payer.golden",
			wantExit: 1,
		},
		{
			name: "create_invalid_amount",
			args: []string{
				"create",
				"--payer", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"--payee", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"--token", "NHB",
				"--amount", "1.23e-1",
				"--fee-bps", "10",
				"--deadline", "+72h",
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

func TestEscrowRPCErrorsAndSuccess(t *testing.T) {
	originalNow := escrowNow
	escrowNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	defer func() { escrowNow = originalNow }()

	// Test RPC error response.
	t.Run("rpc_error", func(t *testing.T) {
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
	})

	// Test RPC success response for create path.
	t.Run("rpc_success", func(t *testing.T) {
		originalCall := escrowRPCCall
		escrowRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "escrow_create" {
				t.Fatalf("unexpected method: %s", method)
			}
			expected := map[string]interface{}{
				"payer":    "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"payee":    "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
				"token":    "NHB",
				"amount":   "100000000000000000000",
				"feeBps":   uint64(10),
				"deadline": int64(1_700_000_000 + 3600),
				"nonce":    uint64(42),
			}
			if diff := diffParams(params, expected); diff != "" {
				t.Fatalf("unexpected params diff: %s", diff)
			}
			return json.RawMessage(`{"id":"0xabc"}`), nil, nil
		}
		defer func() { escrowRPCCall = originalCall }()

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		args := []string{
			"create",
			"--payer", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
			"--payee", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0",
			"--token", "NHB",
			"--amount", "100e18",
			"--fee-bps", "10",
			"--deadline", "+1h",
			"--nonce", "42",
		}
		exitCode := runEscrowCommand(args, stdout, stderr)
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: got %d, want 0", exitCode)
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got %q", stderr.String())
		}
		want := "{\"id\":\"0xabc\"}\n"
		if stdout.String() != want {
			t.Fatalf("unexpected stdout: got %q, want %q", stdout.String(), want)
		}
	})
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
