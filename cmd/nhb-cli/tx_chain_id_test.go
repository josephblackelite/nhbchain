package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
)

// The id a transaction signed by the command line must carry is the one the node reports in the public
// nhb_getNetworkStats call (field txChainId). If the node cannot say, the command signs nothing: it never falls back to
// the shared constant, which would sign a transaction that replays on another network (ledger PL-R5T-29).

func withRPCEndpoint(t *testing.T, url string) {
	t.Helper()
	previous := rpcEndpoint
	rpcEndpoint = url
	t.Cleanup(func() { rpcEndpoint = previous })
	resetTxChainID() // one answer is kept per run: start every test from none
	t.Cleanup(resetTxChainID)
}

func withRPCToken(t *testing.T, token string) {
	t.Helper()
	previous := rpcAuthToken
	rpcAuthToken = token
	t.Cleanup(func() { rpcAuthToken = previous })
}

type cliNode struct {
	*httptest.Server
	mu        sync.Mutex
	stats     string // raw JSON-RPC body for nhb_getNetworkStats
	statsHits int
	submitted []types.Transaction
}

func newCLINode(t *testing.T, stats string) *cliNode {
	t.Helper()
	n := &cliNode{stats: stats}
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
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "nhb_getNetworkStats":
			n.statsHits++
			_, _ = w.Write([]byte(n.stats))
		case "nhb_getBalance":
			_, _ = w.Write([]byte(`{"result":{"nonce":6}}`))
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

func statsBody(id string) string {
	return `{"jsonrpc":"2.0","id":1,"result":{"activeValidators":2,"txChainId":"` + id + `"}}`
}

func TestTxChainIDUsesWhatTheNodeReports(t *testing.T) {
	for _, id := range []string{"18346390202490284624", "5130306"} {
		n := newCLINode(t, statsBody(id))
		withRPCEndpoint(t, n.URL)
		got, err := txChainID()
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got.String() != id {
			t.Fatalf("got %s, want the id the node reports (%s)", got, id)
		}
	}
}

func TestTxChainIDIsAskedOncePerRunAndHandsOutCopies(t *testing.T) {
	n := newCLINode(t, statsBody("42"))
	withRPCEndpoint(t, n.URL)
	a, _ := txChainID()
	a.SetInt64(1)
	b, err := txChainID()
	if err != nil || b.Int64() != 42 {
		t.Fatalf("%v %v", b, err)
	}
	if n.statsHits != 1 {
		t.Fatalf("the node was asked %d times, want 1", n.statsHits)
	}
}

func TestTxChainIDRefusesWhenTheNodeCannotGiveAUsableID(t *testing.T) {
	cases := map[string]string{
		"a node that does not report it (an older release)": `{"result":{"activeValidators":2}}`,
		"an empty value":               `{"result":{"txChainId":""}}`,
		"a value that is not a number": `{"result":{"txChainId":"abc"}}`,
		"hex":                          `{"result":{"txChainId":"0x4e4842"}}`,
		"zero":                         `{"result":{"txChainId":"0"}}`,
		"a negative number":            `{"result":{"txChainId":"-5"}}`,
		"more than a uint64":           `{"result":{"txChainId":"18446744073709551616"}}`,
		"an error reply":               `{"error":{"code":-32601,"message":"method not found"}}`,
		"something that is not json":   `not json`,
		"an empty body":                ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			n := newCLINode(t, body)
			withRPCEndpoint(t, n.URL)
			if got, err := txChainID(); err == nil || got != nil {
				t.Fatalf("returned %v for %q instead of refusing", got, body)
			}
		})
	}
	t.Run("an unreachable node", func(t *testing.T) {
		n := newCLINode(t, statsBody("5"))
		url := n.URL
		n.Close()
		withRPCEndpoint(t, url)
		if got, err := txChainID(); err == nil || got != nil {
			t.Fatalf("returned %v for an unreachable node", got)
		}
	})
}

func writeKeyFile(t *testing.T) (string, *crypto.PrivateKey) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, key.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestSendCommandsSignWithTheNodesChainID(t *testing.T) {
	to, _ := crypto.GeneratePrivateKey()
	recipient := to.PubKey().Address().String()
	for _, id := range []string{"18346390202490284624", "5130306"} {
		for name, send := range map[string]func(key string) error{
			"send-nhb":  func(key string) error { return sendNHB(recipient, "5", key, 21000, 1) },
			"send-znhb": func(key string) error { return sendZNHB(recipient, "5", key, 21000, 1) },
		} {
			n := newCLINode(t, statsBody(id))
			withRPCEndpoint(t, n.URL)
			withRPCToken(t, "token")
			path, key := writeKeyFile(t)
			if err := send(path); err != nil {
				t.Fatalf("%s/%s: %v", name, id, err)
			}
			if len(n.submitted) != 1 {
				t.Fatalf("%s/%s: submitted %d", name, id, len(n.submitted))
			}
			tx := n.submitted[0]
			if tx.ChainID.String() != id {
				t.Fatalf("%s: signed with %s, the node reported %s", name, tx.ChainID, id)
			}
			if from, err := tx.From(); err != nil || string(from) != string(key.PubKey().Address().Bytes()) {
				t.Fatalf("%s: the signature does not recover the sender: %v", name, err)
			}
		}
	}
}

