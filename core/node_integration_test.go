package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func TestCommitBlockRollsBackOnApplyError(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	parentRoot := node.state.CurrentRoot()

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("ab"),
	}
	if err := tx.Sign(validatorKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	fixedTime := time.Unix(1_700_000_000, 0).UTC()
	node.SetTimeSource(func() time.Time { return fixedTime })
	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: fixedTime.Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	block := types.NewBlock(header, []*types.Transaction{tx})

	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected commit error for invalid transaction")
	}

	if got := node.state.CurrentRoot(); got != parentRoot {
		t.Fatalf("current root changed on failed commit: got %x want %x", got.Bytes(), parentRoot.Bytes())
	}
	if height := node.chain.GetHeight(); height != 0 {
		t.Fatalf("unexpected chain height after failed commit: got %d want 0", height)
	}
}

func TestCreateBlockRejectsInvalidChainID(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	sender := userKey.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := node.state.setAccount(sender, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  big.NewInt(999_999),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("bob"),
	}
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	if _, err := node.CreateBlock([]*types.Transaction{tx}); err == nil {
		t.Fatalf("expected error for invalid chain id")
	} else if !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("expected ErrInvalidChainID, got %v", err)
	}
}

func TestCommitBlockRejectsInvalidChainID(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	sender := userKey.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if err := node.state.setAccount(sender, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	parentRoot := node.state.CurrentRoot()

	tx := &types.Transaction{
		ChainID:  big.NewInt(999_999),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    0,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("bob"),
	}
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}

	txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	fixedTime := time.Unix(1_800_000_000, 0).UTC()
	node.SetTimeSource(func() time.Time { return fixedTime })
	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: fixedTime.Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	block := types.NewBlock(header, []*types.Transaction{tx})

	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected commit error for invalid chain id")
	} else if !errors.Is(err, ErrInvalidChainID) {
		t.Fatalf("expected ErrInvalidChainID, got %v", err)
	}

	if got := node.state.CurrentRoot(); got != parentRoot {
		t.Fatalf("current root changed on failed commit: got %x want %x", got.Bytes(), parentRoot.Bytes())
	}
	stored, err := node.state.getAccount(sender)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if stored.Nonce != 0 {
		t.Fatalf("unexpected nonce mutation: got %d want 0", stored.Nonce)
	}
	if height := node.chain.GetHeight(); height != 0 {
		t.Fatalf("unexpected chain height after failed commit: got %d want 0", height)
	}
}

func TestCommitBlockEnforcesTimestampWindow(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	tolerance := 3 * time.Second
	node.SetBlockTimestampTolerance(tolerance)

	genesisTimestamp := node.chain.LastTimestamp()
	baseTime := time.Unix(genesisTimestamp+2, 0)
	node.SetTimeSource(func() time.Time { return baseTime })

	txRoot, err := ComputeTxRoot(nil)
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: baseTime.Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	block := types.NewBlock(header, nil)

	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit within tolerance failed: %v", err)
	}
	if got := node.chain.LastTimestamp(); got != baseTime.Unix() {
		t.Fatalf("unexpected last timestamp: got %d want %d", got, baseTime.Unix())
	}
	if height := node.chain.GetHeight(); height != 1 {
		t.Fatalf("unexpected height after valid commit: got %d want 1", height)
	}

	futureNow := baseTime.Add(time.Second)
	node.SetTimeSource(func() time.Time { return futureNow })
	futureHeader := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: futureNow.Add(5 * time.Second).Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	futureBlock := types.NewBlock(futureHeader, nil)
	if err := node.CommitBlock(futureBlock); err == nil {
		t.Fatalf("expected error for future timestamp beyond tolerance")
	} else if !errors.Is(err, ErrBlockTimestampOutOfWindow) {
		t.Fatalf("expected ErrBlockTimestampOutOfWindow, got %v", err)
	}
	if height := node.chain.GetHeight(); height != 1 {
		t.Fatalf("height mutated on rejected future block: got %d want 1", height)
	}

	laterNow := futureNow.Add(2 * time.Second)
	node.SetTimeSource(func() time.Time { return laterNow })
	pastHeader := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: baseTime.Add(-time.Second).Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	pastBlock := types.NewBlock(pastHeader, nil)
	if err := node.CommitBlock(pastBlock); err == nil {
		t.Fatalf("expected error for timestamp before window")
	} else if !errors.Is(err, ErrBlockTimestampOutOfWindow) {
		t.Fatalf("expected ErrBlockTimestampOutOfWindow for past block, got %v", err)
	}
	if last := node.chain.LastTimestamp(); last != baseTime.Unix() {
		t.Fatalf("last timestamp changed on rejection: got %d want %d", last, baseTime.Unix())
	}
}

