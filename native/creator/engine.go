package creator

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

var (
	errNilState               = errors.New("creator engine: state not configured")
	errContentExists          = errors.New("creator engine: content already exists")
	errContentNotFound        = errors.New("creator engine: content not found")
	errInvalidAmount          = errors.New("creator engine: amount must be positive")
	errInsufficientFunds      = errors.New("creator engine: insufficient balance")
	errStakeNotFound          = errors.New("creator engine: stake not found")
	errPayoutVaultNotSet      = errors.New("creator engine: payout vault not configured")
	errRewardsTreasuryNotSet  = errors.New("creator engine: rewards treasury not configured")
	errPayoutVaultUnderfunded = errors.New("creator engine: payout vault underfunded")
)

const stakingAccrualBps = 250 // 2.5% accrual when staking behind a creator.

type engineState interface {
	CreatorContentGet(id string) (*Content, bool, error)
	CreatorContentPut(content *Content) error
	CreatorStakeGet(creator [20]byte, fan [20]byte) (*Stake, bool, error)
	CreatorStakePut(stake *Stake) error
	CreatorStakeDelete(creator [20]byte, fan [20]byte) error
	CreatorPayoutLedgerGet(creator [20]byte) (*PayoutLedger, bool, error)
	CreatorPayoutLedgerPut(ledger *PayoutLedger) error
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
}

// Engine wires creator economy business logic with persistence and event emission.
type Engine struct {
	state           engineState
	emitter         events.Emitter
	nowFn           func() int64
	payoutVault     [20]byte
	rewardsTreasury [20]byte
}

// NewEngine constructs a creator engine with default dependencies.
func NewEngine() *Engine {
	return &Engine{
		emitter: events.NoopEmitter{},
		nowFn: func() int64 {
			return time.Now().Unix()
		},
	}
}

// SetState configures the state backend used by the engine.
func (e *Engine) SetState(state engineState) { e.state = state }

// SetEmitter configures the event emitter used by the engine.
func (e *Engine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

// SetNowFunc overrides the time source used for deterministic testing.
func (e *Engine) SetNowFunc(now func() int64) {
	if now == nil {
		e.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	e.nowFn = now
}

// SetPayoutVault configures the holding account for pending distributions.
func (e *Engine) SetPayoutVault(addr [20]byte) { e.payoutVault = addr }

// SetRewardsTreasury configures the treasury that funds staking rewards.
func (e *Engine) SetRewardsTreasury(addr [20]byte) { e.rewardsTreasury = addr }

func (e *Engine) emit(evt *types.Event) {
	if e == nil || evt == nil || e.emitter == nil {
		return
	}
	e.emitter.Emit(WrapEvent(evt))
}

func (e *Engine) now() int64 {
	if e == nil || e.nowFn == nil {
		return time.Now().Unix()
	}
	return e.nowFn()
}

func ensureAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	if acc.Stake == nil {
		acc.Stake = big.NewInt(0)
	}
	return acc
}

func sanitizeContentID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", errors.New("content id required")
	}
	return trimmed, nil
}

func hexAddr(addr [20]byte) string {
	return "0x" + hex.EncodeToString(addr[:])
}

func newBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func isZeroAddress(addr [20]byte) bool {
	var zero [20]byte
	return addr == zero
}

func newLedger(creator [20]byte) *PayoutLedger {
	return &PayoutLedger{
		Creator:             creator,
		TotalTips:           big.NewInt(0),
		TotalStakingYield:   big.NewInt(0),
		PendingDistribution: big.NewInt(0),
		LastPayout:          0,
	}
}

// PublishContent registers a new piece of content and emits the corresponding event.
func (e *Engine) PublishContent(creator [20]byte, id string, uri string, metadata string) (*Content, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	sanitized, err := sanitizeContentID(id)
	if err != nil {
		return nil, err
	}
	if existing, ok, err := e.state.CreatorContentGet(sanitized); err != nil {
		return nil, err
	} else if ok && existing != nil {
		return nil, errContentExists
	}
	content := &Content{
		ID:          sanitized,
		Creator:     creator,
		URI:         strings.TrimSpace(uri),
		Metadata:    strings.TrimSpace(metadata),
		PublishedAt: e.now(),
		TotalTips:   big.NewInt(0),
		TotalStake:  big.NewInt(0),
	}
	if err := e.state.CreatorContentPut(content); err != nil {
		return nil, err
	}
	e.emit(ContentPublishedEvent(content.ID, hexAddr(content.Creator), content.URI))
	return content, nil
}

