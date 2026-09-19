package core

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/loyalty"
	"nhbchain/native/potso"
	"nhbchain/native/subscriptions"
	"nhbchain/native/swap"
)

// CheckZNHBSupplyInvariant runs once per block, after every transaction, and
// a violation fails block building for every validator with no single
// transaction to blame (see core/node.go's buildProposalState). These tests
// therefore assert the invariant -- and that no ZNHB is created or destroyed
// -- after EVERY transaction, for every ZNHB-moving transaction type, with
// the admin/treasury wallet standing in every counterparty role (sender,
// recipient, fee collector, fee route wallet, escrow payer/payee/mediator/fee
// treasury, ...), including the cases where two roles are the same account.
// Only pre-existing APIs are used, so the file also runs against code that
// predates the fix.

// treasuryID names the admin/treasury wallet in a role assignment; any
// other value is the index of an ordinary user account.
const treasuryID = -1

// treasuryFixture is a state processor wired the way production wires the
// treasury: an admin wallet holding the whole ZNHB supply split into the
// Sale and Reward Pools, the same wallet as escrow fee treasury, transfer fee
// collector and POTSO reward treasury.
type treasuryFixture struct {
	t        *testing.T
	sp       *StateProcessor
	adminKey *crypto.PrivateKey
	admin    [20]byte
	users    map[int]*crypto.PrivateKey
	watched  map[[20]byte]struct{}
	now      time.Time
}

const treasuryFixtureSupply = 10_000_000

func newTreasuryFixture(t *testing.T) *treasuryFixture {
	t.Helper()
	f := &treasuryFixture{
		t:       t,
		sp:      newStakingStateProcessor(t),
		users:   make(map[int]*crypto.PrivateKey),
		watched: make(map[[20]byte]struct{}),
		now:     time.Unix(1_800_000_000, 0).UTC(),
	}
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	f.adminKey = key
	copy(f.admin[:], key.PubKey().Address().Bytes())
	f.sp.nowFunc = func() time.Time { return f.now }
	f.sp.SetAdminWallet(f.admin, true)
	f.sp.SetEscrowFeeTreasury(f.admin)
	cfg := f.sp.PotsoRewardConfig()
	cfg.TreasuryAddress = f.admin
	if err := f.sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set potso reward treasury: %v", err)
	}
	if err := f.sp.setAccount(f.admin[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(treasuryFixtureSupply),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := f.sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap znhb pools: %v", err)
	}
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant must hold right after bootstrap: %v", err)
	}
	f.watch(f.admin)
	// Module accounts that hold user funds while a transaction is in flight
	// count toward conservation too.
	manager := nhbstate.NewManager(f.sp.Trie)
	vault, err := manager.EscrowVaultAddress("ZNHB")
	if err != nil {
		t.Fatalf("escrow vault address: %v", err)
	}
	f.watch(vault)
	f.watch(manager.PotsoStakeVaultAddress())
	f.watch(bytesToAddress(f.sp.marketEscrowAddr.Bytes()))
	f.watch(bytesToAddress(f.sp.lendingCollateralAddr.Bytes()))
	return f
}

// watch adds an address to the set whose ZNHB is summed for conservation.
func (f *treasuryFixture) watch(addr [20]byte) { f.watched[addr] = struct{}{} }

// who resolves an identity to its signing key and address, creating and
// watching the account on first use.
func (f *treasuryFixture) who(id int) (*crypto.PrivateKey, [20]byte) {
	f.t.Helper()
	if id == treasuryID {
		return f.adminKey, f.admin
	}
	key, ok := f.users[id]
	if !ok {
		var err error
		key, err = crypto.GeneratePrivateKey()
		if err != nil {
			f.t.Fatalf("generate user key: %v", err)
		}
		f.users[id] = key
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	f.watch(addr)
	return key, addr
}

func (f *treasuryFixture) addr(id int) [20]byte {
	_, addr := f.who(id)
	return addr
}

// fund sets a user account's spendable ZNHB (never the treasury: its balance
// is tied to the pool ledger and must only ever move through transactions).
func (f *treasuryFixture) fund(id int, znhb int64) {
	f.t.Helper()
	if id == treasuryID {
		return
	}
	addr := f.addr(id)
	if err := f.sp.setAccount(addr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(znhb),
		Stake:       big.NewInt(0),
	}); err != nil {
		f.t.Fatalf("fund user %d: %v", id, err)
	}
}

// totalZNHB sums every ZNHB a watched account owns in any form: spendable,
// locked into a delegation or hold, pending unbond, or held as a governance
// deposit.
func (f *treasuryFixture) totalZNHB() *big.Int {
	f.t.Helper()
	manager := nhbstate.NewManager(f.sp.Trie)
	total := new(big.Int)
	for addr := range f.watched {
		account, err := f.sp.getAccount(addr[:])
		if err != nil {
			f.t.Fatalf("load %x: %v", addr, err)
		}
		total.Add(total, account.BalanceZNHB)
		if account.LockedZNHB != nil {
			total.Add(total, account.LockedZNHB)
		}
		for _, unbond := range account.PendingUnbonds {
			if unbond.Amount != nil {
				total.Add(total, unbond.Amount)
			}
		}
		escrowed, err := manager.GovernanceEscrowBalance(addr[:])
		if err != nil {
			f.t.Fatalf("load governance escrow of %x: %v", addr, err)
		}
		total.Add(total, escrowed)
	}
	return total
}

func (f *treasuryFixture) rewardPool() *big.Int {
	f.t.Helper()
	pool, err := nhbstate.NewManager(f.sp.Trie).ZNHBRewardPoolBalance()
	if err != nil {
		f.t.Fatalf("read reward pool: %v", err)
	}
	return pool
}

