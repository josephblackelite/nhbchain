package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/engagement"
	"nhbchain/core/epoch"
	"nhbchain/core/events"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/bank"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/escrow"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
	"nhbchain/native/loyalty"
	"nhbchain/native/potso"
	swap "nhbchain/native/swap"
	systemquotas "nhbchain/native/system/quotas"
	swapv1 "nhbchain/proto/swap/v1"
	"nhbchain/storage/trie"

	"github.com/ethereum/go-ethereum/common"
	gethcore "github.com/ethereum/go-ethereum/core"
	gethstate "github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethvm "github.com/ethereum/go-ethereum/core/vm"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const engagementDayFormat = "2006-01-02"

const unbondingPeriod = 72 * time.Hour

const defaultIntentTTL = 24 * time.Hour

var (
	ErrNonceMismatch  = errors.New("transaction nonce mismatch")
	ErrInvalidChainID = errors.New("invalid chain id")
)

var (
	accountMetadataPrefix = []byte("account-meta:")
	usernameIndexKey      = ethcrypto.Keccak256([]byte("username-index"))
	validatorSetKey       = ethcrypto.Keccak256([]byte("validator-set"))
	validatorEligibleKey  = ethcrypto.Keccak256([]byte("validator-eligible-set"))
	epochHistoryKey       = ethcrypto.Keccak256([]byte("epoch-history"))
	rewardHistoryKey      = ethcrypto.Keccak256([]byte("reward-history"))
)

type blockExecutionContext struct {
	height    uint64
	timestamp time.Time
}

type StateProcessor struct {
	Trie               *trie.Trie
	stateDB            *gethstate.CachingDB
	LoyaltyEngine      *loyalty.Engine
	EscrowEngine       *escrow.Engine
	TradeEngine        *escrow.TradeEngine
	pauses             nativecommon.PauseView
	escrowFeeTreasury  [20]byte
	usernameToAddr     map[string][]byte
	ValidatorSet       map[string]*big.Int
	EligibleValidators map[string]*big.Int
	committedRoot      common.Hash
	events             []types.Event
	nowFunc            func() time.Time
	execContext        *blockExecutionContext
	engagementConfig   engagement.Config
	epochConfig        epoch.Config
	epochHistory       []epoch.Snapshot
	rewardConfig       rewards.Config
	rewardAccrual      *rewards.Accumulator
	rewardHistory      []rewards.EpochSettlement
	potsoRewardConfig  potso.RewardConfig
	potsoWeightConfig  potso.WeightParams
	paymasterEnabled   bool
	paymasterLimits    PaymasterLimits
	paymasterTopUp     PaymasterAutoTopUpPolicy
	quotaConfig        map[string]nativecommon.Quota
	quotaStore         *systemquotas.Store
	intentTTL          time.Duration
	feePolicy          fees.Policy
}

func NewStateProcessor(tr *trie.Trie) (*StateProcessor, error) {
	stateDB := gethstate.NewDatabase(tr.TrieDB(), nil)
	escEngine := escrow.NewEngine()
	tradeEngine := escrow.NewTradeEngine(escEngine)
	sp := &StateProcessor{
		Trie:               tr,
		stateDB:            stateDB,
		LoyaltyEngine:      loyalty.NewEngine(),
		EscrowEngine:       escEngine,
		TradeEngine:        tradeEngine,
		usernameToAddr:     make(map[string][]byte),
		ValidatorSet:       make(map[string]*big.Int),
		EligibleValidators: make(map[string]*big.Int),
		committedRoot:      tr.Root(),
		events:             make([]types.Event, 0),
		nowFunc:            time.Now,
		execContext:        nil,
		engagementConfig:   engagement.DefaultConfig(),
		epochConfig:        epoch.DefaultConfig(),
		epochHistory:       make([]epoch.Snapshot, 0),
		rewardConfig:       rewards.DefaultConfig(),
		rewardHistory:      make([]rewards.EpochSettlement, 0),
		potsoRewardConfig:  potso.DefaultRewardConfig(),
		potsoWeightConfig:  potso.DefaultWeightParams(),
		paymasterEnabled:   true,
		paymasterLimits:    PaymasterLimits{},
		paymasterTopUp:     PaymasterAutoTopUpPolicy{Token: "ZNHB"},
		quotaConfig:        make(map[string]nativecommon.Quota),
		intentTTL:          defaultIntentTTL,
		feePolicy:          fees.Policy{Domains: map[string]fees.DomainPolicy{}},
	}
	if err := sp.loadUsernameIndex(); err != nil {
		return nil, err
	}
	if err := sp.loadValidatorSet(); err != nil {
		return nil, err
	}
	if err := sp.loadEpochHistory(); err != nil {
		return nil, err
	}
	if err := sp.loadRewardHistory(); err != nil {
		return nil, err
	}
	return sp, nil
}

func (sp *StateProcessor) SetPauseView(p nativecommon.PauseView) {
	if sp == nil {
		return
	}
	sp.pauses = p
	if sp.EscrowEngine != nil {
		sp.EscrowEngine.SetPauses(p)
	}
	if sp.TradeEngine != nil {
		sp.TradeEngine.SetPauses(p)
	}
	if sp.LoyaltyEngine != nil {
		sp.LoyaltyEngine.SetPauses(p)
	}
}

// SetQuotaConfig installs the per-module quota configuration for the state processor.
func (sp *StateProcessor) SetQuotaConfig(cfg map[string]nativecommon.Quota) {
	if sp == nil {
		return
	}
	if len(cfg) == 0 {
		sp.quotaConfig = make(map[string]nativecommon.Quota)
		sp.quotaStore = nil
		return
	}
	cloned := make(map[string]nativecommon.Quota, len(cfg))
	for module, quota := range cfg {
		name := normalizeModuleName(module)
		if name == "" {
			continue
		}
		cloned[name] = quota
	}
	sp.quotaConfig = cloned
	sp.quotaStore = nil
}

// SetFeePolicy updates the fee policy applied to eligible transactions.
func (sp *StateProcessor) SetFeePolicy(policy fees.Policy) {
	if sp == nil {
		return
	}
	clone := policy.Clone()
	if clone.Domains == nil {
		clone.Domains = make(map[string]fees.DomainPolicy)
	}
	sp.feePolicy = clone
}

// FeePolicy returns a copy of the currently configured fee policy.
func (sp *StateProcessor) FeePolicy() fees.Policy {
	if sp == nil {
		return fees.Policy{}
	}
	return sp.feePolicy.Clone()
}

func (sp *StateProcessor) quotaStoreHandle() (*systemquotas.Store, error) {
	if sp == nil || sp.Trie == nil {
		return nil, fmt.Errorf("quota: state unavailable")
	}
	if sp.quotaStore != nil {
		return sp.quotaStore, nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	store := systemquotas.NewStore(manager)
	sp.quotaStore = store
	return store, nil
}

func (sp *StateProcessor) applyTransactionFee(tx *types.Transaction, sender []byte, fromAcc, toAcc *types.Account) error {
	if sp == nil || tx == nil {
		return nil
	}
	domain := strings.TrimSpace(tx.MerchantAddress)
	if domain == "" {
		return nil
	}
	cfg, ok := sp.feePolicy.DomainConfig(domain)
	if !ok {
		return nil
	}
	if len(sender) != 20 {
		return nil
	}
	var payer [20]byte
	copy(payer[:], sender)
	gross := big.NewInt(0)
	if tx.Value != nil {
		gross = new(big.Int).Set(tx.Value)
	}
	manager := nhbstate.NewManager(sp.Trie)
	counter, windowStart, _, err := manager.FeesGetCounter(domain, payer)
	if err != nil {
		return err
	}
	now := sp.blockTimestamp()
	currentWindow := feeWindowStart(now)
	if windowStart.IsZero() || !sameFeeWindow(windowStart, currentWindow) {
		windowStart = currentWindow
		counter = 0
	}
	result := fees.Apply(fees.ApplyInput{
		Domain:        domain,
		Gross:         gross,
		UsageCount:    counter,
		PolicyVersion: sp.feePolicy.Version,
		Config:        cfg,
		WindowStart:   windowStart,
	})
	if result.WindowStart.IsZero() {
		result.WindowStart = windowStart
	}
	if err := manager.FeesPutCounter(domain, payer, result.Counter, result.WindowStart); err != nil {
		return err
	}
	if err := manager.FeesAccumulateTotals(domain, result.OwnerWallet, gross, result.Fee, result.Net); err != nil {
		return err
	}
	if result.Fee != nil && result.Fee.Sign() > 0 && !isZeroAddress(result.OwnerWallet) {
		routed := new(big.Int).Set(result.Fee)
		deducted := false
		if toAcc != nil && toAcc.BalanceNHB != nil && toAcc.BalanceNHB.Cmp(routed) >= 0 {
			toAcc.BalanceNHB.Sub(toAcc.BalanceNHB, routed)
			deducted = true
		}
		if !deducted && fromAcc != nil && fromAcc.BalanceNHB != nil && fromAcc.BalanceNHB.Cmp(routed) >= 0 {
			fromAcc.BalanceNHB.Sub(fromAcc.BalanceNHB, routed)
			deducted = true
		}
		if !deducted {
			return fmt.Errorf("fees: insufficient balance to route fee")
		}
		routeAcc, err := sp.getAccount(result.OwnerWallet[:])
		if err != nil {
			return err
		}
		if routeAcc.BalanceNHB == nil {
			routeAcc.BalanceNHB = big.NewInt(0)
		}
		routeAcc.BalanceNHB.Add(routeAcc.BalanceNHB, routed)
		if err := sp.setAccount(result.OwnerWallet[:], routeAcc); err != nil {
			return err
		}
	}
	sp.AppendEvent(events.FeeApplied{
		Payer:             payer,
		Domain:            fees.NormalizeDomain(domain),
		Gross:             new(big.Int).Set(gross),
		Fee:               cloneBigInt(result.Fee),
		Net:               cloneBigInt(result.Net),
		PolicyVersion:     result.PolicyVersion,
		OwnerWallet:       result.OwnerWallet,
		FreeTierApplied:   result.FreeTierApplied,
		FreeTierLimit:     result.FreeTierLimit,
		FreeTierRemaining: result.FreeTierRemaining,
		UsageCount:        result.Counter,
		WindowStart:       result.WindowStart,
		FeeBasisPoints:    result.FeeBasisPoints,
	}.Event())
	return nil
}

func isZeroAddress(addr [20]byte) bool {
	for _, b := range addr {
		if b != 0 {
			return false
		}
	}
	return true
}

func feeWindowStart(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Time{}
	}
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func sameFeeWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	ua := a.UTC()
	ub := b.UTC()
	return ua.Year() == ub.Year() && ua.Month() == ub.Month()
}

func quotaEpochFor(q nativecommon.Quota, ts time.Time) uint64 {
	seconds := q.EpochSeconds
	if seconds == 0 {
		seconds = 60
	}
	if seconds == 0 {
		return 0
	}
	unix := ts.Unix()
	if unix < 0 {
		unix = 0
	}
	return uint64(unix) / uint64(seconds)
}

func (sp *StateProcessor) applyQuota(module string, addr []byte, addReq uint32, addNHB uint64) error {
	if sp == nil {
		return fmt.Errorf("quota: state processor unavailable")
	}
	moduleName := normalizeModuleName(module)
	if moduleName == "" {
		return nil
	}
	quota, ok := sp.quotaConfig[moduleName]
	if !ok || (quota.MaxRequestsPerMin == 0 && quota.MaxNHBPerEpoch == 0) {
		return nil
	}
	if addReq == 0 && addNHB == 0 {
		return nil
	}
	store, err := sp.quotaStoreHandle()
	if err != nil {
		return err
	}
	nowEpoch := quotaEpochFor(quota, sp.blockTimestamp())
	_, err = nativecommon.Apply(store, moduleName, nowEpoch, addr, quota, addReq, addNHB)
	if err != nil {
		sp.emitQuotaExceeded(moduleName, addr, nowEpoch, err)
		return fmt.Errorf("quota: %s: %w", moduleName, err)
	}
	return nil
}

func (sp *StateProcessor) emitQuotaExceeded(module string, addr []byte, epoch uint64, cause error) {
	if sp == nil || cause == nil {
		return
	}
	reason := "unknown"
	switch {
	case errors.Is(cause, nativecommon.ErrQuotaRequestsExceeded):
		reason = "requests"
	case errors.Is(cause, nativecommon.ErrQuotaNHBCapExceeded):
		reason = "nhb"
	case errors.Is(cause, nativecommon.ErrQuotaCounterOverflow):
		reason = "overflow"
	}
	attrs := map[string]string{
		"module": module,
		"epoch":  strconv.FormatUint(epoch, 10),
		"reason": reason,
	}
	if len(addr) > 0 {
		if len(addr) == common.AddressLength {
			attrs["address"] = crypto.MustNewAddress(crypto.NHBPrefix, addr).String()
		} else {
			attrs["address"] = hex.EncodeToString(addr)
		}
	}
	sp.AppendEvent(&types.Event{Type: "QuotaExceeded", Attributes: attrs})
}

func (sp *StateProcessor) pruneQuotaCounters(ts time.Time) error {
	if sp == nil {
		return nil
	}
	if len(sp.quotaConfig) == 0 {
		return nil
	}
	store, err := sp.quotaStoreHandle()
	if err != nil {
		return err
	}
	for module, quota := range sp.quotaConfig {
		seconds := quota.EpochSeconds
		if seconds == 0 {
			seconds = 60
		}
		if seconds == 0 {
			continue
		}
		unix := ts.Unix()
		if unix < 0 {
			continue
		}
		current := uint64(unix) / uint64(seconds)
		if current < 2 {
			continue
		}
		pruneEpoch := current - 2
		if err := store.PruneEpoch(module, pruneEpoch); err != nil {
			return fmt.Errorf("quota: prune %s epoch %d: %w", module, pruneEpoch, err)
		}
	}
	return nil
}

// SetEscrowFeeTreasury configures the address receiving escrow fees during
// release transitions.
func (sp *StateProcessor) SetEscrowFeeTreasury(addr [20]byte) {
	sp.escrowFeeTreasury = addr
}

// BeginBlock records the execution context for the block currently being applied.
func (sp *StateProcessor) BeginBlock(height uint64, timestamp time.Time) {
	if sp == nil {
		return
	}
	sp.execContext = &blockExecutionContext{
		height:    height,
		timestamp: timestamp.UTC(),
	}
}

// EndBlock clears any active block execution context.
func (sp *StateProcessor) EndBlock() {
	if sp == nil {
		return
	}
	sp.execContext = nil
}

func (sp *StateProcessor) blockTimestamp() time.Time {
	if sp != nil && sp.execContext != nil {
		return sp.execContext.timestamp
	}
	return sp.now().UTC()
}

