package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-CLM-02: a monthly free tier, counted in transfers, for plain NHB and ZNHB
// transfers. The fee rule it sits beside: free while the sender's lifetime spend
// in the asset is below the allowance, then TransferFeeBps (NHB) or
// TransferFeeBpsZNHB (ZNHB) of the amount. The tests use a one-wei allowance, so
// the first transfer in each asset is waived by the allowance (it starts below
// it) and every later one pays unless the monthly tier waives it, and round fee
// rates: 10 percent for NHB, 5 percent for ZNHB.

var (
	tierNovember = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tierDecember = time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC)
)

type tierEnv struct {
	t         *testing.T
	sp        *StateProcessor
	key       *crypto.PrivateKey
	addr      []byte
	to        []byte
	collector [20]byte
	nonce     uint64
}

func newTierEnv(t *testing.T, gate, perMonth uint64) *tierEnv {
	t.Helper()
	sp := newStakingStateProcessor(t)
	env := &tierEnv{t: t, sp: sp}
	env.collector[19] = 0x66
	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(1),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      env.collector,
		FeeBps:            1_000,
		FeeBpsZNHB:        500,
	})
	sp.SetTransferMonthlyFreeTier(gate, perMonth)
	env.key = env.newSender(10_000_000)
	env.addr = env.key.PubKey().Address().Bytes()
	recipient, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	env.to = recipient.PubKey().Address().Bytes()
	for _, addr := range [][]byte{env.to, env.collector[:]} {
		if err := sp.setAccount(addr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	return env
}

// newSender makes a funded sender key.
func (e *tierEnv) newSender(balance int64) *crypto.PrivateKey {
	e.t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		e.t.Fatalf("sender key: %v", err)
	}
	acc := &types.Account{BalanceNHB: big.NewInt(balance), BalanceZNHB: big.NewInt(balance), Stake: big.NewInt(0)}
	if err := e.sp.setAccount(key.PubKey().Address().Bytes(), acc); err != nil {
		e.t.Fatalf("seed sender: %v", err)
	}
	return key
}

func (e *tierEnv) at(height uint64, ts time.Time) { e.sp.BeginBlock(height, ts) }

func (e *tierEnv) collectorBalance(znhb bool) *big.Int {
	e.t.Helper()
	acc, err := e.sp.getAccount(e.collector[:])
	if err != nil {
		e.t.Fatalf("load collector: %v", err)
	}
	if znhb {
		return new(big.Int).Set(acc.BalanceZNHB)
	}
	return new(big.Int).Set(acc.BalanceNHB)
}

// send applies a signed transfer of value from the given key and returns the fee
// the collector received in that asset.
func (e *tierEnv) send(key *crypto.PrivateKey, kind types.TxType, value int64) *big.Int {
	e.t.Helper()
	znhb := kind == types.TxTypeTransferZNHB
	before := e.collectorBalance(znhb)
	nonce := e.nonceOf(key)
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     kind,
		Nonce:    nonce,
		To:       append([]byte(nil), e.to...),
		Value:    big.NewInt(value),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		e.t.Fatalf("sign: %v", err)
	}
	if err := e.sp.ApplyTransaction(tx); err != nil {
		e.t.Fatalf("apply transfer (nonce %d): %v", nonce, err)
	}
	return new(big.Int).Sub(e.collectorBalance(znhb), before)
}

func (e *tierEnv) nonceOf(key *crypto.PrivateKey) uint64 {
	acc, err := e.sp.getAccount(key.PubKey().Address().Bytes())
	if err != nil {
		e.t.Fatalf("load sender: %v", err)
	}
	return acc.Nonce
}

func (e *tierEnv) sendNHB(value int64) *big.Int {
	e.t.Helper()
	return e.send(e.key, types.TxTypeTransfer, value)
}

func (e *tierEnv) sendZNHB(value int64) *big.Int {
	e.t.Helper()
	return e.send(e.key, types.TxTypeTransferZNHB, value)
}

func wantFee(t *testing.T, label string, got *big.Int, want int64) {
	t.Helper()
	if got.Cmp(big.NewInt(want)) != 0 {
		t.Fatalf("%s: fee collected = %s, want %d", label, got, want)
	}
}