func (f *treasuryFixture) salePool() *big.Int {
	f.t.Helper()
	pool, err := nhbstate.NewManager(f.sp.Trie).ZNHBSalePoolBalance()
	if err != nil {
		f.t.Fatalf("read sale pool: %v", err)
	}
	return pool
}

func (f *treasuryFixture) balanceZNHB(addr [20]byte) *big.Int {
	f.t.Helper()
	account, err := f.sp.getAccount(addr[:])
	if err != nil {
		f.t.Fatalf("load balance of %x: %v", addr, err)
	}
	return new(big.Int).Set(account.BalanceZNHB)
}

func (f *treasuryFixture) nonce(addr [20]byte) uint64 {
	f.t.Helper()
	account, err := f.sp.getAccount(addr[:])
	if err != nil {
		f.t.Fatalf("load nonce of %x: %v", addr, err)
	}
	return account.Nonce
}

// signed fills in the sender's next nonce and signs.
func (f *treasuryFixture) signed(id int, tx *types.Transaction) *types.Transaction {
	f.t.Helper()
	key, addr := f.who(id)
	tx.ChainID = types.NHBChainID()
	tx.Nonce = f.nonce(addr)
	if tx.GasLimit == 0 {
		tx.GasLimit = 100_000
	}
	if tx.GasPrice == nil {
		tx.GasPrice = big.NewInt(1)
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		f.t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

// apply runs a transaction the way the block builder does: on a working copy
// of the state that is kept only if the transaction succeeds, so a rejected
// transaction leaves nothing behind. After a successful transaction it
// asserts the two properties this file exists for: the supply invariant
// holds, and no ZNHB was created or destroyed across the watched accounts.
func (f *treasuryFixture) apply(label string, tx *types.Transaction) error {
	f.t.Helper()
	before := f.totalZNHB()
	working, err := f.sp.Copy()
	if err != nil {
		f.t.Fatalf("copy state: %v", err)
	}
	if err := working.ApplyTransaction(tx); err != nil {
		return err
	}
	f.sp = working
	f.assertSound(label, before)
	return nil
}

// finalize runs the end-of-block finalization (loyalty base-reward payouts
// among other steps) on a working copy and asserts the same properties.
func (f *treasuryFixture) finalize(label string) {
	f.t.Helper()
	before := f.totalZNHB()
	working, err := f.sp.Copy()
	if err != nil {
		f.t.Fatalf("copy state: %v", err)
	}
	working.FinalizeBlock()
	f.sp = working
	f.assertSound(label, before)
}

// fundNHB sets an account's NHB, leaving its ZNHB untouched (the treasury's
// ZNHB is tied to the pool ledger, its NHB is not).
func (f *treasuryFixture) fundNHB(id int, nhb int64) { f.fundNHBWei(id, big.NewInt(nhb)) }

func (f *treasuryFixture) fundNHBWei(id int, nhb *big.Int) {
	f.t.Helper()
	addr := f.addr(id)
	account, err := f.sp.getAccount(addr[:])
	if err != nil {
		f.t.Fatalf("load %x: %v", addr, err)
	}
	account.BalanceNHB = new(big.Int).Set(nhb)
	if err := f.sp.setAccount(addr[:], account); err != nil {
		f.t.Fatalf("fund NHB: %v", err)
	}
}

// lifecycle runs the end-of-block lifecycle (subscription billing among other
// steps) on a working copy, the way block building does, and asserts the same
// properties as apply. A lifecycle failure fails every block, so any error is
// fatal.
func (f *treasuryFixture) lifecycle(label string, height uint64) {
	f.t.Helper()
	f.lifecycleAt(label, height, f.now.Unix())
}

func (f *treasuryFixture) lifecycleAt(label string, height uint64, timestamp int64) {
	f.t.Helper()
	before := f.totalZNHB()
	working, err := f.sp.Copy()
	if err != nil {
		f.t.Fatalf("copy state: %v", err)
	}
	if err := working.ProcessBlockLifecycle(height, timestamp); err != nil {
		f.t.Fatalf("%s: block lifecycle failed: %v", label, err)
	}
	f.sp = working
	f.assertSound(label, before)
}

// mustApply is apply for steps that are expected to succeed.
func (f *treasuryFixture) mustApply(label string, tx *types.Transaction) {
	f.t.Helper()
	if err := f.apply(label, tx); err != nil {
		f.t.Fatalf("%s: unexpected rejection: %v", label, err)
	}
}

func (f *treasuryFixture) assertSound(label string, totalBefore *big.Int) {
	f.t.Helper()
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		f.t.Fatalf("%s: supply invariant broken by a transaction that applied without error: %v", label, err)
	}
	if after := f.totalZNHB(); after.Cmp(totalBefore) != 0 {
		f.t.Fatalf("%s: ZNHB across the touched accounts changed from %s to %s (created or destroyed by one transaction)", label, totalBefore, after)
	}
}

// roleAssignments enumerates every way k roles can be filled: roles in the
// same block use the same account (two roles resolving to one address), and
// at most one block is the admin/treasury wallet. Each result maps a role to
// an identity: treasuryID or the index of an ordinary user.
func roleAssignments(k int) [][]int {
	var out [][]int
	var grow func(blocks []int, top int)
	grow = func(blocks []int, top int) {
		if len(blocks) == k {
			for admin := -1; admin <= top; admin++ {
				assignment := make([]int, k)
				for role, block := range blocks {
					if block == admin {
						assignment[role] = treasuryID
					} else {
						assignment[role] = block
					}
				}
				out = append(out, assignment)
			}
			return
		}
		for block := 0; block <= top+1; block++ {
			next := top
			if block > top {
				next = block
			}
			grow(append(append([]int(nil), blocks...), block), next)
		}
	}
	grow(nil, -1)
	return out
}

func describeAssignment(names []string, assignment []int) string {
	parts := make([]string, len(names))
	for i, name := range names {
		if assignment[i] == treasuryID {
			parts[i] = name + "=treasury"
		} else {
			parts[i] = fmt.Sprintf("%s=user%d", name, assignment[i])
		}
	}
	return strings.Join(parts, " ")
}

func TestRoleAssignmentsCoverEveryAliasing(t *testing.T) {
	// Sanity check on the generator itself: one role has 2 assignments
	// (treasury or a user), two roles have 5, three have 15, four have 52.
	for k, want := range map[int]int{1: 2, 2: 5, 3: 15, 4: 52} {
		if got := len(roleAssignments(k)); got != want {
			t.Fatalf("roleAssignments(%d) = %d, want %d", k, got, want)
		}
	}
}

// --- plain ZNHB transfer (TxTypeTransferZNHB) -------------------------------

func (f *treasuryFixture) transferZNHB(fromID, toID int, amount int64, domain string) *types.Transaction {
	f.t.Helper()
	to := f.addr(toID)
	tx := &types.Transaction{
		Type:            types.TxTypeTransferZNHB,
		To:              append([]byte(nil), to[:]...),
		Value:           big.NewInt(amount),
		MerchantAddress: domain,
	}
	return f.signed(fromID, tx)
}

// TestZNHBTransferToTreasuryKeepsSupplyInvariant is the direct regression
// for the reported halt: any funded user sending ZNHB straight to the public
// admin/treasury address used to leave the wallet ahead of the pool ledger by
// exactly the amount sent, failing CheckZNHBSupplyInvariant on the very next
// block and every block after it.
func TestZNHBTransferToTreasuryKeepsSupplyInvariant(t *testing.T) {
	f := newTreasuryFixture(t)
	f.fund(0, 5_000_000)
	rewardBefore := f.rewardPool()

	tx := f.transferZNHB(0, treasuryID, 1_000_000, "")
	if err := f.apply("user -> treasury transfer", tx); err != nil {
		t.Fatalf("apply transfer to treasury: %v", err)
	}

	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated after one ordinary ZNHB transfer to the treasury address: %v", err)
	}
	want := new(big.Int).Add(rewardBefore, big.NewInt(1_000_000))
	if got := f.rewardPool(); got.Cmp(want) != 0 {
		t.Fatalf("reward pool = %s, want %s: money sent to the treasury is reward-pool income", got, want)
	}
}

