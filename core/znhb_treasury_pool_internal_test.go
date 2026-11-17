package core

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
	"nhbchain/native/pos"
	statebank "nhbchain/state/bank"
)

// The tests in this file use the treasury booking helpers directly; the
// transaction-level behavior they support is covered, with pre-existing APIs
// only, in znhb_treasury_pool_test.go.

func TestTreasuryZNHBFlowTrackedTypes(t *testing.T) {
	tracked := []types.TxType{
		types.TxTypeTransfer, types.TxTypeTransferZNHB,
		types.TxTypeLockEscrow, types.TxTypeReleaseEscrow, types.TxTypeRefundEscrow, types.TxTypeExpireEscrow,
		types.TxTypeArbitrateRelease, types.TxTypeArbitrateRefund,
		types.TxTypeDelegatedReleaseEscrow, types.TxTypeDelegatedRefundEscrow,
		types.TxTypeStakeClaimRewards, types.TxTypePOSCapture,
		types.TxTypePotsoStakeLock, types.TxTypePotsoStakeWithdraw,
		types.TxTypeLendingDepositZNHB, types.TxTypeLendingWithdrawZNHB, types.TxTypeLendingLiquidate,
		types.TxTypeGovFinalize, types.TxTypeGovExecute,
	}
	for _, txType := range tracked {
		if !treasuryZNHBFlowTracked(txType) {
			t.Fatalf("transaction type 0x%02X moves ZNHB through code with no pool logic of its own and must be tracked", byte(txType))
		}
	}
	// Types with their own ledger logic, or that cannot change what the
	// invariant sums, are left alone so their behavior stays byte-identical.
	untracked := []types.TxType{
		types.TxTypeBuyZNHB, types.TxTypeSwapVoucherMint,
		types.TxTypeMarketCreateListing, types.TxTypeMarketFillListing, types.TxTypeMarketCancelListing,
		types.TxTypeGovPropose, types.TxTypeGovVote, types.TxTypeStake, types.TxTypeUnstake, types.TxTypeStakeClaim,
		types.TxTypeHeartbeat, types.TxTypeMint, types.TxTypeRedeemNHB, types.TxTypeAttestRedemption,
		types.TxTypeBuybackRefPrice, types.TxTypeLendingRefPrice,
	}
	for _, txType := range untracked {
		if treasuryZNHBFlowTracked(txType) {
			t.Fatalf("transaction type 0x%02X should not be tracked", byte(txType))
		}
	}
}

// TestTreasuryOutflowBeyondRewardPoolIsRejected: the Reward Pool is the only
// bucket that absorbs non-sale movement, so a transaction that would move more
// out of the treasury wallet than it holds cannot be mirrored and is rejected
// with a sentinel the block builder skips -- it must never abort a block.
func TestTreasuryOutflowBeyondRewardPoolIsRejected(t *testing.T) {
	const beyondRewardPool = 2_500_000 // the pool holds 2,000,000
	t.Run("escrow funded by the treasury", func(t *testing.T) {
		f := newTreasuryFixture(t)
		f.fund(0, 1_000_000)
		e := f.newEscrow(treasuryID, 0, 0, beyondRewardPool, 0)
		err := f.apply("fund", f.signed(treasuryID, &types.Transaction{Type: types.TxTypeLockEscrow, Data: e.id[:]}))
		assertTreasuryPoolRejection(t, f, err)
	})
	t.Run("transfer sent by the treasury", func(t *testing.T) {
		f := newTreasuryFixture(t)
		f.fund(0, 0)
		err := f.apply("transfer", f.transferZNHB(treasuryID, 0, beyondRewardPool, ""))
		assertTreasuryPoolRejection(t, f, err)
	})
	t.Run("transfer fee paid to a collector that is not the treasury", func(t *testing.T) {
		f := newTreasuryFixture(t)
		f.fund(0, 0)
		f.fund(1, 0)
		// The treasury sends everything the Reward Pool holds; the fee on top
		// leaves the wallet for a different collector, which the pool cannot
		// cover.
		f.sp.SetTransferGasPolicy(TransferGasPolicy{Enabled: false, FeeCollector: f.addr(1), FeeBps: 20, FeeBpsZNHB: 10})
		err := f.apply("transfer", f.transferZNHB(treasuryID, 0, 2_000_000, ""))
		assertTreasuryPoolRejection(t, f, err)
	})
}