// Without the tier the rule is as it always was: the allowance waives the first
// transfer and every later one pays 10 percent.
func TestTransferMonthlyFreeTier_OffLeavesTheFeeRuleAlone(t *testing.T) {
	cases := map[string]struct{ gate, perMonth uint64 }{
		"no height":                {0, 100},
		"no count":                 {100, 0},
		"neither":                  {0, 0},
		"height not yet reached":   {5_000, 100},
		"count set, height at max": {^uint64(0), 100},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := newTierEnv(t, c.gate, c.perMonth)
			env.at(100, tierNovember)
			wantFee(t, "first transfer (allowance)", env.sendNHB(1_000), 0)
			wantFee(t, "second transfer", env.sendNHB(1_000), 100)
			wantFee(t, "third transfer", env.sendNHB(2_000), 200)
			wantFee(t, "ZNHB first (allowance)", env.sendZNHB(1_000), 0)
			wantFee(t, "ZNHB second", env.sendZNHB(1_000), 50)
		})
	}
}

// With the tier on, a sender's first N transfers of the month are free whatever
// their size, the count includes the one the allowance waived, and the next pays.
func TestTransferMonthlyFreeTier_WaivesTheFirstNTransfersOfTheMonth(t *testing.T) {
	env := newTierEnv(t, 100, 3)
	env.at(100, tierNovember)
	wantFee(t, "#1 (allowance)", env.sendNHB(1_000), 0)
	wantFee(t, "#2 (tier)", env.sendNHB(5_000), 0)
	wantFee(t, "#3 (tier)", env.sendNHB(7_000), 0)
	wantFee(t, "#4 (past the tier)", env.sendNHB(1_000), 100)
	wantFee(t, "#5", env.sendNHB(2_000), 200)
}

func TestTransferMonthlyFreeTier_StartsAtTheActivationHeight(t *testing.T) {
	env := newTierEnv(t, 100, 3)
	env.at(99, tierNovember)
	wantFee(t, "#1 (allowance)", env.sendNHB(1_000), 0)
	wantFee(t, "#2 one block before the height", env.sendNHB(1_000), 100)
	env.at(100, tierNovember)
	wantFee(t, "#3 at the height (count starts then)", env.sendNHB(1_000), 0)
}

// A new UTC month starts the count again.
func TestTransferMonthlyFreeTier_ResetsEachMonth(t *testing.T) {
	env := newTierEnv(t, 100, 2)
	env.at(100, tierNovember)
	wantFee(t, "Nov #1", env.sendNHB(1_000), 0)
	wantFee(t, "Nov #2", env.sendNHB(1_000), 0)
	wantFee(t, "Nov #3", env.sendNHB(1_000), 100)
	env.at(101, tierDecember)
	wantFee(t, "Dec #1", env.sendNHB(1_000), 0)
	wantFee(t, "Dec #2", env.sendNHB(1_000), 0)
	wantFee(t, "Dec #3", env.sendNHB(1_000), 100)
}

// The last second of a month and the first of the next are different months.
func TestTransferMonthlyFreeTier_MonthBoundaryIsUTC(t *testing.T) {
	env := newTierEnv(t, 100, 1)
	env.at(100, time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC))
	wantFee(t, "last second of November #1", env.sendNHB(1_000), 0)
	wantFee(t, "last second of November #2", env.sendNHB(1_000), 100)
	env.at(101, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	wantFee(t, "first second of December #1", env.sendNHB(1_000), 0)
}

// The count is per sender.
func TestTransferMonthlyFreeTier_CountsEachSenderSeparately(t *testing.T) {
	// Two per month, so each sender's second transfer is waived by the tier alone
	// (the allowance is spent by the first) and a counter shared between senders
	// would show: B would meet A's three transfers already counted.
	env := newTierEnv(t, 100, 2)
	other := env.newSender(10_000_000)
	env.at(100, tierNovember)
	wantFee(t, "A #1 (allowance)", env.send(env.key, types.TxTypeTransfer, 1_000), 0)
	wantFee(t, "A #2 (tier)", env.send(env.key, types.TxTypeTransfer, 1_000), 0)
	wantFee(t, "A #3", env.send(env.key, types.TxTypeTransfer, 1_000), 100)
	wantFee(t, "B #1 (allowance; not affected by A)", env.send(other, types.TxTypeTransfer, 1_000), 0)
	wantFee(t, "B #2 (tier; its own count is 1)", env.send(other, types.TxTypeTransfer, 1_000), 0)
	wantFee(t, "B #3", env.send(other, types.TxTypeTransfer, 1_000), 100)
}

// NHB and ZNHB transfers draw on the same count; each asset keeps its own fee rate
// and its own allowance.
func TestTransferMonthlyFreeTier_ZNHBSharesTheCount(t *testing.T) {
	env := newTierEnv(t, 100, 3)
	env.at(100, tierNovember)
	wantFee(t, "NHB #1 (NHB allowance)", env.sendNHB(1_000), 0)
	wantFee(t, "ZNHB #2 (ZNHB allowance)", env.sendZNHB(1_000), 0)
	wantFee(t, "ZNHB #3 (tier; its allowance is spent)", env.sendZNHB(1_000), 0)
	wantFee(t, "ZNHB #4 (past the tier)", env.sendZNHB(1_000), 50)
	wantFee(t, "NHB #5 (past the tier)", env.sendNHB(1_000), 100)
}

