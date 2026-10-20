package rpc

// A retired method answers "retired" (HTTP 410, code codeMethodDisabled) and
// runs nothing, so the node's operator-role gate must not hide that answer
// behind its own 401/403: every caller, credentialed or not, gets the same
// reply, and a client can tell a method that is gone from one it is not allowed
// to call. Every other operator-only method stays behind the gate.
//
// The list below is kept here on purpose, apart from the registry in
// rpc/http.go, so a name cannot be added to or dropped from the registry
// without a test noticing.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// retiredMethodNames are the operator-table names whose handler only ever
// answers that the method is disabled.
var retiredMethodNames = []string{
	// Methods that used to change validator-local state or write files.
	"pos_sweepVoids",
	"potso_reward_claim",
	"reputation_verifySkill",
	"sync_snapshot_export",
	"sync_snapshot_import",
	"tx_setSponsorshipEnabled",
	// Milestone escrow writers.
	"escrow_milestoneCreate",
	"escrow_milestoneFund",
	"escrow_milestoneRelease",
	"escrow_milestoneCancel",
	"escrow_milestoneSubscriptionUpdate",
	// Legacy direct-state writers.
	"lending_supplyNHB",
	"lending_withdrawNHB",
	"lending_depositZNHB",
	"lending_withdrawZNHB",
	"lending_borrowNHB",
	"lending_borrowNHBWithFee",
	"lending_repayNHB",
	"lending_liquidate",
	"stake_delegate",
	"stake_undelegate",
	"stake_claim",
	"stake_claimRewards",
	"loyalty_createBusiness",
	"loyalty_setPaymaster",
	"loyalty_addMerchant",
	"loyalty_removeMerchant",
	"loyalty_createProgram",
	"loyalty_updateProgram",
	"loyalty_pauseProgram",
	"loyalty_resumeProgram",
	"creator_publish",
	"creator_tip",
	"creator_stake",
	"creator_unstake",
	"identity_setAlias",
	"identity_setAvatar",
	"identity_addAddress",
	"identity_removeAddress",
	"identity_setPrimary",
	"identity_rename",
	"identity_createClaimable",
	"identity_claim",
	"claimable_create",
	"claimable_claim",
	"claimable_cancel",
	"escrow_create",
	"escrow_fund",
	"escrow_release",
	"escrow_refund",
	"escrow_dispute",
	"escrow_expire",
	"escrow_resolve",
	"p2p_createTrade",
	"p2p_settle",
	"p2p_dispute",
	"p2p_resolve",
	// Removed from the dispatch table altogether: every caller gets
	// "unknown method" rather than a retired answer.
	removedRetiredMethod,
}

// removedRetiredMethod has no case in the dispatch table, so it answers 404
// (unknown method) to everyone instead of 410.
const removedRetiredMethod = "potso_submitEvidence"

// retiredTestCaller is one way of reaching the server.
type retiredTestCaller struct {
	label string
	token string
}

func retiredTestCallers(t *testing.T, env *testEnv) []retiredTestCaller {
	t.Helper()
	return []retiredTestCaller{
		{"anonymous", ""},
		{"non-operator token", env.token},
		{"operator token", signEnvJWTWithRole(t, env.now, "operator")},
	}
}

// requireAnswer checks one reply's HTTP status and JSON-RPC error code.
func requireAnswer(t *testing.T, label string, rec *httptest.ResponseRecorder, rpcErr *RPCError, wantStatus, wantCode int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("%s: HTTP %d, want %d (error %+v)", label, rec.Code, wantStatus, rpcErr)
	}
	if rpcErr == nil || rpcErr.Code != wantCode {
		t.Fatalf("%s: error %+v, want code %d", label, rpcErr, wantCode)
	}
}

func TestRetiredMethodsAnswerGoneToEveryCaller(t *testing.T) {
	for _, name := range retiredMethodNames {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			before := env.node.PendingStateRoot()
			for _, caller := range retiredTestCallers(t, env) {
				rec, rpcErr := serveBearer(t, env.server, caller.token, name)
				label := name + " / " + caller.label
				if name == removedRetiredMethod {
					requireAnswer(t, label, rec, rpcErr, http.StatusNotFound, codeMethodNotFound)
					continue
				}
				requireRetired(t, label, rec, rpcErr)
			}
			if after := env.node.PendingStateRoot(); !bytes.Equal(before, after) {
				t.Fatalf("%s changed the state the next block would carry", name)
			}
		})
	}
}