func (sp *StateProcessor) blockHeight() uint64 {
	if sp != nil && sp.execContext != nil {
		return sp.execContext.height
	}
	return 0
}

// EngagementConfig returns the configuration currently used for engagement
// scoring.
func (sp *StateProcessor) EngagementConfig() engagement.Config {
	return sp.engagementConfig
}

// SetEngagementConfig replaces the engagement configuration. Callers must
// ensure the new configuration is valid network wide.
func (sp *StateProcessor) SetEngagementConfig(cfg engagement.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.engagementConfig = cfg
	return nil
}

// EpochConfig returns the active epoch configuration.
func (sp *StateProcessor) EpochConfig() epoch.Config {
	return sp.epochConfig
}

// SetEpochConfig replaces the epoch configuration after validation.
func (sp *StateProcessor) SetEpochConfig(cfg epoch.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.epochConfig = cfg
	sp.pruneEpochHistory()
	return nil
}

// RewardConfig returns the current reward emission configuration.
func (sp *StateProcessor) RewardConfig() rewards.Config {
	return sp.rewardConfig.Clone()
}

// SetRewardConfig updates the reward configuration after validation.
func (sp *StateProcessor) SetRewardConfig(cfg rewards.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.rewardConfig = cfg.Clone()
	sp.rewardAccrual = nil
	sp.pruneRewardHistory()
	return sp.persistRewardHistory()
}

// PotsoRewardConfig returns the current POTSO reward configuration snapshot.
func (sp *StateProcessor) PotsoRewardConfig() potso.RewardConfig {
	return clonePotsoRewardConfig(sp.potsoRewardConfig)
}

// SetPotsoRewardConfig replaces the POTSO reward distribution configuration.
func (sp *StateProcessor) SetPotsoRewardConfig(cfg potso.RewardConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.potsoRewardConfig = clonePotsoRewardConfig(cfg)
	return nil
}

func clonePotsoRewardConfig(cfg potso.RewardConfig) potso.RewardConfig {
	clone := cfg
	if cfg.MinPayoutWei != nil {
		clone.MinPayoutWei = new(big.Int).Set(cfg.MinPayoutWei)
	}
	if cfg.EmissionPerEpoch != nil {
		clone.EmissionPerEpoch = new(big.Int).Set(cfg.EmissionPerEpoch)
	}
	return clone
}

// PotsoWeightConfig returns the current POTSO weight configuration snapshot.
func (sp *StateProcessor) PotsoWeightConfig() potso.WeightParams {
	return clonePotsoWeightConfig(sp.potsoWeightConfig)
}

// SetPotsoWeightConfig replaces the POTSO weight configuration.
func (sp *StateProcessor) SetPotsoWeightConfig(cfg potso.WeightParams) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	sp.potsoWeightConfig = clonePotsoWeightConfig(cfg)
	return nil
}

func clonePotsoWeightConfig(cfg potso.WeightParams) potso.WeightParams {
	clone := cfg
	if cfg.MinStakeToWinWei != nil {
		clone.MinStakeToWinWei = new(big.Int).Set(cfg.MinStakeToWinWei)
	} else {
		clone.MinStakeToWinWei = big.NewInt(0)
	}
	if cfg.MinStakeToEarnWei != nil {
		clone.MinStakeToEarnWei = new(big.Int).Set(cfg.MinStakeToEarnWei)
	} else {
		clone.MinStakeToEarnWei = big.NewInt(0)
	}
	return clone
}

func (sp *StateProcessor) maybeProcessPotsoRewards(height uint64, timestamp int64) error {
	cfg := sp.potsoRewardConfig
	if cfg.EpochLengthBlocks == 0 {
		return nil
	}
	if cfg.EmissionPerEpoch == nil || cfg.EmissionPerEpoch.Sign() <= 0 {
		return nil
	}
	currentEpoch := height / cfg.EpochLengthBlocks
	if currentEpoch == 0 {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		return err
	}
	target := currentEpoch - 1
	start := uint64(0)
	if ok {
		if lastProcessed >= target {
			return nil
		}
		start = lastProcessed + 1
	}
	if start > target {
		return nil
	}
	day := time.Unix(timestamp, 0).UTC().Format(potso.DayFormat)
	for epochNumber := start; epochNumber <= target; epochNumber++ {
		if err := sp.processPotsoRewardEpoch(manager, cfg, epochNumber, day, timestamp); err != nil {
			return err
		}
		if err := manager.PotsoRewardsSetLastProcessedEpoch(epochNumber); err != nil {
			return err
		}
	}
	return nil
}

