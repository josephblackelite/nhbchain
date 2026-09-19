package core

import (
	"errors"
	"fmt"
	"math/big"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	statebank "nhbchain/state/bank"
)

// ErrTreasuryRewardPoolInsufficient indicates a transaction would move more
// ZNHB off the admin/treasury wallet than the Reward Pool sub-ledger holds.
// CheckZNHBSupplyInvariant requires the Sale Pool and Reward Pool to account
// for every ZNHB the wallet owns, and only the Reward Pool absorbs movement
// that is not a curve sale, so the outflow cannot be mirrored and the
// transaction is rejected instead. The outcome depends only on committed
// state that later inflows can change (fees, forfeited deposits), so
// classifyProposalError treats it as skippable, not prunable. Because
// admission simulates the identical execution path, the same rejection
// happens at mempool admission.
var ErrTreasuryRewardPoolInsufficient = errors.New("znhb: treasury reward pool cannot cover this outflow")

// treasuryZNHBFlowTracked reports whether transactions of this type can move
// ZNHB onto or off the admin/treasury wallet through code that does not keep
// the Sale/Reward Pool sub-ledgers in step itself (a plain transfer paying
// the wallet, an escrow fee routed to it, a POS capture, a staking-reward
// payout out of it, ...). executeTransaction books whatever such a type
// leaves unmirrored into the Reward Pool in the SAME state transition, so
// CheckZNHBSupplyInvariant -- which only runs once per block, after every
// transaction -- can never be broken by a single ordinary transaction.
//
// Types that carry their own pool logic (BuyZNHB, swap voucher mint, the
// market) are deliberately absent, as are types that cannot change the
// wallet's tracked ZNHB at all (staking moves it between BalanceZNHB/
// LockedZNHB/PendingUnbonds, which the invariant sums together; a governance
// deposit moves it into an escrow the invariant also counts). Governance
// finalize and execute are tracked even though the engine mirrors a forfeited
// deposit itself: both run the engine and then persist the sender's
// pre-transaction account object, so when the sender is also the account the
// engine just refunded, swept to or paid, the engine's write is overwritten
// and only booking the resulting movement keeps the invariant intact.
func treasuryZNHBFlowTracked(t types.TxType) bool {
	switch t {
	case types.TxTypeTransfer,
		types.TxTypeTransferZNHB,
		types.TxTypeLockEscrow,
		types.TxTypeReleaseEscrow,
		types.TxTypeRefundEscrow,
		types.TxTypeExpireEscrow,
		types.TxTypeArbitrateRelease,
		types.TxTypeArbitrateRefund,
		types.TxTypeDelegatedReleaseEscrow,
		types.TxTypeDelegatedRefundEscrow,
		types.TxTypeStakeClaimRewards,
		types.TxTypePOSCapture,
		types.TxTypePotsoStakeLock,
		types.TxTypePotsoStakeWithdraw,
		types.TxTypeLendingDepositZNHB,
		types.TxTypeLendingWithdrawZNHB,
		types.TxTypeLendingLiquidate,
		types.TxTypeGovFinalize,
		types.TxTypeGovExecute:
		return true
	}
	return false
}

// treasuryPoolPosition is a snapshot of how far the admin/treasury wallet's
// tracked ZNHB (adminZNHBOwned) sits above the Sale Pool plus Reward Pool.
// CheckZNHBSupplyInvariant demands the gap be exactly zero; capturing it
// before an operation and comparing afterwards isolates whatever that
// operation moved without mirroring, whether or not the gap started at zero.
type treasuryPoolPosition struct {
	gap *big.Int
}

// treasuryPoolGap reads the current gap. ok is false, with no error, when
// there is nothing to track: no configured admin wallet, or the pool ledger
// has not been bootstrapped yet (EnsureZNHBPoolsBootstrapped splits whatever
// the wallet holds at that point, so earlier movement needs no booking).
// Only reads state.
func (sp *StateProcessor) treasuryPoolGap() (gap *big.Int, ok bool, err error) {
	if sp == nil || !sp.hasAdminWallet {
		return nil, false, nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	bootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		return nil, false, fmt.Errorf("znhb: check pool bootstrap flag: %w", err)
	}
	if !bootstrapped {
		return nil, false, nil
	}
	adminAccount, err := sp.getAccount(sp.adminWallet[:])
	if err != nil {
		return nil, false, fmt.Errorf("znhb: load admin wallet: %w", err)
	}
	govEscrow, err := manager.GovernanceEscrowBalance(sp.adminWallet[:])
	if err != nil {
		return nil, false, fmt.Errorf("znhb: load admin governance escrow balance: %w", err)
	}
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		return nil, false, fmt.Errorf("znhb: load sale pool balance: %w", err)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		return nil, false, fmt.Errorf("znhb: load reward pool balance: %w", err)
	}
	gap = adminZNHBOwned(adminAccount, govEscrow)
	gap.Sub(gap, salePool)
	gap.Sub(gap, rewardPool)
	return gap, true, nil
}