func assertTreasuryPoolRejection(t *testing.T, f *treasuryFixture, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("a transaction moving more ZNHB out of the treasury than the reward pool holds was accepted")
	}
	if !errors.Is(err, ErrTreasuryRewardPoolInsufficient) {
		t.Fatalf("rejection is not ErrTreasuryRewardPoolInsufficient: %v", err)
	}
	if got := classifyProposalError(err); got != proposalDispositionSkip {
		t.Fatalf("classifyProposalError = %v, want skip: the block builder must drop just this transaction", got)
	}
	// apply keeps the working copy only on success, so the fixture state is
	// exactly what the block builder is left with.
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant broken after a rejected transaction: %v", err)
	}
}

// TestBookTreasuryPoolMovementInLifecycleDrawsSalePool: block-lifecycle steps
// have no transaction to reject, so an outflow beyond the Reward Pool takes
// the remainder from the Sale Pool instead of failing the block.
func TestBookTreasuryPoolMovementInLifecycleDrawsSalePool(t *testing.T) {
	f := newTreasuryFixture(t)
	before, err := f.sp.captureTreasuryPoolPosition()
	if err != nil || before == nil {
		t.Fatalf("capture position: %v %v", before, err)
	}
	admin, err := f.sp.getAccount(f.admin[:])
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	admin.BalanceZNHB = new(big.Int).Sub(admin.BalanceZNHB, big.NewInt(2_500_000))
	if err := f.sp.setAccount(f.admin[:], admin); err != nil {
		t.Fatalf("debit admin: %v", err)
	}
	if err := f.sp.bookTreasuryPoolMovement(before, false); !errors.Is(err, ErrTreasuryRewardPoolInsufficient) {
		t.Fatalf("a transaction-level booking must refuse the shortfall, got %v", err)
	}
	if err := f.sp.bookTreasuryPoolMovement(before, true); err != nil {
		t.Fatalf("lifecycle booking must not fail: %v", err)
	}
	if got := f.rewardPool(); got.Sign() != 0 {
		t.Fatalf("reward pool = %s, want fully drawn down", got)
	}
	if got, want := f.salePool(), big.NewInt(8_000_000-500_000); got.Cmp(want) != 0 {
		t.Fatalf("sale pool = %s, want %s", got, want)
	}
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant broken after lifecycle booking: %v", err)
	}
}