// TipContent processes a fan tipping a piece of content.
func (e *Engine) TipContent(fan [20]byte, contentID string, amount *big.Int) (*Tip, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	if isZeroAddress(e.payoutVault) {
		return nil, errPayoutVaultNotSet
	}
	sanitized, err := sanitizeContentID(contentID)
	if err != nil {
		return nil, err
	}
	content, ok, err := e.state.CreatorContentGet(sanitized)
	if err != nil {
		return nil, err
	}
	if !ok || content == nil {
		return nil, errContentNotFound
	}
	fanAccount, err := e.state.GetAccount(fan[:])
	if err != nil {
		return nil, err
	}
	fanAccount = ensureAccount(fanAccount)
	if fanAccount.BalanceNHB.Cmp(amount) < 0 {
		return nil, errInsufficientFunds
	}
	fanAccount.BalanceNHB = new(big.Int).Sub(fanAccount.BalanceNHB, amount)
	vaultAccount, err := e.state.GetAccount(e.payoutVault[:])
	if err != nil {
		return nil, err
	}
	vaultAccount = ensureAccount(vaultAccount)
	vaultAccount.BalanceNHB = new(big.Int).Add(vaultAccount.BalanceNHB, amount)
	if err := e.state.PutAccount(fan[:], fanAccount); err != nil {
		return nil, err
	}
	if err := e.state.PutAccount(e.payoutVault[:], vaultAccount); err != nil {
		return nil, err
	}
	content.TotalTips = new(big.Int).Add(content.TotalTips, amount)
	if err := e.state.CreatorContentPut(content); err != nil {
		return nil, err
	}
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(content.Creator)
	if err != nil {
		return nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(content.Creator)
	}
	ledger.TotalTips = new(big.Int).Add(ledger.TotalTips, amount)
	ledger.PendingDistribution = new(big.Int).Add(ledger.PendingDistribution, amount)
	if err := e.state.CreatorPayoutLedgerPut(ledger); err != nil {
		return nil, err
	}
	e.emit(ContentTippedEvent(content.ID, hexAddr(content.Creator), hexAddr(fan), amount.String()))
	e.emit(CreatorPayoutAccruedEvent(hexAddr(content.Creator), ledger.PendingDistribution.String(), ledger.TotalTips.String(), ledger.TotalStakingYield.String()))
	tip := &Tip{
		ContentID: sanitized,
		Creator:   content.Creator,
		Fan:       fan,
		Amount:    newBigInt(amount),
		TippedAt:  e.now(),
	}
	return tip, nil
}

// StakeCreator allows a fan to stake behind a creator and accrues a payout share.
func (e *Engine) StakeCreator(fan [20]byte, creator [20]byte, amount *big.Int) (*Stake, *big.Int, error) {
	if e == nil || e.state == nil {
		return nil, nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, nil, errInvalidAmount
	}
	if isZeroAddress(e.payoutVault) {
		return nil, nil, errPayoutVaultNotSet
	}
	fanAccount, err := e.state.GetAccount(fan[:])
	if err != nil {
		return nil, nil, err
	}
	fanAccount = ensureAccount(fanAccount)
	if fanAccount.BalanceNHB.Cmp(amount) < 0 {
		return nil, nil, errInsufficientFunds
	}
	fanAccount.BalanceNHB = new(big.Int).Sub(fanAccount.BalanceNHB, amount)
	if err := e.state.PutAccount(fan[:], fanAccount); err != nil {
		return nil, nil, err
	}
	stake, ok, err := e.state.CreatorStakeGet(creator, fan)
	if err != nil {
		return nil, nil, err
	}
	if !ok || stake == nil {
		stake = &Stake{
			Creator:     creator,
			Fan:         fan,
			Amount:      big.NewInt(0),
			Shares:      big.NewInt(0),
			StakedAt:    e.now(),
			LastAccrual: e.now(),
		}
	}
	stake.Amount = new(big.Int).Add(stake.Amount, amount)
	stake.Shares = new(big.Int).Add(stake.Shares, amount)
	stake.LastAccrual = e.now()
	if err := e.state.CreatorStakePut(stake); err != nil {
		return nil, nil, err
	}
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(creator)
	if err != nil {
		return nil, nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(creator)
	}
	reward := big.NewInt(0)
	if amount.Sign() > 0 {
		candidate := new(big.Int).Mul(amount, big.NewInt(stakingAccrualBps))
		candidate = candidate.Div(candidate, big.NewInt(10_000))
		if candidate.Sign() > 0 {
			if isZeroAddress(e.rewardsTreasury) {
				return nil, nil, errRewardsTreasuryNotSet
			}
			treasuryAcc, err := e.state.GetAccount(e.rewardsTreasury[:])
			if err != nil {
				return nil, nil, err
			}
			treasuryAcc = ensureAccount(treasuryAcc)
			if treasuryAcc.BalanceNHB.Cmp(candidate) >= 0 {
				treasuryAcc.BalanceNHB = new(big.Int).Sub(treasuryAcc.BalanceNHB, candidate)
				if err := e.state.PutAccount(e.rewardsTreasury[:], treasuryAcc); err != nil {
					return nil, nil, err
				}
				vaultAccount, err := e.state.GetAccount(e.payoutVault[:])
				if err != nil {
					return nil, nil, err
				}
				vaultAccount = ensureAccount(vaultAccount)
				vaultAccount.BalanceNHB = new(big.Int).Add(vaultAccount.BalanceNHB, candidate)
				if err := e.state.PutAccount(e.payoutVault[:], vaultAccount); err != nil {
					return nil, nil, err
				}
				ledger.TotalStakingYield = new(big.Int).Add(ledger.TotalStakingYield, candidate)
				ledger.PendingDistribution = new(big.Int).Add(ledger.PendingDistribution, candidate)
				reward = candidate
			}
		}
	}
	if err := e.state.CreatorPayoutLedgerPut(ledger); err != nil {
		return nil, nil, err
	}
	e.emit(CreatorStakedEvent(hexAddr(creator), hexAddr(fan), amount.String(), stake.Shares.String()))
	if reward.Sign() > 0 {
		e.emit(CreatorPayoutAccruedEvent(hexAddr(creator), ledger.PendingDistribution.String(), ledger.TotalTips.String(), ledger.TotalStakingYield.String()))
	}
	return stake, newBigInt(reward), nil
}

