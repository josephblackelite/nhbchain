package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
)

func TestDetermineAction(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		proposal govProposal
		wantType types.TxType
		wantOK   bool
	}{
		{
			name: "voting still open",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Add(time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "voting closed, needs finalize",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Add(-time.Minute).Format(time.RFC3339),
			},
			wantType: types.TxTypeGovFinalize,
			wantOK:   true,
		},
		{
			name: "voting closed exactly now, needs finalize",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Format(time.RFC3339),
			},
			wantType: types.TxTypeGovFinalize,
			wantOK:   true,
		},
		{
			name: "passed and not yet queued, needs queue",
			proposal: govProposal{
				Status: proposalStatusPassed,
				Queued: false,
			},
			wantType: types.TxTypeGovQueue,
			wantOK:   true,
		},
		{
			name: "queued but timelock not elapsed",
			proposal: govProposal{
				Status:      proposalStatusPassed,
				Queued:      true,
				TimelockEnd: now.Add(time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "queued and timelock elapsed, needs execute",
			proposal: govProposal{
				Status:      proposalStatusPassed,
				Queued:      true,
				TimelockEnd: now.Add(-time.Minute).Format(time.RFC3339),
			},
			wantType: types.TxTypeGovExecute,
			wantOK:   true,
		},
		{
			name: "rejected proposal, nothing to do",
			proposal: govProposal{
				Status: proposalStatusRejected,
			},
			wantOK: false,
		},
		{
			name: "executed proposal, nothing to do",
			proposal: govProposal{
				Status: proposalStatusExecuted,
			},
			wantOK: false,
		},
		{
			// Reproduces the real proposal-1 case discovered live: an
			// already-executed proposal keeps queued=true forever, which
			// must not make determineAction try to re-execute it.
			name: "executed proposal with queued still true, nothing to do",
			proposal: govProposal{
				Status:      proposalStatusExecuted,
				Queued:      true,
				TimelockEnd: now.Add(-24 * time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "voting_period with empty votingEnd is never actionable",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: "",
			},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, _, gotOK := determineAction(tc.proposal, now)
			if gotOK != tc.wantOK {
				t.Fatalf("determineAction() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotType != tc.wantType {
				t.Fatalf("determineAction() type = %v, want %v", gotType, tc.wantType)
			}
		})
	}
}

func TestRunOnceIncrementsNonceAcrossMultipleActions(t *testing.T) {
	// Regression guard: within one poll tick, multiple due proposals must
	// each get the next sequential nonce, not the same starting nonce
	// repeated (which would make every submission after the first one fail
	// as a duplicate/stale nonce).
	proposals := []govProposal{
		{ID: 1, Status: proposalStatusPassed, Queued: false},
		{ID: 2, Status: proposalStatusPassed, Queued: false},
		{ID: 3, Status: proposalStatusVotingPeriod, VotingEnd: "not-a-real-timestamp"},
	}

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var actioned []uint64
	nonce := uint64(41)
	for _, p := range proposals {
		_, _, ok := determineAction(p, now)
		if !ok {
			continue
		}
		actioned = append(actioned, nonce)
		nonce++
	}

	if len(actioned) != 2 {
		t.Fatalf("expected 2 actionable proposals, got %d", len(actioned))
	}
	if actioned[0] != 41 || actioned[1] != 42 {
		t.Fatalf("expected sequential nonces [41 42], got %v", actioned)
	}
}

// --- the chain id the daemon signs with (ledger PL-R5T-29) ---------------------------------------------------------

type chainIDFakeNode struct {
	*httptest.Server
	mu        sync.Mutex
	stats     string
	submitted []types.Transaction
	methods   []string
}

func newChainIDFakeNode(t *testing.T, stats string) *chainIDFakeNode {
	t.Helper()
	n := &chainIDFakeNode{stats: stats}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r) // a node serves JSON-RPC on its root only
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		n.mu.Lock()
		defer n.mu.Unlock()
		n.methods = append(n.methods, req.Method)
		switch req.Method {
		case "nhb_getNetworkStats":
			_, _ = w.Write([]byte(n.stats))
		case "gov_list":
			_, _ = w.Write([]byte(`{"result":{"proposals":[{"id":7,"status":3,"queued":false},{"id":8,"status":3,"queued":false}]}}`))
		case "nhb_getBalance":
			_, _ = w.Write([]byte(`{"result":{"nonce":41}}`))
		case "nhb_sendTransaction":
			var tx types.Transaction
			if err := json.Unmarshal(req.Params[0], &tx); err != nil {
				t.Errorf("decode tx: %v", err)
			}
			n.submitted = append(n.submitted, tx)
			_, _ = w.Write([]byte(`{"result":"0xabc"}`))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	t.Cleanup(n.Close)
	return n
}

func TestLifecycleTransactionsCarryTheChainIDTheNodeReports(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"18346390202490284624", "5130306"} {
		node := newChainIDFakeNode(t, `{"result":{"txChainId":"`+id+`"}}`)
		client := newRPCClient(node.URL, "token")
		runOnce(client, key, config{listLimit: 10})
		if len(node.submitted) != 2 {
			t.Fatalf("id %s: submitted %d transactions, want 2 (both proposals are due)", id, len(node.submitted))
		}
		for i, tx := range node.submitted {
			if tx.ChainID.String() != id {
				t.Fatalf("id %s: transaction %d carries %s", id, i, tx.ChainID)
			}
			if from, err := tx.From(); err != nil || string(from) != string(key.PubKey().Address().Bytes()) {
				t.Fatalf("id %s: transaction %d does not recover the signer: %v", id, i, err)
			}
		}
		stats := 0
		for _, m := range node.methods {
			if m == "nhb_getNetworkStats" {
				stats++
			}
		}
		if stats != 1 {
			t.Fatalf("id %s: the node was asked %d times for the chain id, want 1 per tick", id, stats)
		}
	}
}

