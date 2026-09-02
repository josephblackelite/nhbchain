package rpc

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestIsExplorerUserFacingTypeExcludesSenderlessOracleSubmissions is a
// direct regression test for a real production symptom: buybackd's
// automated TxTypeBuybackRefPrice/TxTypeLendingRefPrice submissions carry no
// real From/To (see types.RequiresSignature's doc comment -- both are
// senderless) and fire on every price-refresh cycle, so leaving them
// user-facing let them fill every slot of the fixed-size latestTransactions
// window, pushing genuine user transfers entirely out of view. Confirmed
// live: 20/20 recent transaction slots were refprice submissions.
// TxTypeMint/TxTypeSwapVoucherMint are also senderless but must stay
// user-facing (they carry a real voucher recipient) -- this test guards
// against ever accidentally filtering those out too.
func TestIsExplorerUserFacingTypeExcludesSenderlessOracleSubmissions(t *testing.T) {
	excluded := []types.TxType{types.TxTypeHeartbeat, types.TxTypeBuybackRefPrice, types.TxTypeLendingRefPrice}
	for _, tt := range excluded {
		if isExplorerUserFacingType(tt) {
			t.Errorf("expected TxType %v to be excluded from the explorer feed", tt)
		}
	}

	stillVisible := []types.TxType{types.TxTypeTransfer, types.TxTypeTransferZNHB, types.TxTypeMint, types.TxTypeSwapVoucherMint, types.TxTypePOSAuthorize}
	for _, tt := range stillVisible {
		if !isExplorerUserFacingType(tt) {
			t.Errorf("expected TxType %v to remain visible in the explorer feed", tt)
		}
	}
}

// TestBuildExplorerSnapshotFindsMultipleAddressesViaBackfill is the
// regression test for the second half of the same production symptom: the
// Addresses tab showed only ONE address even though real transfer activity
// existed further back in chain history. Root cause was the backfill loop's
// early-exit condition -- "found at least one address with activity" --
// which combined with a small initial window (and, live, with
// latestTransactions already padded past its cap by refprice noise) meant
// the scan gave up as soon as a single real address was found, never
// continuing to fill out a real page. Commits enough blocks, each
// transferring to a distinct recipient, that a naive small initial window
// would only see the most recent one or two -- backfill must walk far
// enough back to surface several, up to explorerActiveAddressLimit.
func TestBuildExplorerSnapshotFindsMultipleAddressesViaBackfill(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	var senderAddr [20]byte
	copy(senderAddr[:], senderKey.PubKey().Address().Bytes())
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(senderAddr[:], &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	sendTransfer := func(nonce uint64, to [20]byte) {
		t.Helper()
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    nonce,
			To:       append([]byte(nil), to[:]...),
			Value:    big.NewInt(10),
			GasLimit: 21_000,
			GasPrice: big.NewInt(1),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign transfer (nonce %d): %v", nonce, err)
		}
		block, err := node.CreateBlock([]*types.Transaction{tx})
		if err != nil {
			t.Fatalf("create block (nonce %d): %v", nonce, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit block (nonce %d): %v", nonce, err)
		}
	}

	// Oldest blocks first: 5 distinct new recipients, buried furthest back
	// in history -- these are the addresses a broken backfill would never
	// reach.
	const distinctRecipients = 5
	nonce := uint64(0)
	for i := 0; i < distinctRecipients; i++ {
		recipientKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate recipient %d key: %v", i, err)
		}
		var recipientAddr [20]byte
		copy(recipientAddr[:], recipientKey.PubKey().Address().Bytes())
		sendTransfer(nonce, recipientAddr)
		nonce++
	}

	// Most recent blocks: many repeats to the SAME single recipient. This
	// pads latestTransactions past explorerDefaultLatestTxCount using only
	// 2 distinct addresses (sender + this recipient) -- exactly the
	// condition that let the old "len(addressStats) > 0" exit fire before
	// ever reaching the 5 distinct recipients above.
	recycledKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recycled recipient key: %v", err)
	}
	var recycledAddr [20]byte
	copy(recycledAddr[:], recycledKey.PubKey().Address().Bytes())
	const recycledTransferCount = 25
	for i := 0; i < recycledTransferCount; i++ {
		sendTransfer(nonce, recycledAddr)
		nonce++
	}

	server := newTestServer(t, node, nil, ServerConfig{})
	// A small recentBlocks window forces the backfill path to do real work.
	snapshot, err := server.buildExplorerSnapshot(2)
	if err != nil {
		t.Fatalf("build explorer snapshot: %v", err)
	}

	found := make(map[string]bool, len(snapshot.ActiveAddresses))
	for _, a := range snapshot.ActiveAddresses {
		found[a.Address] = true
	}
	if len(found) <= 2 {
		t.Fatalf("expected backfill to surface more than the 2 recent-window addresses (sender + recycled recipient), got %d: %+v", len(found), snapshot.ActiveAddresses)
	}
}