func (sp *StateProcessor) processPotsoRewardEpoch(manager *nhbstate.Manager, cfg potso.RewardConfig, epochNumber uint64, day string, settledAt int64) error {
	if manager == nil {
		return fmt.Errorf("potso: state manager unavailable")
	}
	if _, exists, err := manager.PotsoRewardsGetMeta(epochNumber); err != nil {
		return err
	} else if exists {
		return nil
	}

	stakeOwners, err := manager.PotsoStakeOwners()
	if err != nil {
		return err
	}
	stakeTotals := make(map[[20]byte]*big.Int, len(stakeOwners))
	for _, owner := range stakeOwners {
		amount, err := manager.PotsoStakeBondedTotal(owner)
		if err != nil {
			return err
		}
		if amount == nil || amount.Sign() <= 0 {
			continue
		}
		stakeTotals[owner] = new(big.Int).Set(amount)
	}

	participants, err := manager.PotsoListParticipants(day)
	if err != nil {
		return err
	}

	prevEngagement := make(map[[20]byte]uint64)
	if epochNumber > 0 {
		if stored, ok, err := manager.PotsoMetricsGetSnapshot(epochNumber - 1); err != nil {
			return err
		} else if ok && stored != nil {
			for _, entry := range stored.Entries {
				prevEngagement[entry.Address] = entry.Engagement
			}
		}
	}

	addressSet := make(map[[20]byte]struct{})
	for owner := range stakeTotals {
		addressSet[owner] = struct{}{}
	}
	for _, addr := range participants {
		addressSet[addr] = struct{}{}
	}
	for addr := range prevEngagement {
		addressSet[addr] = struct{}{}
	}

	addresses := make([][20]byte, 0, len(addressSet))
	for addr := range addressSet {
		addresses = append(addresses, addr)
	}
	sort.Slice(addresses, func(i, j int) bool {
		return bytes.Compare(addresses[i][:], addresses[j][:]) < 0
	})

	entries := make([]potso.RewardSnapshotEntry, 0, len(addresses))
	for _, addr := range addresses {
		meter, _, err := manager.PotsoGetMeter(addr, day)
		if err != nil {
			return err
		}
		engagementMeter := potso.EngagementMeter{}
		if meter != nil {
			engagementMeter.TxCount = meter.TxCount
			engagementMeter.EscrowCount = meter.EscrowEvents
			engagementMeter.UptimeDevices = meter.UptimeSeconds / 60
		}
		if err := manager.PotsoMetricsSetMeter(epochNumber, addr, &engagementMeter); err != nil {
			return err
		}
		stake := big.NewInt(0)
		if value, ok := stakeTotals[addr]; ok && value != nil {
			stake = new(big.Int).Set(value)
		}
		prev := prevEngagement[addr]
		entries = append(entries, potso.RewardSnapshotEntry{
			Address:            addr,
			Stake:              stake,
			Meter:              engagementMeter,
			PreviousEngagement: prev,
		})
	}

	snapshot := potso.RewardSnapshot{
		Epoch:   epochNumber,
		Day:     potso.NormaliseDay(day),
		Entries: entries,
	}

	emission := big.NewInt(0)
	if cfg.EmissionPerEpoch != nil {
		emission = new(big.Int).Set(cfg.EmissionPerEpoch)
	}
	treasuryAcc, err := manager.GetAccount(cfg.TreasuryAddress[:])
	if err != nil {
		return err
	}
	if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
	}
	treasuryBalance := new(big.Int).Set(treasuryAcc.BalanceZNHB)
	budget := new(big.Int).Set(emission)
	if treasuryBalance.Cmp(budget) < 0 {
		budget = new(big.Int).Set(treasuryBalance)
	}

	weightCfg := sp.potsoWeightConfig
	weightCfg.AlphaStakeBps = cfg.AlphaStakeBps
	outcome, err := potso.ComputeRewards(cfg, weightCfg, snapshot, budget)
	if err != nil {
		return err
	}

	if outcome.WeightSnapshot != nil {
		if err := manager.PotsoMetricsSetSnapshot(epochNumber, outcome.WeightSnapshot.ToStored()); err != nil {
			return err
		}
	}

	winnersAddrs := make([][20]byte, 0, len(outcome.Winners))
	for _, winner := range outcome.Winners {
		winnersAddrs = append(winnersAddrs, winner.Address)
	}

	payoutMode := cfg.EffectivePayoutMode()
	totalPaid := new(big.Int).Set(outcome.TotalPaid)
	for _, winner := range outcome.Winners {
		if err := manager.PotsoRewardsSetPayout(epochNumber, winner.Address, winner.Amount); err != nil {
			return err
		}
	}
	if totalPaid.Sign() > 0 && treasuryBalance.Cmp(totalPaid) < 0 {
		return potso.ErrInsufficientTreasury
	}
	switch payoutMode {
	case potso.RewardPayoutModeClaim:
		for _, winner := range outcome.Winners {
			claim := &potso.RewardClaim{
				Amount:    new(big.Int).Set(winner.Amount),
				Claimed:   false,
				ClaimedAt: 0,
				Mode:      potso.RewardPayoutModeClaim,
			}
			if err := manager.PotsoRewardsSetClaim(epochNumber, winner.Address, claim); err != nil {
				return err
			}
			if winner.Amount.Sign() > 0 {
				if evt := (events.PotsoRewardReady{Epoch: epochNumber, Address: winner.Address, Amount: new(big.Int).Set(winner.Amount), Mode: potso.RewardPayoutModeClaim}).Event(); evt != nil {
					sp.AppendEvent(evt)
				}
			}
		}
	default:
		if totalPaid.Sign() > 0 {
			treasuryAcc.BalanceZNHB = new(big.Int).Sub(treasuryAcc.BalanceZNHB, totalPaid)
			if err := manager.PutAccount(cfg.TreasuryAddress[:], treasuryAcc); err != nil {
				return err
			}
		}
		for _, winner := range outcome.Winners {
			if totalPaid.Sign() > 0 && winner.Amount.Sign() > 0 {
				account, err := manager.GetAccount(winner.Address[:])
				if err != nil {
					return err
				}
				if account.BalanceZNHB == nil {
					account.BalanceZNHB = big.NewInt(0)
				}
				account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, winner.Amount)
				if err := manager.PutAccount(winner.Address[:], account); err != nil {
					return err
				}
			}
			claim := &potso.RewardClaim{
				Amount:    new(big.Int).Set(winner.Amount),
				Claimed:   winner.Amount.Sign() <= 0 || totalPaid.Sign() > 0,
				ClaimedAt: uint64(settledAt),
				Mode:      potso.RewardPayoutModeAuto,
			}
			if err := manager.PotsoRewardsSetClaim(epochNumber, winner.Address, claim); err != nil {
				return err
			}
			if winner.Amount.Sign() > 0 && totalPaid.Sign() > 0 {
				if err := manager.PotsoRewardsAppendHistory(winner.Address, potso.RewardHistoryEntry{Epoch: epochNumber, Amount: new(big.Int).Set(winner.Amount), Mode: potso.RewardPayoutModeAuto}); err != nil {
					return err
				}
				if evt := (events.PotsoRewardPaid{Epoch: epochNumber, Address: winner.Address, Amount: new(big.Int).Set(winner.Amount), Mode: potso.RewardPayoutModeAuto}).Event(); evt != nil {
					sp.AppendEvent(evt)
				}
			}
		}
	}

	if err := manager.PotsoRewardsSetWinners(epochNumber, winnersAddrs); err != nil {
		return err
	}

	stakeTotal := big.NewInt(0)
	engagementTotal := big.NewInt(0)
	if outcome.WeightSnapshot != nil {
		if outcome.WeightSnapshot.TotalStake != nil {
			stakeTotal = new(big.Int).Set(outcome.WeightSnapshot.TotalStake)
		}
		engagementTotal = new(big.Int).SetUint64(outcome.WeightSnapshot.TotalEngagement)
	}

	meta := &potso.RewardEpochMeta{
		Epoch:           epochNumber,
		Day:             snapshot.Day,
		StakeTotal:      stakeTotal,
		EngagementTotal: engagementTotal,
		AlphaBps:        cfg.AlphaStakeBps,
		Emission:        new(big.Int).Set(emission),
		Budget:          new(big.Int).Set(outcome.Budget),
		TotalPaid:       new(big.Int).Set(outcome.TotalPaid),
		Remainder:       new(big.Int).Set(outcome.Remainder),
		Winners:         uint64(len(outcome.Winners)),
	}
	if err := manager.PotsoRewardsSetMeta(epochNumber, meta); err != nil {
		return err
	}

	if evt := (events.PotsoRewardEpoch{
		Epoch:     epochNumber,
		TotalPaid: new(big.Int).Set(outcome.TotalPaid),
		Winners:   uint64(len(outcome.Winners)),
		Emission:  new(big.Int).Set(emission),
		Budget:    new(big.Int).Set(outcome.Budget),
		Remainder: new(big.Int).Set(outcome.Remainder),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

// CurrentRoot returns the last committed state root.
func (sp *StateProcessor) CurrentRoot() common.Hash {
	return sp.committedRoot
}

// PendingRoot returns the root of the trie including in-memory mutations.
func (sp *StateProcessor) PendingRoot() common.Hash {
	return sp.Trie.Hash()
}

// ResetToRoot discards any in-memory changes and reloads the trie at the
// provided root hash.
func (sp *StateProcessor) ResetToRoot(root common.Hash) error {
	if err := sp.Trie.Reset(root); err != nil {
		return err
	}
	sp.committedRoot = root
	return nil
}

// Commit persists the current trie contents and returns the resulting state
// root.
func (sp *StateProcessor) Commit(blockNumber uint64) (common.Hash, error) {
	newRoot, err := sp.Trie.Commit(sp.committedRoot, blockNumber)
	if err != nil {
		return common.Hash{}, err
	}
	sp.committedRoot = newRoot
	return newRoot, nil
}

// Copy returns a shallow clone of the state processor that can be used for
// speculative execution without mutating the canonical state.
func (sp *StateProcessor) Copy() (*StateProcessor, error) {
	trieCopy, err := sp.Trie.Copy()
	if err != nil {
		return nil, err
	}
	usernameCopy := make(map[string][]byte, len(sp.usernameToAddr))
	for k, v := range sp.usernameToAddr {
		usernameCopy[k] = append([]byte(nil), v...)
	}
	validatorCopy := make(map[string]*big.Int, len(sp.ValidatorSet))
	for k, v := range sp.ValidatorSet {
		validatorCopy[k] = new(big.Int).Set(v)
	}
	eligibleCopy := make(map[string]*big.Int, len(sp.EligibleValidators))
	for k, v := range sp.EligibleValidators {
		eligibleCopy[k] = new(big.Int).Set(v)
	}
	eventsCopy := make([]types.Event, len(sp.events))
	for i := range sp.events {
		attrs := make(map[string]string, len(sp.events[i].Attributes))
		for k, v := range sp.events[i].Attributes {
			attrs[k] = v
		}
		eventsCopy[i] = types.Event{Type: sp.events[i].Type, Attributes: attrs}
	}
	historyCopy := make([]epoch.Snapshot, len(sp.epochHistory))
	for i := range sp.epochHistory {
		snapshot := sp.epochHistory[i]
		totalWeight := big.NewInt(0)
		if snapshot.TotalWeight != nil {
			totalWeight = new(big.Int).Set(snapshot.TotalWeight)
		}
		copied := epoch.Snapshot{
			Epoch:       snapshot.Epoch,
			Height:      snapshot.Height,
			FinalizedAt: snapshot.FinalizedAt,
			TotalWeight: totalWeight,
			Weights:     make([]epoch.Weight, len(snapshot.Weights)),
			Selected:    make([][]byte, len(snapshot.Selected)),
		}
		for j := range snapshot.Weights {
			stake := big.NewInt(0)
			if snapshot.Weights[j].Stake != nil {
				stake = new(big.Int).Set(snapshot.Weights[j].Stake)
			}
			composite := big.NewInt(0)
			if snapshot.Weights[j].Composite != nil {
				composite = new(big.Int).Set(snapshot.Weights[j].Composite)
			}
			copied.Weights[j] = epoch.Weight{
				Address:    append([]byte(nil), snapshot.Weights[j].Address...),
				Stake:      stake,
				Engagement: snapshot.Weights[j].Engagement,
				Composite:  composite,
			}
		}
		for j := range snapshot.Selected {
			copied.Selected[j] = append([]byte(nil), snapshot.Selected[j]...)
		}
		historyCopy[i] = copied
	}

	rewardHistoryCopy := make([]rewards.EpochSettlement, len(sp.rewardHistory))
	for i := range sp.rewardHistory {
		rewardHistoryCopy[i] = sp.rewardHistory[i].Clone()
	}

	var quotaCopy map[string]nativecommon.Quota
	if len(sp.quotaConfig) > 0 {
		quotaCopy = make(map[string]nativecommon.Quota, len(sp.quotaConfig))
		for module, quota := range sp.quotaConfig {
			quotaCopy[module] = quota
		}
	}

	return &StateProcessor{
		Trie:               trieCopy,
		stateDB:            sp.stateDB,
		LoyaltyEngine:      sp.LoyaltyEngine,
		EscrowEngine:       sp.EscrowEngine,
		TradeEngine:        sp.TradeEngine,
		pauses:             sp.pauses,
		usernameToAddr:     usernameCopy,
		ValidatorSet:       validatorCopy,
		EligibleValidators: eligibleCopy,
		committedRoot:      sp.committedRoot,
		events:             eventsCopy,
		nowFunc:            sp.nowFunc,
		engagementConfig:   sp.engagementConfig,
		epochConfig:        sp.epochConfig,
		epochHistory:       historyCopy,
		rewardConfig:       sp.rewardConfig.Clone(),
		rewardHistory:      rewardHistoryCopy,
		potsoRewardConfig:  clonePotsoRewardConfig(sp.potsoRewardConfig),
		potsoWeightConfig:  clonePotsoWeightConfig(sp.potsoWeightConfig),
		paymasterEnabled:   sp.paymasterEnabled,
		paymasterLimits:    sp.paymasterLimits.Clone(),
		quotaConfig:        quotaCopy,
		intentTTL:          sp.intentTTL,
		feePolicy:          sp.feePolicy.Clone(),
	}, nil
}

// PaymasterEnabled reports whether transaction sponsorship is currently active.
func (sp *StateProcessor) PaymasterEnabled() bool {
	if sp == nil {
		return false
	}
	return sp.paymasterEnabled
}

// SetPaymasterEnabled toggles the transaction sponsorship module.
func (sp *StateProcessor) SetPaymasterEnabled(enabled bool) {
	if sp == nil {
		return
	}
	sp.paymasterEnabled = enabled
}

func (sp *StateProcessor) ApplyTransaction(tx *types.Transaction) error {
	_, err := sp.executeTransaction(tx)
	return err
}

// ExecuteTransaction applies the transaction and returns execution metadata.
func (sp *StateProcessor) ExecuteTransaction(tx *types.Transaction) (*SimulationResult, error) {
	return sp.executeTransaction(tx)
}

func (sp *StateProcessor) executeTransaction(tx *types.Transaction) (*SimulationResult, error) {
	if !types.IsValidChainID(tx.ChainID) {
		return nil, fmt.Errorf("%w: %v", ErrInvalidChainID, tx.ChainID)
	}
	var (
		intentManager *nhbstate.Manager
		intentExpiry  uint64
		recordIntent  bool
	)
	if len(tx.IntentRef) > 0 {
		intentManager = nhbstate.NewManager(sp.Trie)
		ttl := sp.intentTTL
		if ttl <= 0 {
			ttl = defaultIntentTTL
		}
		expiry, err := intentManager.IntentRegistryValidate(tx.IntentRef, tx.IntentExpiry, sp.blockTimestamp(), ttl)
		if err != nil {
			return nil, err
		}
		intentExpiry = expiry
		recordIntent = true
	}
	var (
		sender        []byte
		senderAccount *types.Account
		err           error
	)
	if tx.Type != types.TxTypeMint {
		sender, senderAccount, err = sp.validateSenderAccount(tx)
		if err != nil {
			return nil, err
		}
	}
	start := len(sp.events)
	var result *SimulationResult
	switch tx.Type {
	case types.TxTypeMint:
		err = sp.applyMintTransaction(tx)
		result = &SimulationResult{}
	case types.TxTypeTransfer:
		result, err = sp.applyEvmTransaction(tx)
	default:
		err = sp.handleNativeTransaction(tx, sender, senderAccount)
		result = &SimulationResult{}
	}
	if err != nil {
		if len(sp.events) > start {
			sp.events = sp.events[:start]
		}
		return nil, err
	}
	if recordIntent {
		if intentManager == nil {
			intentManager = nhbstate.NewManager(sp.Trie)
		}
		if err := intentManager.IntentRegistryConsume(tx.IntentRef, intentExpiry); err != nil {
			if len(sp.events) > start {
				sp.events = sp.events[:start]
			}
			return nil, err
		}
		if err := sp.emitPaymentIntentConsumed(tx); err != nil {
			if len(sp.events) > start {
				sp.events = sp.events[:start]
			}
			return nil, err
		}
	}
	if result == nil {
		result = &SimulationResult{}
	}
	newEvents := sp.events[start:]
	if len(newEvents) > 0 {
		copied := make([]types.Event, len(newEvents))
		for i := range newEvents {
			attrs := make(map[string]string, len(newEvents[i].Attributes))
			for k, v := range newEvents[i].Attributes {
				attrs[k] = v
			}
			copied[i] = types.Event{Type: newEvents[i].Type, Attributes: attrs}
		}
		result.Events = copied
	} else {
		result.Events = nil
	}
	return result, nil
}

type sponsorshipRuntime struct {
	sponsor  common.Address
	sender   common.Address
	budget   *big.Int
	gasPrice *big.Int
	txHash   [32]byte
	merchant string
	device   string
	day      string
}

func (sp *StateProcessor) validateSenderAccount(tx *types.Transaction) ([]byte, *types.Account, error) {
	sender, err := tx.From()
	if err != nil {
		return nil, nil, err
	}
	account, err := sp.getAccount(sender)
	if err != nil {
		return nil, nil, err
	}
	if tx.Nonce != account.Nonce {
		return nil, nil, fmt.Errorf("%w: account=%d tx=%d", ErrNonceMismatch, account.Nonce, tx.Nonce)
	}
	return sender, account, nil
}

// --- EVM path (Geth v1.16.x) ---
func (sp *StateProcessor) applyEvmTransaction(tx *types.Transaction) (*SimulationResult, error) {
	exec := &SimulationResult{}
	from, err := tx.From()
	if err != nil {
		return nil, err
	}
	isTransfer := tx.Type == types.TxTypeTransfer && tx.Value != nil && tx.Value.Sign() > 0
	var txHash [32]byte
	var txHashReady bool
	var refundOrigin [32]byte
	var hasRefundOrigin bool
	if isTransfer {
		hashBytes, err := tx.Hash()
		if err != nil {
			return nil, fmt.Errorf("refund ledger: compute hash: %w", err)
		}
		if len(hashBytes) != len(txHash) {
			return nil, fmt.Errorf("refund ledger: expected 32-byte tx hash, got %d", len(hashBytes))
		}
		copy(txHash[:], hashBytes)
		txHashReady = true
		trimmedRefund := strings.TrimSpace(tx.RefundOf)
		if trimmedRefund != "" {
			originHash, err := bank.ParseTxHash(trimmedRefund)
			if err != nil {
				return nil, err
			}
			manager := nhbstate.NewManager(sp.Trie)
			if err := bank.ValidateRefund(manager, originHash, tx.Value); err != nil {
				return nil, err
			}
			refundOrigin = originHash
			hasRefundOrigin = true
		}
	}
	parentRoot := sp.Trie.Hash()
	statedb, err := gethstate.New(parentRoot, sp.stateDB)
	if err != nil {
		return nil, fmt.Errorf("statedb init: %w", err)
	}

	fromAddr := common.BytesToAddress(from)
	var toAddrPtr *common.Address
	if tx.To != nil {
		addr := common.BytesToAddress(tx.To)
		toAddrPtr = &addr
	}

	blockTime := sp.blockTimestamp()
	blockCtx := gethvm.BlockContext{
		CanTransfer: gethcore.CanTransfer,
		Transfer:    gethcore.Transfer,
		GetHash: func(uint64) common.Hash {
			return common.Hash{}
		},
		Coinbase:    common.Address{},
		BlockNumber: new(big.Int).SetUint64(sp.blockHeight()),
		Time:        uint64(blockTime.Unix()),
		Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
	}

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		return nil, err
	}
	var sponsorshipCtx *sponsorshipRuntime
	var txHashBytes []byte
	if len(tx.Paymaster) > 0 {
		txHashBytes, _ = tx.Hash()
	}

	msg := gethcore.Message{
		From:          fromAddr,
		To:            toAddrPtr,
		Nonce:         tx.Nonce,
		Value:         tx.Value,
		GasLimit:      tx.GasLimit,
		GasPrice:      tx.GasPrice,
		GasFeeCap:     tx.GasPrice,
		GasTipCap:     tx.GasPrice,
		Data:          tx.Data,
		AccessList:    nil,
		BlobGasFeeCap: nil,
		BlobHashes:    nil,
	}
	txCtx := gethcore.NewEVMTxContext(&msg)

	evm := gethvm.NewEVM(blockCtx, statedb, params.TestChainConfig, gethvm.Config{NoBaseFee: true})
	evm.SetTxContext(txCtx)

	if assessment != nil {
		switch assessment.Status {
		case SponsorshipStatusReady:
			ctx := &sponsorshipRuntime{
				sponsor:  assessment.Sponsor,
				sender:   fromAddr,
				budget:   big.NewInt(0),
				gasPrice: big.NewInt(0),
				merchant: assessment.merchant,
				device:   assessment.deviceID,
				day:      assessment.day,
			}
			if len(txHashBytes) > 0 {
				ctx.txHash = bytesToHash32(txHashBytes)
			}
			if assessment.GasCost != nil {
				ctx.budget = new(big.Int).Set(assessment.GasCost)
			}
			if assessment.GasPrice != nil {
				ctx.gasPrice = new(big.Int).Set(assessment.GasPrice)
			}
			if ctx.budget.Sign() > 0 {
				budget := uint256.MustFromBig(ctx.budget)
				statedb.SubBalance(ctx.sponsor, budget, tracing.BalanceChangeTransfer)
				statedb.AddBalance(ctx.sender, budget, tracing.BalanceChangeTransfer)
			}
			sponsorshipCtx = ctx
		case SponsorshipStatusNone:
			// no sponsorship requested
		default:
			if len(txHashBytes) > 0 {
				sp.emitSponsorshipFailureEvent(fromAddr, assessment, bytesToHash32(txHashBytes))
			}
		}
	}

	gp := new(gethcore.GasPool).AddGas(tx.GasLimit)
	result, err := gethcore.ApplyMessage(evm, &msg, gp)
	if err != nil {
		return nil, fmt.Errorf("ApplyMessage: %w", err)
	}
	if result != nil && result.Err != nil {
		return nil, fmt.Errorf("EVM error: %w", result.Err)
	}

	gasPriceUsed := tx.GasPrice
	var paymasterCharged *big.Int
	if sponsorshipCtx != nil {
		usedCost := new(big.Int).Mul(new(big.Int).SetUint64(result.UsedGas), sponsorshipCtx.gasPrice)
		budget := new(big.Int).Set(sponsorshipCtx.budget)
		refund := new(big.Int).Sub(budget, usedCost)
		if refund.Sign() < 0 {
			refund = big.NewInt(0)
		}
		if refund.Sign() > 0 {
			refundUint := uint256.MustFromBig(refund)
			statedb.SubBalance(sponsorshipCtx.sender, refundUint, tracing.BalanceChangeTransfer)
			statedb.AddBalance(sponsorshipCtx.sponsor, refundUint, tracing.BalanceChangeTransfer)
		}
		charged := new(big.Int).Sub(budget, refund)
		if charged.Sign() < 0 {
			charged = big.NewInt(0)
		}
		paymasterCharged = new(big.Int).Set(charged)
		sp.emitSponsorshipSuccessEvent(sponsorshipCtx, result.UsedGas, charged, refund)
		if sponsorshipCtx.gasPrice != nil && sponsorshipCtx.gasPrice.Sign() > 0 {
			gasPriceUsed = sponsorshipCtx.gasPrice
		}
	}

	exec.GasUsed = result.UsedGas
	if gasPriceUsed != nil {
		exec.GasCost = new(big.Int).Mul(new(big.Int).SetUint64(result.UsedGas), new(big.Int).Set(gasPriceUsed))
	}

	newRoot, err := statedb.Commit(0, false, false)
	if err != nil {
		return nil, fmt.Errorf("statedb commit: %w", err)
	}
	if err := sp.Trie.Reset(newRoot); err != nil {
		return nil, fmt.Errorf("trie reset: %w", err)
	}
	if isTransfer && txHashReady {
		amount := new(big.Int).Set(tx.Value)
		timestamp := uint64(blockTime.Unix())
		manager := nhbstate.NewManager(sp.Trie)
		if hasRefundOrigin {
			if err := bank.RecordRefund(manager, refundOrigin, txHash, amount, timestamp); err != nil {
				return nil, err
			}
		} else {
			if err := bank.RecordOrigin(manager, txHash, amount, timestamp); err != nil {
				return nil, err
			}
		}
	}

	if sponsorshipCtx != nil {
		if err := sp.recordPaymasterUsage(sponsorshipCtx, paymasterCharged); err != nil {
			return nil, err
		}
	}

	fromAcc, err := sp.getAccount(from)
	if err != nil {
		return nil, err
	}
	var toAcc *types.Account
	if tx.To != nil {
		toAcc, err = sp.getAccount(tx.To)
		if err != nil {
			return nil, err
		}
	}

	if err := sp.applyTransactionFee(tx, from, fromAcc, toAcc); err != nil {
		return nil, err
	}

	if tx.To != nil {
		ctx := &loyalty.BaseRewardContext{
			From:  append([]byte(nil), from...),
			To:    append([]byte(nil), tx.To...),
			Token: "NHB",
			Amount: func() *big.Int {
				if tx.Value == nil {
					return big.NewInt(0)
				}
				return new(big.Int).Set(tx.Value)
			}(),
			Timestamp:   blockTime,
			FromAccount: fromAcc,
			ToAccount:   toAcc,
		}
		sp.LoyaltyEngine.OnTransactionSuccess(sp, ctx)
	}

	if err := sp.setAccount(from, fromAcc); err != nil {
		return nil, err
	}
	if tx.To != nil && toAcc != nil {
		if err := sp.setAccount(tx.To, toAcc); err != nil {
			return nil, err
		}
	}
	if sponsorshipCtx != nil && len(tx.Paymaster) > 0 {
		sponsorAcc, err := sp.getAccount(tx.Paymaster)
		if err != nil {
			return nil, err
		}
		if err := sp.setAccount(tx.Paymaster, sponsorAcc); err != nil {
			return nil, err
		}
	}

	if err := sp.recordEngagementActivity(from, sp.blockTimestamp(), 1, 0, 0); err != nil {
		return nil, err
	}

	fmt.Printf("EVM transaction processed. Gas used: %d. Output: %x\n", result.UsedGas, result.ReturnData)
	return exec, nil
}

// --- Native handlers (original semantics + new dispute flow) ---

func (sp *StateProcessor) applyMintTransaction(tx *types.Transaction) error {
	voucher, signature, err := decodeMintTransaction(tx.Data)
	if err != nil {
		return err
	}
	if voucher == nil {
		return fmt.Errorf("%w: voucher required", ErrMintInvalidPayload)
	}
	amount, err := voucher.AmountBig()
	if err != nil {
		return err
	}
	if voucher.ChainID != MintChainID {
		return ErrMintInvalidChainID
	}
	blockTime := sp.blockTimestamp()
	if voucher.Expiry <= blockTime.Unix() {
		return ErrMintExpired
	}
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		return err
	}
	if len(signature) != 65 {
		return fmt.Errorf("invalid signature length")
	}
	digest := ethcrypto.Keccak256(canonical)
	pubKey, err := ethcrypto.SigToPub(digest, signature)
	if err != nil {
		return fmt.Errorf("recover signer: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	var recoveredBytes [20]byte
	copy(recoveredBytes[:], recovered.Bytes())

	token := voucher.NormalizedToken()
	var requiredRole string
	switch token {
	case "NHB":
		requiredRole = "MINTER_NHB"
	case "ZNHB":
		requiredRole = "MINTER_ZNHB"
	default:
		return fmt.Errorf("unsupported token %q", voucher.Token)
	}

	invoiceID := voucher.TrimmedInvoiceID()
	if invoiceID == "" {
		return fmt.Errorf("invoiceId required")
	}
	recipientRef := voucher.TrimmedRecipient()
	if recipientRef == "" {
		return fmt.Errorf("recipient required")
	}

	manager := nhbstate.NewManager(sp.Trie)
	if !manager.HasRole(requiredRole, recoveredBytes[:]) {
		return ErrMintInvalidSigner
	}
	key := nhbstate.MintInvoiceKey(invoiceID)
	var used bool
	if ok, err := manager.KVGet(key, &used); err != nil {
		return err
	} else if ok && used {
		return ErrMintInvoiceUsed
	}

	var recipient [20]byte
	if decoded, err := crypto.DecodeAddress(recipientRef); err == nil {
		copy(recipient[:], decoded.Bytes())
	} else {
		resolved, ok := manager.IdentityResolve(recipientRef)
		if !ok || resolved == nil {
			return fmt.Errorf("recipient not found: %s", recipientRef)
		}
		recipient = resolved.Primary
	}

	account, err := manager.GetAccount(recipient[:])
	if err != nil {
		return err
	}
	switch token {
	case "NHB":
		account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
	if err := manager.PutAccount(recipient[:], account); err != nil {
		return err
	}
	if err := manager.KVPut(key, true); err != nil {
		return err
	}

	hashBytes, err := tx.Hash()
	if err != nil {
		return err
	}
	txHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
	voucherHash, err := MintVoucherHash(voucher, signature)
	if err != nil {
		return err
	}
	evt := events.MintSettled{
		InvoiceID:   invoiceID,
		Recipient:   recipient,
		Token:       token,
		Amount:      amount,
		TxHash:      txHash,
		VoucherHash: voucherHash,
	}.Event()
	if evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

func (sp *StateProcessor) handleNativeTransaction(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	switch tx.Type {
	case types.TxTypeRegisterIdentity:
		if err := sp.applyRegisterIdentity(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 0, 0)
	case types.TxTypeCreateEscrow:
		if err := sp.applyQuota(moduleEscrow, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyCreateEscrow(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeReleaseEscrow:
		if err := sp.applyQuota(moduleEscrow, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyReleaseEscrow(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeRefundEscrow:
		if err := sp.applyQuota(moduleEscrow, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyRefundEscrow(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeStake:
		if err := sp.applyQuota(modulePotso, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyStake(tx, sender); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 0, 1)
	case types.TxTypeUnstake:
		if err := sp.applyQuota(modulePotso, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyUnstake(tx, sender); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 0, 1)
	case types.TxTypeStakeClaim:
		if err := sp.applyQuota(modulePotso, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyStakeClaim(tx, sender); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 0, 1)
	case types.TxTypeHeartbeat:
		if err := sp.applyQuota(modulePotso, sender, 1, 0); err != nil {
			return err
		}
		return sp.applyHeartbeat(tx, sender, senderAccount)

	// --- NEW DISPUTE RESOLUTION CASES ---
	case types.TxTypeLockEscrow:
		if err := sp.applyQuota(moduleEscrow, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyLockEscrow(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeDisputeEscrow:
		if err := sp.applyQuota(moduleEscrow, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyDisputeEscrow(tx, sender, senderAccount); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeArbitrateRelease:
		if err := sp.applyQuota(moduleTrade, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyArbitrate(tx, sender, senderAccount, true); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeArbitrateRefund:
		if err := sp.applyQuota(moduleTrade, sender, 1, 0); err != nil {
			return err
		}
		if err := sp.applyArbitrate(tx, sender, senderAccount, false); err != nil {
			return err
		}
		return sp.recordEngagementActivity(sender, sp.blockTimestamp(), 1, 1, 0)
	case types.TxTypeSwapPayoutReceipt:
		if err := sp.applySwapPayoutReceipt(tx); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown native transaction type: %d", tx.Type)
}

func (sp *StateProcessor) applyRegisterIdentity(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	username := string(tx.Data)
	if len(username) < 3 || len(username) > 20 {
		return fmt.Errorf("username must be 3-20 characters")
	}
	if _, ok := sp.usernameToAddr[username]; ok {
		return fmt.Errorf("username '%s' taken", username)
	}
	if senderAccount.Username != "" {
		return fmt.Errorf("account already has username")
	}
	senderAccount.Username = username
	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}
	fmt.Printf("Identity processed: Username '%s' registered to %s.\n",
		username, crypto.MustNewAddress(crypto.NHBPrefix, sender).String())
	return nil
}

func (sp *StateProcessor) applyCreateEscrow(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	tradeEngine, _ := sp.configureTradeEngine()
	_ = tradeEngine

	var payload struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
		Mediator []byte   `json:"mediator,omitempty"`
		Meta     []byte   `json:"meta,omitempty"`
		Realm    string   `json:"realm,omitempty"`
	}
	if err := json.Unmarshal(tx.Data, &payload); err != nil {
		return fmt.Errorf("invalid escrow payload: %w", err)
	}
	if len(payload.Payee) != common.AddressLength {
		return fmt.Errorf("payee address must be %d bytes", common.AddressLength)
	}
	if payload.Amount == nil || payload.Amount.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("escrow amount must be positive")
	}
	if strings.TrimSpace(payload.Token) == "" {
		return fmt.Errorf("token required")
	}
	if payload.Deadline <= 0 {
		return fmt.Errorf("deadline must be positive")
	}
	if payload.Nonce == 0 {
		return fmt.Errorf("escrow nonce must be positive")
	}
	payer := bytesToAddress(sender)
	var payee [common.AddressLength]byte
	copy(payee[:], payload.Payee)
	mediatorAddr := [common.AddressLength]byte{}
	if len(payload.Mediator) != 0 {
		if len(payload.Mediator) != common.AddressLength {
			return fmt.Errorf("mediator address must be %d bytes", common.AddressLength)
		}
		mediatorAddr = bytesToAddress(payload.Mediator)
	}
	meta := [32]byte{}
	if len(payload.Meta) > len(meta) {
		return fmt.Errorf("meta payload must be <= 32 bytes")
	}
	copy(meta[:], payload.Meta)
	if _, err := sp.EscrowEngine.Create(payer, payee, payload.Token, payload.Amount, payload.FeeBps, payload.Deadline, payload.Nonce, &mediatorAddr, meta, strings.TrimSpace(payload.Realm)); err != nil {
		return err
	}
	return sp.updateSenderNonce(sender, senderAccount, senderAccount.Nonce+1)
}

func (sp *StateProcessor) applyReleaseEscrow(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	id, err := decodeEscrowID(tx.Data)
	if err != nil {
		return err
	}
	_, manager := sp.configureTradeEngine()
	if _, err := sp.ensureEscrowReady(id, manager); err != nil {
		return err
	}
	caller := bytesToAddress(sender)
	if err := sp.EscrowEngine.Release(id, caller); err != nil {
		return err
	}
	return sp.updateSenderNonce(sender, senderAccount, senderAccount.Nonce+1)
}

func (sp *StateProcessor) applyRefundEscrow(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	id, err := decodeEscrowID(tx.Data)
	if err != nil {
		return err
	}
	_, manager := sp.configureTradeEngine()
	if _, err := sp.ensureEscrowReady(id, manager); err != nil {
		return err
	}
	caller := bytesToAddress(sender)
	if err := sp.EscrowEngine.Refund(id, caller); err != nil {
		return err
	}
	return sp.updateSenderNonce(sender, senderAccount, senderAccount.Nonce+1)
}

// --- NEW: Lock -> Dispute -> Arbitrate flow ---

func (sp *StateProcessor) applyLockEscrow(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	id, err := decodeEscrowID(tx.Data)
	if err != nil {
		return err
	}
	_, manager := sp.configureTradeEngine()
	if _, err := sp.ensureEscrowReady(id, manager); err != nil {
		return err
	}
	caller := bytesToAddress(sender)
	if err := sp.EscrowEngine.Fund(id, caller); err != nil {
		return err
	}
	return sp.updateSenderNonce(sender, senderAccount, senderAccount.Nonce+1)
}

func (sp *StateProcessor) applyDisputeEscrow(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	id, err := decodeEscrowID(tx.Data)
	if err != nil {
		return err
	}
	_, manager := sp.configureTradeEngine()
	if _, err := sp.ensureEscrowReady(id, manager); err != nil {
		return err
	}
	caller := bytesToAddress(sender)
	if err := sp.EscrowEngine.Dispute(id, caller); err != nil {
		return err
	}
	return sp.updateSenderNonce(sender, senderAccount, senderAccount.Nonce+1)
}

func (sp *StateProcessor) applySwapPayoutReceipt(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("swap: transaction required")
	}
	if len(tx.Data) == 0 {
		return fmt.Errorf("swap: payout receipt payload required")
	}
	var packed anypb.Any
	if err := proto.Unmarshal(tx.Data, &packed); err != nil {
		return fmt.Errorf("swap: decode payload: %w", err)
	}
	if url := packed.GetTypeUrl(); url != "type.googleapis.com/swap.v1.MsgPayoutReceipt" {
		return fmt.Errorf("swap: unsupported payload %s", url)
	}
	var msg swapv1.MsgPayoutReceipt
	if err := packed.UnmarshalTo(&msg); err != nil {
		return fmt.Errorf("swap: decode payout receipt: %w", err)
	}
	protoReceipt := msg.GetReceipt()
	if protoReceipt == nil {
		return fmt.Errorf("swap: receipt required")
	}
	receipt, err := swapPayoutReceiptFromProto(protoReceipt)
	if err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	store := swap.NewStableStore(manager)
	if err := store.RecordPayoutReceipt(receipt); err != nil {
		return fmt.Errorf("swap: record payout receipt: %w", err)
	}
	return nil
}

func swapPayoutReceiptFromProto(msg *swapv1.PayoutReceipt) (*swap.PayoutReceipt, error) {
	if msg == nil {
		return nil, fmt.Errorf("swap: receipt required")
	}
	trimmedID := strings.TrimSpace(msg.GetIntentId())
	if trimmedID == "" {
		return nil, fmt.Errorf("swap: intent id required")
	}
	stableAmount, ok := new(big.Int).SetString(strings.TrimSpace(msg.GetStableAmount()), 10)
	if !ok {
		return nil, fmt.Errorf("swap: invalid stable amount %q", msg.GetStableAmount())
	}
	nhbAmount, ok := new(big.Int).SetString(strings.TrimSpace(msg.GetNhbAmount()), 10)
	if !ok {
		return nil, fmt.Errorf("swap: invalid nhb amount %q", msg.GetNhbAmount())
	}
	receipt := &swap.PayoutReceipt{
		ReceiptID:    strings.TrimSpace(msg.GetReceiptId()),
		IntentID:     trimmedID,
		StableAsset:  swap.StableAsset(strings.ToUpper(strings.TrimSpace(msg.GetStableAsset()))),
		StableAmount: stableAmount,
		NhbAmount:    nhbAmount,
		TxHash:       strings.TrimSpace(msg.GetTxHash()),
		EvidenceURI:  strings.TrimSpace(msg.GetEvidenceUri()),
		SettledAt:    msg.GetSettledAt(),
	}
	return receipt, nil
}

func (sp *StateProcessor) applyArbitrate(tx *types.Transaction, _ []byte, _ *types.Account, releaseToBuyer bool) error {
	_ = releaseToBuyer
	var payload struct {
		EscrowID   string          `json:"escrowId"`
		Decision   json.RawMessage `json:"decision"`
		Signatures []string        `json:"signatures"`
	}
	if len(tx.Data) == 0 {
		return fmt.Errorf("arbitration payload required")
	}
	if err := json.Unmarshal(tx.Data, &payload); err != nil {
		return fmt.Errorf("invalid arbitration payload: %w", err)
	}
	trimmedID := strings.TrimSpace(payload.EscrowID)
	if trimmedID == "" {
		return fmt.Errorf("arbitration escrowId required")
	}
	rawID, err := hex.DecodeString(strings.TrimPrefix(trimmedID, "0x"))
	if err != nil {
		return fmt.Errorf("arbitration escrowId must be hex: %w", err)
	}
	var id [32]byte
	if len(rawID) != len(id) {
		return fmt.Errorf("arbitration escrowId must be %d bytes", len(id))
	}
	copy(id[:], rawID)
	if len(payload.Decision) == 0 {
		return fmt.Errorf("arbitration decision payload required")
	}
	if len(payload.Signatures) == 0 {
		return fmt.Errorf("arbitration signature bundle required")
	}
	sigs := make([][]byte, len(payload.Signatures))
	for i, sigHex := range payload.Signatures {
		trimmed := strings.TrimSpace(sigHex)
		decoded, err := hex.DecodeString(strings.TrimPrefix(trimmed, "0x"))
		if err != nil {
			return fmt.Errorf("invalid arbitration signature %d: %w", i, err)
		}
		sigs[i] = decoded
	}
	_, manager := sp.configureTradeEngine()
	if _, err := sp.ensureEscrowReady(id, manager); err != nil {
		return err
	}
	if err := sp.EscrowEngine.ResolveWithSignatures(id, []byte(payload.Decision), sigs); err != nil {
		return err
	}
	return nil
}

func (sp *StateProcessor) StakeDelegate(delegator, validator []byte, amount *big.Int) (*types.Account, error) {
	if len(delegator) == 0 {
		return nil, fmt.Errorf("delegator address required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("stake must be positive")
	}
	target := validator
	if len(target) == 0 {
		target = append([]byte(nil), delegator...)
	} else {
		target = append([]byte(nil), target...)
	}
	if len(target) != 20 {
		return nil, fmt.Errorf("validator address must be 20 bytes")
	}

	delegatorAcc, err := sp.getAccount(delegator)
	if err != nil {
		return nil, err
	}
	if delegatorAcc.BalanceZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient ZapNHB")
	}
	if len(delegatorAcc.DelegatedValidator) > 0 && !bytes.Equal(delegatorAcc.DelegatedValidator, target) && delegatorAcc.LockedZNHB.Sign() > 0 {
		return nil, fmt.Errorf("existing delegation must be fully undelegated before switching validators")
	}

	sameValidator := bytes.Equal(target, delegator)

	delegatorAcc.BalanceZNHB.Sub(delegatorAcc.BalanceZNHB, amount)
	delegatorAcc.LockedZNHB.Add(delegatorAcc.LockedZNHB, amount)
	delegatorAcc.DelegatedValidator = append([]byte(nil), target...)
	if sameValidator {
		delegatorAcc.Stake.Add(delegatorAcc.Stake, amount)
	}
	delegatorAcc.Nonce++

	if !sameValidator {
		validatorAcc, err := sp.getAccount(target)
		if err != nil {
			return nil, err
		}
		validatorAcc.Stake.Add(validatorAcc.Stake, amount)
		if err := sp.setAccount(target, validatorAcc); err != nil {
			return nil, err
		}
	}

	if err := sp.setAccount(delegator, delegatorAcc); err != nil {
		return nil, err
	}

	delegatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, delegator)
	validatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, target)
	sp.AppendEvent(&types.Event{
		Type: "stake.delegated",
		Attributes: map[string]string{
			"delegator": delegatorAddr.String(),
			"validator": validatorAddr.String(),
			"amount":    amount.String(),
			"locked":    delegatorAcc.LockedZNHB.String(),
		},
	})

	return delegatorAcc, nil
}

func (sp *StateProcessor) StakeUndelegate(delegator []byte, amount *big.Int) (*types.StakeUnbond, error) {
	if len(delegator) == 0 {
		return nil, fmt.Errorf("delegator address required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("unstake must be positive")
	}
	delegatorAcc, err := sp.getAccount(delegator)
	if err != nil {
		return nil, err
	}
	if delegatorAcc.LockedZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient locked stake")
	}
	if len(delegatorAcc.DelegatedValidator) == 0 {
		return nil, fmt.Errorf("no active delegation")
	}

	validator := append([]byte(nil), delegatorAcc.DelegatedValidator...)
	sameValidator := bytes.Equal(validator, delegator)

	if sameValidator {
		if delegatorAcc.Stake.Cmp(amount) < 0 {
			return nil, fmt.Errorf("validator stake underflow")
		}
		delegatorAcc.Stake.Sub(delegatorAcc.Stake, amount)
	}
	delegatorAcc.LockedZNHB.Sub(delegatorAcc.LockedZNHB, amount)

	releaseTime := uint64(sp.now().Add(unbondingPeriod).Unix())
	nextID := delegatorAcc.NextUnbondingID
	if nextID == 0 {
		nextID = 1
	}
	entry := types.StakeUnbond{
		ID:          nextID,
		Validator:   append([]byte(nil), validator...),
		Amount:      new(big.Int).Set(amount),
		ReleaseTime: releaseTime,
	}
	delegatorAcc.PendingUnbonds = append(delegatorAcc.PendingUnbonds, entry)
	delegatorAcc.NextUnbondingID = nextID + 1
	if delegatorAcc.LockedZNHB.Sign() == 0 {
		delegatorAcc.DelegatedValidator = nil
	}
	delegatorAcc.Nonce++

	if !sameValidator {
		validatorAcc, err := sp.getAccount(validator)
		if err != nil {
			return nil, err
		}
		if validatorAcc.Stake.Cmp(amount) < 0 {
			return nil, fmt.Errorf("validator stake underflow")
		}
		validatorAcc.Stake.Sub(validatorAcc.Stake, amount)
		if err := sp.setAccount(validator, validatorAcc); err != nil {
			return nil, err
		}
	}

	if err := sp.setAccount(delegator, delegatorAcc); err != nil {
		return nil, err
	}

	delegatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, delegator)
	validatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, validator)
	sp.AppendEvent(&types.Event{
		Type: "stake.undelegated",
		Attributes: map[string]string{
			"delegator":   delegatorAddr.String(),
			"validator":   validatorAddr.String(),
			"amount":      amount.String(),
			"releaseTime": strconv.FormatUint(releaseTime, 10),
			"unbondingId": strconv.FormatUint(entry.ID, 10),
		},
	})

	return &entry, nil
}

func (sp *StateProcessor) StakeClaim(delegator []byte, unbondID uint64) (*types.StakeUnbond, error) {
	if len(delegator) == 0 {
		return nil, fmt.Errorf("delegator address required")
	}
	if unbondID == 0 {
		return nil, fmt.Errorf("unbondingId must be greater than zero")
	}
	delegatorAcc, err := sp.getAccount(delegator)
	if err != nil {
		return nil, err
	}
	var (
		index = -1
		entry types.StakeUnbond
	)
	for i, candidate := range delegatorAcc.PendingUnbonds {
		if candidate.ID == unbondID {
			entry = types.StakeUnbond{
				ID:          candidate.ID,
				Validator:   append([]byte(nil), candidate.Validator...),
				Amount:      new(big.Int).Set(candidate.Amount),
				ReleaseTime: candidate.ReleaseTime,
			}
			index = i
			break
		}
	}
	if index == -1 {
		return nil, fmt.Errorf("unbonding entry %d not found", unbondID)
	}
	if uint64(sp.now().Unix()) < entry.ReleaseTime {
		return nil, fmt.Errorf("unbonding entry %d is not yet claimable", unbondID)
	}

	delegatorAcc.PendingUnbonds = append(delegatorAcc.PendingUnbonds[:index], delegatorAcc.PendingUnbonds[index+1:]...)
	delegatorAcc.BalanceZNHB.Add(delegatorAcc.BalanceZNHB, entry.Amount)
	delegatorAcc.Nonce++

	if err := sp.setAccount(delegator, delegatorAcc); err != nil {
		return nil, err
	}

	delegatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, delegator)
	validatorAddr := crypto.MustNewAddress(crypto.NHBPrefix, entry.Validator)
	sp.AppendEvent(&types.Event{
		Type: "stake.claimed",
		Attributes: map[string]string{
			"delegator":   delegatorAddr.String(),
			"validator":   validatorAddr.String(),
			"amount":      entry.Amount.String(),
			"unbondingId": strconv.FormatUint(entry.ID, 10),
		},
	})

	return &entry, nil
}

func (sp *StateProcessor) applyStake(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("stake must be positive")
	}
	var payload struct {
		Validator []byte `json:"validator,omitempty"`
	}
	if len(tx.Data) > 0 {
		if err := json.Unmarshal(tx.Data, &payload); err != nil {
			return fmt.Errorf("invalid stake payload: %w", err)
		}
	}
	_, err := sp.StakeDelegate(sender, payload.Validator, tx.Value)
	return err
}

func (sp *StateProcessor) applyUnstake(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("unstake must be positive")
	}
	unbond, err := sp.StakeUndelegate(sender, tx.Value)
	if err != nil {
		return err
	}
	if len(tx.Data) > 0 {
		var payload struct {
			Validator []byte `json:"validator,omitempty"`
		}
		if err := json.Unmarshal(tx.Data, &payload); err != nil {
			return fmt.Errorf("invalid unstake payload: %w", err)
		}
		if len(payload.Validator) > 0 && !bytes.Equal(payload.Validator, unbond.Validator) {
			return fmt.Errorf("unstake validator mismatch")
		}
	}
	return nil
}

func (sp *StateProcessor) applyStakeClaim(tx *types.Transaction, sender []byte) error {
	var payload struct {
		UnbondingID uint64 `json:"unbondingId"`
	}
	if len(tx.Data) == 0 {
		return fmt.Errorf("claim payload required")
	}
	if err := json.Unmarshal(tx.Data, &payload); err != nil {
		return fmt.Errorf("invalid claim payload: %w", err)
	}
	if payload.UnbondingID == 0 {
		return fmt.Errorf("unbondingId must be greater than zero")
	}
	_, err := sp.StakeClaim(sender, payload.UnbondingID)
	return err
}

func (sp *StateProcessor) applyHeartbeat(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	payload := types.HeartbeatPayload{}
	if len(tx.Data) > 0 {
		if err := json.Unmarshal(tx.Data, &payload); err != nil {
			return fmt.Errorf("invalid heartbeat payload: %w", err)
		}
	}
	if payload.Timestamp == 0 {
		payload.Timestamp = time.Now().UTC().Unix()
	}
	now := time.Unix(payload.Timestamp, 0).UTC()

	updates := sp.rolloverEngagement(senderAccount, now)
	if senderAccount.EngagementLastHeartbeat != 0 {
		minDelta := int64(sp.engagementConfig.HeartbeatInterval.Seconds())
		last := int64(senderAccount.EngagementLastHeartbeat)
		if payload.Timestamp <= last {
			return fmt.Errorf("heartbeat replay detected")
		}
		if payload.Timestamp-last < minDelta {
			return fmt.Errorf("heartbeat rate limited")
		}
	}

	minutes := uint64(1)
	if senderAccount.EngagementLastHeartbeat != 0 {
		delta := payload.Timestamp - int64(senderAccount.EngagementLastHeartbeat)
		if delta > 0 {
			minutes = uint64(delta / int64(time.Minute/time.Second))
			if minutes == 0 {
				minutes = 1
			}
		}
	}
	if minutes > sp.engagementConfig.MaxMinutesPerHeartbeat {
		minutes = sp.engagementConfig.MaxMinutesPerHeartbeat
	}

	senderAccount.EngagementMinutes += minutes
	senderAccount.EngagementLastHeartbeat = uint64(payload.Timestamp)
	senderAccount.Nonce++
	if err := sp.setAccount(sender, senderAccount); err != nil {
		return err
	}
	sp.emitScoreUpdates(sender, updates)

	var addr [20]byte
	copy(addr[:], sender)
	evt := events.EngagementHeartbeat{
		Address:   addr,
		DeviceID:  payload.DeviceID,
		Minutes:   minutes,
		Timestamp: payload.Timestamp,
	}.Event()
	if evt != nil {
		sp.AppendEvent(evt)
	}

	fmt.Printf("Heartbeat processed: %s recorded %d minute(s).\n",
		crypto.MustNewAddress(crypto.NHBPrefix, sender).String(), minutes)
	return nil
}

type stakeUnbond struct {
	ID          uint64
	Validator   []byte
	Amount      *big.Int
	ReleaseTime uint64
}

type accountMetadata struct {
	BalanceZNHB             *big.Int
	Stake                   *big.Int
	LockedZNHB              *big.Int
	CollateralBalance       *big.Int
	DebtPrincipal           *big.Int
	SupplyShares            *big.Int
	LendingSupplyIndex      *big.Int
	LendingBorrowIndex      *big.Int
	DelegatedValidator      []byte
	Unbonding               []stakeUnbond
	UnbondingSeq            uint64
	Username                string
	EngagementScore         uint64
	EngagementDay           string
	EngagementMinutes       uint64
	EngagementTxCount       uint64
	EngagementEscrowEvents  uint64
	EngagementGovEvents     uint64
	EngagementLastHeartbeat uint64

	LendingCollateralDisabled bool
	LendingBorrowDisabled     bool
}

func ensureAccountDefaults(account *types.Account) {
	if account.BalanceNHB == nil {
		account.BalanceNHB = big.NewInt(0)
	}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
	if account.Stake == nil {
		account.Stake = big.NewInt(0)
	}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
	if account.CollateralBalance == nil {
		account.CollateralBalance = big.NewInt(0)
	}
	if account.DebtPrincipal == nil {
		account.DebtPrincipal = big.NewInt(0)
	}
	if account.SupplyShares == nil {
		account.SupplyShares = big.NewInt(0)
	}
	if account.LendingSnapshot.SupplyIndex == nil {
		account.LendingSnapshot.SupplyIndex = big.NewInt(0)
	}
	if account.LendingSnapshot.BorrowIndex == nil {
		account.LendingSnapshot.BorrowIndex = big.NewInt(0)
	}
	if account.PendingUnbonds == nil {
		account.PendingUnbonds = make([]types.StakeUnbond, 0)
	}
	if len(account.StorageRoot) == 0 {
		account.StorageRoot = gethtypes.EmptyRootHash.Bytes()
	}
	if len(account.CodeHash) == 0 {
		account.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
}

func (sp *StateProcessor) minimumValidatorStake() (*big.Int, error) {
	if sp == nil || sp.Trie == nil {
		return governance.DefaultMinimumValidatorStake(), nil
	}
	key := nhbstate.ParamStoreKey(governance.ParamKeyMinimumValidatorStake)
	raw, err := sp.Trie.Get(ethcrypto.Keccak256(key))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return governance.DefaultMinimumValidatorStake(), nil
	}
	var stored []byte
	if err := rlp.DecodeBytes(raw, &stored); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(stored))
	if trimmed == "" {
		return governance.DefaultMinimumValidatorStake(), nil
	}
	parsed, success := new(big.Int).SetString(trimmed, 10)
	if !success {
		return nil, fmt.Errorf("minimum validator stake must be a base-10 integer")
	}
	if parsed.Sign() <= 0 {
		return nil, fmt.Errorf("minimum validator stake must be positive")
	}
	return parsed, nil
}

// --- Helpers ---

func (sp *StateProcessor) getAccount(addr []byte) (*types.Account, error) {
	stateAcc, err := sp.loadStateAccount(addr)
	if err != nil {
		return nil, err
	}
	meta, err := sp.loadAccountMetadata(addr)
	if err != nil {
		return nil, err
	}

	account := &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
		EngagementScore:         0,
		EngagementDay:           "",
		EngagementMinutes:       0,
		EngagementTxCount:       0,
		EngagementEscrowEvents:  0,
		EngagementGovEvents:     0,
		EngagementLastHeartbeat: 0,
		StorageRoot:             gethtypes.EmptyRootHash.Bytes(),
		CodeHash:                gethtypes.EmptyCodeHash.Bytes(),
	}
	if stateAcc != nil {
		if stateAcc.Balance != nil {
			account.BalanceNHB = new(big.Int).Set(stateAcc.Balance.ToBig())
		}
		account.Nonce = stateAcc.Nonce
		account.StorageRoot = stateAcc.Root.Bytes()
		account.CodeHash = common.CopyBytes(stateAcc.CodeHash)
	}
	if meta != nil {
		if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
		}
		if meta.Stake != nil {
			account.Stake = new(big.Int).Set(meta.Stake)
		}
		if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
		}
		if meta.CollateralBalance != nil {
			account.CollateralBalance = new(big.Int).Set(meta.CollateralBalance)
		}
		if meta.DebtPrincipal != nil {
			account.DebtPrincipal = new(big.Int).Set(meta.DebtPrincipal)
		}
		if meta.SupplyShares != nil {
			account.SupplyShares = new(big.Int).Set(meta.SupplyShares)
		}
		if meta.LendingSupplyIndex != nil {
			account.LendingSnapshot.SupplyIndex = new(big.Int).Set(meta.LendingSupplyIndex)
		}
		if meta.LendingBorrowIndex != nil {
			account.LendingSnapshot.BorrowIndex = new(big.Int).Set(meta.LendingBorrowIndex)
		}
		if len(meta.DelegatedValidator) > 0 {
			account.DelegatedValidator = append([]byte(nil), meta.DelegatedValidator...)
		}
		if len(meta.Unbonding) > 0 {
			account.PendingUnbonds = make([]types.StakeUnbond, len(meta.Unbonding))
			for i, entry := range meta.Unbonding {
				amount := big.NewInt(0)
				if entry.Amount != nil {
					amount = new(big.Int).Set(entry.Amount)
				}
				var validator []byte
				if len(entry.Validator) > 0 {
					validator = append([]byte(nil), entry.Validator...)
				}
				account.PendingUnbonds[i] = types.StakeUnbond{
					ID:          entry.ID,
					Validator:   validator,
					Amount:      amount,
					ReleaseTime: entry.ReleaseTime,
				}
			}
		}
		account.NextUnbondingID = meta.UnbondingSeq
		account.Username = meta.Username
		account.EngagementScore = meta.EngagementScore
		account.EngagementDay = meta.EngagementDay
		account.EngagementMinutes = meta.EngagementMinutes
		account.EngagementTxCount = meta.EngagementTxCount
		account.EngagementEscrowEvents = meta.EngagementEscrowEvents
		account.EngagementGovEvents = meta.EngagementGovEvents
		account.EngagementLastHeartbeat = meta.EngagementLastHeartbeat
		account.LendingBreaker = types.LendingBreakerFlags{
			CollateralDisabled: meta.LendingCollateralDisabled,
			BorrowDisabled:     meta.LendingBorrowDisabled,
		}
	}
	ensureAccountDefaults(account)
	return account, nil
}

