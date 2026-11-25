package core

import (
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-EXT-91: what one account read can refuse is refused before the admission simulation copies the state.

func simulationsRun(n *Node) uint64 { return atomic.LoadUint64(&n.admissionSimulations) }

func fundWithNonce(t *testing.T, node *Node, addr []byte, nhb, znhb int64, nonce uint64) {
	t.Helper()
	err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(addr, &types.Account{
			BalanceNHB:  big.NewInt(nhb),
			BalanceZNHB: big.NewInt(znhb),
			Stake:       big.NewInt(0),
			Nonce:       nonce,
		})
	})
	if err != nil {
		t.Fatalf("fund account: %v", err)
	}
}

func requireRefusedWithoutSimulation(t *testing.T, node *Node, tx *types.Transaction, wantText string) {
	t.Helper()
	before := simulationsRun(node)
	err := node.AddTransaction(tx)
	if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), wantText) {
		t.Fatalf("want ErrInvalidTransaction containing %q, got %v", wantText, err)
	}
	if got := simulationsRun(node); got != before {
		t.Fatalf("the refusal must come before the state copy: simulations went from %d to %d", before, got)
	}
}

func TestAdmissionPrecheckRefusesWithoutCopyingState(t *testing.T) {
	to := make([]byte, 20)
	to[19] = 0x77

	t.Run("NHB value above the balance", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		addr := key.PubKey().Address().Bytes()
		fundWithNonce(t, node, addr, 1_000, 0, 0)
		requireRefusedWithoutSimulation(t, node, livenessSign(t, key, types.TxTypeTransfer, 0, nil, to, 5_000), "insufficient funds for transfer+gas")
		// The same transaction sent again, and with another recipient, costs no simulation either.
		to2 := append([]byte(nil), to...)
		to2[18] = 0x01
		requireRefusedWithoutSimulation(t, node, livenessSign(t, key, types.TxTypeTransfer, 0, nil, to2, 5_000), "insufficient funds for transfer+gas")
	})

	t.Run("ZNHB value above the balance", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		addr := key.PubKey().Address().Bytes()
		fundWithNonce(t, node, addr, 1_000_000, 10, 0)
		requireRefusedWithoutSimulation(t, node, livenessSign(t, key, types.TxTypeTransferZNHB, 0, nil, to, 100), "znhb transfer: insufficient balance")
	})

	t.Run("a nonce below the account's", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 3)
		requireRefusedWithoutSimulation(t, node, livenessSign(t, key, types.TxTypeTransfer, 1, nil, to, 1), "strict nonce sequence breached: expected 3, got 1")
	})

	t.Run("a nonce above the account's", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 3)
		requireRefusedWithoutSimulation(t, node, livenessSign(t, key, types.TxTypeTransfer, 5, nil, to, 1), "strict nonce sequence breached: expected 3, got 5")
	})
}

func TestAdmissionPrecheckLetsEverythingElseReachTheSimulation(t *testing.T) {
	to := make([]byte, 20)
	to[19] = 0x77

	t.Run("a funded transfer with the right nonce is admitted and simulated once", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 0)
		before := simulationsRun(node)
		if err := node.AddTransaction(livenessSign(t, key, types.TxTypeTransfer, 0, nil, to, 5)); err != nil {
			t.Fatalf("a valid transfer must be admitted: %v", err)
		}
		if got := simulationsRun(node) - before; got != 1 {
			t.Fatalf("a valid transfer is simulated exactly once, got %d", got)
		}
	})

	t.Run("a value equal to the balance is not refused by the pre-check", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000, 0, 0)
		before := simulationsRun(node)
		_ = node.AddTransaction(livenessSign(t, key, types.TxTypeTransfer, 0, nil, to, 1_000)) // the simulation decides
		if got := simulationsRun(node) - before; got != 1 {
			t.Fatalf("the pre-check refused a value equal to the balance: simulations %d", got)
		}
	})

	t.Run("a ZNHB value equal to the balance is not refused by the pre-check", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 50, 0)
		before := simulationsRun(node)
		_ = node.AddTransaction(livenessSign(t, key, types.TxTypeTransferZNHB, 0, nil, to, 50))
		if got := simulationsRun(node) - before; got != 1 {
			t.Fatalf("the pre-check refused a ZNHB value equal to the balance: simulations %d", got)
		}
	})

	t.Run("a zero-value transfer from an empty account is the simulation's to judge", func(t *testing.T) {
		node := newTestNode(t)
		key := livenessKey(t)
		fundWithNonce(t, node, key.PubKey().Address().Bytes(), 0, 0, 0)
		before := simulationsRun(node)
		_ = node.AddTransaction(livenessSign(t, key, types.TxTypeTransfer, 0, nil, to, 0))
		if got := simulationsRun(node) - before; got != 1 {
			t.Fatalf("a zero value needs no balance: simulations %d", got)
		}
	})
}

func TestAdmissionBalanceBelow(t *testing.T) {
	five, three := big.NewInt(5), big.NewInt(3)
	if !balanceBelow(three, five) || balanceBelow(five, five) || balanceBelow(five, three) {
		t.Fatalf("balanceBelow must be a strict less-than")
	}
	if !balanceBelow(nil, five) {
		t.Fatalf("a missing balance counts as zero")
	}
	if balanceBelow(nil, big.NewInt(0)) {
		t.Fatalf("zero is not above a missing balance")
	}
}

// With the admission simulation switched off validateTransaction returns before the pre-check, so the
// nonce rule of addTransaction is the only one left and must still refuse a nonce out of sequence.
func TestAddTransactionStillRefusesABadNonceWithoutTheSimulation(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	key := livenessKey(t)
	fundWithNonce(t, node, key.PubKey().Address().Bytes(), 1_000_000, 0, 3)
	to := make([]byte, 20)
	to[19] = 0x77
	for _, nonce := range []uint64{1, 5} {
		err := node.AddTransaction(livenessSign(t, key, types.TxTypeTransfer, nonce, nil, to, 1))
		if err == nil || !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "strict nonce sequence breached: expected 3") {
			t.Fatalf("nonce %d: want the strict nonce refusal, got %v", nonce, err)
		}
	}
	if got := simulationsRun(node); got != 0 {
		t.Fatalf("the simulation is off, yet %d ran", got)
	}
}
