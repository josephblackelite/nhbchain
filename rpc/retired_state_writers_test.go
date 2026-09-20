package rpc

// The RPC methods that wrote the node's live state trie from outside block
// execution (pos_sweepVoids, potso_reward_claim, reputation_verifySkill) and the
// two snapshot methods that overwrote or exported it (sync_snapshot_import,
// sync_snapshot_export) answer with the retired response, whatever the caller
// sends and whether or not it holds a token, and they run nothing: the state the
// node would put in its next block is the same before and after. These go
// through the real dispatch, so they hold for any tree that can be pointed at.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/potso"
	"nhbchain/native/reputation"
)

// serveBearer sends one JSON-RPC call through the full dispatch, with a bearer
// token when token is not empty.
func serveBearer(t *testing.T, srv *Server, token, method string, params ...any) (*httptest.ResponseRecorder, *RPCError) {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(params))
	for _, p := range params {
		raw = append(raw, marshalParam(t, p))
	}
	body := buildRPCRequestBody(t, 1, method, raw)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	_, rpcErr := decodeRPCResponse(t, rec)
	return rec, rpcErr
}

func requireRetired(t *testing.T, label string, rec *httptest.ResponseRecorder, rpcErr *RPCError) {
	t.Helper()
	if rec.Code != http.StatusGone {
		t.Fatalf("%s: HTTP %d, want %d (retired); body %s", label, rec.Code, http.StatusGone, rec.Body.String())
	}
	if rpcErr == nil || rpcErr.Code != codeMethodDisabled {
		t.Fatalf("%s: error %+v, want code %d (disabled)", label, rpcErr, codeMethodDisabled)
	}
	if !strings.Contains(rpcErr.Message, "disabled") {
		t.Fatalf("%s: message %q does not say the method is disabled", label, rpcErr.Message)
	}
}

func TestStateWritingRPCsAreRetired(t *testing.T) {
	env := newTestEnv(t)
	outDir := t.TempDir()

	verifier := [20]byte{0x51}
	subject := [20]byte{0x52}
	claimant := [20]byte{0x53}
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.SetRole("ROLE_REPUTATION_VERIFIER", verifier[:])
	}); err != nil {
		t.Fatalf("grant verifier role: %v", err)
	}

	calls := []struct {
		method string
		params []any
	}{
		{"pos_sweepVoids", []any{map[string]int64{"timestamp": 4102444800}}},
		{"pos_sweepVoids", nil},
		{"potso_reward_claim", []any{map[string]any{"epoch": 0, "address": crypto.MustNewAddress(crypto.NHBPrefix, claimant[:]).String(), "signature": "0x" + strings.Repeat("11", 65)}}},
		{"reputation_verifySkill", []any{map[string]any{
			"verifier": crypto.MustNewAddress(crypto.NHBPrefix, verifier[:]).String(),
			"subject":  crypto.MustNewAddress(crypto.NHBPrefix, subject[:]).String(),
			"skill":    "Solidity",
		}}},
		{"sync_snapshot_export", []any{map[string]string{"outDir": outDir}}},
		{"sync_snapshot_import", []any{map[string]any{"chunkDir": outDir, "manifest": map[string]any{}}}},
	}

	before := env.node.PendingStateRoot()
	for _, call := range calls {
		for _, token := range []string{env.token, ""} {
			label := fmt.Sprintf("%s (token=%t)", call.method, token != "")
			rec, rpcErr := serveBearer(t, env.server, token, call.method, call.params...)
			requireRetired(t, label, rec, rpcErr)
		}
	}
	if after := env.node.PendingStateRoot(); !bytes.Equal(before, after) {
		t.Fatalf("the retired methods changed the state the next block would carry: %x -> %x", before, after)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("sync_snapshot_export wrote %d files to the directory it was given", len(entries))
	}

	// The read-only neighbours are still served.
	rec, rpcErr := serveBearer(t, env.server, env.token, "sync_status")
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("sync_status: HTTP %d, error %+v", rec.Code, rpcErr)
	}
}

// A claim-mode reward with a genuine signature from its owner used to be paid by
// the RPC out of the treasury on the live trie. It is not paid any more.
func TestRetiredRewardClaimMovesNoFunds(t *testing.T) {
	env := newTestEnv(t)
	cfg := env.node.PotsoRewardConfig()
	cfg.PayoutMode = potso.RewardPayoutModeClaim
	if err := env.node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("switch to claim mode: %v", err)
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate claimant key: %v", err)
	}
	var claimant [20]byte
	copy(claimant[:], key.PubKey().Address().Bytes())
	address := key.PubKey().Address().String()
	amount := big.NewInt(1_000_000)
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.PotsoRewardsSetClaim(0, claimant, &potso.RewardClaim{Amount: new(big.Int).Set(amount), Mode: potso.RewardPayoutModeClaim})
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	digest := sha256.Sum256([]byte(fmt.Sprintf("potso_reward_claim|%d|%s", 0, strings.ToLower(address))))
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign claim: %v", err)
	}
	params := map[string]any{"epoch": 0, "address": address, "signature": "0x" + hex.EncodeToString(sig)}

	balance := func() *big.Int {
		var out *big.Int
		if err := env.node.WithStateView(func(m *nhbstate.Manager) error {
			account, err := m.GetAccount(claimant[:])
			if err != nil {
				return err
			}
			out = new(big.Int).Set(account.BalanceZNHB)
			return nil
		}); err != nil {
			t.Fatalf("read claimant balance: %v", err)
		}
		return out
	}
	before := env.node.PendingStateRoot()

	rec, rpcErr := serveBearer(t, env.server, env.token, "potso_reward_claim", params)
	requireRetired(t, "potso_reward_claim", rec, rpcErr)

	if got := balance(); got.Sign() != 0 {
		t.Fatalf("the claimant was paid %s by a retired method", got)
	}
	if after := env.node.PendingStateRoot(); !bytes.Equal(before, after) {
		t.Fatalf("the retired claim changed the state the next block would carry")
	}
	if err := env.node.WithStateView(func(m *nhbstate.Manager) error {
		claim, ok, err := m.PotsoRewardsGetClaim(0, claimant)
		if err != nil {
			return err
		}
		if !ok || claim == nil || claim.Claimed {
			return fmt.Errorf("claim record %+v was marked claimed", claim)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A skill attestation from a genuine verifier used to be written by the RPC into
// the live trie. Nothing is recorded any more.
func TestRetiredVerifySkillRecordsNothing(t *testing.T) {
	env := newTestEnv(t)
	verifier := [20]byte{0x61}
	subject := [20]byte{0x62}
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.SetRole("ROLE_REPUTATION_VERIFIER", verifier[:])
	}); err != nil {
		t.Fatalf("grant verifier role: %v", err)
	}
	before := env.node.PendingStateRoot()

	rec, rpcErr := serveBearer(t, env.server, env.token, "reputation_verifySkill", map[string]any{
		"verifier":  crypto.MustNewAddress(crypto.NHBPrefix, verifier[:]).String(),
		"subject":   crypto.MustNewAddress(crypto.NHBPrefix, subject[:]).String(),
		"skill":     "Solidity",
		"expiresAt": time.Now().Add(time.Hour).Unix(),
	})
	requireRetired(t, "reputation_verifySkill", rec, rpcErr)

	if after := env.node.PendingStateRoot(); !bytes.Equal(before, after) {
		t.Fatalf("the retired method changed the state the next block would carry")
	}
	if err := env.node.WithStateView(func(m *nhbstate.Manager) error {
		_, ok, err := reputation.NewLedger(m).Get(subject, "Solidity", verifier)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("an attestation was recorded by a retired method")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