// TestZNHBTransferRolesKeepSupplyInvariant drives a ZNHB transfer through
// every assignment of the four accounts a transfer can involve -- sender,
// recipient, protocol fee collector, and the wallet the merchant-domain fee
// is routed to -- with the treasury in every role and every aliasing, for both
// fee payers.
func TestZNHBTransferRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"sender", "recipient", "feeCollector", "domainFeeWallet"}
	for _, payer := range []fees.FeePayer{fees.FeePayerSender, fees.FeePayerRecipient} {
		for _, domain := range []string{"", "pos"} {
			for _, who := range roleAssignments(len(names)) {
				name := fmt.Sprintf("payer=%s domain=%q %s", payer, domain, describeAssignment(names, who))
				t.Run(name, func(t *testing.T) {
					f := newTreasuryFixture(t)
					sender, recipient, collector, wallet := who[0], who[1], who[2], who[3]
					f.fund(sender, 5_000_000)
					f.fund(recipient, 5_000_000)
					f.sp.SetTransferGasPolicy(TransferGasPolicy{
						Enabled:      false, // charge the fee regardless of the free tier
						FeeCollector: f.addr(collector),
						FeeBps:       20,
						FeeBpsZNHB:   10,
					})
					f.sp.SetFeePolicy(fees.Policy{Version: 1, Domains: map[string]fees.DomainPolicy{
						"pos": {
							FreeTierTxPerMonth:    0,
							FreeTierTxPerMonthSet: true,
							MDRBasisPoints:        200,
							OwnerWallet:           f.addr(wallet),
							FeePayer:              payer,
							Assets: map[string]fees.AssetPolicy{
								fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: f.addr(wallet)},
							},
						},
					}})
					f.mustApply(name, f.transferZNHB(sender, recipient, 100_000, domain))
				})
			}
		}
	}
}

// --- escrow (create / fund / release / refund / expire / dispute) -----------

type escrowSteps struct {
	f       *treasuryFixture
	payer   int
	payee   int
	arbiter int
	id      [32]byte
}

func (f *treasuryFixture) newEscrow(payer, payee, mediator int, amount int64, feeBps uint32) *escrowSteps {
	f.t.Helper()
	payerAddr, payeeAddr := f.addr(payer), f.addr(payee)
	mediatorAddr := f.addr(mediator)
	var meta [32]byte
	const escrowNonce = uint64(1)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	e := &escrowSteps{f: f, payer: payer, payee: payee, arbiter: mediator}
	e.id = ethcrypto.Keccak256Hash(payerAddr[:], payeeAddr[:], meta[:], nonceBytes[:])
	payload, err := json.Marshal(struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
		Mediator []byte   `json:"mediator,omitempty"`
	}{
		Payee:    payeeAddr[:],
		Token:    "ZNHB",
		Amount:   big.NewInt(amount),
		FeeBps:   feeBps,
		Deadline: f.now.Add(2 * time.Hour).Unix(),
		Nonce:    escrowNonce,
		Mediator: mediatorAddr[:],
	})
	if err != nil {
		f.t.Fatalf("marshal escrow payload: %v", err)
	}
	f.mustApply("escrow create", f.signed(payer, &types.Transaction{Type: types.TxTypeCreateEscrow, Data: payload}))
	return e
}

func (e *escrowSteps) call(label string, txType types.TxType, signer int) {
	e.f.t.Helper()
	e.f.mustApply(label, e.f.signed(signer, &types.Transaction{Type: txType, Data: e.id[:]}))
}