func (sp *StateProcessor) setAccount(addr []byte, account *types.Account) error {
	if account == nil {
		return fmt.Errorf("nil account")
	}
	ensureAccountDefaults(account)

	prevMeta, err := sp.loadAccountMetadata(addr)
	if err != nil {
		return err
	}

	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		return fmt.Errorf("balance overflow")
	}

	stateAcc := &gethtypes.StateAccount{
		Nonce:    account.Nonce,
		Balance:  balance,
		Root:     common.BytesToHash(account.StorageRoot),
		CodeHash: common.CopyBytes(account.CodeHash),
	}
	if len(stateAcc.CodeHash) == 0 {
		stateAcc.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}

	if err := sp.writeStateAccount(addr, stateAcc); err != nil {
		return err
	}

	var delegated []byte
	if len(account.DelegatedValidator) > 0 {
		delegated = append([]byte(nil), account.DelegatedValidator...)
	}
	unbonding := make([]stakeUnbond, len(account.PendingUnbonds))
	for i, entry := range account.PendingUnbonds {
		amount := big.NewInt(0)
		if entry.Amount != nil {
			amount = new(big.Int).Set(entry.Amount)
		}
		var validator []byte
		if len(entry.Validator) > 0 {
			validator = append([]byte(nil), entry.Validator...)
		}
		unbonding[i] = stakeUnbond{
			ID:          entry.ID,
			Validator:   validator,
			Amount:      amount,
			ReleaseTime: entry.ReleaseTime,
		}
	}
	meta := &accountMetadata{
		BalanceZNHB:               new(big.Int).Set(account.BalanceZNHB),
		Stake:                     new(big.Int).Set(account.Stake),
		LockedZNHB:                new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:         new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:             new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:              new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex:        new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex:        new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		DelegatedValidator:        delegated,
		Unbonding:                 unbonding,
		UnbondingSeq:              account.NextUnbondingID,
		Username:                  account.Username,
		EngagementScore:           account.EngagementScore,
		EngagementDay:             account.EngagementDay,
		EngagementMinutes:         account.EngagementMinutes,
		EngagementTxCount:         account.EngagementTxCount,
		EngagementEscrowEvents:    account.EngagementEscrowEvents,
		EngagementGovEvents:       account.EngagementGovEvents,
		EngagementLastHeartbeat:   account.EngagementLastHeartbeat,
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
	}
	if err := sp.writeAccountMetadata(addr, meta); err != nil {
		return err
	}

	prevUsername := ""
	if prevMeta != nil {
		prevUsername = prevMeta.Username
	}

	if prevUsername != "" && prevUsername != account.Username {
		delete(sp.usernameToAddr, prevUsername)
	}
	if account.Username != "" {
		sp.usernameToAddr[account.Username] = append([]byte(nil), addr...)
	}
	if err := sp.persistUsernameIndex(); err != nil {
		return err
	}

	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return err
	}
	meetsStake := account.Stake.Cmp(minStake) >= 0
	addrKey := string(addr)

	if sp.EligibleValidators == nil {
		sp.EligibleValidators = make(map[string]*big.Int)
	}
	if meetsStake {
		sp.EligibleValidators[addrKey] = new(big.Int).Set(account.Stake)
	} else {
		delete(sp.EligibleValidators, addrKey)
	}
	if err := sp.persistEligibleValidatorSet(); err != nil {
		return err
	}

	if !sp.epochConfig.RotationEnabled {
		if meetsStake {
			sp.ValidatorSet[addrKey] = new(big.Int).Set(account.Stake)
		} else {
			delete(sp.ValidatorSet, addrKey)
		}
		if err := sp.persistValidatorSet(); err != nil {
			return err
		}
	} else if !meetsStake {
		if _, exists := sp.ValidatorSet[addrKey]; exists {
			delete(sp.ValidatorSet, addrKey)
			if err := sp.persistValidatorSet(); err != nil {
				return err
			}
		}
	}

	return nil
}