// TestTreasuryBookingIsInertBeforePoolBootstrap: before EnsureZNHBPoolsBootstrapped
// runs there is no pool ledger to keep in step (bootstrap splits whatever the
// wallet holds then), so tracked operations must leave state byte-identical.
func TestTreasuryBookingIsInertBeforePoolBootstrap(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var admin [20]byte
	admin[0] = 0xAD
	sp.SetAdminWallet(admin, true)
	if err := sp.setAccount(admin[:], &types.Account{BalanceZNHB: big.NewInt(1_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	credit := func(sp *StateProcessor) error {
		account, err := sp.getAccount(admin[:])
		if err != nil {
			return err
		}
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, big.NewInt(5))
		return sp.setAccount(admin[:], account)
	}
	direct, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	booked, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := credit(direct); err != nil {
		t.Fatalf("direct credit: %v", err)
	}
	if err := booked.withTreasuryPoolBooking(false, func() error { return credit(booked) }); err != nil {
		t.Fatalf("booked credit: %v", err)
	}
	if direct.Trie.Hash() != booked.Trie.Hash() {
		t.Fatalf("booking wrote state before the pools were bootstrapped")
	}
	if position, err := booked.captureTreasuryPoolPosition(); err != nil || position != nil {
		t.Fatalf("capture before bootstrap = %v, %v; want nothing to track", position, err)
	}
}

// TestTreasuryBookingLeavesAlreadyMirroredTransactionsUntouched is the parity
// check for transaction types that have run on the live chain: for a
// transaction whose effect on the treasury wallet the existing code already
// mirrors into the pools (the only kind that could ever have been included in
// a committed block, since the invariant is checked after every block), the
// full execution path must leave exactly the state the handler alone leaves --
// the booking adds nothing.
func TestTreasuryBookingLeavesAlreadyMirroredTransactionsUntouched(t *testing.T) {
	type scenario struct {
		name  string
		build func(f *treasuryFixture) (from int, tx *types.Transaction, handler func(sp *StateProcessor, tx *types.Transaction, sender []byte, account *types.Account) error)
	}
	znhbHandler := func(sp *StateProcessor, tx *types.Transaction, sender []byte, account *types.Account) error {
		_, err := sp.applyTransferZNHB(tx, sender, account)
		return err
	}
	nhbHandler := func(sp *StateProcessor, tx *types.Transaction, sender []byte, account *types.Account) error {
		_, err := sp.applyEvmTransaction(tx)
		return err
	}
	withFee := func(f *treasuryFixture, collector int) {
		f.sp.SetTransferGasPolicy(TransferGasPolicy{Enabled: false, FeeCollector: f.addr(collector), FeeBps: 20, FeeBpsZNHB: 10})
	}
	scenarios := []scenario{
		{"user to user, fee to treasury", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			f.fund(0, 5_000_000)
			f.fund(1, 0)
			withFee(f, treasuryID)
			return 0, f.transferZNHB(0, 1, 100_000, ""), znhbHandler
		}},
		{"treasury to user, treasury collects its own fee", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			f.fund(0, 0)
			withFee(f, treasuryID)
			return treasuryID, f.transferZNHB(treasuryID, 0, 100_000, ""), znhbHandler
		}},
		{"treasury to itself", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			withFee(f, treasuryID)
			return treasuryID, f.transferZNHB(treasuryID, treasuryID, 100_000, ""), znhbHandler
		}},
		{"user to user, unrelated collector", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			f.fund(0, 5_000_000)
			f.fund(1, 0)
			f.fund(2, 0)
			withFee(f, 2)
			return 0, f.transferZNHB(0, 1, 100_000, ""), znhbHandler
		}},
		{"governance finalize refunding a user's deposit", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			return f.proposalToFinalize(0, 1)
		}},
		{"governance finalize refunding the treasury's deposit", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			return f.proposalToFinalize(treasuryID, 1)
		}},
		{"governance finalize forfeiting a user's deposit to the treasury", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			return f.proposalToFinalizeWithQuorum(0, 1, 5_000)
		}},
		{"NHB transfer between users", func(f *treasuryFixture) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
			f.fund(0, 0)
			f.fund(1, 0)
			f.fundNHB(0, 1_000_000)
			to := f.addr(1)
			return 0, f.signed(0, &types.Transaction{Type: types.TxTypeTransfer, To: append([]byte(nil), to[:]...), Value: big.NewInt(1_000)}), nhbHandler
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			from, tx, handler := sc.build(f)
			handlerOnly, err := f.sp.Copy()
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			full, err := f.sp.Copy()
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			sender, account, err := handlerOnly.validateSenderAccount(tx)
			if err != nil {
				t.Fatalf("validate sender (%d): %v", from, err)
			}
			if err := handler(handlerOnly, tx, sender, account); err != nil {
				t.Fatalf("handler alone: %v", err)
			}
			if err := full.ApplyTransaction(tx); err != nil {
				t.Fatalf("full execution: %v", err)
			}
			if handlerOnly.Trie.Hash() != full.Trie.Hash() {
				t.Fatalf("the treasury booking changed the state of a transaction the existing code already mirrors into the pools")
			}
		})
	}
}

// TestBookedSlasherMirrorsForfeitIntoRewardPool: slashing forfeits a
// validator's locked ZNHB into the treasury wallet with no transaction and no
// pool update of its own.
func TestBookedSlasherMirrorsForfeitIntoRewardPool(t *testing.T) {
	for _, offender := range []int{0, treasuryID} {
		name := "offender=user"
		if offender == treasuryID {
			name = "offender=treasury"
		}
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			addr := f.addr(offender)
			account, err := f.sp.getAccount(addr[:])
			if err != nil {
				t.Fatalf("load offender: %v", err)
			}
			account.BalanceZNHB = new(big.Int).Sub(account.BalanceZNHB, big.NewInt(0))
			account.LockedZNHB = big.NewInt(1_000)
			account.Stake = big.NewInt(1_000)
			if offender == treasuryID {
				// Locking is a move within the wallet's own ZNHB.
				account.BalanceZNHB = new(big.Int).Sub(account.BalanceZNHB, big.NewInt(1_000))
			}
			if err := f.sp.setAccount(addr[:], account); err != nil {
				t.Fatalf("seed offender: %v", err)
			}
			before := f.totalZNHB()
			rewardBefore := f.rewardPool()

			slasher := f.sp.bookedSlasher(statebank.NewValidatorSlasher(nhbstate.NewManager(f.sp.Trie), f.admin))
			if err := slasher.Slash(addr, big.NewInt(600)); err != nil {
				t.Fatalf("slash: %v", err)
			}
			f.assertSound("slash", before)
			wantReward := new(big.Int).Set(rewardBefore)
			if offender != treasuryID {
				wantReward.Add(wantReward, big.NewInt(600)) // new ZNHB landed on the treasury
			}
			if got := f.rewardPool(); got.Cmp(wantReward) != 0 {
				t.Fatalf("reward pool = %s, want %s", got, wantReward)
			}
		})
	}
}