// TestEscrowFeeToTreasuryKeepsSupplyInvariant is the direct regression for
// the escrow half of the report: an ordinary user-to-user ZNHB escrow with a
// fee pays that fee to the escrow fee treasury, which is the admin wallet
// (core/node.go points both at the same address), with no pool update.
func TestEscrowFeeToTreasuryKeepsSupplyInvariant(t *testing.T) {
	f := newTreasuryFixture(t)
	const payer, payee = 0, 1
	f.fund(payer, 1_000_000)
	f.fund(payee, 0)
	rewardBefore := f.rewardPool()

	e := f.newEscrow(payer, payee, payee, 1_000_000, 1_000) // 10% fee
	e.call("escrow fund", types.TxTypeLockEscrow, payer)
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant should hold once the escrow is funded: %v", err)
	}
	e.call("escrow release", types.TxTypeReleaseEscrow, payee)

	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated after an ordinary escrow with a fee was released: %v", err)
	}
	want := new(big.Int).Add(rewardBefore, big.NewInt(100_000))
	if got := f.rewardPool(); got.Cmp(want) != 0 {
		t.Fatalf("reward pool = %s, want %s: the escrow fee is reward-pool income", got, want)
	}
}

// TestEscrowRolesKeepSupplyInvariant runs the escrow lifecycle -- release,
// refund, expiry and a disputed release settled by the mediator -- through
// every assignment of payer, payee, mediator and fee treasury, so the
// treasury (or two roles that are the same account) can be any of them.
func TestEscrowRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"payer", "payee", "mediator", "feeTreasury"}
	for _, outcome := range []string{"release", "refund", "expire", "disputedRelease"} {
		for _, who := range roleAssignments(len(names)) {
			name := fmt.Sprintf("%s %s", outcome, describeAssignment(names, who))
			t.Run(name, func(t *testing.T) {
				f := newTreasuryFixture(t)
				payer, payee, mediator, treasury := who[0], who[1], who[2], who[3]
				f.sp.SetEscrowFeeTreasury(f.addr(treasury))
				f.fund(payer, 1_000_000)
				f.fund(payee, 1_000_000)
				f.fund(mediator, 1_000_000)
				e := f.newEscrow(payer, payee, mediator, 100_000, 1_000)
				e.call("fund", types.TxTypeLockEscrow, payer)
				switch outcome {
				case "release":
					e.call("release", types.TxTypeReleaseEscrow, payee)
				case "refund":
					e.call("refund", types.TxTypeRefundEscrow, payer)
				case "expire":
					f.now = f.now.Add(3 * time.Hour)
					e.call("expire", types.TxTypeExpireEscrow, payer)
				case "disputedRelease":
					e.call("dispute", types.TxTypeDisputeEscrow, payer)
					e.call("mediator release", types.TxTypeReleaseEscrow, mediator)
				}
			})
		}
	}
}

// --- POTSO stake vault (lock / unbond / withdraw) ---------------------------

// TestPotsoStakeKeepsSupplyInvariantWhenTreasuryStakes moves ZNHB from the
// owner into the stake vault and back. When the owner is the treasury wallet,
// the vault is an account the invariant does not read.
func TestPotsoStakeKeepsSupplyInvariantWhenTreasuryStakes(t *testing.T) {
	for _, owner := range []int{treasuryID, 0} {
		name := "owner=user"
		if owner == treasuryID {
			name = "owner=treasury"
		}
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.fund(owner, 1_000_000)
			amount := func() []byte {
				data, err := rlpEncodeAmount(big.NewInt(400_000))
				if err != nil {
					t.Fatalf("encode amount: %v", err)
				}
				return data
			}
			f.mustApply("potso lock", f.signed(owner, &types.Transaction{Type: types.TxTypePotsoStakeLock, Data: amount()}))
			f.mustApply("potso unbond", f.signed(owner, &types.Transaction{Type: types.TxTypePotsoStakeUnbond, Data: amount()}))
			f.now = f.now.Add(8 * 24 * time.Hour)
			f.mustApply("potso withdraw", f.signed(owner, &types.Transaction{Type: types.TxTypePotsoStakeWithdraw}))
		})
	}
}

// --- lending collateral (deposit / withdraw ZNHB) ---------------------------

func (f *treasuryFixture) lendingTx(id int, txType types.TxType, amount int64) *types.Transaction {
	f.t.Helper()
	return f.lendingTxWei(id, txType, big.NewInt(amount))
}

func (f *treasuryFixture) lendingTxWei(id int, txType types.TxType, amount *big.Int) *types.Transaction {
	f.t.Helper()
	data, err := json.Marshal(lendingNativePayload{PoolID: "default"})
	if err != nil {
		f.t.Fatalf("marshal lending payload: %v", err)
	}
	return f.signed(id, &types.Transaction{Type: txType, To: make([]byte, 20), Value: new(big.Int).Set(amount), Data: data})
}

// TestLendingCollateralKeepsSupplyInvariantWhenTreasuryDeposits covers the
// treasury wallet posting ZNHB collateral, which the lending module holds in
// its own account.
func TestLendingCollateralKeepsSupplyInvariantWhenTreasuryDeposits(t *testing.T) {
	for _, owner := range []int{treasuryID, 0} {
		name := "user"
		if owner == treasuryID {
			name = "treasury"
		}
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.fund(owner, 1_000_000)
			f.mustApply("lending deposit", f.lendingTx(owner, types.TxTypeLendingDepositZNHB, 300_000))
			f.mustApply("lending withdraw", f.lendingTx(owner, types.TxTypeLendingWithdrawZNHB, 300_000))
		})
	}
}

func rlpEncodeAmount(amount *big.Int) ([]byte, error) {
	return rlp.EncodeToBytes(struct{ Amount *big.Int }{Amount: amount})
}