type engagementScoreUpdate struct {
	Day string
	Raw uint64
	Old uint64
	New uint64
}

func (sp *StateProcessor) computeRawEngagement(minutes, tx, escrow, gov uint64) uint64 {
	cfg := sp.engagementConfig
	total := new(big.Int)
	tmp := new(big.Int)

	if cfg.HeartbeatWeight > 0 && minutes > 0 {
		tmp.SetUint64(minutes)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.HeartbeatWeight))
		total.Add(total, tmp)
	}
	if cfg.TxWeight > 0 && tx > 0 {
		tmp.SetUint64(tx)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.TxWeight))
		total.Add(total, tmp)
	}
	if cfg.EscrowWeight > 0 && escrow > 0 {
		tmp.SetUint64(escrow)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.EscrowWeight))
		total.Add(total, tmp)
	}
	if cfg.GovWeight > 0 && gov > 0 {
		tmp.SetUint64(gov)
		tmp.Mul(tmp, new(big.Int).SetUint64(cfg.GovWeight))
		total.Add(total, tmp)
	}

	if total.BitLen() > 64 {
		return ^uint64(0)
	}
	return total.Uint64()
}

func (sp *StateProcessor) applyEMAScore(prev, raw uint64) uint64 {
	cfg := sp.engagementConfig
	if cfg.LambdaDenominator == 0 {
		return raw
	}
	prevComponent := new(big.Int).SetUint64(prev)
	prevComponent.Mul(prevComponent, new(big.Int).SetUint64(cfg.LambdaNumerator))

	contribution := cfg.LambdaDenominator - cfg.LambdaNumerator
	rawComponent := new(big.Int).SetUint64(raw)
	rawComponent.Mul(rawComponent, new(big.Int).SetUint64(contribution))

	prevComponent.Add(prevComponent, rawComponent)
	prevComponent.Div(prevComponent, new(big.Int).SetUint64(cfg.LambdaDenominator))

	if prevComponent.BitLen() > 64 {
		return ^uint64(0)
	}
	return prevComponent.Uint64()
}