// captureTreasuryPoolPosition snapshots the wallet's pool gap ahead of an
// operation. It returns nil, with no error, when there is nothing to track,
// which bookTreasuryPoolMovement accepts as a no-op.
func (sp *StateProcessor) captureTreasuryPoolPosition() (*treasuryPoolPosition, error) {
	gap, ok, err := sp.treasuryPoolGap()
	if err != nil || !ok {
		return nil, err
	}
	return &treasuryPoolPosition{gap: gap}, nil
}

// bookTreasuryPoolMovement mirrors into the Reward Pool whatever net ZNHB
// the admin/treasury wallet gained or lost since before was captured and the
// operation did not already account for in the pool sub-ledgers itself --
// the same bucket every existing fee, refund and escrow path already routes
// into (see adjustRewardPoolForAdminZNHBMovement). Operations that adjust
// the pools explicitly leave no residue, so calling this after them changes
// nothing. When the outflow exceeds the Reward Pool, a transaction is
// rejected with ErrTreasuryRewardPoolInsufficient; mayDrawSalePool is for
// block-lifecycle steps, which have no transaction to reject (failing them
// would fail every block), and takes the shortfall from the Sale Pool so the
// two pools still sum to the wallet's holdings.
func (sp *StateProcessor) bookTreasuryPoolMovement(before *treasuryPoolPosition, mayDrawSalePool bool) error {
	if before == nil {
		return nil
	}
	gap, ok, err := sp.treasuryPoolGap()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	shift := new(big.Int).Sub(gap, before.gap)
	if shift.Sign() == 0 {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		return fmt.Errorf("znhb: load reward pool balance for treasury booking: %w", err)
	}
	newRewardPool := new(big.Int).Add(rewardPool, shift)
	if newRewardPool.Sign() < 0 {
		if !mayDrawSalePool {
			return fmt.Errorf("%w: moves %s out of the treasury wallet, reward pool holds %s", ErrTreasuryRewardPoolInsufficient, new(big.Int).Neg(shift), rewardPool)
		}
		salePool, err := manager.ZNHBSalePoolBalance()
		if err != nil {
			return fmt.Errorf("znhb: load sale pool balance for treasury booking: %w", err)
		}
		newSalePool := new(big.Int).Add(salePool, newRewardPool)
		if newSalePool.Sign() < 0 {
			return fmt.Errorf("znhb: treasury outflow %s exceeds the sale and reward pools (%s + %s)", new(big.Int).Neg(shift), salePool, rewardPool)
		}
		if err := manager.ZNHBSetSalePoolBalance(newSalePool); err != nil {
			return fmt.Errorf("znhb: update sale pool balance for treasury booking: %w", err)
		}
		newRewardPool = big.NewInt(0)
	}
	if err := manager.ZNHBSetRewardPoolBalance(newRewardPool); err != nil {
		return fmt.Errorf("znhb: update reward pool balance for treasury booking: %w", err)
	}
	return nil
}

// withTreasuryPoolBooking runs a state transition that may move the
// admin/treasury wallet's ZNHB without touching the pool sub-ledgers and books
// the net movement into the Reward Pool once it succeeds. See
// bookTreasuryPoolMovement for mayDrawSalePool.
func (sp *StateProcessor) withTreasuryPoolBooking(mayDrawSalePool bool, fn func() error) error {
	before, err := sp.captureTreasuryPoolPosition()
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return sp.bookTreasuryPoolMovement(before, mayDrawSalePool)
}

// treasuryBookedSlasher wraps a Slasher so the ZNHB it forfeits into the
// treasury wallet is mirrored into the Reward Pool in the same transition.
type treasuryBookedSlasher struct {
	sp    *StateProcessor
	inner statebank.Slasher
}

// bookedSlasher returns inner wrapped so every slash keeps the treasury pool
// ledger in step. Slashing runs outside any transaction, so a shortfall can
// only ever come out of the Sale Pool, never abort the block.
func (sp *StateProcessor) bookedSlasher(inner statebank.Slasher) statebank.Slasher {
	return treasuryBookedSlasher{sp: sp, inner: inner}
}

func (s treasuryBookedSlasher) Slash(addr [20]byte, amount *big.Int) error {
	return s.sp.withTreasuryPoolBooking(true, func() error { return s.inner.Slash(addr, amount) })
}