// Every name in the operator table is either retired (same answer for every
// caller) or still gated (401 without a credential, 403 with a credential that
// lacks the operator role). The retired answer is judged from what the handler
// really does for a credential that passes the gate, so a method that starts
// doing real work again cannot stay exempt without this failing.
func TestRetiredMethodRegistryMatchesHandlerBehaviour(t *testing.T) {
	retired := make(map[string]struct{}, len(retiredMethodNames))
	for _, name := range retiredMethodNames {
		retired[name] = struct{}{}
	}
	for name := range retired {
		if _, ok := OperatorOnlyMethods[name]; !ok {
			t.Errorf("%s is listed as retired but is not in OperatorOnlyMethods", name)
		}
		if !isRetiredMethod(name) {
			t.Errorf("isRetiredMethod(%q) = false, want true", name)
		}
	}

	names := make([]string, 0, len(OperatorOnlyMethods))
	for name := range OperatorOnlyMethods {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		// These three carry their own authorization and are exempt from the
		// operator gate for that reason, not because they are retired.
		if isPublicSwapMethod(name) || isSelfAuthenticatedMintMethod(name) || isSignatureThresholdAuthorizedMethod(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			operatorRec, operatorErr := serveBearer(t, env.server, signEnvJWTWithRole(t, env.now, "operator"), name)
			anonRec, anonErr := serveBearer(t, env.server, "", name)
			plainRec, plainErr := serveBearer(t, env.server, env.token, name)

			answersRetired := operatorRec.Code == http.StatusGone && operatorErr != nil && operatorErr.Code == codeMethodDisabled
			_, listed := retired[name]

			switch {
			case name == removedRetiredMethod:
				requireAnswer(t, name+" / operator token", operatorRec, operatorErr, http.StatusNotFound, codeMethodNotFound)
				requireAnswer(t, name+" / anonymous", anonRec, anonErr, http.StatusNotFound, codeMethodNotFound)
				requireAnswer(t, name+" / non-operator token", plainRec, plainErr, http.StatusNotFound, codeMethodNotFound)
			case listed:
				if !answersRetired {
					t.Fatalf("%s is exempt from the operator gate but its handler answered HTTP %d (%+v) to an operator", name, operatorRec.Code, operatorErr)
				}
				requireRetired(t, name+" / anonymous", anonRec, anonErr)
				requireRetired(t, name+" / non-operator token", plainRec, plainErr)
			default:
				if answersRetired {
					t.Fatalf("%s answers 410 to an operator but is not exempt from the operator gate, so other callers get 401/403 instead", name)
				}
				requireAnswer(t, name+" / anonymous", anonRec, anonErr, http.StatusUnauthorized, codeUnauthorized)
				requireAnswer(t, name+" / non-operator token", plainRec, plainErr, http.StatusForbidden, codeOperatorOnly)
			}
		})
	}
}

// forgetLimiterBuckets empties the server's rate limiter table, so the next
// request is not held back by what earlier requests of the same test used.
func forgetLimiterBuckets(srv *Server) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.rateLimiters = make(map[string]*rateLimiter)
}

// The exemption is for the exact names only. A padded, re-cased or decorated
// spelling still resolves to an operator-only method in the gate, so it is
// refused like any other caller that lacks the role instead of slipping past.
func TestRetiredMethodVariantsStayBehindGate(t *testing.T) {
	for _, name := range retiredMethodNames {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			variants := []string{
				" " + name + " ",
				strings.ToUpper(name),
				name[:3] + zeroWidthSpace + name[3:],
			}
			for _, variant := range variants {
				if variant == name {
					continue
				}
				if isRetiredMethod(variant) {
					t.Fatalf("isRetiredMethod(%q) = true, want false for a variant spelling", variant)
				}
				// The rate limiter runs before the gate, and a spelling the
				// server does not serve shares one bucket per source with every
				// other such spelling. This test is about the gate, so each
				// request starts with an empty limiter table.
				forgetLimiterBuckets(env.server)
				anonRec, anonErr := serveBearer(t, env.server, "", variant)
				requireAnswer(t, "anonymous "+variant, anonRec, anonErr, http.StatusUnauthorized, codeUnauthorized)
				forgetLimiterBuckets(env.server)
				plainRec, plainErr := serveBearer(t, env.server, env.token, variant)
				requireAnswer(t, "non-operator token "+variant, plainRec, plainErr, http.StatusForbidden, codeOperatorOnly)
			}
		})
	}
}

// Methods whose read path is live are not retired and keep needing a
// credential: creator_payouts reads the creator ledger, escrow_milestoneGet
// reads a project, and sync_status reports sync progress.
func TestLiveNeighboursOfRetiredMethodsStillNeedACredential(t *testing.T) {
	for _, name := range []string{"creator_payouts", "sync_status"} {
		if isRetiredMethod(name) {
			t.Fatalf("isRetiredMethod(%q) = true, want false: its handler does real work", name)
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			anonRec, anonErr := serveBearer(t, env.server, "", name)
			requireAnswer(t, name+" / anonymous", anonRec, anonErr, http.StatusUnauthorized, codeUnauthorized)
			plainRec, plainErr := serveBearer(t, env.server, env.token, name)
			requireAnswer(t, name+" / non-operator token", plainRec, plainErr, http.StatusForbidden, codeOperatorOnly)
		})
	}

	// escrow_milestoneGet is not in the operator table; its handler asks for a
	// credential itself.
	if isRetiredMethod("escrow_milestoneGet") {
		t.Fatalf("isRetiredMethod(escrow_milestoneGet) = true, want false: its handler does real work")
	}
	env := newTestEnv(t)
	rec, rpcErr := serveBearer(t, env.server, "", "escrow_milestoneGet")
	requireAnswer(t, "escrow_milestoneGet / anonymous", rec, rpcErr, http.StatusUnauthorized, codeUnauthorized)
}