func (sp *StateProcessor) rolloverEngagement(account *types.Account, now time.Time) []engagementScoreUpdate {
	currentDay := now.UTC().Format(engagementDayFormat)
	if account.EngagementDay == "" {
		account.EngagementDay = currentDay
		return nil
	}
	if account.EngagementDay == currentDay {
		return nil
	}
	startDay, err := time.Parse(engagementDayFormat, account.EngagementDay)
	if err != nil {
		account.EngagementDay = currentDay
		account.EngagementMinutes = 0
		account.EngagementTxCount = 0
		account.EngagementEscrowEvents = 0
		account.EngagementGovEvents = 0
		return nil
	}
	targetDay, err := time.Parse(engagementDayFormat, currentDay)
	if err != nil {
		return nil
	}

	updates := make([]engagementScoreUpdate, 0)
	dayCursor := startDay
	for dayCursor.Before(targetDay) {
		raw := sp.computeRawEngagement(account.EngagementMinutes, account.EngagementTxCount, account.EngagementEscrowEvents, account.EngagementGovEvents)
		if raw > sp.engagementConfig.DailyCap {
			raw = sp.engagementConfig.DailyCap
		}
		oldScore := account.EngagementScore
		newScore := sp.applyEMAScore(oldScore, raw)
		updates = append(updates, engagementScoreUpdate{
			Day: dayCursor.Format(engagementDayFormat),
			Raw: raw,
			Old: oldScore,
			New: newScore,
		})
		account.EngagementScore = newScore
		account.EngagementMinutes = 0
		account.EngagementTxCount = 0
		account.EngagementEscrowEvents = 0
		account.EngagementGovEvents = 0
		dayCursor = dayCursor.AddDate(0, 0, 1)
	}
	account.EngagementDay = currentDay
	return updates
}

func (sp *StateProcessor) emitScoreUpdates(addr []byte, updates []engagementScoreUpdate) {
	if len(updates) == 0 {
		return
	}
	var address [20]byte
	copy(address[:], addr)
	for _, upd := range updates {
		evt := events.EngagementScoreUpdated{
			Address:  address,
			Day:      upd.Day,
			RawScore: upd.Raw,
			OldScore: upd.Old,
			NewScore: upd.New,
		}.Event()
		if evt != nil {
			sp.AppendEvent(evt)
		}
	}
}

func bytesToHash32(b []byte) [32]byte {
	var out [32]byte
	copy(out[:], b)
	return out
}

func addressToArray(addr common.Address) [20]byte {
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}

func (sp *StateProcessor) emitSponsorshipFailureEvent(sender common.Address, assessment *SponsorshipAssessment, txHash [32]byte) {
	if sp == nil || assessment == nil {
		return
	}
	evt := events.TxSponsorshipFailed{
		TxHash:  txHash,
		Sender:  addressToArray(sender),
		Sponsor: addressToArray(assessment.Sponsor),
		Status:  string(assessment.Status),
		Reason:  assessment.Reason,
	}
	sp.AppendEvent(evt.Event())
	if assessment.Throttle != nil {
		sp.emitPaymasterThrottledEvent(txHash, assessment.Throttle)
	}
}

func (sp *StateProcessor) emitSponsorshipSuccessEvent(ctx *sponsorshipRuntime, gasUsed uint64, charged, refund *big.Int) {
	if sp == nil || ctx == nil {
		return
	}
	var gasPriceCopy *big.Int
	if ctx.gasPrice != nil {
		gasPriceCopy = new(big.Int).Set(ctx.gasPrice)
	}
	var chargedCopy *big.Int
	if charged != nil {
		chargedCopy = new(big.Int).Set(charged)
	}
	var refundCopy *big.Int
	if refund != nil {
		refundCopy = new(big.Int).Set(refund)
	}
	evt := events.TxSponsorshipApplied{
		TxHash:   ctx.txHash,
		Sender:   addressToArray(ctx.sender),
		Sponsor:  addressToArray(ctx.sponsor),
		GasUsed:  gasUsed,
		GasPrice: gasPriceCopy,
		Charged:  chargedCopy,
		Refund:   refundCopy,
	}
	sp.AppendEvent(evt.Event())
}