// TestPOSCaptureRolesKeepSupplyInvariant releases a POS hold to the merchant
// with the treasury as payer, merchant or both, through the same booking
// executeTransaction applies to TxTypePOSCapture. (The capture transaction's
// authorization-id decoding cannot address a real authorization at the moment,
// so the lifecycle engine is driven directly.)
func TestPOSCaptureRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"payer", "merchant"}
	for _, capture := range []int64{100_000, 60_000} { // full, and partial with a refund
		for _, who := range roleAssignments(len(names)) {
			name := fmt.Sprintf("capture=%d %s", capture, describeAssignment(names, who))
			t.Run(name, func(t *testing.T) {
				f := newTreasuryFixture(t)
				payer, merchant := who[0], who[1]
				f.fund(payer, 1_000_000)
				f.fund(merchant, 1_000_000)
				lifecycle := func(sp *StateProcessor) *pos.Lifecycle {
					l := pos.NewLifecycle(nhbstate.NewManager(sp.Trie))
					l.SetNowFunc(func() time.Time { return f.now })
					return l
				}
				run := func(label string, step func(l *pos.Lifecycle) error) {
					before := f.totalZNHB()
					working, err := f.sp.Copy()
					if err != nil {
						t.Fatalf("copy: %v", err)
					}
					if err := working.withTreasuryPoolBooking(false, func() error { return step(lifecycle(working)) }); err != nil {
						t.Fatalf("%s: %v", label, err)
					}
					f.sp = working
					f.assertSound(label, before)
				}
				var authID [32]byte
				run("authorize", func(l *pos.Lifecycle) error {
					auth, err := l.Authorize(f.addr(payer), f.addr(merchant), big.NewInt(100_000), uint64(f.now.Add(time.Hour).Unix()), []byte("intent"))
					if err == nil {
						authID = auth.ID
					}
					return err
				})
				run("capture", func(l *pos.Lifecycle) error {
					_, err := l.Capture(authID, big.NewInt(capture), f.addr(merchant))
					return err
				})
				// The hold is fully released: nothing may stay locked.
				payerAccount, err := f.sp.getAccount(func() []byte { a := f.addr(payer); return a[:] }())
				if err != nil {
					t.Fatalf("load payer: %v", err)
				}
				if payerAccount.LockedZNHB.Sign() != 0 {
					t.Fatalf("payer still has %s locked after the capture", payerAccount.LockedZNHB)
				}
			})
		}
	}
}

// proposalToFinalize submits a proposal with a ZNHB deposit, lets voting
// close, and returns the finalize transaction (sent by finalizer) with the
// handler that applies it on its own.
func (f *treasuryFixture) proposalToFinalize(proposer, finalizer int) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
	return f.proposalToFinalizeWithQuorum(proposer, finalizer, 0)
}

// proposalToFinalizeWithQuorum is proposalToFinalize with a quorum: with no
// votes cast, a nonzero quorum leaves the proposal rejected, which forfeits
// its deposit to the treasury wallet.
func (f *treasuryFixture) proposalToFinalizeWithQuorum(proposer, finalizer int, quorumBps uint64) (int, *types.Transaction, func(*StateProcessor, *types.Transaction, []byte, *types.Account) error) {
	f.t.Helper()
	f.fund(proposer, 1_000_000)
	f.fund(finalizer, 1_000_000)
	f.sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     100,
		AllowedParams:       []string{"fees.baseFee"},
		QuorumBps:           quorumBps,
	})
	proposeData, err := rlp.EncodeToBytes(struct {
		Kind    string
		Payload string
		Deposit *big.Int
	}{Kind: governance.ProposalKindParamUpdate, Payload: `{"fees.baseFee":1000}`, Deposit: big.NewInt(100_000)})
	if err != nil {
		f.t.Fatalf("encode proposal: %v", err)
	}
	f.mustApply("propose", f.signed(proposer, &types.Transaction{Type: types.TxTypeGovPropose, Data: proposeData}))
	f.now = f.now.Add(200 * time.Second)
	finalizeData, err := rlp.EncodeToBytes(struct{ ProposalID uint64 }{ProposalID: 1})
	if err != nil {
		f.t.Fatalf("encode finalize: %v", err)
	}
	return finalizer, f.signed(finalizer, &types.Transaction{Type: types.TxTypeGovFinalize, Data: finalizeData}),
		func(sp *StateProcessor, tx *types.Transaction, sender []byte, account *types.Account) error {
			return sp.applyGovFinalizeTransaction(tx, sender, account)
		}
}
