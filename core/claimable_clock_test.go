package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/claimable"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

func fundedAccount(nhb int64) *types.Account {
	return &types.Account{BalanceNHB: big.NewInt(nhb), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func claimableHashLock(preimage []byte) [32]byte {
	var lock [32]byte
	copy(lock[:], ethcrypto.Keccak256(preimage))
	return lock
}

// The node's claimable methods (dormant: their RPC methods answer 410 and nothing
// else calls them) used to read the machine's wall clock directly. They now take
// the time from the state processor, which inside block execution is the block's
// own timestamp, so two nodes executing the same transaction reach the same record
// (PL-DC-40, PL-DC-55). The tests give the processor a clock far from the wall
// clock and check that every method uses it.
func TestClaimableNodeMethodsTakeTheirTimeFromTheStateProcessor(t *testing.T) {
	node := newTestNode(t)
	fixed := time.Unix(1_900_000_000, 0).UTC()
	node.state.nowFunc = func() time.Time { return fixed }

	payer := [20]byte{0x31}
	payee := [20]byte{0x32}
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.PutAccount(payer[:], fundedAccount(1_000)); err != nil {
		t.Fatalf("fund payer: %v", err)
	}

	preimage := []byte("secret-one")
	hashLock := claimableHashLock(preimage)
	id, err := node.ClaimableCreate(payer, "NHB", big.NewInt(100), hashLock, fixed.Unix()+3600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	record, err := node.ClaimableGet(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.CreatedAt != fixed.Unix() {
		t.Fatalf("CreatedAt = %d, want the processor's clock %d (wall clock leaked in)", record.CreatedAt, fixed.Unix())
	}

	// Claim at the processor's time: the deadline is an hour ahead of the fake clock
	// and a long time behind the wall clock, so a wall-clock read would see it as
	// expired.
	if err := node.ClaimableClaim(id, preimage, payee); err != nil {
		t.Fatalf("claim with the deadline an hour ahead of the processor's clock: %v", err)
	}
	record, _ = node.ClaimableGet(id)
	if record.Status != claimable.ClaimStatusClaimed {
		t.Fatalf("status = %v, want claimed", record.Status)
	}
}

func TestClaimableExpireAndCancelUseTheStateProcessorClock(t *testing.T) {
	node := newTestNode(t)
	fixed := time.Unix(1_900_000_000, 0).UTC()
	node.state.nowFunc = func() time.Time { return fixed }
	payer := [20]byte{0x41}
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.PutAccount(payer[:], fundedAccount(1_000)); err != nil {
		t.Fatalf("fund payer: %v", err)
	}

	// Deadline one hour AFTER the processor's clock but long BEFORE the wall clock:
	// expiry on the wall clock would succeed, expiry on the processor's clock must
	// refuse because the deadline has not been reached.
	id, err := node.ClaimableCreate(payer, "NHB", big.NewInt(50), claimableHashLock([]byte("x")), fixed.Unix()+3600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := node.ClaimableExpire(id); err == nil {
		t.Fatalf("expire succeeded before the deadline of the processor's clock: a wall clock was read")
	}

	// Move the processor's clock past the deadline: now expiry works.
	later := fixed.Add(2 * time.Hour)
	node.state.nowFunc = func() time.Time { return later }
	if err := node.ClaimableExpire(id); err != nil {
		t.Fatalf("expire after the deadline of the processor's clock: %v", err)
	}
}