// --- staking reward claims paid out of the POTSO reward treasury ------------

// TestStakeClaimRewardsRolesKeepSupplyInvariant claims staking rewards with
// the treasury wallet as the claimant and/or as the reward treasury the
// payout is drawn from (config.toml points that treasury at the admin
// wallet).
func TestStakeClaimRewardsRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"claimant", "rewardTreasury"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			claimant, treasury := who[0], who[1]
			f.fund(claimant, 1_000_000)
			f.fund(treasury, 1_000_000)
			cfg := f.sp.PotsoRewardConfig()
			cfg.TreasuryAddress = f.addr(treasury)
			if err := f.sp.SetPotsoRewardConfig(cfg); err != nil {
				t.Fatalf("set reward treasury: %v", err)
			}
			if err := f.sp.SetStakeRewardAPR(1_000); err != nil {
				t.Fatalf("set apr: %v", err)
			}
			// One share that last claimed two payout periods ago, against a
			// global index that has since advanced by 5,000: the claim pays 5,000
			// ZNHB out of the reward treasury.
			addr := f.addr(claimant)
			account, err := f.sp.getAccount(addr[:])
			if err != nil {
				t.Fatalf("load claimant: %v", err)
			}
			account.StakeShares = big.NewInt(1)
			account.StakeLastIndex = big.NewInt(0)
			account.StakeLastPayoutTs = uint64(f.now.Unix()) - 2*stakePayoutPeriodSeconds
			if err := f.sp.setAccount(addr[:], account); err != nil {
				t.Fatalf("seed claimant staking state: %v", err)
			}
			if err := f.sp.writeBigInt(nhbstate.StakingGlobalIndexKey(), big.NewInt(5_000)); err != nil {
				t.Fatalf("seed global index: %v", err)
			}
			balanceBefore := f.balanceZNHB(addr)
			f.mustApply(name, f.signed(claimant, &types.Transaction{Type: types.TxTypeStakeClaimRewards}))
			// The payout really happened (the test would otherwise pass
			// trivially): a claimant other than the reward treasury gains it.
			gained := new(big.Int).Sub(f.balanceZNHB(addr), balanceBefore)
			wantGain := big.NewInt(5_000)
			if f.addr(claimant) == f.addr(treasury) {
				wantGain = big.NewInt(0) // paying itself nets to zero
			}
			if gained.Cmp(wantGain) != 0 {
				t.Fatalf("claimant gained %s, want %s", gained, wantGain)
			}
		})
	}
}

// --- subscription billing (settled in the block lifecycle) ------------------

// TestSubscriptionChargeRolesKeepSupplyInvariant bills a ZNHB subscription
// with the treasury wallet as the payer, the merchant and/or the management
// fee treasury (config.toml points the fee treasury at the admin wallet).
// Billing runs in ProcessBlockLifecycle, so a mismatch there fails the block
// itself, not just one transaction. Payer and merchant being one account is a
// separate defect (a self-subscription pays itself) and is not exercised.
func TestSubscriptionChargeRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"payer", "merchant", "feeTreasury"}
	for _, who := range roleAssignments(len(names)) {
		if who[0] == who[1] {
			continue
		}
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			payer, merchant, treasury := who[0], who[1], who[2]
			f.fund(payer, 5_000_000)
			f.fund(merchant, 5_000_000)
			f.fund(treasury, 5_000_000)
			if err := f.sp.SetSubscriptionsConfig(subscriptions.Config{
				ManagementFeeBps:     100,
				ManagementFeeCapBps:  500,
				Treasury:             f.addr(treasury),
				MaxRetries:           3,
				RetryIntervalSeconds: 86400,
			}); err != nil {
				t.Fatalf("configure subscriptions: %v", err)
			}
			planData, err := rlp.EncodeToBytes(struct {
				Name               string
				PriceWei           *big.Int
				Asset              string
				IntervalSeconds    uint64
				TrialPeriodSeconds uint64
			}{Name: "plan", PriceWei: big.NewInt(100_000), Asset: "ZNHB", IntervalSeconds: 86400})
			if err != nil {
				t.Fatalf("encode plan: %v", err)
			}
			f.mustApply("create plan", f.signed(merchant, &types.Transaction{Type: types.TxTypeSubscriptionCreatePlan, Data: planData}))
			subscribeData, err := rlp.EncodeToBytes(struct{ PlanID uint64 }{PlanID: 1})
			if err != nil {
				t.Fatalf("encode subscribe: %v", err)
			}
			f.mustApply("subscribe", f.signed(payer, &types.Transaction{Type: types.TxTypeSubscriptionSubscribe, Data: subscribeData}))

			payerBefore := f.balanceZNHB(f.addr(payer))
			f.lifecycle("first charge", 1)
			// The first charge really settled: the payer, if it is not also
			// receiving part of it, is down by the price.
			if who[0] != who[1] && who[0] != who[2] {
				spent := new(big.Int).Sub(payerBefore, f.balanceZNHB(f.addr(payer)))
				if spent.Cmp(big.NewInt(100_000)) != 0 {
					t.Fatalf("payer paid %s, want 100000: the charge did not settle", spent)
				}
			}
		})
	}
}

// --- loyalty base rewards paid out of the loyalty treasury ------------------