// UnstakeCreator unlocks a fan stake and returns the funds back to the fan balance.
func (e *Engine) UnstakeCreator(fan [20]byte, creator [20]byte, amount *big.Int) (*Stake, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	stake, ok, err := e.state.CreatorStakeGet(creator, fan)
	if err != nil {
		return nil, err
	}
	if !ok || stake == nil || stake.Amount.Cmp(amount) < 0 {
		return nil, errStakeNotFound
	}
	stake.Amount = new(big.Int).Sub(stake.Amount, amount)
	if stake.Shares == nil {
		stake.Shares = big.NewInt(0)
	}
	if stake.Shares.Cmp(amount) >= 0 {
		stake.Shares = new(big.Int).Sub(stake.Shares, amount)
	} else {
		stake.Shares = big.NewInt(0)
	}
	if stake.Amount.Sign() == 0 {
		if err := e.state.CreatorStakeDelete(creator, fan); err != nil {
			return nil, err
		}
	} else {
		if err := e.state.CreatorStakePut(stake); err != nil {
			return nil, err
		}
	}
	fanAccount, err := e.state.GetAccount(fan[:])
	if err != nil {
		return nil, err
	}
	fanAccount = ensureAccount(fanAccount)
	fanAccount.BalanceNHB = new(big.Int).Add(fanAccount.BalanceNHB, amount)
	if err := e.state.PutAccount(fan[:], fanAccount); err != nil {
		return nil, err
	}
	e.emit(CreatorUnstakedEvent(hexAddr(creator), hexAddr(fan), amount.String()))
	return stake, nil
}

// ClaimPayouts settles the pending distribution for the creator and credits their balance.
func (e *Engine) ClaimPayouts(creator [20]byte) (*PayoutLedger, *big.Int, error) {
	if e == nil || e.state == nil {
		return nil, nil, errNilState
	}
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(creator)
	if err != nil {
		return nil, nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(creator)
	}
	pending := newBigInt(ledger.PendingDistribution)
	if pending.Sign() == 0 {
		return ledger.Clone(), big.NewInt(0), nil
	}
	if isZeroAddress(e.payoutVault) {
		return nil, nil, errPayoutVaultNotSet
	}
	creatorAccount, err := e.state.GetAccount(creator[:])
	if err != nil {
		return nil, nil, err
	}
	creatorAccount = ensureAccount(creatorAccount)
	vaultAccount, err := e.state.GetAccount(e.payoutVault[:])
	if err != nil {
		return nil, nil, err
	}
	vaultAccount = ensureAccount(vaultAccount)
	if vaultAccount.BalanceNHB.Cmp(pending) < 0 {
		return nil, nil, errPayoutVaultUnderfunded
	}
	creatorAccount.BalanceNHB = new(big.Int).Add(creatorAccount.BalanceNHB, pending)
	vaultAccount.BalanceNHB = new(big.Int).Sub(vaultAccount.BalanceNHB, pending)
	if err := e.state.PutAccount(e.payoutVault[:], vaultAccount); err != nil {
		return nil, nil, err
	}
	if err := e.state.PutAccount(creator[:], creatorAccount); err != nil {
		return nil, nil, err
	}
	ledger.PendingDistribution = big.NewInt(0)
	ledger.LastPayout = e.now()
	if err := e.state.CreatorPayoutLedgerPut(ledger); err != nil {
		return nil, nil, err
	}
	e.emit(CreatorPayoutAccruedEvent(hexAddr(creator), ledger.PendingDistribution.String(), ledger.TotalTips.String(), ledger.TotalStakingYield.String()))
	return ledger.Clone(), pending, nil
}

// Payouts returns the payout ledger for the supplied creator without mutating state.
func (e *Engine) Payouts(creator [20]byte) (*PayoutLedger, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(creator)
	if err != nil {
		return nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(creator)
	}
	return ledger.Clone(), nil
}

// DebugString returns a textual description of the engine state. Useful for tracing.
func (e *Engine) DebugString() string {
	if e == nil {
		return "creator engine <nil>"
	}
	return fmt.Sprintf("creator engine emitter=%T", e.emitter)
}