func TestCommitBlockRejectsHeightMismatch(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	parentRoot := node.state.CurrentRoot()

	fixedTime := time.Unix(1_900_000_000, 0).UTC()
	node.SetTimeSource(func() time.Time { return fixedTime })

	txRoot, err := ComputeTxRoot(nil)
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 2,
		Timestamp: fixedTime.Unix(),
		PrevHash:  node.chain.Tip(),
		TxRoot:    txRoot,
		Validator: validatorKey.PubKey().Address().Bytes(),
	}
	block := types.NewBlock(header, nil)

	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected error for mismatched block height")
	} else if !strings.Contains(err.Error(), "height mismatch") {
		t.Fatalf("expected height mismatch error, got %v", err)
	}

	if got := node.chain.GetHeight(); got != 0 {
		t.Fatalf("chain height mutated on mismatch: got %d want 0", got)
	}
	if got := node.state.CurrentRoot(); got != parentRoot {
		t.Fatalf("state root changed on mismatch: got %x want %x", got.Bytes(), parentRoot.Bytes())
	}
}

func TestCommitBlockSequentialHeightsAdvanceEpochs(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	cfg := node.state.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 1
	cfg.RotationEnabled = true
	cfg.MaxValidators = 1
	cfg.SnapshotHistory = 8
	if err := node.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}

	validatorAddr := seedEligibleValidator(t, node.state, 5000, 0)

	txRoot, err := ComputeTxRoot(nil)
	if err != nil {
		t.Fatalf("compute tx root: %v", err)
	}

	currentTime := time.Unix(1_900_000_100, 0).UTC()
	node.SetTimeSource(func() time.Time { return currentTime })

	for i := 0; i < 2; i++ {
		currentTime = currentTime.Add(time.Second)
		nextHeight := node.chain.GetHeight() + 1
		header := &types.BlockHeader{
			Height:    nextHeight,
			Timestamp: currentTime.Unix(),
			PrevHash:  node.chain.Tip(),
			TxRoot:    txRoot,
			Validator: validatorKey.PubKey().Address().Bytes(),
		}
		block := types.NewBlock(header, nil)
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit block %d: %v", nextHeight, err)
		}
	}

	if got := node.chain.GetHeight(); got != 2 {
		t.Fatalf("unexpected chain height: got %d want 2", got)
	}

	history := node.state.EpochHistory()
	if len(history) != 2 {
		t.Fatalf("expected two epoch snapshots, got %d", len(history))
	}
	for i, snapshot := range history {
		expected := uint64(i + 1)
		if snapshot.Height != expected {
			t.Fatalf("snapshot %d height mismatch: got %d want %d", i, snapshot.Height, expected)
		}
		if snapshot.Epoch != expected {
			t.Fatalf("snapshot %d epoch mismatch: got %d want %d", i, snapshot.Epoch, expected)
		}
		if len(snapshot.Selected) != 1 {
			t.Fatalf("snapshot %d selected count mismatch: got %d want 1", i, len(snapshot.Selected))
		}
		if string(snapshot.Selected[0]) != string(validatorAddr) {
			t.Fatalf("snapshot %d selected validator mismatch", i)
		}
	}

	if _, ok := node.state.ValidatorSet[string(validatorAddr)]; !ok {
		t.Fatalf("validator not in active set after commits")
	}
}