func TestNothingIsSignedWhenTheNodeGivesNoChainID(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	for name, stats := range map[string]string{
		"no id":         `{"result":{"activeValidators":2}}`,
		"zero":          `{"result":{"txChainId":"0"}}`,
		"not a number":  `{"result":{"txChainId":"NHB"}}`,
		"a node error":  `{"error":{"code":-32601,"message":"method not found"}}`,
		"an empty body": ``,
	} {
		node := newChainIDFakeNode(t, stats)
		runOnce(newRPCClient(node.URL, "token"), key, config{listLimit: 10})
		if len(node.submitted) != 0 {
			t.Fatalf("%s: %d transactions were signed and sent", name, len(node.submitted))
		}
	}
	// A client built without a reader cannot sign either.
	node := newChainIDFakeNode(t, `{"result":{"txChainId":"5"}}`)
	bare := &rpcClient{endpoint: node.URL, authToken: "token"}
	if hash, err := bare.sendLifecycleTx(key, 1, types.TxTypeGovQueue, 7); err == nil || hash != "" {
		t.Fatalf("a client with no chain id source signed: %q %v", hash, err)
	}
	if len(node.submitted) != 0 {
		t.Fatal("a transaction reached the node")
	}
}

func TestTheDaemonDoesNotRefertoTheSharedConstant(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "NHBChainID()") {
		t.Fatal("main.go signs with the shared constant instead of the id the node reports (PL-R5T-29)")
	}
}

// tick is what the running daemon calls: it must build its client through newRPCClient, so the transactions it signs carry the
// id the node reports.
func TestTickSignsWithTheNodesChainID(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOV_KEEPER_TEST_SECRET", "a-test-secret")
	node := newChainIDFakeNode(t, `{"result":{"txChainId":"18346390202490284624"}}`)
	tick(config{rpcEndpoint: node.URL, jwtSecretEnv: "GOV_KEEPER_TEST_SECRET", jwtIssuer: "i", jwtAudience: "a", listLimit: 10}, key)
	if len(node.submitted) != 2 {
		t.Fatalf("tick submitted %d transactions, want 2", len(node.submitted))
	}
	for _, tx := range node.submitted {
		if tx.ChainID.String() != "18346390202490284624" {
			t.Fatalf("tick signed with %s", tx.ChainID)
		}
	}
}

func TestTheClientKeepsTheChainIDOnlyBrieflyAcrossTheActivationHeight(t *testing.T) {
	c := newRPCClient("http://127.0.0.1:1", "token")
	if c.chainIDs.Expiry() != txchainid.DefaultTTL {
		t.Fatalf("the client keeps a chain id for %v", c.chainIDs.Expiry())
	}
}