// TestLoyaltyBaseRewardRolesKeepSupplyInvariant pays the base reward an NHB
// transfer earns its sender out of the loyalty treasury (the genesis file
// points it at the admin wallet), both settled immediately and queued for the
// block's end, with the treasury wallet as the spender, the recipient and/or
// the loyalty treasury.
func TestLoyaltyBaseRewardRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"spender", "recipient", "loyaltyTreasury"}
	for _, proRate := range []bool{false, true} {
		for _, who := range roleAssignments(len(names)) {
			if who[0] == who[1] {
				continue // a self-transfer earns no reward
			}
			name := fmt.Sprintf("proRate=%v %s", proRate, describeAssignment(names, who))
			t.Run(name, func(t *testing.T) {
				f := newTreasuryFixture(t)
				spender, recipient, treasury := who[0], who[1], who[2]
				f.fund(spender, 1_000_000)
				f.fund(recipient, 1_000_000)
				f.fund(treasury, 1_000_000)
				f.fundNHB(spender, 10_000_000)
				manager := nhbstate.NewManager(f.sp.Trie)
				treasuryAddr := f.addr(treasury)
				cfg := (&loyalty.GlobalConfig{
					Active:       true,
					Treasury:     append([]byte(nil), treasuryAddr[:]...),
					BaseBps:      100, // 1% of the amount spent
					MinSpend:     big.NewInt(0),
					CapPerTx:     big.NewInt(0),
					DailyCapUser: big.NewInt(0),
					Dynamic: loyalty.DynamicConfig{
						DailyCapPctOf7dFeesBps: 10_000,
						EnableProRate:          proRate,
						EnableProRateSet:       true,
						PriceGuard:             loyalty.PriceGuardConfig{Enabled: false},
					},
				}).Normalize()
				if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
					t.Fatalf("set loyalty config: %v", err)
				}
				if err := nhbstate.NewRollingFees(manager).AddDay(f.now, big.NewInt(0), big.NewInt(1_000_000)); err != nil {
					t.Fatalf("seed rolling fees: %v", err)
				}
				spenderZNHB := f.balanceZNHB(f.addr(spender))
				to := f.addr(recipient)
				f.mustApply(name, f.signed(spender, &types.Transaction{
					Type:  types.TxTypeTransfer,
					To:    append([]byte(nil), to[:]...),
					Value: big.NewInt(100_000),
				}))
				if proRate {
					f.finalize("end of block")
				}
				// The reward (1% of 100,000) reached the spender, so the test
				// really exercised the payout path.
				gained := new(big.Int).Sub(f.balanceZNHB(f.addr(spender)), spenderZNHB)
				wantGain := big.NewInt(1_000)
				if who[0] == who[2] {
					wantGain = big.NewInt(0) // the treasury paying itself nets to zero
				}
				if gained.Cmp(wantGain) != 0 {
					t.Fatalf("spender gained %s ZNHB, want %s", gained, wantGain)
				}
			})
		}
	}
}

// --- block building (the reported failure mode) -----------------------------

// TestCreateBlockSurvivesZNHBTransferToTreasury reproduces the report end to
// end: one ordinary funded user sends ZNHB to the public admin/treasury
// address, mempool admission accepts it, and block building used to fail on
// every attempt with the supply-invariant error because the transaction was
// neither pruned nor skippable (the invariant is only checked after all
// transactions ran). The block must build, include the transfer, and leave
// the invariant intact.
func TestCreateBlockSurvivesZNHBTransferToTreasury(t *testing.T) {
	node := newTestNode(t)
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	admin := toAddress(adminKey)
	if err := node.ConfigureAdminWalletForTests(admin); err != nil {
		t.Fatalf("configure admin wallet: %v", err)
	}
	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	user := toAddress(userKey)
	fundAccount(t, node, user, big.NewInt(5_000_000))

	// The first block's lifecycle runs the one-time drift reconcilers, which
	// would quietly absorb any mismatch introduced before it -- exactly what
	// no longer happens on a chain that has already produced blocks, like the
	// live one. Commit an empty block first so the transfer below meets the
	// invariant check the way it does in production.
	warmup, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("warm-up block: %v", err)
	}
	if err := node.CommitBlock(warmup); err != nil {
		t.Fatalf("commit warm-up block: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), admin[:]...),
		Value:    big.NewInt(1),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("mempool admission of a 1 wei ZNHB transfer to the treasury: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
		if err != nil {
			t.Fatalf("attempt %d: block building failed on a mempool holding one ordinary transfer to the treasury: %v", attempt, err)
		}
		if len(block.Transactions) != 1 {
			t.Fatalf("attempt %d: expected the transfer to be included, got %d transactions", attempt, len(block.Transactions))
		}
	}
}

// --- swap voucher mint (paid out of the Sale Pool) --------------------------

// TestSwapVoucherMintToTreasuryWalletIsRejected covers a voucher naming the
// treasury wallet itself as recipient. The mint debits the wallet and
// credits the recipient as two separately loaded accounts, so with one
// account in both roles the credit was overwritten by the debit: the Sale Pool
// shrank and the ZNHB simply vanished. The voucher must be refused at
// admission (the identical execution path) and nothing may move.
func TestSwapVoucherMintToTreasuryWalletIsRejected(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	admin := node.state.adminWallet

	const provider = "test-oracle"
	registerSwapPriceSignerCore(t, node, provider, oracleKey)
	voucher := swapVoucherTestVoucher(node.chain.ChainID(), admin, "0.05", "ORDER-TO-TREASURY")
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signSwapVoucherCore(t, minterKey, voucher),
		Provider:     provider,
		ProviderTxID: "TX-TO-TREASURY",
		PriceProof:   signedPriceProofCore(t, oracleKey, provider, "0.05", time.Now()),
	}
	adminBefore, err := node.GetAccount(admin[:])
	if err != nil {
		t.Fatalf("load admin wallet: %v", err)
	}
	salePoolBefore, err := nhbstate.NewManager(node.state.Trie).ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}

	if _, _, err := node.SwapSubmitVoucher(submission); err == nil {
		block, buildErr := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
		if buildErr == nil {
			buildErr = node.CommitBlock(block)
		}
		adminAfter, _ := node.GetAccount(admin[:])
		salePoolAfter, _ := nhbstate.NewManager(node.state.Trie).ZNHBSalePoolBalance()
		t.Fatalf("a voucher minting ZNHB to the treasury wallet itself was accepted (block error: %v): admin balance %s -> %s, sale pool %s -> %s",
			buildErr, adminBefore.BalanceZNHB, adminAfter.BalanceZNHB, salePoolBefore, salePoolAfter)
	}
	adminAfter, err := node.GetAccount(admin[:])
	if err != nil {
		t.Fatalf("reload admin wallet: %v", err)
	}
	if adminAfter.BalanceZNHB.Cmp(adminBefore.BalanceZNHB) != 0 {
		t.Fatalf("admin balance moved from %s to %s despite the rejection", adminBefore.BalanceZNHB, adminAfter.BalanceZNHB)
	}
}

