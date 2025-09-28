package creator

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"lukechampine.com/blake3"
	"nhbchain/core/events"
	"nhbchain/core/types"
)

var (
	errNilState               = errors.New("creator engine: state not configured")
	errContentExists          = errors.New("creator engine: content already exists")
	errContentNotFound        = errors.New("creator engine: content not found")
	errInvalidAmount          = errors.New("creator engine: amount must be positive")
	errDepositTooSmall        = errors.New("creator engine: deposit below minimum")
	errZeroShareMint          = errors.New("creator engine: deposit too small for share precision")
	errInsufficientFunds      = errors.New("creator engine: insufficient balance")
	errStakeNotFound          = errors.New("creator engine: stake not found")
	errPayoutVaultNotSet      = errors.New("creator engine: payout vault not configured")
	errRewardsTreasuryNotSet  = errors.New("creator engine: rewards treasury not configured")
	errPayoutVaultUnderfunded = errors.New("creator engine: payout vault underfunded")
	errSharesDepleted         = errors.New("creator engine: share supply depleted")
	errInsufficientShares     = errors.New("creator engine: insufficient shares")
	errRedeemTooSmall         = errors.New("creator engine: redeem value below precision")
	errStakeEpochCap          = errors.New("creator engine: per-epoch stake cap exceeded")
	errTipRateLimited         = errors.New("creator engine: tip rate limit exceeded")
	errInvalidURI             = errors.New("creator engine: invalid content uri")
	errInvalidMetadata        = errors.New("creator engine: invalid content metadata")
)

const stakingAccrualBps = 250 // 2.5% accrual when staking behind a creator.

const (
	stakeEpochSeconds    = int64(3600) // 1h fan staking window
	tipRateWindowSeconds = int64(1)
	tipRateBurst         = 5
	maxURILength         = 512
	maxMetadataLength    = 4096
)

var (
	fanStakeEpochCap = big.NewInt(1_000_000_000_000)
)

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
	mu              sync.Mutex
	stakeWindows    map[[20]byte]*stakeWindow
	tipWindows      map[[20]byte]*tipWindow
}

type stakeWindow struct {
	windowStart int64
	amount      *big.Int
}

type tipWindow struct {
	timestamps []int64
}

// NewEngine constructs a creator engine with default dependencies.
func NewEngine() *Engine {
	return &Engine{
		emitter: events.NoopEmitter{},
		nowFn: func() int64 {
			return time.Now().Unix()
		},
		stakeWindows: make(map[[20]byte]*stakeWindow),
		tipWindows:   make(map[[20]byte]*tipWindow),
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

func ensureStake(stake *Stake) *Stake {
	if stake == nil {
		return nil
	}
	if stake.Amount == nil {
		stake.Amount = big.NewInt(0)
	}
	if stake.Shares == nil {
		stake.Shares = big.NewInt(0)
	}
	return stake
}

func ensureLedgerFields(ledger *PayoutLedger) *PayoutLedger {
	if ledger == nil {
		return nil
	}
	if ledger.TotalTips == nil {
		ledger.TotalTips = big.NewInt(0)
	}
	if ledger.TotalStakingYield == nil {
		ledger.TotalStakingYield = big.NewInt(0)
	}
	if ledger.PendingDistribution == nil {
		ledger.PendingDistribution = big.NewInt(0)
	}
	if ledger.TotalAssets == nil {
		ledger.TotalAssets = big.NewInt(0)
	}
	if ledger.TotalShares == nil {
		ledger.TotalShares = big.NewInt(0)
	}
	if ledger.IndexRay == nil {
		ledger.IndexRay = new(big.Int).Set(oneRay)
	}
	return ledger
}

func sanitizeContentID(id string) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", errors.New("content id required")
	}
	return trimmed, nil
}

var allowedURISchemes = map[string]struct{}{
	"https": {},
	"ipfs":  {},
	"ar":    {},
	"nhb":   {},
}

