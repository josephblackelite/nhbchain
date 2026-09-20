package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The id mutators call node methods that are retired. Each reports that and
// exits non-zero without contacting the node.
func TestIdentityMutatorsAreRetired(t *testing.T) {
	originalCall := identityRPCCall
	identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
		t.Fatalf("unexpected RPC call for method %s", method)
		return nil, nil, nil
	}
	defer func() { identityRPCCall = originalCall }()

	owner := "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq0"
	addr := "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq1"
	cases := []struct {
		args   []string
		method string
	}{
		{[]string{"set-alias", "--addr", addr, "--alias", "builder"}, "identity_setAlias"},
		{[]string{"set-avatar", "--addr", addr, "--avatar", "https://example.invalid/a.png"}, "identity_setAvatar"},
		{[]string{"add-address", "--owner", owner, "--alias", "builder", "--addr", addr}, "identity_addAddress"},
		{[]string{"remove-address", "--owner", owner, "--alias", "builder", "--addr", addr}, "identity_removeAddress"},
		{[]string{"set-primary", "--owner", owner, "--alias", "builder", "--addr", addr}, "identity_setPrimary"},
		{[]string{"rename", "--owner", owner, "--alias", "builder", "--new-alias", "artisan"}, "identity_rename"},
		{[]string{"create-claimable", "--payer", owner, "--recipient", "builder", "--amount", "1", "--deadline", "1700000000"}, "identity_createClaimable"},
		{[]string{"claim", "--id", "0x01", "--payee", addr}, "identity_claim"},
		// Without any flags: the retired answer does not depend on the arguments.
		{[]string{"add-address"}, "identity_addAddress"},
		{[]string{"rename"}, "identity_rename"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args[:1], ""), func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if exit := runIdentityCommand(tc.args, stdout, stderr); exit != 1 {
				t.Fatalf("exit code %d, want 1", exit)
			}
			if stdout.Len() != 0 {
				t.Fatalf("expected empty stdout, got %q", stdout.String())
			}
			out := stderr.String()
			if !strings.Contains(out, "retired") || !strings.Contains(out, tc.method) {
				t.Fatalf("stderr %q does not say that %s is retired", out, tc.method)
			}
		})
	}
}

func TestIdentityLookupsStillWork(t *testing.T) {
	original := identityRPCCall
	defer func() { identityRPCCall = original }()

	t.Run("resolve", func(t *testing.T) {
		identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "identity_resolve" || requireAuth {
				t.Fatalf("unexpected call %s (auth %v)", method, requireAuth)
			}
			if len(params) != 1 || params[0] != "builder" {
				t.Fatalf("unexpected params %v", params)
			}
			return json.RawMessage(`{"alias":"builder"}`), nil, nil
		}
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if exit := runIdentityCommand([]string{"resolve", "--alias", "builder"}, stdout, stderr); exit != 0 {
			t.Fatalf("exit code %d, stderr %q", exit, stderr.String())
		}
		if stdout.String() != "{\"alias\":\"builder\"}\n" {
			t.Fatalf("unexpected stdout: %q", stdout.String())
		}
	})

	t.Run("reverse", func(t *testing.T) {
		identityRPCCall = func(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
			if method != "identity_reverse" || requireAuth {
				t.Fatalf("unexpected call %s (auth %v)", method, requireAuth)
			}
			return json.RawMessage(`{"alias":"builder"}`), nil, nil
		}
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if exit := runIdentityCommand([]string{"reverse", "--addr", "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq9uq1"}, stdout, stderr); exit != 0 {
			t.Fatalf("exit code %d, stderr %q", exit, stderr.String())
		}
	})
}