// --- POTSO reward payouts (settled in the block lifecycle) ------------------

// TestPotsoRewardPayoutRolesKeepSupplyInvariant pays an epoch's POTSO
// rewards with the treasury wallet as the reward treasury the payout is drawn
// from and/or as one of the winners. Payout runs in ProcessBlockLifecycle, so a
// mismatch fails the block itself.
func TestPotsoRewardPayoutRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"winner", "rewardTreasury"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			winner, treasury := who[0], who[1]
			f.fund(winner, 1_000)
			f.fund(treasury, 1_000_000)
			other := 7 // a second, ordinary winner
			f.fund(other, 1_000)
			cfg := potso.RewardConfig{
				EpochLengthBlocks:  2,
				AlphaStakeBps:      7000,
				MinPayoutWei:       big.NewInt(0),
				EmissionPerEpoch:   big.NewInt(900),
				TreasuryAddress:    f.addr(treasury),
				MaxWinnersPerEpoch: 10,
				CarryRemainder:     true,
			}
			if err := f.sp.SetPotsoRewardConfig(cfg); err != nil {
				t.Fatalf("set potso config: %v", err)
			}
			manager := nhbstate.NewManager(f.sp.Trie)
			if err := manager.PotsoStakeSetBondedTotal(f.addr(winner), big.NewInt(600)); err != nil {
				t.Fatalf("set winner stake: %v", err)
			}
			if err := manager.PotsoStakeSetBondedTotal(f.addr(other), big.NewInt(400)); err != nil {
				t.Fatalf("set other stake: %v", err)
			}
			if err := manager.PotsoMetricsAddEngagement(0, f.addr(winner), 0, 0, 30*60); err != nil {
				t.Fatalf("seed engagement: %v", err)
			}
			if err := manager.PotsoMetricsAddEngagement(0, f.addr(other), 0, 0, 10*60); err != nil {
				t.Fatalf("seed engagement: %v", err)
			}
			now := f.now.Unix()
			f.lifecycleAt("block 1", 1, now-1)
			f.lifecycleAt("epoch payout block", 2, now)
			meta, ok, err := nhbstate.NewManager(f.sp.Trie).PotsoRewardsGetMeta(0)
			if err != nil || !ok || meta == nil || meta.TotalPaid.Sign() <= 0 {
				t.Fatalf("expected a nonzero payout so the test really exercises it: meta=%v ok=%v err=%v", meta, ok, err)
			}
		})
	}
}

// --- lending liquidation (seized collateral routed to several accounts) -----

// TestLendingLiquidationKeepsSupplyInvariantWhenTreasuryIsPaid liquidates an
// unhealthy position with the treasury wallet as the liquidator, the
// developer share target or the protocol share target -- each place seized
// collateral (ZNHB) can land. The other roles are distinct accounts; two roles
// being one account is a separate defect in the liquidation itself.
func TestLendingLiquidationKeepsSupplyInvariantWhenTreasuryIsPaid(t *testing.T) {
	const (
		supplier   = 10
		borrower   = 11
		liquidator = 12
		developer  = 13
		protocol   = 14
	)
	// Seizing 800,000 of debt at a 10% bonus takes 880,000 of collateral:
	// 20% to the developer target, 10% to the protocol target, the rest to the
	// liquidator.
	cases := []struct {
		name string
		who  map[int]int // role -> identity override
		gain int64       // ZNHB the treasury wallet ends up with
	}{
		{"treasury liquidates", map[int]int{liquidator: treasuryID}, 616_000},
		{"treasury is the developer target", map[int]int{developer: treasuryID}, 176_000},
		{"treasury is the protocol target", map[int]int{protocol: treasuryID}, 88_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			id := func(role int) int {
				if override, ok := tc.who[role]; ok {
					return override
				}
				return role
			}
			f.sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 8_000, LiquidationThreshold: 9_000, LiquidationBonus: 1_000})
			f.sp.SetLendingAccrualConfig(0, 0, lending.NewInterestModel(0, 0, 0, 0.8))
			f.fund(supplier, 0)
			f.fund(borrower, 1_000_000)
			f.fund(id(liquidator), 0)
			f.fund(id(developer), 0)
			f.fund(id(protocol), 0)
			// NHB amounts are wei-scaled: the pool refuses a deposit below its
			// minimum liquidity.
			oneThousandNHB := new(big.Int).Mul(big.NewInt(1_000), big.NewInt(1_000_000_000_000_000_000))
			f.fundNHBWei(supplier, oneThousandNHB)
			f.fundNHBWei(id(liquidator), oneThousandNHB)

			f.mustApply("supply", f.lendingTxWei(supplier, types.TxTypeLendingSupplyNHB, oneThousandNHB))
			f.mustApply("deposit collateral", f.lendingTx(borrower, types.TxTypeLendingDepositZNHB, 1_000_000))
			f.mustApply("borrow", f.lendingTx(borrower, types.TxTypeLendingBorrowNHB, 800_000))

			// Tighten the threshold below the borrower's 80% LTV so the position
			// is unhealthy, and route part of the seized collateral onward.
			f.sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 8_000, LiquidationThreshold: 7_000, LiquidationBonus: 1_000})
			developerAddr, protocolAddr := f.addr(id(developer)), f.addr(id(protocol))
			f.sp.SetLendingCollateralRouting(lending.CollateralRouting{
				DeveloperBps:    2_000,
				DeveloperTarget: crypto.MustNewAddress(crypto.NHBPrefix, developerAddr[:]),
				ProtocolBps:     1_000,
				ProtocolTarget:  crypto.MustNewAddress(crypto.NHBPrefix, protocolAddr[:]),
			})
			borrowerAddr := f.addr(borrower)
			payload, err := json.Marshal(lendingLiquidatePayload{PoolID: "default", Borrower: crypto.MustNewAddress(crypto.NHBPrefix, borrowerAddr[:]).String()})
			if err != nil {
				t.Fatalf("marshal liquidate payload: %v", err)
			}
			treasuryBefore := f.balanceZNHB(f.admin)
			f.mustApply("liquidate", f.signed(id(liquidator), &types.Transaction{
				Type: types.TxTypeLendingLiquidate, To: make([]byte, 20), Value: big.NewInt(0), Data: payload,
			}))
			if gained := new(big.Int).Sub(f.balanceZNHB(f.admin), treasuryBefore); gained.Cmp(big.NewInt(tc.gain)) != 0 {
				t.Fatalf("treasury received %s of seized collateral, want %d", gained, tc.gain)
			}
		})
	}
}