func sanitizeURI(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errInvalidURI
	}
	if len(trimmed) > maxURILength {
		return "", errInvalidURI
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.Scheme == "" {
		return "", errInvalidURI
	}
	if _, ok := allowedURISchemes[strings.ToLower(parsed.Scheme)]; !ok {
		return "", errInvalidURI
	}
	return trimmed, nil
}

func sanitizeMetadata(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > maxMetadataLength {
		return "", "", errInvalidMetadata
	}
	if !utf8.ValidString(trimmed) {
		return "", "", errInvalidMetadata
	}
	sum := blake3.Sum256([]byte(trimmed))
	return trimmed, hex.EncodeToString(sum[:]), nil
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
		TotalAssets:         big.NewInt(0),
		TotalShares:         big.NewInt(0),
		IndexRay:            new(big.Int).Set(oneRay),
	}
}

func (e *Engine) enforceStakeLimit(fan [20]byte, amount *big.Int) error {
	if e == nil || amount == nil || amount.Sign() <= 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	window, ok := e.stakeWindows[fan]
	now := e.now()
	if !ok || window == nil {
		window = &stakeWindow{windowStart: now, amount: big.NewInt(0)}
		e.stakeWindows[fan] = window
	}
	if now-window.windowStart >= stakeEpochSeconds {
		window.windowStart = now
		window.amount = big.NewInt(0)
	}
	if window.amount == nil {
		window.amount = big.NewInt(0)
	}
	candidate := new(big.Int).Add(window.amount, amount)
	if candidate.Cmp(fanStakeEpochCap) > 0 {
		return errStakeEpochCap
	}
	window.amount = candidate
	return nil
}

