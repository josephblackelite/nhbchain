package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestIdentityCommandArgValidation(t *testing.T) {
	originalCall := identityRPCCall
	identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
		t.Fatalf("unexpected RPC call for method %s", method)
		return nil, nil, nil
	}
	defer func() { identityRPCCall = originalCall }()

	cases := []struct {
		name     string
		args     []string
		wantExit int
		wantFile string
	}{
		{
			name:     "add_missing_flags",
			args:     []string{"add-address"},
			wantExit: 1,
			wantFile: "identity_add_missing.golden",
		},
		{
			name:     "remove_missing_flags",
			args:     []string{"remove-address"},
			wantExit: 1,
			wantFile: "identity_remove_missing.golden",
		},
		{
			name:     "set_primary_missing_flags",
			args:     []string{"set-primary"},
			wantExit: 1,
			wantFile: "identity_set_primary_missing.golden",
		},
		{
			name:     "rename_missing_flags",
			args:     []string{"rename"},
			wantExit: 1,
			wantFile: "identity_rename_missing.golden",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exit := runIdentityCommand(tc.args, stdout, stderr)
			if exit != tc.wantExit {
				t.Fatalf("unexpected exit code: got %d, want %d", exit, tc.wantExit)
			}
			if stdout.Len() != 0 {
				t.Fatalf("expected empty stdout, got %q", stdout.String())
			}
			if got := stderr.String(); got != readGolden(t, tc.wantFile) {
				t.Fatalf("stderr mismatch\n--- got ---\n%q\n--- want ---\n%q", got, readGolden(t, tc.wantFile))
			}
		})
	}
}

func TestIdentityCommandRPCSuccess(t *testing.T) {
	owner := "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0"
	alias := "builder"
	addr := "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq1"

	t.Run("add_address", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		original := identityRPCCall
		identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "identity_addAddress" {
				t.Fatalf("unexpected method %s", method)
			}
			if !requireAuth {
				t.Fatalf("expected authenticated call")
			}
			expected := map[string]interface{}{
				"owner":   owner,
				"alias":   alias,
				"address": addr,
			}
			if len(params) != 1 {
				t.Fatalf("expected single parameter object")
			}
			if diff := diffParams(params[0], expected); diff != "" {
				t.Fatalf("unexpected params diff: %s", diff)
			}
			return json.RawMessage(`{"alias":"builder"}`), nil, nil
		}
		defer func() { identityRPCCall = original }()

		exit := runIdentityCommand([]string{"add-address", "--owner", owner, "--alias", alias, "--addr", addr}, stdout, stderr)
		if exit != 0 {
			t.Fatalf("unexpected exit code: %d", exit)
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got %q", stderr.String())
		}
		if stdout.String() != "{\"alias\":\"builder\"}\n" {
			t.Fatalf("unexpected stdout: %q", stdout.String())
		}
	})

	t.Run("rename", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		original := identityRPCCall
		identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "identity_rename" {
				t.Fatalf("unexpected method %s", method)
			}
			expected := map[string]interface{}{
				"owner":    owner,
				"alias":    alias,
				"newAlias": "artisan",
			}
			if diff := diffParams(params[0], expected); diff != "" {
				t.Fatalf("unexpected params diff: %s", diff)
			}
			return json.RawMessage(`{"alias":"artisan"}`), nil, nil
		}
		defer func() { identityRPCCall = original }()

		exit := runIdentityCommand([]string{"rename", "--owner", owner, "--alias", alias, "--new-alias", "artisan"}, stdout, stderr)
		if exit != 0 {
			t.Fatalf("unexpected exit code: %d", exit)
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got %q", stderr.String())
		}
		if stdout.String() != "{\"alias\":\"artisan\"}\n" {
			t.Fatalf("unexpected stdout: %q", stdout.String())
		}
	})
}
