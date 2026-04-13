package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/crypto"
)

type rpcPotsoHeartbeatResult struct {
	Accepted    bool           `json:"accepted"`
	UptimeDelta uint64         `json:"uptimeDelta"`
	Meter       *rpcPotsoMeter `json:"meter"`
}

type rpcPotsoMeter struct {
	Day           string `json:"day"`
	UptimeSeconds uint64 `json:"uptimeSeconds"`
	RawScore      uint64 `json:"rawScore"`
	Score         uint64 `json:"score"`
}

func signHeartbeat(t *testing.T, key *crypto.PrivateKey, user string, blockHeight uint64, blockHash []byte, ts int64) string {
	t.Helper()
	payload := testHeartbeatDigest(user, blockHeight, blockHash, ts)
	sig, err := ethcrypto.Sign(payload, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign heartbeat: %v", err)
	}
	return "0x" + hex.EncodeToString(sig)
}

func TestHandlePotsoHeartbeatSignatureMismatch(t *testing.T) {
	env := newTestEnv(t)
	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	otherKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	block, err := env.node.Chain().GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("load genesis block: %v", err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	ts := time.Now().UTC().Unix()
	params := potsoHeartbeatParams{
		User:          userKey.PubKey().Address().String(),
		LastBlock:     block.Header.Height,
		LastBlockHash: "0x" + hex.EncodeToString(hash),
		Timestamp:     ts,
		Signature:     signHeartbeat(t, otherKey, userKey.PubKey().Address().String(), block.Header.Height, hash, ts),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, params)}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected signature mismatch error")
	}
}

func TestHandlePotsoHeartbeatBadHash(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := env.node.Chain().GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("load genesis block: %v", err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	badHash := append([]byte(nil), hash...)
	badHash[0] ^= 0xFF
	ts := time.Now().UTC().Unix()
	params := potsoHeartbeatParams{
		User:          key.PubKey().Address().String(),
		LastBlock:     block.Header.Height,
		LastBlockHash: "0x" + hex.EncodeToString(badHash),
		Timestamp:     ts,
		Signature:     signHeartbeat(t, key, key.PubKey().Address().String(), block.Header.Height, badHash, ts),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, params)}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected bad hash error")
	}
}

func TestHandlePotsoHeartbeatReplayGuard(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := env.node.Chain().GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("load genesis block: %v", err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	user := key.PubKey().Address().String()
	ts := time.Now().UTC().Unix()
	params := potsoHeartbeatParams{
		User:          user,
		LastBlock:     block.Header.Height,
		LastBlockHash: "0x" + hex.EncodeToString(hash),
		Timestamp:     ts,
		Signature:     signHeartbeat(t, key, user, block.Header.Height, hash, ts),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, params)}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(recorder, env.newRequest(), req)
	if _, rpcErr := decodeRPCResponse(t, recorder); rpcErr != nil {
		t.Fatalf("first heartbeat rejected: %+v", rpcErr)
	}
	replayParams := params
	replayParams.Timestamp = ts + 30
	replayParams.Signature = signHeartbeat(t, key, user, block.Header.Height, hash, replayParams.Timestamp)
	replayReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, replayParams)}}
	replayRec := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(replayRec, env.newRequest(), replayReq)
	result, rpcErr := decodeRPCResponse(t, replayRec)
	if rpcErr != nil {
		t.Fatalf("replay heartbeat returned error: %+v", rpcErr)
	}
	var parsed rpcPotsoHeartbeatResult
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("decode replay result: %v", err)
	}
	if parsed.Accepted {
		t.Fatalf("expected heartbeat to be throttled")
	}
	if parsed.UptimeDelta != 0 {
		t.Fatalf("expected zero delta, got %d", parsed.UptimeDelta)
	}
}

func TestHandlePotsoHeartbeatAggregates(t *testing.T) {
	env := newTestEnv(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := env.node.Chain().GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("load genesis block: %v", err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	user := key.PubKey().Address().String()
	ts := time.Now().UTC().Unix()
	params := potsoHeartbeatParams{
		User:          user,
		LastBlock:     block.Header.Height,
		LastBlockHash: "0x" + hex.EncodeToString(hash),
		Timestamp:     ts,
		Signature:     signHeartbeat(t, key, user, block.Header.Height, hash, ts),
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, params)}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("heartbeat error: %+v", rpcErr)
	}
	var first rpcPotsoHeartbeatResult
	if err := json.Unmarshal(result, &first); err != nil {
		t.Fatalf("decode first result: %v", err)
	}
	if !first.Accepted || first.UptimeDelta == 0 {
		t.Fatalf("expected first heartbeat to be accepted")
	}
	followTs := ts + 90
	followParams := params
	followParams.Timestamp = followTs
	followParams.Signature = signHeartbeat(t, key, user, block.Header.Height, hash, followTs)
	followReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, followParams)}}
	followRec := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(followRec, env.newRequest(), followReq)
	followResult, rpcErr := decodeRPCResponse(t, followRec)
	if rpcErr != nil {
		t.Fatalf("second heartbeat error: %+v", rpcErr)
	}
	var second rpcPotsoHeartbeatResult
	if err := json.Unmarshal(followResult, &second); err != nil {
		t.Fatalf("decode second result: %v", err)
	}
	if second.Meter == nil {
		t.Fatalf("missing meter data")
	}
	expectedUptime := first.Meter.UptimeSeconds + 90
	if second.Meter.UptimeSeconds != expectedUptime {
		t.Fatalf("unexpected uptime: got %d want %d", second.Meter.UptimeSeconds, expectedUptime)
	}
	if second.Meter.RawScore != second.Meter.Score {
		t.Fatalf("expected raw and final score to match")
	}
}

func testHeartbeatDigest(user string, block uint64, hash []byte, ts int64) []byte {
	payload := fmt.Sprintf("potso_heartbeat|%s|%d|%s|%d", strings.ToLower(strings.TrimSpace(user)), block, strings.ToLower(hex.EncodeToString(hash)), ts)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}