func (e *Engine) enforceTipLimit(creator [20]byte, now int64) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	window, ok := e.tipWindows[creator]
	if !ok || window == nil {
		window = &tipWindow{timestamps: make([]int64, 0, tipRateBurst)}
		e.tipWindows[creator] = window
	}
	cutoff := now - tipRateWindowSeconds
	kept := window.timestamps[:0]
	for _, ts := range window.timestamps {
		if ts >= cutoff {
			kept = append(kept, ts)
		}
	}
	window.timestamps = kept
	if len(window.timestamps) >= tipRateBurst {
		return errTipRateLimited
	}
	window.timestamps = append(window.timestamps, now)
	return nil
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
	sanitizedURI, err := sanitizeURI(uri)
	if err != nil {
		return nil, err
	}
	sanitizedMetadata, hash, err := sanitizeMetadata(metadata)
	if err != nil {
		return nil, err
	}
	content := &Content{
		ID:          sanitized,
		Creator:     creator,
		URI:         sanitizedURI,
		Metadata:    sanitizedMetadata,
		Hash:        hash,
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
	now := e.now()
	if err := e.enforceTipLimit(content.Creator, now); err != nil {
		return nil, err
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
	ledger = ensureLedgerFields(ledger)
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
		TippedAt:  now,
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
	deposit := new(big.Int).Set(amount)
	if err := e.enforceStakeLimit(fan, deposit); err != nil {
		return nil, nil, err
	}
	fanAccount, err := e.state.GetAccount(fan[:])
	if err != nil {
		return nil, nil, err
	}
	fanAccount = ensureAccount(fanAccount)
	if fanAccount.BalanceNHB.Cmp(deposit) < 0 {
		return nil, nil, errInsufficientFunds
	}
	now := e.now()
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
			StakedAt:    now,
			LastAccrual: now,
		}
	} else {
		stake = ensureStake(stake)
	}
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(creator)
	if err != nil {
		return nil, nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(creator)
	} else {
		ledger = ensureLedgerFields(ledger)
	}
	mintedShares, bootstrapShares, err := calculateMintShares(deposit, ledger.TotalShares, ledger.TotalAssets)
	if err != nil {
		return nil, nil, err
	}
	fanAccount.BalanceNHB = new(big.Int).Sub(fanAccount.BalanceNHB, deposit)
	if err := e.state.PutAccount(fan[:], fanAccount); err != nil {
		return nil, nil, err
	}
	stake.Amount = new(big.Int).Add(stake.Amount, deposit)
	stake.Shares = new(big.Int).Add(stake.Shares, mintedShares)
	if stake.StakedAt == 0 {
		stake.StakedAt = now
	}
	stake.LastAccrual = now
	reward := big.NewInt(0)
	if deposit.Sign() > 0 {
		candidate := new(big.Int).Mul(deposit, big.NewInt(stakingAccrualBps))
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
	ledger.TotalAssets = new(big.Int).Add(ledger.TotalAssets, deposit)
	ledger.TotalShares = new(big.Int).Add(ledger.TotalShares, mintedShares)
	if bootstrapShares != nil && bootstrapShares.Sign() > 0 {
		ledger.TotalShares = new(big.Int).Add(ledger.TotalShares, bootstrapShares)
	}
	ledger.IndexRay = computeIndex(ledger.TotalAssets, ledger.TotalShares)
	if err := e.state.CreatorStakePut(stake); err != nil {
		return nil, nil, err
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
	if !ok || stake == nil {
		return nil, errStakeNotFound
	}
	stake = ensureStake(stake)
	ledger, ok, err := e.state.CreatorPayoutLedgerGet(creator)
	if err != nil {
		return nil, err
	}
	if !ok || ledger == nil {
		ledger = newLedger(creator)
	} else {
		ledger = ensureLedgerFields(ledger)
	}
	if stake.Shares.Cmp(amount) < 0 {
		return nil, errInsufficientShares
	}
	assetsOut, err := calculateRedeemAssets(amount, ledger.TotalShares, ledger.TotalAssets)
	if err != nil {
		return nil, err
	}
	remainingShares := new(big.Int).Sub(ledger.TotalShares, amount)
	remainingAssets := new(big.Int).Sub(ledger.TotalAssets, assetsOut)
	if remainingShares.Cmp(minLiquidity) <= 0 || remainingAssets.Sign() <= 0 {
		if remainingAssets.Sign() > 0 {
			assetsOut = new(big.Int).Add(assetsOut, remainingAssets)
		}
		remainingShares = big.NewInt(0)
		remainingAssets = big.NewInt(0)
	}
	stake.Shares = new(big.Int).Sub(stake.Shares, amount)
	if stake.Amount.Cmp(assetsOut) >= 0 {
		stake.Amount = new(big.Int).Sub(stake.Amount, assetsOut)
	} else {
		stake.Amount = big.NewInt(0)
	}
	ledger.TotalShares = remainingShares
	ledger.TotalAssets = remainingAssets
	if ledger.TotalAssets.Sign() <= 0 || ledger.TotalShares.Sign() == 0 {
		ledger.TotalAssets = big.NewInt(0)
		ledger.TotalShares = big.NewInt(0)
		ledger.IndexRay = new(big.Int).Set(oneRay)
	} else {
		ledger.IndexRay = computeIndex(ledger.TotalAssets, ledger.TotalShares)
	}
	if stake.Amount.Sign() == 0 || stake.Shares.Sign() == 0 {
		if err := e.state.CreatorStakeDelete(creator, fan); err != nil {
			return nil, err
		}
	} else {
		if err := e.state.CreatorStakePut(stake); err != nil {
			return nil, err
		}
	}
	if err := e.state.CreatorPayoutLedgerPut(ledger); err != nil {
		return nil, err
	}
	fanAccount, err := e.state.GetAccount(fan[:])
	if err != nil {
		return nil, err
	}
	fanAccount = ensureAccount(fanAccount)
	fanAccount.BalanceNHB = new(big.Int).Add(fanAccount.BalanceNHB, assetsOut)
	if err := e.state.PutAccount(fan[:], fanAccount); err != nil {
		return nil, err
	}
	e.emit(CreatorUnstakedEvent(hexAddr(creator), hexAddr(fan), assetsOut.String()))
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
	} else {
		ledger = ensureLedgerFields(ledger)
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
	} else {
		ledger = ensureLedgerFields(ledger)
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