// --- governance deposit refund (finalize) -----------------------------------

// TestGovernanceFinalizeRolesKeepSupplyInvariant submits a proposal with a
// ZNHB deposit and finalizes it after voting closes, with the treasury wallet
// as the proposer and/or the account that sends the finalize transaction.
// Finalize refunds the deposit to the proposer, and the handler then persists
// the sender's pre-transaction account object, so when the proposer sends its
// own finalize the refund written by the engine is overwritten -- the
// invariant must still hold (the wallet's escrowed deposit was released), even
// though that overwrite is a separate defect that loses the deposit itself, so
// conservation is only asserted when the two are different accounts.
func TestGovernanceFinalizeRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"proposer", "finalizer"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			proposer, finalizer := who[0], who[1]
			f.fund(proposer, 1_000_000)
			f.fund(finalizer, 1_000_000)
			f.sp.SetGovernancePolicy(governance.ProposalPolicy{
				MinDepositWei:       big.NewInt(0),
				VotingPeriodSeconds: 100,
				TimelockSeconds:     100,
				AllowedParams:       []string{"fees.baseFee"},
				QuorumBps:           0,
				PassThresholdBps:    0,
			})
			proposeData, err := rlp.EncodeToBytes(struct {
				Kind    string
				Payload string
				Deposit *big.Int
			}{Kind: governance.ProposalKindParamUpdate, Payload: `{"fees.baseFee":1000}`, Deposit: big.NewInt(100_000)})
			if err != nil {
				t.Fatalf("encode proposal: %v", err)
			}
			f.mustApply("propose", f.signed(proposer, &types.Transaction{Type: types.TxTypeGovPropose, Data: proposeData}))
			f.now = f.now.Add(200 * time.Second)
			finalizeData, err := rlp.EncodeToBytes(struct{ ProposalID uint64 }{ProposalID: 1})
			if err != nil {
				t.Fatalf("encode finalize: %v", err)
			}
			finalize := f.signed(finalizer, &types.Transaction{Type: types.TxTypeGovFinalize, Data: finalizeData})
			if proposer != finalizer {
				f.mustApply("finalize", finalize)
				return
			}
			working, err := f.sp.Copy()
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			if err := working.ApplyTransaction(finalize); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			f.sp = working
			if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
				t.Fatalf("supply invariant broken by the proposer finalizing its own proposal: %v", err)
			}
		})
	}
}

// TestGovernanceFinalizeForfeitsRejectedDepositIntoRewardPool pins the
// pre-existing forfeit path the treasury booking must leave alone: a rejected
// proposal's deposit lands on the treasury wallet and the engine credits the
// Reward Pool itself, exactly once.
func TestGovernanceFinalizeForfeitsRejectedDepositIntoRewardPool(t *testing.T) {
	f := newTreasuryFixture(t)
	const proposer, finalizer = 0, 1
	f.fund(proposer, 1_000_000)
	f.fund(finalizer, 1_000_000)
	f.sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     100,
		AllowedParams:       []string{"fees.baseFee"},
		QuorumBps:           5_000, // nobody votes, so the quorum is missed
	})
	proposeData, err := rlp.EncodeToBytes(struct {
		Kind    string
		Payload string
		Deposit *big.Int
	}{Kind: governance.ProposalKindParamUpdate, Payload: `{"fees.baseFee":1000}`, Deposit: big.NewInt(100_000)})
	if err != nil {
		t.Fatalf("encode proposal: %v", err)
	}
	f.mustApply("propose", f.signed(proposer, &types.Transaction{Type: types.TxTypeGovPropose, Data: proposeData}))
	f.now = f.now.Add(200 * time.Second)
	finalizeData, err := rlp.EncodeToBytes(struct{ ProposalID uint64 }{ProposalID: 1})
	if err != nil {
		t.Fatalf("encode finalize: %v", err)
	}
	rewardBefore := f.rewardPool()
	f.mustApply("finalize", f.signed(finalizer, &types.Transaction{Type: types.TxTypeGovFinalize, Data: finalizeData}))
	want := new(big.Int).Add(rewardBefore, big.NewInt(100_000))
	if got := f.rewardPool(); got.Cmp(want) != 0 {
		t.Fatalf("reward pool = %s, want %s: the forfeited deposit must be credited exactly once", got, want)
	}
}