func TestSendCommandsSignNothingWhenTheNodeCannotGiveAChainID(t *testing.T) {
	to, _ := crypto.GeneratePrivateKey()
	recipient := to.PubKey().Address().String()
	for name, body := range map[string]string{
		"no id":    `{"result":{"activeValidators":2}}`,
		"zero":     `{"result":{"txChainId":"0"}}`,
		"an error": `{"error":{"code":-1,"message":"down"}}`,
	} {
		for cmd, send := range map[string]func(key string) error{
			"send-nhb":  func(key string) error { return sendNHB(recipient, "5", key, 21000, 1) },
			"send-znhb": func(key string) error { return sendZNHB(recipient, "5", key, 21000, 1) },
		} {
			n := newCLINode(t, body)
			withRPCEndpoint(t, n.URL)
			withRPCToken(t, "token")
			path, _ := writeKeyFile(t)
			if err := send(path); err == nil {
				t.Fatalf("%s/%s: the command reported success", cmd, name)
			}
			if len(n.submitted) != 0 {
				t.Fatalf("%s/%s: %d transactions reached the node", cmd, name, len(n.submitted))
			}
		}
	}
}

// A transaction signed by a CLI command must take its id from txChainID; a literal types.NHBChainID() in a command
// would keep signing with the shared constant after the chain reaches its activation height, and a txChainID()
// call whose error is dropped would sign with nothing.
func TestNoCommandSignsWithTheLiteralLegacyChainID(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	// Every call is followed by an error check whose body only reports and returns: it never assigns a replacement id.
	use := regexp.MustCompile(`(?m)^\s*chainID, err :?= txChainID\(\)\n\s*if err != nil \{\n(?:\s*fmt\.Printf\([^\n]*\)\n)?\s*return [^\n]*\n\s*\}`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "tx_chain_id.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if strings.Contains(text, "types.NHBChainID()") {
			t.Errorf("%s builds a transaction with the literal legacy chain id; use txChainID()", f)
		}
		calls := strings.Count(text, "txChainID()")
		checked += calls
		if got := len(use.FindAllString(text, -1)); got != calls {
			t.Errorf("%s: %d calls of txChainID() but %d of them are followed by an error check", f, calls, got)
		}
		if strings.Contains(text, "ChainID:  txChainID()") || regexp.MustCompile(`ChainID:\s+txChainID\(\)`).MatchString(text) {
			t.Errorf("%s puts txChainID() straight into a transaction, dropping its error", f)
		}
	}
	if checked < 13 {
		t.Fatalf("only %d signing sites found: the scan no longer sees the commands", checked)
	}
}

func TestTheCommandKeepsTheChainIDOnlyBrieflyAcrossTheActivationHeight(t *testing.T) {
	if txChainIDReader.Expiry() != txchainid.DefaultTTL {
		t.Fatalf("the command keeps a chain id for %v", txChainIDReader.Expiry())
	}
}

// --- the operator's explicit override: --chain-id and NHB_CHAIN_ID -------------------------------------------------------------
//
// An override is the operator's own choice, for a node that does not report txChainId (a release older than r13) or a network
// they know better. It is never a default and never a fallback: with none given the id comes from the node and a node that
// cannot say means nothing is signed.

func sendViaRun(t *testing.T, nodeURL, keyFile, recipient string, extra ...string) int {
	t.Helper()
	args := append([]string{}, extra...)
	args = append(args, "--rpc", nodeURL, "send-nhb", recipient, "5", keyFile)
	return run(args)
}

func recipientAddress(t *testing.T) string {
	t.Helper()
	to, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return to.PubKey().Address().String()
}

func TestTheFlagOverridesTheChainIDAndTheNodeIsNotAsked(t *testing.T) {
	n := newCLINode(t, statsBody("18346390202490284624"))
	withRPCEndpoint(t, n.URL)
	withRPCToken(t, "token")
	t.Setenv("NHB_CHAIN_ID", "")
	path, key := writeKeyFile(t)
	for _, form := range [][]string{{"--chain-id", "777"}, {"--chain-id=777"}, {"--chain-id", " 777 "}} {
		n.submitted = nil
		if code := sendViaRun(t, n.URL, path, recipientAddress(t), form...); code != 0 {
			t.Fatalf("%v: exit %d", form, code)
		}
		if len(n.submitted) != 1 || n.submitted[0].ChainID.Int64() != 777 {
			t.Fatalf("%v: submitted %v", form, n.submitted)
		}
		if from, err := n.submitted[0].From(); err != nil || string(from) != string(key.PubKey().Address().Bytes()) {
			t.Fatalf("%v: the signature does not recover the sender: %v", form, err)
		}
	}
	if n.statsHits != 0 {
		t.Fatalf("the node was asked %d times although the operator chose the id", n.statsHits)
	}
}

