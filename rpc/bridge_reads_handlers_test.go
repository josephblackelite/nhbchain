package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

// PL-BC-C: bridge_getLock, bridge_listLocks and bridge_getRelease answer anonymously, validate their parameters, report
// exactly what the state holds and never write. The state is seeded with the records the chain's own lock and release
// code writes (same keys, same RLP shapes); the chain-side reads are tested in core against real lock transactions.

type seededLock struct {
	Sender    []byte
	Amount    *big.Int
	DestChain string
	Dest      string
	Height    uint64
}

type seededRelease struct {
	Recipient []byte
	Amount    *big.Int
	Height    uint64
	Status    uint8
}

type seededPending struct {
	Recipient    []byte
	Amount       *big.Int
	QueuedAt     uint64
	ExecutableAt uint64
}

func seedBridgeLocks(t *testing.T, env *testEnv, n int) (sender []byte) {
	t.Helper()
	sender = make([]byte, 20)
	sender[0], sender[19] = 0xA1, 0x07
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		for i := 1; i <= n; i++ {
			rec := seededLock{Sender: sender, Amount: big.NewInt(int64(i) * 1000), DestChain: "eip155:8453", Dest: "0x1111111111111111111111111111111111111111", Height: uint64(100 + i)}
			if err := m.KVPut([]byte("bridge/lock/"+strconv.Itoa(i)), rec); err != nil {
				return err
			}
		}
		return m.KVPut([]byte("bridge/seq"), uint64(n))
	}); err != nil {
		t.Fatalf("seed locks: %v", err)
	}
	return sender
}

func bridgeCall(t *testing.T, env *testEnv, method string, params ...json.RawMessage) (json.RawMessage, *RPCError) {
	t.Helper()
	return callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		switch method {
		case "bridge_getLock":
			env.server.handleBridgeGetLock(w, env.newRequest(), req)
		case "bridge_listLocks":
			env.server.handleBridgeListLocks(w, env.newRequest(), req)
		case "bridge_getRelease":
			env.server.handleBridgeGetRelease(w, env.newRequest(), req)
		default:
			t.Fatalf("unknown method %s", method)
		}
	}, params...)
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestBridgeGetLockReportsTheStoredRecord(t *testing.T) {
	env := newTestEnv(t)
	sender := seedBridgeLocks(t, env, 3)
	for _, param := range []string{`2`, `"2"`, `" 2 "`} {
		result, rpcErr := bridgeCall(t, env, "bridge_getLock", raw(param))
		if rpcErr != nil {
			t.Fatalf("param %s: %+v", param, rpcErr)
		}
		var got bridgeLockResult
		if err := json.Unmarshal(result, &got); err != nil {
			t.Fatal(err)
		}
		want := crypto.MustNewAddress(crypto.NHBPrefix, sender).String()
		if got.ID != 2 || got.Sender != want || got.AmountWei != "2000" || got.DestChain != "eip155:8453" || got.Dest != "0x1111111111111111111111111111111111111111" || got.Height != 102 {
			t.Fatalf("param %s: lock 2 reads %+v, want sender %s", param, got, want)
		}
	}
}

func TestBridgeGetLockRefusesBadParametersAndUnknownIds(t *testing.T) {
	env := newTestEnv(t)
	seedBridgeLocks(t, env, 3)
	for _, params := range [][]json.RawMessage{
		{}, {raw(`1`), raw(`2`)}, {raw(`0`)}, {raw(`-1`)}, {raw(`1.5`)}, {raw(`"abc"`)}, {raw(`""`)}, {raw(`null`)}, {raw(`18446744073709551616`)},
	} {
		if _, rpcErr := bridgeCall(t, env, "bridge_getLock", params...); rpcErr == nil || rpcErr.Code != codeInvalidParams || strings.Contains(rpcErr.Message, "no such lock") {
			t.Fatalf("params %v must be refused as invalid parameters (not as a missing lock), got %+v", params, rpcErr)
		}
	}
	if _, rpcErr := bridgeCall(t, env, "bridge_getLock", raw(`4`)); rpcErr == nil || !strings.Contains(rpcErr.Message, "no such lock") {
		t.Fatalf("an id past the last lock must say so, got %+v", rpcErr)
	}
}

