package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/fees"
	"nhbchain/storage"
)

// PL-R5T-42: nhb_getOwnerWalletStats reports the owner wallet's fee total from the
// durable per-domain totals, not from the node's bounded, restart-empty event log.
func TestOwnerWalletStatsFeeAccrualIsReadFromTheDurableTotals(t *testing.T) {
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	ownerAddr := ownerKey.PubKey().Address()
	var owner [20]byte
	copy(owner[:], ownerAddr.Bytes())

	global := node.GlobalConfig()
	global.Fees.OwnerWallet = ownerAddr.String()
	if err := node.SetGlobalConfig(global); err != nil {
		t.Fatalf("set global config: %v", err)
	}
	node.SetFeePolicy(fees.Policy{Domains: map[string]fees.DomainPolicy{
		"pos": {MDRBasisPoints: 100, OwnerWallet: owner},
	}})
	if err := node.WithState(func(m *nhbstate.Manager) error {
		if err := m.FeesAccumulateTotals("pos", "NHB", owner, big.NewInt(20_000), big.NewInt(1_000), big.NewInt(19_000)); err != nil {
			return err
		}
		return m.FeesAccumulateTotals("pos", "ZNHB", owner, big.NewInt(2_000), big.NewInt(100), big.NewInt(1_900))
	}); err != nil {
		t.Fatalf("seed fee totals: %v", err)
	}

	server := newTestServer(t, node, nil, ServerConfig{})
	rec := httptest.NewRecorder()
	server.handleGetOwnerWalletStats(rec, httptest.NewRequest(http.MethodPost, "/", nil), &RPCRequest{ID: 1})
	var reply struct {
		Result struct {
			FeeAccrual        string            `json:"feeAccrual"`
			FeeAccrualByAsset map[string]string `json:"feeAccrualByAsset"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if reply.Result.FeeAccrual != "1100" || reply.Result.FeeAccrualByAsset["NHB"] != "1000" || reply.Result.FeeAccrualByAsset["ZNHB"] != "100" {
		t.Fatalf("feeAccrual %q by asset %v, want 1100 = NHB 1000 + ZNHB 100 from the durable totals (the node holds no fee events)", reply.Result.FeeAccrual, reply.Result.FeeAccrualByAsset)
	}
}