func TestTheEnvironmentOverridesTheChainIDAndTheFlagBeatsIt(t *testing.T) {
	n := newCLINode(t, statsBody("18346390202490284624"))
	withRPCEndpoint(t, n.URL)
	withRPCToken(t, "token")
	path, _ := writeKeyFile(t)

	t.Setenv("NHB_CHAIN_ID", " 888 ")
	if code := sendViaRun(t, n.URL, path, recipientAddress(t)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := n.submitted[len(n.submitted)-1].ChainID.Int64(); got != 888 {
		t.Fatalf("NHB_CHAIN_ID was not used: %d", got)
	}
	if code := sendViaRun(t, n.URL, path, recipientAddress(t), "--chain-id", "999"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := n.submitted[len(n.submitted)-1].ChainID.Int64(); got != 999 {
		t.Fatalf("the flag must beat the environment: %d", got)
	}
	if n.statsHits != 0 {
		t.Fatalf("the node was asked %d times", n.statsHits)
	}
}

func TestAnOverrideThatIsNotAPositiveIntegerIsRefused(t *testing.T) {
	for _, bad := range []string{"0", "-5", "abc", "0x4e4842", "1.5", "1e3", "+7", "18446744073709551616", "12 34"} {
		// as a flag: a usage error before anything runs
		n := newCLINode(t, statsBody("5130306"))
		withRPCEndpoint(t, n.URL)
		withRPCToken(t, "token")
		t.Setenv("NHB_CHAIN_ID", "")
		path, _ := writeKeyFile(t)
		if code := sendViaRun(t, n.URL, path, recipientAddress(t), "--chain-id", bad); code == 0 || len(n.submitted) != 0 {
			t.Fatalf("flag %q: exit %d, %d transactions sent", bad, code, len(n.submitted))
		}
		// as the environment: the signing command refuses; it does not fall back to the node's id or to a constant
		t.Setenv("NHB_CHAIN_ID", bad)
		if code := sendViaRun(t, n.URL, path, recipientAddress(t)); code == 0 || len(n.submitted) != 0 {
			t.Fatalf("env %q: exit %d, %d transactions sent", bad, code, len(n.submitted))
		}
	}
	// An empty value is not an override.
	n := newCLINode(t, statsBody("5130306"))
	withRPCEndpoint(t, n.URL)
	withRPCToken(t, "token")
	path, _ := writeKeyFile(t)
	if code := sendViaRun(t, n.URL, path, recipientAddress(t), "--chain-id="); code == 0 || len(n.submitted) != 0 {
		t.Fatalf("--chain-id= must be refused: exit %d", code)
	}
	if code := run([]string{"--chain-id"}); code == 0 {
		t.Fatal("--chain-id with no value must be refused")
	}
}

func TestABadEnvironmentValueDoesNotStopCommandsThatSignNothing(t *testing.T) {
	t.Setenv("NHB_CHAIN_ID", "0x4e4842")
	if code := run([]string{"help"}); code != 0 {
		t.Fatalf("help exited %d because of NHB_CHAIN_ID", code)
	}
}

func TestWithNoOverrideTheNodeIsStillAskedAndNothingFallsBack(t *testing.T) {
	t.Setenv("NHB_CHAIN_ID", "")
	n := newCLINode(t, `{"result":{"activeValidators":2}}`)
	withRPCEndpoint(t, n.URL)
	withRPCToken(t, "token")
	path, _ := writeKeyFile(t)
	if code := sendViaRun(t, n.URL, path, recipientAddress(t)); code == 0 || len(n.submitted) != 0 {
		t.Fatalf("a node with no id and no override signed: exit %d, %d sent", code, len(n.submitted))
	}
	if n.statsHits == 0 {
		t.Fatal("the node was not asked")
	}
}

func TestTheOverrideNeverAppliesToAnUnsetFlagFromAnEarlierRun(t *testing.T) {
	n := newCLINode(t, statsBody("18346390202490284624"))
	withRPCEndpoint(t, n.URL)
	withRPCToken(t, "token")
	t.Setenv("NHB_CHAIN_ID", "")
	path, _ := writeKeyFile(t)
	if code := sendViaRun(t, n.URL, path, recipientAddress(t), "--chain-id", "777"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	resetTxChainID()
	if code := sendViaRun(t, n.URL, path, recipientAddress(t)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := n.submitted[len(n.submitted)-1].ChainID.String(); got != "18346390202490284624" {
		t.Fatalf("the flag of a previous run leaked into this one: %s", got)
	}
}