func (sp *StateProcessor) emitPaymasterThrottledEvent(txHash [32]byte, throttle *PaymasterThrottle) {
	if sp == nil || throttle == nil {
		return
	}
	evt := events.PaymasterThrottled{
		TxHash:       txHash,
		Scope:        string(throttle.Scope),
		Merchant:     strings.TrimSpace(throttle.Merchant),
		DeviceID:     strings.TrimSpace(throttle.DeviceID),
		Day:          strings.TrimSpace(throttle.Day),
		TxCount:      throttle.TxCount,
		LimitTxCount: throttle.LimitTxCount,
	}
	if throttle.LimitWei != nil {
		evt.LimitWei = new(big.Int).Set(throttle.LimitWei)
	}
	if throttle.UsedBudgetWei != nil {
		evt.UsedBudgetWei = new(big.Int).Set(throttle.UsedBudgetWei)
	}
	if throttle.AttemptBudgetWei != nil {
		evt.AttemptWei = new(big.Int).Set(throttle.AttemptBudgetWei)
	}
	sp.AppendEvent(evt.Event())
}

func (sp *StateProcessor) recordPaymasterUsage(ctx *sponsorshipRuntime, charged *big.Int) error {
	if sp == nil || ctx == nil {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	day := nhbstate.NormalizePaymasterDay(ctx.day)
	if day == "" {
		day = sp.currentPaymasterDay()
	}
	budget := big.NewInt(0)
	if ctx.budget != nil {
		budget = new(big.Int).Set(ctx.budget)
	}
	chargedTotal := big.NewInt(0)
	if charged != nil {
		chargedTotal = new(big.Int).Set(charged)
	}

	global, _, err := manager.PaymasterGetGlobalDay(day)
	if err != nil {
		return err
	}
	if global == nil {
		global = &nhbstate.PaymasterGlobalDay{Day: day, BudgetWei: big.NewInt(0), ChargedWei: big.NewInt(0)}
	}
	global.TxCount++
	global.BudgetWei = new(big.Int).Add(global.BudgetWei, budget)
	global.ChargedWei = new(big.Int).Add(global.ChargedWei, chargedTotal)
	if err := manager.PaymasterPutGlobalDay(global); err != nil {
		return err
	}

	merchant := nhbstate.NormalizePaymasterMerchant(ctx.merchant)
	if merchant != "" {
		merchantRecord, _, err := manager.PaymasterGetMerchantDay(merchant, day)
		if err != nil {
			return err
		}
		if merchantRecord == nil {
			merchantRecord = &nhbstate.PaymasterMerchantDay{Merchant: merchant, Day: day, BudgetWei: big.NewInt(0), ChargedWei: big.NewInt(0)}
		}
		merchantRecord.TxCount++
		merchantRecord.BudgetWei = new(big.Int).Add(merchantRecord.BudgetWei, budget)
		merchantRecord.ChargedWei = new(big.Int).Add(merchantRecord.ChargedWei, chargedTotal)
		if err := manager.PaymasterPutMerchantDay(merchantRecord); err != nil {
			return err
		}
	}

	device := nhbstate.NormalizePaymasterDevice(ctx.device)
	if merchant != "" && device != "" {
		deviceRecord, _, err := manager.PaymasterGetDeviceDay(merchant, device, day)
		if err != nil {
			return err
		}
		if deviceRecord == nil {
			deviceRecord = &nhbstate.PaymasterDeviceDay{Merchant: merchant, DeviceID: device, Day: day, BudgetWei: big.NewInt(0), ChargedWei: big.NewInt(0)}
		}
		deviceRecord.TxCount++
		deviceRecord.BudgetWei = new(big.Int).Add(deviceRecord.BudgetWei, budget)
		deviceRecord.ChargedWei = new(big.Int).Add(deviceRecord.ChargedWei, chargedTotal)
		if err := manager.PaymasterPutDeviceDay(deviceRecord); err != nil {
			return err
		}
	}
	return nil
}

func (sp *StateProcessor) recordEngagementActivity(addr []byte, now time.Time, txDelta, escrowDelta, govDelta uint64) error {
	if txDelta == 0 && escrowDelta == 0 && govDelta == 0 {
		return nil
	}
	account, err := sp.getAccount(addr)
	if err != nil {
		return err
	}
	updates := sp.rolloverEngagement(account, now)
	if txDelta > 0 {
		account.EngagementTxCount += txDelta
	}
	if escrowDelta > 0 {
		account.EngagementEscrowEvents += escrowDelta
	}
	if govDelta > 0 {
		account.EngagementGovEvents += govDelta
	}
	if err := sp.setAccount(addr, account); err != nil {
		return err
	}
	if txDelta > 0 || escrowDelta > 0 {
		if err := sp.updatePotsoActivity(addr, now, txDelta, escrowDelta); err != nil {
			return err
		}
	}
	sp.emitScoreUpdates(addr, updates)
	return nil
}

func (sp *StateProcessor) updatePotsoActivity(addr []byte, now time.Time, txDelta, escrowDelta uint64) error {
	if len(addr) != 20 {
		return nil
	}
	day := now.UTC().Format(potso.DayFormat)
	manager := nhbstate.NewManager(sp.Trie)
	var address [20]byte
	copy(address[:], addr)
	meter, _, err := manager.PotsoGetMeter(address, day)
	if err != nil {
		return err
	}
	meter.Day = day
	meter.TxCount += txDelta
	meter.EscrowEvents += escrowDelta
	meter.RecomputeScore()
	return manager.PotsoPutMeter(address, meter)
}

func accountStateKey(addr []byte) []byte {
	return ethcrypto.Keccak256(addr)
}

func accountMetadataKey(addr []byte) []byte {
	buf := make([]byte, len(accountMetadataPrefix)+len(addr))
	copy(buf, accountMetadataPrefix)
	copy(buf[len(accountMetadataPrefix):], addr)
	return ethcrypto.Keccak256(buf)
}

func (sp *StateProcessor) loadStateAccount(addr []byte) (*gethtypes.StateAccount, error) {
	key := accountStateKey(addr)
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	stateAcc := new(gethtypes.StateAccount)
	if err := rlp.DecodeBytes(data, stateAcc); err != nil {
		slim := new(gethtypes.SlimAccount)
		if errSlim := rlp.DecodeBytes(data, slim); errSlim == nil {
			restored := &gethtypes.StateAccount{
				Nonce:   slim.Nonce,
				Balance: slim.Balance,
				Root:    gethtypes.EmptyRootHash,
				CodeHash: func() []byte {
					if len(slim.CodeHash) == 0 {
						return gethtypes.EmptyCodeHash.Bytes()
					}
					return append([]byte(nil), slim.CodeHash...)
				}(),
			}
			if len(slim.Root) != 0 {
				restored.Root = common.BytesToHash(slim.Root)
			}
			return restored, nil
		}
		legacy := new(types.Account)
		if errLegacy := rlp.DecodeBytes(data, legacy); errLegacy != nil {
			return nil, err
		}
		migrated, migrateErr := sp.migrateLegacyAccount(addr, legacy)
		if migrateErr != nil {
			return nil, migrateErr
		}
		return migrated, nil
	}
	return stateAcc, nil
}

func (sp *StateProcessor) migrateLegacyAccount(addr []byte, legacy *types.Account) (*gethtypes.StateAccount, error) {
	ensureAccountDefaults(legacy)

	balance, overflow := uint256.FromBig(legacy.BalanceNHB)
	if overflow {
		return nil, fmt.Errorf("balance overflow")
	}

	stateAcc := &gethtypes.StateAccount{
		Nonce:    legacy.Nonce,
		Balance:  balance,
		Root:     common.BytesToHash(legacy.StorageRoot),
		CodeHash: common.CopyBytes(legacy.CodeHash),
	}
	if len(stateAcc.CodeHash) == 0 {
		stateAcc.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := sp.writeStateAccount(addr, stateAcc); err != nil {
		return nil, err
	}

	meta := &accountMetadata{
		BalanceZNHB:        new(big.Int).Set(legacy.BalanceZNHB),
		Stake:              new(big.Int).Set(legacy.Stake),
		LockedZNHB:         big.NewInt(0),
		CollateralBalance:  big.NewInt(0),
		DebtPrincipal:      big.NewInt(0),
		SupplyShares:       big.NewInt(0),
		LendingSupplyIndex: big.NewInt(0),
		LendingBorrowIndex: big.NewInt(0),
		Unbonding:          make([]stakeUnbond, 0),
		Username:           legacy.Username,
		EngagementScore:    legacy.EngagementScore,
	}
	if err := sp.writeAccountMetadata(addr, meta); err != nil {
		return nil, err
	}

	if legacy.Username != "" {
		sp.usernameToAddr[legacy.Username] = append([]byte(nil), addr...)
		if err := sp.persistUsernameIndex(); err != nil {
			return nil, err
		}
	}
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return nil, err
	}
	if legacy.Stake.Cmp(minStake) >= 0 {
		if sp.EligibleValidators == nil {
			sp.EligibleValidators = make(map[string]*big.Int)
		}
		key := string(addr)
		sp.EligibleValidators[key] = new(big.Int).Set(legacy.Stake)
		sp.ValidatorSet[key] = new(big.Int).Set(legacy.Stake)
		if err := sp.persistEligibleValidatorSet(); err != nil {
			return nil, err
		}
		if err := sp.persistValidatorSet(); err != nil {
			return nil, err
		}
	}
	return stateAcc, nil
}

func (sp *StateProcessor) writeStateAccount(addr []byte, stateAcc *gethtypes.StateAccount) error {
	key := accountStateKey(addr)
	encoded, err := rlp.EncodeToBytes(stateAcc)
	if err != nil {
		return err
	}
	return sp.Trie.Update(key, encoded)
}

func (sp *StateProcessor) loadAccountMetadata(addr []byte) (*accountMetadata, error) {
	key := accountMetadataKey(addr)
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	meta := &accountMetadata{
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
		LockedZNHB:  big.NewInt(0),
		Unbonding:   make([]stakeUnbond, 0),
	}
	if len(data) == 0 {
		return meta, nil
	}
	if err := rlp.DecodeBytes(data, meta); err != nil {
		return nil, err
	}
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
	if meta.CollateralBalance == nil {
		meta.CollateralBalance = big.NewInt(0)
	}
	if meta.DebtPrincipal == nil {
		meta.DebtPrincipal = big.NewInt(0)
	}
	if meta.SupplyShares == nil {
		meta.SupplyShares = big.NewInt(0)
	}
	if meta.LendingSupplyIndex == nil {
		meta.LendingSupplyIndex = big.NewInt(0)
	}
	if meta.LendingBorrowIndex == nil {
		meta.LendingBorrowIndex = big.NewInt(0)
	}
	if meta.Unbonding == nil {
		meta.Unbonding = make([]stakeUnbond, 0)
	}
	return meta, nil
}

func (sp *StateProcessor) writeAccountMetadata(addr []byte, meta *accountMetadata) error {
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
	if meta.CollateralBalance == nil {
		meta.CollateralBalance = big.NewInt(0)
	}
	if meta.DebtPrincipal == nil {
		meta.DebtPrincipal = big.NewInt(0)
	}
	if meta.SupplyShares == nil {
		meta.SupplyShares = big.NewInt(0)
	}
	if meta.LendingSupplyIndex == nil {
		meta.LendingSupplyIndex = big.NewInt(0)
	}
	if meta.LendingBorrowIndex == nil {
		meta.LendingBorrowIndex = big.NewInt(0)
	}
	if meta.Unbonding == nil {
		meta.Unbonding = make([]stakeUnbond, 0)
	}
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return sp.Trie.Update(accountMetadataKey(addr), encoded)
}

type usernameIndexEntry struct {
	Username string
	Address  []byte
}

func (sp *StateProcessor) loadUsernameIndex() error {
	data, err := sp.Trie.Get(usernameIndexKey)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var entries []usernameIndexEntry
	if err := rlp.DecodeBytes(data, &entries); err != nil {
		return err
	}
	sp.usernameToAddr = make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.Username == "" {
			continue
		}
		sp.usernameToAddr[entry.Username] = append([]byte(nil), entry.Address...)
	}
	return nil
}