func TestBridgeListLocksPagesAndReportsTheLastId(t *testing.T) {
	env := newTestEnv(t)
	seedBridgeLocks(t, env, 5)
	list := func(params ...json.RawMessage) bridgeLocksResult {
		t.Helper()
		result, rpcErr := bridgeCall(t, env, "bridge_listLocks", params...)
		if rpcErr != nil {
			t.Fatalf("params %v: %+v", params, rpcErr)
		}
		var got bridgeLocksResult
		if err := json.Unmarshal(result, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	all := list()
	if len(all.Locks) != 5 || all.LastLockID != 5 || all.Locks[0].ID != 1 || all.Locks[4].ID != 5 {
		t.Fatalf("no parameters must list everything: %+v", all)
	}
	page := list(raw(`1`), raw(`2`))
	if len(page.Locks) != 2 || page.Locks[0].ID != 2 || page.Locks[1].ID != 3 || page.LastLockID != 5 {
		t.Fatalf("after 1, limit 2: %+v", page)
	}
	page = list(raw(`"4"`))
	if len(page.Locks) != 1 || page.Locks[0].ID != 5 {
		t.Fatalf("after 4: %+v", page)
	}
	empty := list(raw(`5`))
	if len(empty.Locks) != 0 || empty.LastLockID != 5 {
		t.Fatalf("after the last lock: %+v", empty)
	}
	// "locks" must be an empty JSON array, never null, so a client can range over it.
	result, _ := bridgeCall(t, env, "bridge_listLocks", raw(`9`))
	if !strings.Contains(string(result), `"locks":[]`) {
		t.Fatalf("an empty listing must carry an empty array: %s", result)
	}
	// A limit above the cap is lowered to the cap, not refused.
	huge := list(raw(`0`), raw(strconv.Itoa(core.BridgeListLocksMax+50)))
	if len(huge.Locks) != 5 {
		t.Fatalf("a huge limit must still list what exists: %+v", huge)
	}
}

func TestBridgeListLocksRefusesBadParameters(t *testing.T) {
	env := newTestEnv(t)
	seedBridgeLocks(t, env, 2)
	for _, params := range [][]json.RawMessage{
		{raw(`1`), raw(`2`), raw(`3`)}, {raw(`-1`)}, {raw(`"x"`)}, {raw(`1`), raw(`0`)}, {raw(`1`), raw(`-4`)}, {raw(`1`), raw(`"y"`)},
	} {
		if _, rpcErr := bridgeCall(t, env, "bridge_listLocks", params...); rpcErr == nil || rpcErr.Code != codeInvalidParams {
			t.Fatalf("params %v must be refused as invalid, got %+v", params, rpcErr)
		}
	}
}

func TestBridgeGetReleaseReportsNoneWaitingPaidAndCancelled(t *testing.T) {
	env := newTestEnv(t)
	recipient := make([]byte, 20)
	recipient[0], recipient[19] = 0xB2, 0x09
	idPaid, idWait, idCancel := []byte("paid-1"), []byte("wait-1"), []byte("cancel-1")
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		if err := m.KVPut([]byte("bridge/rel/"+hexOf(idPaid)), seededRelease{Recipient: recipient, Amount: big.NewInt(40), Height: 77, Status: 1}); err != nil {
			return err
		}
		if err := m.KVPut([]byte("bridge/rel/"+hexOf(idCancel)), seededRelease{Recipient: recipient, Amount: big.NewInt(50), Height: 88, Status: 2}); err != nil {
			return err
		}
		return m.KVPut([]byte("bridge/pend/"+hexOf(idWait)), seededPending{Recipient: recipient, Amount: big.NewInt(60), QueuedAt: 90, ExecutableAt: 140})
	}); err != nil {
		t.Fatal(err)
	}
	get := func(id string) bridgeReleaseResult {
		t.Helper()
		result, rpcErr := bridgeCall(t, env, "bridge_getRelease", raw(`"`+id+`"`))
		if rpcErr != nil {
			t.Fatalf("burn id %s: %+v", id, rpcErr)
		}
		var got bridgeReleaseResult
		if err := json.Unmarshal(result, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := crypto.MustNewAddress(crypto.NHBPrefix, recipient).String()
	if g := get(hexOf(idPaid)); g.Status != "paid" || g.AmountWei != "40" || g.Height != 77 || g.Recipient != want || g.BurnID != hexOf(idPaid) || g.ExecutableAt != 0 {
		t.Fatalf("paid reads %+v", g)
	}
	if g := get("0x" + hexOf(idPaid)); g.Status != "paid" {
		t.Fatalf("a 0x prefix must be accepted: %+v", g)
	}
	if g := get(hexOf(idCancel)); g.Status != "cancelled" || g.AmountWei != "50" || g.Height != 88 {
		t.Fatalf("cancelled reads %+v", g)
	}
	if g := get(hexOf(idWait)); g.Status != "pending" || g.AmountWei != "60" || g.Height != 90 || g.ExecutableAt != 140 {
		t.Fatalf("pending reads %+v", g)
	}
	if g := get(hexOf([]byte("never-used"))); g.Status != "none" || g.AmountWei != "0" || g.Recipient != "" {
		t.Fatalf("an unused id reads %+v", g)
	}
}

func TestBridgeGetReleaseRefusesBadBurnIds(t *testing.T) {
	env := newTestEnv(t)
	for _, params := range [][]json.RawMessage{
		{}, {raw(`"aa"`), raw(`"bb"`)}, {raw(`1`)}, {raw(`""`)}, {raw(`"0x"`)}, {raw(`"zz"`)}, {raw(`"abc"`)}, {raw(`"` + strings.Repeat("ab", 65) + `"`)},
	} {
		if _, rpcErr := bridgeCall(t, env, "bridge_getRelease", params...); rpcErr == nil || rpcErr.Code != codeInvalidParams {
			t.Fatalf("params %v must be refused as invalid, got %+v", params, rpcErr)
		}
	}
	// 64 bytes is the longest the chain accepts for a burn id.
	if _, rpcErr := bridgeCall(t, env, "bridge_getRelease", raw(`"`+strings.Repeat("ab", 64)+`"`)); rpcErr != nil {
		t.Fatalf("a 64 byte burn id is allowed: %+v", rpcErr)
	}
}

// The three methods are reachable through the real dispatcher with no credential at all (a client that hits the node
// over HTTP, not a test that calls the handler directly), and an unknown sibling is not.
func TestBridgeReadsAreAnonymousThroughTheDispatcher(t *testing.T) {
	env := newTestEnv(t)
	seedBridgeLocks(t, env, 2)
	post := func(method string, params ...json.RawMessage) (json.RawMessage, *RPCError, int) {
		t.Helper()
		body := buildRPCRequestBody(t, 5, method, params)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		env.server.ServeHTTP(rec, req) // no credential
		result, rpcErr := decodeRPCResponse(t, rec)
		return result, rpcErr, rec.Code
	}
	if result, rpcErr, code := post("bridge_getLock", raw(`1`)); rpcErr != nil || code != http.StatusOK || !strings.Contains(string(result), `"amountWei":"1000"`) {
		t.Fatalf("bridge_getLock without a credential: %d %+v %s", code, rpcErr, result)
	}
	if result, rpcErr, code := post("bridge_listLocks"); rpcErr != nil || code != http.StatusOK || !strings.Contains(string(result), `"lastLockId":2`) {
		t.Fatalf("bridge_listLocks without a credential: %d %+v %s", code, rpcErr, result)
	}
	if result, rpcErr, code := post("bridge_getRelease", raw(`"aabb"`)); rpcErr != nil || code != http.StatusOK || !strings.Contains(string(result), `"status":"none"`) {
		t.Fatalf("bridge_getRelease without a credential: %d %+v %s", code, rpcErr, result)
	}
	if _, rpcErr, _ := post("bridge_getLocks", raw(`1`)); rpcErr == nil || rpcErr.Code != codeMethodNotFound {
		t.Fatalf("a method name that does not exist must be refused as unknown, got %+v", rpcErr)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}