// The waiver changes only the fee. The recipient gets the whole amount, the sender
// is debited the amount and the fee, and the nonce moves.
func TestTransferMonthlyFreeTier_OnlyTheFeeChanges(t *testing.T) {
	env := newTierEnv(t, 100, 2)
	env.at(100, tierNovember)
	env.sendNHB(1_000) // allowance
	wantFee(t, "tier", env.sendNHB(3_000), 0)
	wantFee(t, "paid", env.sendNHB(2_000), 200)
	sender, err := env.sp.getAccount(env.addr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if want := big.NewInt(10_000_000 - 1_000 - 3_000 - 2_000 - 200); sender.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("sender balance = %s, want %s", sender.BalanceNHB, want)
	}
	recipient, err := env.sp.getAccount(env.to)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if want := big.NewInt(1_000 + 3_000 + 2_000); recipient.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("recipient balance = %s, want %s", recipient.BalanceNHB, want)
	}
	if sender.Nonce != 3 {
		t.Fatalf("sender nonce = %d, want 3", sender.Nonce)
	}
}

// A transfer the node rejects is not counted.
func TestTransferMonthlyFreeTier_ARejectedTransferIsNotCounted(t *testing.T) {
	env := newTierEnv(t, 100, 2)
	env.at(100, tierNovember)
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), env.to...),
		Value:    big.NewInt(99_000_000), // more than the sender holds
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(env.key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := env.sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("an unaffordable transfer must be rejected")
	}
	var wallet [20]byte
	copy(wallet[:], env.addr)
	count, err := nhbstate.NewManager(env.sp.Trie).TransferGasMonthlyTxCount(wallet, tierNovember)
	if err != nil || count != 0 {
		t.Fatalf("a rejected transfer was counted: count=%d err=%v", count, err)
	}
}

// Off means off in the state too: no transfer counter is written
// whether or not the tier is configured, so a node running the new code with the
// setting at its default replays old blocks to the same roots.
func TestTransferMonthlyFreeTier_OffWritesNothingToState(t *testing.T) {
	// With the tier configured but off (height zero, count zero or a height the
	// block has not reached), no counter key may exist for the sender.
	for name, c := range map[string]struct{ gate, perMonth uint64 }{
		"height zero":     {0, 100},
		"height ahead":    {5_000, 100},
		"count zero":      {100, 0},
		"both zero":       {0, 0},
		"configured, off": {0, 7},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTierEnv(t, c.gate, c.perMonth)
			env.at(100, tierNovember)
			env.sendNHB(1_000)
			env.sendNHB(1_000)
			env.sendZNHB(1_000)
			var wallet [20]byte
			copy(wallet[:], env.addr)
			count, err := nhbstate.NewManager(env.sp.Trie).TransferGasMonthlyTxCount(wallet, tierNovember)
			if err != nil || count != 0 {
				t.Fatalf("a counter was written while the tier is off: count=%d err=%v", count, err)
			}
		})
	}
}

func TestTransferMonthlyFreeTier_TheSettingSurvivesCopy(t *testing.T) {
	env := newTierEnv(t, 100, 2)
	copied, err := env.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.transferMonthlyFreeTierFromHeight != 100 || copied.transferMonthlyFreeTierTxPerMonth != 2 {
		t.Fatalf("Copy lost the tier: height=%d count=%d", copied.transferMonthlyFreeTierFromHeight, copied.transferMonthlyFreeTierTxPerMonth)
	}
}

func TestNodeTransferMonthlyFreeTier_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetTransferMonthlyFreeTier(7, 9)
	node.stateMu.Lock()
	gotH, gotN := node.state.transferMonthlyFreeTierFromHeight, node.state.transferMonthlyFreeTierTxPerMonth
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH, rebuiltN := node.state.transferMonthlyFreeTierFromHeight, node.state.transferMonthlyFreeTierTxPerMonth
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 7 || gotN != 9 {
		t.Fatalf("setter left processor at %d/%d, want 7/9", gotH, gotN)
	}
	if rebuiltH != 7 || rebuiltN != 9 {
		t.Fatalf("a rebuilt processor lost the tier: %d/%d, want 7/9", rebuiltH, rebuiltN)
	}
}