func (sp *StateProcessor) persistUsernameIndex() error {
	entries := make([]usernameIndexEntry, 0, len(sp.usernameToAddr))
	for username, addr := range sp.usernameToAddr {
		entries = append(entries, usernameIndexEntry{
			Username: username,
			Address:  append([]byte(nil), addr...),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Username < entries[j].Username })

	encoded, err := rlp.EncodeToBytes(entries)
	if err != nil {
		return err
	}
	return sp.Trie.Update(usernameIndexKey, encoded)
}

func (sp *StateProcessor) loadValidatorSet() error {
	data, err := sp.Trie.Get(validatorSetKey)
	if err != nil {
		return err
	}
	decoded, err := nhbstate.DecodeValidatorSet(data)
	if err != nil {
		return err
	}
	sp.ValidatorSet = make(map[string]*big.Int, len(decoded))
	for k, v := range decoded {
		if v == nil {
			v = big.NewInt(0)
		}
		sp.ValidatorSet[k] = new(big.Int).Set(v)
	}
	eligibleData, err := sp.Trie.Get(validatorEligibleKey)
	if err != nil {
		return err
	}
	if len(eligibleData) == 0 {
		sp.EligibleValidators = make(map[string]*big.Int, len(sp.ValidatorSet))
		for k, v := range sp.ValidatorSet {
			sp.EligibleValidators[k] = new(big.Int).Set(v)
		}
		return nil
	}
	eligibleDecoded, err := nhbstate.DecodeValidatorSet(eligibleData)
	if err != nil {
		return err
	}
	sp.EligibleValidators = make(map[string]*big.Int, len(eligibleDecoded))
	for k, v := range eligibleDecoded {
		if v == nil {
			v = big.NewInt(0)
		}
		sp.EligibleValidators[k] = new(big.Int).Set(v)
	}
	return nil
}

func (sp *StateProcessor) persistValidatorSet() error {
	encoded, err := nhbstate.EncodeValidatorSet(sp.ValidatorSet)
	if err != nil {
		return err
	}
	return sp.Trie.Update(validatorSetKey, encoded)
}

func (sp *StateProcessor) persistEligibleValidatorSet() error {
	encoded, err := nhbstate.EncodeValidatorSet(sp.EligibleValidators)
	if err != nil {
		return err
	}
	return sp.Trie.Update(validatorEligibleKey, encoded)
}

func (sp *StateProcessor) loadBigInt(key []byte) (*big.Int, error) {
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	value := new(big.Int)
	if err := rlp.DecodeBytes(data, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (sp *StateProcessor) writeBigInt(key []byte, amount *big.Int) error {
	if amount == nil {
		amount = big.NewInt(0)
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("negative value not allowed")
	}
	encoded, err := rlp.EncodeToBytes(amount)
	if err != nil {
		return err
	}
	return sp.Trie.Update(key, encoded)
}

func (sp *StateProcessor) PutAccount(addr []byte, account *types.Account) error {
	return sp.setAccount(addr, account)
}

func bytesToAddress(b []byte) [20]byte {
	var addr [20]byte
	copy(addr[:], b)
	return addr
}

func decodeEscrowID(data []byte) ([32]byte, error) {
	var id [32]byte
	if len(data) != len(id) {
		return id, fmt.Errorf("escrow id must be %d bytes", len(id))
	}
	copy(id[:], data)
	return id, nil
}

func (sp *StateProcessor) updateSenderNonce(sender []byte, senderAccount *types.Account, newNonce uint64) error {
	account, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	account.Nonce = newNonce
	if senderAccount != nil {
		senderAccount.Nonce = newNonce
	}
	return sp.setAccount(sender, account)
}

func (sp *StateProcessor) ensureEscrowReady(id [32]byte, manager *nhbstate.Manager) (*escrow.Escrow, error) {
	if manager == nil {
		manager = nhbstate.NewManager(sp.Trie)
	}
	if esc, ok := manager.EscrowGet(id); ok {
		return esc, nil
	}
	return sp.migrateLegacyEscrow(id, manager)
}

func (sp *StateProcessor) migrateLegacyEscrow(id [32]byte, manager *nhbstate.Manager) (*escrow.Escrow, error) {
	if manager == nil {
		manager = nhbstate.NewManager(sp.Trie)
	}
	legacyKey := ethcrypto.Keccak256(append([]byte("escrow-"), id[:]...))
	data, err := sp.Trie.Get(legacyKey)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("escrow %x not found", id)
	}
	legacy := new(escrow.LegacyEscrow)
	if err := rlp.DecodeBytes(data, legacy); err != nil {
		return nil, err
	}
	converted, err := sp.convertLegacyEscrow(id, legacy)
	if err != nil {
		return nil, err
	}
	if err := manager.EscrowPut(converted); err != nil {
		return nil, err
	}
	if err := sp.Trie.Update(legacyKey, nil); err != nil {
		return nil, err
	}
	if converted.Status == escrow.EscrowFunded || converted.Status == escrow.EscrowDisputed {
		if err := manager.EscrowCredit(converted.ID, converted.Token, converted.Amount); err != nil {
			return nil, err
		}
		if err := sp.creditEscrowVault(manager, converted.Token, converted.Amount); err != nil {
			return nil, err
		}
	}
	migrated, ok := manager.EscrowGet(converted.ID)
	if !ok {
		return nil, fmt.Errorf("escrow migration failed")
	}
	return migrated, nil
}

func (sp *StateProcessor) convertLegacyEscrow(id [32]byte, legacy *escrow.LegacyEscrow) (*escrow.Escrow, error) {
	if legacy == nil {
		return nil, fmt.Errorf("legacy escrow not found")
	}
	amount := big.NewInt(0)
	if legacy.Amount != nil {
		amount = new(big.Int).Set(legacy.Amount)
	}
	payer := bytesToAddress(legacy.Seller)
	payee := payer
	if len(legacy.Buyer) > 0 {
		payee = bytesToAddress(legacy.Buyer)
	}
	status := escrow.EscrowFunded
	switch legacy.Status {
	case escrow.LegacyStatusReleased:
		status = escrow.EscrowReleased
	case escrow.LegacyStatusRefunded:
		status = escrow.EscrowRefunded
	case escrow.LegacyStatusDisputed:
		status = escrow.EscrowDisputed
	case escrow.LegacyStatusOpen, escrow.LegacyStatusInProgress:
		status = escrow.EscrowFunded
	default:
		status = escrow.EscrowFunded
	}
	now := sp.now().UTC()
	created := now.Unix()
	deadline := now.Add(30 * 24 * time.Hour).Unix()
	if deadline < created {
		deadline = created
	}
	return &escrow.Escrow{
		ID:        id,
		Payer:     payer,
		Payee:     payee,
		Mediator:  [20]byte{},
		Token:     "NHB",
		Amount:    amount,
		FeeBps:    0,
		Deadline:  deadline,
		CreatedAt: created,
		Nonce:     1,
		Status:    status,
	}, nil
}

func (sp *StateProcessor) creditEscrowVault(manager *nhbstate.Manager, token string, amount *big.Int) error {
	if amount == nil || amount.Sign() <= 0 {
		return nil
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return err
	}
	vault, err := manager.EscrowVaultAddress(normalized)
	if err != nil {
		return err
	}
	account, err := manager.GetAccount(vault[:])
	if err != nil {
		return err
	}
	ensureAccountDefaults(account)
	switch normalized {
	case "NHB":
		account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
	return manager.PutAccount(vault[:], account)
}

func (sp *StateProcessor) now() time.Time {
	if sp != nil && sp.nowFunc != nil {
		return sp.nowFunc()
	}
	return time.Now()
}

func (sp *StateProcessor) LoyaltyGlobalConfig() (*loyalty.GlobalConfig, error) {
	key := nhbstate.LoyaltyGlobalStorageKey()
	data, err := sp.Trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	cfg := new(loyalty.GlobalConfig)
	if err := rlp.DecodeBytes(data, cfg); err != nil {
		return nil, err
	}
	return cfg.Normalize(), nil
}

func (sp *StateProcessor) LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	key := nhbstate.LoyaltyBaseDailyMeterKey(addr, day)
	return sp.loadBigInt(key)
}

func (sp *StateProcessor) SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	key := nhbstate.LoyaltyBaseDailyMeterKey(addr, day)
	return sp.writeBigInt(key, amount)
}

func (sp *StateProcessor) LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	key := nhbstate.LoyaltyBaseTotalMeterKey(addr)
	return sp.loadBigInt(key)
}

func (sp *StateProcessor) SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	key := nhbstate.LoyaltyBaseTotalMeterKey(addr)
	return sp.writeBigInt(key, amount)
}

func (sp *StateProcessor) configureTradeEngine() (*escrow.TradeEngine, *nhbstate.Manager) {
	manager := nhbstate.NewManager(sp.Trie)
	if sp.EscrowEngine == nil {
		sp.EscrowEngine = escrow.NewEngine()
	}
	sp.EscrowEngine.SetState(manager)
	sp.EscrowEngine.SetEmitter(stateProcessorEmitter{sp: sp})
	sp.EscrowEngine.SetFeeTreasury(sp.escrowFeeTreasury)
	sp.EscrowEngine.SetNowFunc(func() int64 { return sp.now().Unix() })
	if sp.TradeEngine == nil {
		sp.TradeEngine = escrow.NewTradeEngine(sp.EscrowEngine)
	}
	sp.TradeEngine.SetState(manager)
	sp.TradeEngine.SetEmitter(stateProcessorEmitter{sp: sp})
	sp.TradeEngine.SetNowFunc(func() int64 { return sp.now().Unix() })
	return sp.TradeEngine, manager
}

type stateProcessorEmitter struct {
	sp *StateProcessor
}

func (e stateProcessorEmitter) Emit(evt events.Event) {
	if e.sp == nil || evt == nil {
		return
	}
	if provider, ok := evt.(interface{ Event() *types.Event }); ok {
		if payload := provider.Event(); payload != nil {
			e.sp.AppendEvent(payload)
		}
		return
	}
	e.sp.AppendEvent(&types.Event{Type: evt.EventType(), Attributes: map[string]string{}})
}

func (sp *StateProcessor) SettleTradeAtomic(tradeID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.SettleAtomic(tradeID)
}

func (sp *StateProcessor) TradeTryExpire(tradeID [32]byte, now int64) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.TradeTryExpire(tradeID, now)
}

func (sp *StateProcessor) OnTradeFundingProgress(tradeID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.OnFundingProgress(tradeID)
}

func (sp *StateProcessor) OnEscrowFunded(escrowID [32]byte) error {
	tradeEngine, _ := sp.configureTradeEngine()
	return tradeEngine.HandleEscrowFunded(escrowID)
}

func (sp *StateProcessor) emitPaymentIntentConsumed(tx *types.Transaction) error {
	if tx == nil || len(tx.IntentRef) == 0 {
		return nil
	}
	hash, err := tx.Hash()
	if err != nil {
		return err
	}
	evt := events.PaymentIntentConsumed{
		IntentRef: append([]byte(nil), tx.IntentRef...),
		TxHash:    hash,
		Merchant:  tx.MerchantAddress,
		DeviceID:  tx.DeviceID,
	}.Event()
	if evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

func (sp *StateProcessor) AppendEvent(evt *types.Event) {
	if evt == nil {
		return
	}
	attrs := make(map[string]string, len(evt.Attributes))
	for k, v := range evt.Attributes {
		attrs[k] = v
	}
	sp.events = append(sp.events, types.Event{Type: evt.Type, Attributes: attrs})
}

func (sp *StateProcessor) Events() []types.Event {
	out := make([]types.Event, len(sp.events))
	for i := range sp.events {
		attrs := make(map[string]string, len(sp.events[i].Attributes))
		for k, v := range sp.events[i].Attributes {
			attrs[k] = v
		}
		out[i] = types.Event{Type: sp.events[i].Type, Attributes: attrs}
	}
	return out
}

func (sp *StateProcessor) GetAccount(addr []byte) (*types.Account, error) { return sp.getAccount(addr) }
func (sp *StateProcessor) IsValidator(addr []byte) bool {
	_, ok := sp.ValidatorSet[string(addr)]
	return ok
}

func (sp *StateProcessor) ResolveUsername(username string) ([]byte, bool) {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return nil, false
	}
	addr, ok := sp.usernameToAddr[trimmed]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), addr...), true
}

func (sp *StateProcessor) HasRole(role string, addr []byte) bool {
	if len(addr) == 0 {
		return false
	}
	manager := nhbstate.NewManager(sp.Trie)
	return manager.HasRole(role, addr)
}

func (sp *StateProcessor) LoyaltyBusinessByID(id loyalty.BusinessID) (*loyalty.Business, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	business := new(loyalty.Business)
	ok, err := manager.KVGet(nhbstate.LoyaltyBusinessKey(id), business)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return business, true, nil
}

func (sp *StateProcessor) LoyaltyProgramByID(id loyalty.ProgramID) (*loyalty.Program, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	program := new(loyalty.Program)
	ok, err := manager.KVGet(loyalty.ProgramStorageKey(id), program)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return program, true, nil
}

func (sp *StateProcessor) LoyaltyProgramsByOwner(owner [20]byte) ([]loyalty.ProgramID, error) {
	manager := nhbstate.NewManager(sp.Trie)
	var raw [][]byte
	if err := manager.KVGetList(loyalty.ProgramOwnerIndexKey(owner), &raw); err != nil {
		return nil, err
	}
	ids := make([]loyalty.ProgramID, 0, len(raw))
	seen := make(map[[32]byte]struct{}, len(raw))
	for _, entry := range raw {
		if len(entry) != len(loyalty.ProgramID{}) {
			continue
		}
		var id loyalty.ProgramID
		copy(id[:], entry)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	return ids, nil
}

func (sp *StateProcessor) LoyaltyBusinessByMerchant(merchant [20]byte) (*loyalty.Business, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	var id loyalty.BusinessID
	exists, err := manager.KVGet(nhbstate.LoyaltyMerchantIndexKey(merchant[:]), &id)
	if err != nil || !exists {
		if err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if id == (loyalty.BusinessID{}) {
		return nil, false, nil
	}
	business := new(loyalty.Business)
	ok, err := manager.KVGet(nhbstate.LoyaltyBusinessKey(id), business)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return business, true, nil
}

func (sp *StateProcessor) LoyaltyProgramDailyAccrued(programID loyalty.ProgramID, addr []byte, day string) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.LoyaltyProgramDailyAccrued(programID, addr, day)
}

func (sp *StateProcessor) SetLoyaltyProgramDailyAccrued(programID loyalty.ProgramID, addr []byte, day string, amount *big.Int) error {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.SetLoyaltyProgramDailyAccrued(programID, addr, day, amount)
}

func (sp *StateProcessor) LoyaltyProgramDailyTotalAccrued(programID loyalty.ProgramID, day string) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.LoyaltyProgramDailyTotalAccrued(programID, day)
}

func (sp *StateProcessor) SetLoyaltyProgramDailyTotalAccrued(programID loyalty.ProgramID, day string, amount *big.Int) error {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.SetLoyaltyProgramDailyTotalAccrued(programID, day, amount)
}

func (sp *StateProcessor) LoyaltyProgramEpochAccrued(programID loyalty.ProgramID, epoch uint64) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.LoyaltyProgramEpochAccrued(programID, epoch)
}

func (sp *StateProcessor) SetLoyaltyProgramEpochAccrued(programID loyalty.ProgramID, epoch uint64, amount *big.Int) error {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.SetLoyaltyProgramEpochAccrued(programID, epoch, amount)
}

func (sp *StateProcessor) LoyaltyProgramIssuanceAccrued(programID loyalty.ProgramID, addr []byte) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.LoyaltyProgramIssuanceAccrued(programID, addr)
}

func (sp *StateProcessor) SetLoyaltyProgramIssuanceAccrued(programID loyalty.ProgramID, addr []byte, amount *big.Int) error {
	manager := nhbstate.NewManager(sp.Trie)
	return manager.SetLoyaltyProgramIssuanceAccrued(programID, addr, amount)
}

func (sp *StateProcessor) MintToken(symbol string, addr []byte, amount *big.Int) error {
	if len(addr) != 20 {
		return fmt.Errorf("mint: address must be 20 bytes")
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("mint: invalid amount")
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("mint: token symbol required")
	}
	account, err := sp.getAccount(addr)
	if err != nil {
		return err
	}
	switch normalized {
	case "NHB":
		account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
		return sp.setAccount(addr, account)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
		return sp.setAccount(addr, account)
	default:
		manager := nhbstate.NewManager(sp.Trie)
		balance, err := manager.Balance(addr, normalized)
		if err != nil {
			return err
		}
		updated := new(big.Int).Add(balance, amount)
		return manager.SetBalance(addr, normalized, updated)
	}
}
