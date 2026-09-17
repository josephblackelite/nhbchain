package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestAccrualTopUpAndUnstake(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}

	mgr := nhbstate.NewManager(tr)
	engine := nhbstate.NewRewardEngine(mgr)

	var addr [20]byte
	addr[19] = 0xAA

	unit := new(big.Int).Lsh(big.NewInt(1), 128)
	encode := func(value *big.Int) []byte {
		buf := make([]byte, 32)
		value.FillBytes(buf)
		return buf
	}
	calcReward := func(start, end, stake *big.Int) *big.Int {
		delta := new(big.Int).Sub(end, start)
		if delta.Sign() <= 0 {
			return big.NewInt(0)
		}
		reward := new(big.Int).Mul(delta, stake)
		reward.Quo(reward, unit)
		return reward
	}

	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		LockedZNHB:  big.NewInt(100),
		Stake:       big.NewInt(0),
		StakeShares: big.NewInt(0),
	}
	if err := mgr.PutAccount(addr[:], account); err != nil {
		t.Fatalf("put account: %v", err)
	}

	initialSnap := &nhbstate.AccountSnap{
		LastIndexUQ128x128: encode(new(big.Int).Set(unit)),
		AccruedZNHB:        big.NewInt(0),
	}
	if err := mgr.PutStakingSnap(addr[:], initialSnap); err != nil {
		t.Fatalf("put snap: %v", err)
	}

	if err := mgr.PutGlobalIndex(&nhbstate.GlobalIndex{UQ128x128: encode(new(big.Int).Set(unit)), LastUpdateUnix: 1}); err != nil {
		t.Fatalf("put initial global: %v", err)
	}

	idxOnePointFive := new(big.Int).Mul(unit, big.NewInt(3))
	idxOnePointFive.Quo(idxOnePointFive, big.NewInt(2))
	if err := mgr.PutGlobalIndex(&nhbstate.GlobalIndex{UQ128x128: encode(idxOnePointFive), LastUpdateUnix: 2}); err != nil {
		t.Fatalf("put 1.5 index: %v", err)
	}

	if err := engine.SettleDelegate(addr[:], big.NewInt(50)); err != nil {
		t.Fatalf("settle delegate: %v", err)
	}

	snap, err := mgr.GetStakingSnap(addr[:])
	if err != nil {
		t.Fatalf("get snap after delegate: %v", err)
	}
	expectedAfterDelegate := calcReward(unit, idxOnePointFive, big.NewInt(100))
	if snap.AccruedZNHB.Cmp(expectedAfterDelegate) != 0 {
		t.Fatalf("accrued after delegate: got %s want %s", snap.AccruedZNHB, expectedAfterDelegate)
	}

	updatedAcc, err := mgr.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account after delegate: %v", err)
	}
	if updatedAcc.LockedZNHB.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("locked after delegate: got %s want 150", updatedAcc.LockedZNHB)
	}

	idxOnePointEight := new(big.Int).Mul(unit, big.NewInt(9))
	idxOnePointEight.Quo(idxOnePointEight, big.NewInt(5))
	if err := mgr.PutGlobalIndex(&nhbstate.GlobalIndex{UQ128x128: encode(idxOnePointEight), LastUpdateUnix: 3}); err != nil {
		t.Fatalf("put 1.8 index: %v", err)
	}

	if err := engine.AccrueAccount(addr[:]); err != nil {
		t.Fatalf("accrue account: %v", err)
	}

	snap, err = mgr.GetStakingSnap(addr[:])
	if err != nil {
		t.Fatalf("get snap after accrue: %v", err)
	}
	expectedAfterAccrue := new(big.Int).Add(expectedAfterDelegate, calcReward(idxOnePointFive, idxOnePointEight, big.NewInt(150)))
	if snap.AccruedZNHB.Cmp(expectedAfterAccrue) != 0 {
		t.Fatalf("accrued after accrue: got %s want %s", snap.AccruedZNHB, expectedAfterAccrue)
	}
	if new(big.Int).SetBytes(snap.LastIndexUQ128x128).Cmp(idxOnePointEight) != 0 {
		t.Fatalf("last index after accrue: got %s want %s", new(big.Int).SetBytes(snap.LastIndexUQ128x128), idxOnePointEight)
	}

	idxTwo := new(big.Int).Mul(unit, big.NewInt(2))
	if err := mgr.PutGlobalIndex(&nhbstate.GlobalIndex{UQ128x128: encode(idxTwo), LastUpdateUnix: 4}); err != nil {
		t.Fatalf("put 2.0 index: %v", err)
	}

	if err := engine.SettleUndelegate(addr[:], big.NewInt(60)); err != nil {
		t.Fatalf("settle undelegate: %v", err)
	}

	snap, err = mgr.GetStakingSnap(addr[:])
	if err != nil {
		t.Fatalf("get snap after undelegate: %v", err)
	}
	expectedAfterUndelegate := new(big.Int).Add(expectedAfterAccrue, calcReward(idxOnePointEight, idxTwo, big.NewInt(150)))
	if snap.AccruedZNHB.Cmp(expectedAfterUndelegate) != 0 {
		t.Fatalf("accrued after undelegate: got %s want %s", snap.AccruedZNHB, expectedAfterUndelegate)
	}
	if new(big.Int).SetBytes(snap.LastIndexUQ128x128).Cmp(idxTwo) != 0 {
		t.Fatalf("last index after undelegate: got %s want %s", new(big.Int).SetBytes(snap.LastIndexUQ128x128), idxTwo)
	}

	finalAcc, err := mgr.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account after undelegate: %v", err)
	}
	if finalAcc.LockedZNHB.Cmp(big.NewInt(90)) != 0 {
		t.Fatalf("locked after undelegate: got %s want 90", finalAcc.LockedZNHB)
	}
}
