package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestP2PCommandArgValidation(t *testing.T) {
	originalCall := p2pRPCCall
	p2pRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
		t.Fatalf("unexpected RPC call for method %s", method)
		return nil, nil, nil
	}
	defer func() { p2pRPCCall = originalCall }()

	cases := []struct {
		name     string
		args     []string
		wantFile string
		wantExit int
	}{
		{
			name:     "usage",
			args:     nil,
			wantFile: "p2p_usage.golden",
			wantExit: 1,
		},
		{
			name:     "unknown_subcommand",
			args:     []string{"unknown"},
			wantFile: "p2p_unknown.golden",
			wantExit: 1,
		},
		{
			name: "get_invalid_id",
			args: []string{
				"get",
				"--id", "0x1234",
			},
			wantFile: "p2p_get_invalid_id.golden",
			wantExit: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exitCode := runP2PCommand(tc.args, stdout, stderr)
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

func TestP2PGetRPCErrorsAndSuccess(t *testing.T) {
	t.Run("rpc_error", func(t *testing.T) {
		originalCall := p2pRPCCall
		p2pRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "p2p_getTrade" {
				t.Fatalf("unexpected method: %s", method)
			}
			return nil, &rpcError{Code: -32022, Message: "not_found"}, nil
		}
		defer func() { p2pRPCCall = originalCall }()

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		args := []string{"get", "--id", "0x" + strings.Repeat("0", 64)}
		exitCode := runP2PCommand(args, stdout, stderr)
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

	t.Run("rpc_success", func(t *testing.T) {
		originalCall := p2pRPCCall
		p2pRPCCall = func(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "p2p_getTrade" {
				t.Fatalf("unexpected method: %s", method)
			}
			return json.RawMessage(`{"tradeId":"0xabc"}`), nil, nil
		}
		defer func() { p2pRPCCall = originalCall }()

		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		exitCode := runP2PCommand([]string{"get", "--id", "0x" + strings.Repeat("0", 64)}, stdout, stderr)
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: got %d, want 0", exitCode)
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got %q", stderr.String())
		}
		want := "{\"tradeId\":\"0xabc\"}\n"
		if stdout.String() != want {
			t.Fatalf("unexpected stdout: got %q, want %q", stdout.String(), want)
		}
	})
}
