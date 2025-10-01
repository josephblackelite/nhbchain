package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/consensus/codec"
	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/claimable"
	"nhbchain/core/engagement"
	"nhbchain/core/epoch"
	"nhbchain/core/events"
	"nhbchain/core/identity"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	syncmgr "nhbchain/core/sync"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/creator"
	"nhbchain/native/escrow"
	govcfg "nhbchain/native/gov"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/loyalty"
	nativeparams "nhbchain/native/params"
	"nhbchain/native/potso"
	"nhbchain/native/reputation"
	swap "nhbchain/native/swap"
	"nhbchain/p2p"
	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/storage"
	"nhbchain/storage/trie"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

// Node is the central controller, wiring all components together.
type Node struct {
	db                           storage.Database
	state                        *StateProcessor
	chain                        *Blockchain
	syncMgr                      *syncmgr.Manager
	validatorKey                 *crypto.PrivateKey
	mempool                      []*types.Transaction
	mempoolMu                    sync.Mutex
	proposedTxs                  map[string]struct{}
	mempoolLimit                 int
	allowUnlimitedMempool        bool
	txValidationMu               sync.RWMutex
	txSimulationEnabled          bool
	bftEngine                    *bft.Engine
	stateMu                      sync.Mutex
	escrowTreasury               [20]byte
	engagementMgr                *engagement.Manager
	govPolicy                    governance.ProposalPolicy
	govPolicyMu                  sync.RWMutex
	swapCfgMu                    sync.RWMutex
	swapCfg                      swap.Config
	swapOracle                   swap.PriceOracle
	swapManual                   *swap.ManualOracle
	swapSanctions                swap.SanctionsChecker
	swapStatusMu                 sync.RWMutex
	swapOracleLast               int64
	swapRefundSink               [20]byte
	evidenceStore                *evidence.Store
	evidenceMaxAge               uint64
	paymasterMu                  sync.RWMutex
	paymasterEnabled             bool
	timestampTolerance           time.Duration
	timeConfigMu                 sync.RWMutex
	timeSource                   func() time.Time
	lendingMu                    sync.RWMutex
	lendingParams                lending.RiskParameters
	lendingModuleAddr            crypto.Address
	lendingCollateralAddr        crypto.Address
	lendingDeveloperFeeBps       uint64
	lendingDeveloperFeeCollector crypto.Address
	lendingInterestModel         *lending.InterestModel
	lendingReserveFactorBps      uint64
	lendingProtocolFeeBps        uint64
	lendingCollateralRouting     lending.CollateralRouting
	creatorPayoutVaultAddr       crypto.Address
	creatorRewardsTreasuryAddr   crypto.Address
	modulePauseMu                sync.RWMutex
	modulePauses                 map[string]bool
	moduleQuotaMu                sync.RWMutex
	moduleQuotas                 map[string]nativecommon.Quota
	potsoEngineMu                sync.Mutex
	potsoEngine                  *potso.Engine
	globalCfgMu                  sync.RWMutex
	globalCfg                    config.Global
}

const (
	rolePaymasterAdmin     = "ROLE_PAYMASTER_ADMIN"
	roleReputationVerifier = "ROLE_REPUTATION_VERIFIER"
	moduleLending          = "lending"
	moduleSwap             = "swap"
	moduleEscrow           = "escrow"
	moduleTrade            = "trade"
	moduleLoyalty          = "loyalty"
	modulePotso            = "potso"
)

var ErrPaymasterUnauthorized = errors.New("paymaster: caller lacks ROLE_PAYMASTER_ADMIN")

// ErrMilestoneUnsupported is returned when milestone functionality has not been
// wired into the node yet. The current implementation exposes the RPC surface
// but leaves execution to follow-on upgrades.
var ErrMilestoneUnsupported = errors.New("escrow: milestone engine not enabled")

// ErrReputationVerifierUnauthorized is returned when a caller lacks the
// required verifier role to issue skill attestations.
var ErrReputationVerifierUnauthorized = errors.New("reputation: caller lacks verifier role")

// ErrMempoolFull is returned when the node's mempool has reached its configured capacity.
var ErrMempoolFull = errors.New("mempool: transaction limit reached")

// ErrInvalidTransaction marks transactions that fail basic validation or cannot be executed.
var ErrInvalidTransaction = errors.New("mempool: invalid transaction")

// DefaultBlockTimestampTolerance bounds how far ahead of the local clock a
// block timestamp may drift before it is rejected.
const DefaultBlockTimestampTolerance = 5 * time.Second

// ErrBlockTimestampOutOfWindow marks blocks whose timestamps fall outside the
// permitted window derived from the previous block and the local clock.
var ErrBlockTimestampOutOfWindow = errors.New("block timestamp outside allowed window")

func (n *Node) blockTimestampTolerance() time.Duration {
	n.timeConfigMu.RLock()
	defer n.timeConfigMu.RUnlock()
	if n == nil || n.timestampTolerance <= 0 {
		return DefaultBlockTimestampTolerance
	}
	return n.timestampTolerance
}

// SetBlockTimestampTolerance configures the permissible drift when validating
// block timestamps. Zero or negative values restore the default tolerance.
func (n *Node) SetBlockTimestampTolerance(tolerance time.Duration) {
	if n == nil {
		return
	}
	if tolerance <= 0 {
		tolerance = DefaultBlockTimestampTolerance
	}
	n.timeConfigMu.Lock()
	n.timestampTolerance = tolerance
	n.timeConfigMu.Unlock()
}

func (n *Node) applyTimestampTolerance(seconds uint64) {
	tolerance := DefaultBlockTimestampTolerance
	if seconds > 0 {
		tolerance = time.Duration(seconds) * time.Second
	}
	n.SetBlockTimestampTolerance(tolerance)
}

// SetTimeSource overrides the node's clock. Passing nil restores the system
// clock. Primarily used by tests to simulate deterministic timelines.
func (n *Node) SetTimeSource(now func() time.Time) {
	if n == nil {
		return
	}
	source := now
	if source == nil {
		source = func() time.Time { return time.Now().UTC() }
	}
	n.timeConfigMu.Lock()
	n.timeSource = source
	n.timeConfigMu.Unlock()
}

func (n *Node) currentTime() time.Time {
	n.timeConfigMu.RLock()
	source := n.timeSource
	n.timeConfigMu.RUnlock()
	if source == nil {
		return time.Now().UTC()
	}
	return source().UTC()
}

func (n *Node) validateBlockTimestamp(ts int64) error {
	if n == nil || n.chain == nil {
		return fmt.Errorf("%w: chain unavailable", ErrBlockTimestampOutOfWindow)
	}
	prev := n.chain.LastTimestamp()
	tolerance := n.blockTimestampTolerance()
	now := n.currentTime()
	min := now.Add(-tolerance).Unix()
	if prev > min {
		min = prev
	}
	if ts < min {
		return fmt.Errorf("%w: timestamp %d precedes minimum %d", ErrBlockTimestampOutOfWindow, ts, min)
	}
	max := now.Add(tolerance).Unix()
	if ts > max {
		return fmt.Errorf("%w: timestamp %d exceeds maximum %d (now=%d tolerance=%s)", ErrBlockTimestampOutOfWindow, ts, max, now.Unix(), tolerance)
	}
	return nil
}

// PotsoLeaderboardEntry represents a participant's score for a specific day.
type PotsoLeaderboardEntry struct {
	Address [20]byte
	Meter   *potso.Meter
}

type governanceEventEmitter struct {
	state *StateProcessor
}

func (e governanceEventEmitter) Emit(evt events.Event) {
	if e.state == nil || evt == nil {
		return
	}
	type payload interface{ Event() *types.Event }
	if withPayload, ok := evt.(payload); ok {
		if event := withPayload.Event(); event != nil {
			e.state.AppendEvent(event)
		}
	}
}

func NewNode(db storage.Database, key *crypto.PrivateKey, genesisPath string, allowAutogenesis bool) (*Node, error) {
	validatorAddr := key.PubKey().Address()
	fmt.Printf("Starting node with validator address: %s\n", validatorAddr.String())

	chain, err := NewBlockchain(db, genesisPath, allowAutogenesis)
	if err != nil {
		return nil, err
	}

	// Load current state root from the chain tip (if any), then open the trie.
	var root []byte
	if header := chain.CurrentHeader(); header != nil {
		root = header.StateRoot
	}
	stateTrie, err := trie.NewTrie(db, root)
	if err != nil {
		return nil, err
	}
	stateProcessor, err := NewStateProcessor(stateTrie)
	if err != nil {
		return nil, err
	}

	var treasury [20]byte
	copy(treasury[:], validatorAddr.Bytes())
	stateProcessor.SetEscrowFeeTreasury(treasury)

	moduleAddr := deriveModuleAddress("module/lending/treasury", crypto.NHBPrefix)
	collateralAddr := deriveModuleAddress("module/lending/collateral", crypto.ZNHBPrefix)
	creatorVaultAddr := deriveModuleAddress("module/creator/payout", crypto.NHBPrefix)
	creatorRewardsAddr := deriveModuleAddress("module/creator/rewards", crypto.NHBPrefix)

	potsoEngine, err := potso.NewEngine(potso.DefaultEngineParams())
	if err != nil {
		return nil, err
	}

	node := &Node{
		db:                         db,
		state:                      stateProcessor,
		chain:                      chain,
		validatorKey:               key,
		mempool:                    make([]*types.Transaction, 0),
		proposedTxs:                make(map[string]struct{}),
		escrowTreasury:             treasury,
		engagementMgr:              engagement.NewManager(stateProcessor.EngagementConfig()),
		swapSanctions:              swap.DefaultSanctionsChecker,
		swapRefundSink:             treasury,
		evidenceStore:              evidence.NewStore(db),
		evidenceMaxAge:             evidence.DefaultMaxAgeBlocks,
		paymasterEnabled:           stateProcessor.PaymasterEnabled(),
		timestampTolerance:         DefaultBlockTimestampTolerance,
		timeSource:                 func() time.Time { return time.Now().UTC() },
		lendingModuleAddr:          moduleAddr,
		lendingCollateralAddr:      collateralAddr,
		creatorPayoutVaultAddr:     creatorVaultAddr,
		creatorRewardsTreasuryAddr: creatorRewardsAddr,
		modulePauses:               make(map[string]bool),
		moduleQuotas:               make(map[string]nativecommon.Quota),
		potsoEngine:                potsoEngine,
		txSimulationEnabled:        true,
		globalCfg: config.Global{
			Governance: config.Governance{
				QuorumBPS:        6000,
				PassThresholdBPS: 5000,
				VotingPeriodSecs: config.MinVotingPeriodSeconds,
			},
			Slashing: config.Slashing{
				MinWindowSecs: 60,
				MaxWindowSecs: 60,
			},
			Mempool: config.Mempool{MaxBytes: 1},
			Blocks:  config.Blocks{MaxTxs: 1},
		},
	}

	stateProcessor.SetQuotaConfig(node.moduleQuotas)

	node.SetModulePauses(config.Pauses{})
	node.stateMu.Lock()
	err = node.refreshModulePauses()
	node.stateMu.Unlock()
	if err != nil {
		return nil, err
	}

	node.SetLendingRiskParameters(lending.RiskParameters{})
	node.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	// Initialise fast-sync manager.
	if trieDB := stateTrie.TrieDB(); trieDB != nil {
		mgr := syncmgr.NewManager(chain.ChainID(), chain.Height(), trieDB)
		mgr.SetValidatorSet(buildValidatorSet(stateProcessor.ValidatorSet))
		node.syncMgr = mgr
	}

	return node, nil
}

func normalizeModuleName(module string) string {
	return strings.ToLower(strings.TrimSpace(module))
}

// refreshModulePauses loads pause configuration from state. Callers must hold
// stateMu while invoking this helper.
func (n *Node) refreshModulePauses() error {
	if n == nil || n.state == nil || n.state.Trie == nil {
		return nil
	}
	store := nativeparams.NewStore(nhbstate.NewManager(n.state.Trie))
	pauses, err := store.Pauses()
	if err != nil {
		return err
	}
	n.SetModulePauses(pauses)
	return nil
}

func (n *Node) SetModulePauses(pauses config.Pauses) {
	if n == nil {
		return
	}
	n.modulePauseMu.Lock()
	if n.modulePauses == nil {
		n.modulePauses = make(map[string]bool)
	}
	n.modulePauses[moduleLending] = pauses.Lending
	n.modulePauses[moduleSwap] = pauses.Swap
	n.modulePauses[moduleEscrow] = pauses.Escrow
	n.modulePauses[moduleTrade] = pauses.Trade
	n.modulePauses[moduleLoyalty] = pauses.Loyalty
	n.modulePauses[modulePotso] = pauses.POTSO
	n.modulePauseMu.Unlock()
	if n.state != nil {
		n.state.SetPauseView(n)
	}
}

// SetModuleQuotas updates the configured per-module quotas.
func (n *Node) SetModuleQuotas(quotas map[string]nativecommon.Quota) {
	if n == nil {
		return
	}
	snapshot := make(map[string]nativecommon.Quota, len(quotas))
	for module, quota := range quotas {
		name := normalizeModuleName(module)
		if name == "" {
			continue
		}
		snapshot[name] = quota
	}
	n.moduleQuotaMu.Lock()
	n.moduleQuotas = snapshot
	n.moduleQuotaMu.Unlock()
	if n.state != nil {
		n.state.SetQuotaConfig(snapshot)
	}
}

func (n *Node) SetModulePaused(module string, paused bool) {
	if n == nil {
		return
	}
	name := normalizeModuleName(module)
	if name == "" {
		return
	}
	n.modulePauseMu.Lock()
	if n.modulePauses == nil {
		n.modulePauses = make(map[string]bool)
	}
	n.modulePauses[name] = paused
	n.modulePauseMu.Unlock()
}

func (n *Node) moduleQuotaSnapshot() map[string]nativecommon.Quota {
	n.moduleQuotaMu.RLock()
	defer n.moduleQuotaMu.RUnlock()
	snapshot := make(map[string]nativecommon.Quota, len(n.moduleQuotas))
	for module, quota := range n.moduleQuotas {
		snapshot[module] = quota
	}
	return snapshot
}

func (n *Node) IsPaused(module string) bool {
	if n == nil {
		return false
	}
	name := normalizeModuleName(module)
	if name == "" {
		return false
	}
	n.modulePauseMu.RLock()
	paused := n.modulePauses[name]
	n.modulePauseMu.RUnlock()
	return paused
}

// SetMempoolUnlimitedOptIn toggles acceptance of an unbounded mempool.
func (n *Node) SetMempoolUnlimitedOptIn(allow bool) {
	if n == nil {
		return
	}
	n.mempoolMu.Lock()
	n.allowUnlimitedMempool = allow
	n.mempoolMu.Unlock()
}

// SetMempoolLimit configures the maximum number of transactions retained in the mempool.
// A zero limit disables enforcement only when unlimited operation has been explicitly enabled.
func (n *Node) SetMempoolLimit(limit int) {
	if n == nil {
		return
	}
	n.mempoolMu.Lock()
	allowUnlimited := n.allowUnlimitedMempool
	if limit <= 0 {
		if allowUnlimited {
			limit = 0
		} else {
			limit = config.DefaultMempoolMaxTransactions
		}
	}
	n.mempoolLimit = limit
	if limit > 0 && len(n.mempool) > limit {
		start := len(n.mempool) - limit
		removed := n.mempool[:start]
		if len(removed) > 0 && len(n.proposedTxs) > 0 {
			for _, tx := range removed {
				if key, err := transactionKey(tx); err == nil {
					delete(n.proposedTxs, key)
				}
			}
		}
		trimmed := make([]*types.Transaction, limit)
		copy(trimmed, n.mempool[start:])
		n.mempool = trimmed
	}
	n.mempoolMu.Unlock()
}

// SetTransactionSimulationEnabled toggles execution pre-checks during transaction validation.
// Primarily used by tests to bypass the expensive state copy when stressing the mempool.
func (n *Node) SetTransactionSimulationEnabled(enabled bool) {
	if n == nil {
		return
	}
	n.txValidationMu.Lock()
	n.txSimulationEnabled = enabled
	n.txValidationMu.Unlock()
}

func (n *Node) transactionSimulationEnabled() bool {
	if n == nil {
		return false
	}
	n.txValidationMu.RLock()
	enabled := n.txSimulationEnabled
	n.txValidationMu.RUnlock()
	return enabled
}

// SetGovernancePolicy updates the governance proposal policy applied to RPC actions.
func (n *Node) SetGovernancePolicy(policy governance.ProposalPolicy) {
	if n == nil {
		return
	}
	copyPolicy := governance.ProposalPolicy{
		VotingPeriodSeconds:            policy.VotingPeriodSeconds,
		TimelockSeconds:                policy.TimelockSeconds,
		AllowedParams:                  append([]string{}, policy.AllowedParams...),
		QuorumBps:                      policy.QuorumBps,
		PassThresholdBps:               policy.PassThresholdBps,
		AllowedRoles:                   append([]string{}, policy.AllowedRoles...),
		TreasuryAllowList:              append([][20]byte{}, policy.TreasuryAllowList...),
		BlockTimestampToleranceSeconds: policy.BlockTimestampToleranceSeconds,
	}
	if policy.MinDepositWei != nil {
		copyPolicy.MinDepositWei = new(big.Int).Set(policy.MinDepositWei)
	}
	n.govPolicyMu.Lock()
	n.govPolicy = copyPolicy
	n.govPolicyMu.Unlock()
	n.applyTimestampTolerance(copyPolicy.BlockTimestampToleranceSeconds)
}

// SetGlobalConfig records the last validated global configuration to use when
// preflighting governance policy proposals. Callers must ensure the
// configuration has been validated before invoking this method.
func (n *Node) SetGlobalConfig(cfg config.Global) {
	if n == nil {
		return
	}
	n.globalCfgMu.Lock()
	n.globalCfg = cfg
	n.globalCfgMu.Unlock()
}

func (n *Node) globalConfigSnapshot() config.Global {
	n.globalCfgMu.RLock()
	defer n.globalCfgMu.RUnlock()
	snapshot := config.Global{
		Governance: config.Governance{
			QuorumBPS:        n.globalCfg.Governance.QuorumBPS,
			PassThresholdBPS: n.globalCfg.Governance.PassThresholdBPS,
			VotingPeriodSecs: n.globalCfg.Governance.VotingPeriodSecs,
		},
		Slashing: config.Slashing{
			MinWindowSecs: n.globalCfg.Slashing.MinWindowSecs,
			MaxWindowSecs: n.globalCfg.Slashing.MaxWindowSecs,
		},
		Mempool: config.Mempool{MaxBytes: n.globalCfg.Mempool.MaxBytes},
		Blocks:  config.Blocks{MaxTxs: n.globalCfg.Blocks.MaxTxs},
		Pauses: config.Pauses{
			Lending: n.globalCfg.Pauses.Lending,
			Swap:    n.globalCfg.Pauses.Swap,
			Escrow:  n.globalCfg.Pauses.Escrow,
			Trade:   n.globalCfg.Pauses.Trade,
			Loyalty: n.globalCfg.Pauses.Loyalty,
			POTSO:   n.globalCfg.Pauses.POTSO,
		},
		Quotas: config.Quotas{
			Lending: n.globalCfg.Quotas.Lending,
			Swap:    n.globalCfg.Quotas.Swap,
			Escrow:  n.globalCfg.Quotas.Escrow,
			Trade:   n.globalCfg.Quotas.Trade,
			Loyalty: n.globalCfg.Quotas.Loyalty,
			POTSO:   n.globalCfg.Quotas.POTSO,
		},
	}
	return snapshot
}

func (n *Node) governancePolicy() governance.ProposalPolicy {
	n.govPolicyMu.RLock()
	defer n.govPolicyMu.RUnlock()
	policy := governance.ProposalPolicy{
		VotingPeriodSeconds:            n.govPolicy.VotingPeriodSeconds,
		TimelockSeconds:                n.govPolicy.TimelockSeconds,
		AllowedParams:                  append([]string{}, n.govPolicy.AllowedParams...),
		QuorumBps:                      n.govPolicy.QuorumBps,
		PassThresholdBps:               n.govPolicy.PassThresholdBps,
		AllowedRoles:                   append([]string{}, n.govPolicy.AllowedRoles...),
		TreasuryAllowList:              append([][20]byte{}, n.govPolicy.TreasuryAllowList...),
		BlockTimestampToleranceSeconds: n.govPolicy.BlockTimestampToleranceSeconds,
	}
	if n.govPolicy.MinDepositWei != nil {
		policy.MinDepositWei = new(big.Int).Set(n.govPolicy.MinDepositWei)
	}
	return policy
}

func (n *Node) newGovernanceEngine(manager *nhbstate.Manager) *governance.Engine {
	engine := governance.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(governanceEventEmitter{state: n.state})
	engine.SetPolicy(n.governancePolicy())
	engine.SetPolicyValidator(func(cur governance.PolicyBaseline, delta governance.PolicyDelta) error {
		baseline := n.globalConfigSnapshot()
		baseline.Governance.QuorumBPS = cur.QuorumBps
		baseline.Governance.PassThresholdBPS = cur.PassThresholdBps
		baseline.Governance.VotingPeriodSecs = cur.VotingPeriodSecs
		var proposal govcfg.PolicyDelta
		if delta.QuorumBps != nil || delta.PassThresholdBps != nil {
			proposal.Governance = &govcfg.GovernanceDelta{}
			if delta.QuorumBps != nil {
				quorum := *delta.QuorumBps
				proposal.Governance.QuorumBPS = &quorum
			}
			if delta.PassThresholdBps != nil {
				threshold := *delta.PassThresholdBps
				proposal.Governance.PassThresholdBPS = &threshold
			}
		}
		return govcfg.PreflightPolicyApply(baseline, proposal)
	})
	return engine
}

func (n *Node) SetBftEngine(bftEngine *bft.Engine) {
	n.bftEngine = bftEngine
}

// SetSwapConfig installs the swap mint configuration after applying canonical
// defaults to avoid surprising zero values.
func (n *Node) SetSwapConfig(cfg swap.Config) {
	if n == nil {
		return
	}
	normalised := cfg.Normalise()
	n.swapCfgMu.Lock()
	n.swapCfg = normalised
	n.swapCfgMu.Unlock()
}

// swapConfig returns a copy of the currently configured swap settings.
func (n *Node) swapConfig() swap.Config {
	n.swapCfgMu.RLock()
	cfg := n.swapCfg
	n.swapCfgMu.RUnlock()
	if len(cfg.AllowedFiat) == 0 {
		cfg = cfg.Normalise()
	}
	return cfg
}

// PaymasterModuleEnabled reports whether paymaster sponsorship is active.
func (n *Node) PaymasterModuleEnabled() bool {
	if n == nil {
		return false
	}
	n.paymasterMu.RLock()
	defer n.paymasterMu.RUnlock()
	return n.paymasterEnabled
}

// SetPaymasterModuleEnabled toggles the paymaster module after verifying the caller has admin privileges.
func (n *Node) SetPaymasterModuleEnabled(caller []byte, enabled bool) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	if len(caller) == 0 {
		return fmt.Errorf("caller address required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	if !n.state.HasRole(rolePaymasterAdmin, caller) {
		return ErrPaymasterUnauthorized
	}
	n.state.SetPaymasterEnabled(enabled)
	n.paymasterMu.Lock()
	n.paymasterEnabled = enabled
	n.paymasterMu.Unlock()
	return nil
}

// EvaluateSponsorship returns the sponsorship assessment for the provided transaction without executing it.
func (n *Node) EvaluateSponsorship(tx *types.Transaction) (*SponsorshipAssessment, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	if tx == nil {
		return nil, fmt.Errorf("transaction required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	return n.state.EvaluateSponsorship(tx)
}

// SetSwapOracle wires the price oracle aggregator used to validate vouchers.
func (n *Node) SetSwapOracle(oracle swap.PriceOracle) {
	if n == nil {
		return
	}
	n.swapCfgMu.Lock()
	n.swapOracle = oracle
	n.swapCfgMu.Unlock()
}

// SetSwapManualOracle records the manual oracle handle so tests and incident
// tooling can seed deterministic quotes.
func (n *Node) SetSwapManualOracle(manual *swap.ManualOracle) {
	if n == nil {
		return
	}
	n.swapCfgMu.Lock()
	n.swapManual = manual
	n.swapCfgMu.Unlock()
}

// SetSwapRefundSink overrides the refund sink used for voucher reversals.
func (n *Node) SetSwapRefundSink(addr [20]byte) {
	if n == nil {
		return
	}
	n.swapCfgMu.Lock()
	n.swapRefundSink = addr
	n.swapCfgMu.Unlock()
}

// SetSwapSanctionsChecker configures the sanctions hook invoked during swap mint processing.
func (n *Node) SetSwapSanctionsChecker(checker swap.SanctionsChecker) {
	if n == nil {
		return
	}
	if checker == nil {
		checker = swap.DefaultSanctionsChecker
	}
	n.swapCfgMu.Lock()
	n.swapSanctions = checker
	n.swapCfgMu.Unlock()
}

// SetSwapManualQuote publishes a manual override rate for the supplied pair. The
// quote timestamp is truncated to seconds to match other oracle adapters.
func (n *Node) SetSwapManualQuote(base, quote, rate string, ts time.Time) error {
	n.swapCfgMu.RLock()
	manual := n.swapManual
	n.swapCfgMu.RUnlock()
	if manual == nil {
		return fmt.Errorf("swap: manual oracle not configured")
	}
	return manual.SetDecimal(base, quote, rate, ts.UTC())
}

func (n *Node) swapSanctionsChecker() swap.SanctionsChecker {
	n.swapCfgMu.RLock()
	checker := n.swapSanctions
	n.swapCfgMu.RUnlock()
	if checker == nil {
		return swap.DefaultSanctionsChecker
	}
	return checker
}

// LendingModuleAddress returns the deterministic NHB treasury address used by the lending engine.
func (n *Node) LendingModuleAddress() crypto.Address {
	n.lendingMu.RLock()
	defer n.lendingMu.RUnlock()
	return cloneAddress(n.lendingModuleAddr)
}

// LendingCollateralAddress returns the deterministic ZNHB collateral vault for the lending engine.
func (n *Node) LendingCollateralAddress() crypto.Address {
	n.lendingMu.RLock()
	defer n.lendingMu.RUnlock()
	return cloneAddress(n.lendingCollateralAddr)
}

// SetLendingRiskParameters updates the risk configuration exposed to RPC clients.
func (n *Node) SetLendingRiskParameters(params lending.RiskParameters) {
	if n == nil {
		return
	}
	copyParams := lending.RiskParameters{
		MaxLTV:               params.MaxLTV,
		LiquidationThreshold: params.LiquidationThreshold,
		LiquidationBonus:     params.LiquidationBonus,
		CircuitBreakerActive: params.CircuitBreakerActive,
		DeveloperFeeCapBps:   params.DeveloperFeeCapBps,
		BorrowCaps:           params.BorrowCaps.Clone(),
		Oracle:               params.Oracle,
		Pauses:               params.Pauses,
	}
	if params.OracleAddress.Bytes() != nil {
		copyParams.OracleAddress = cloneAddress(params.OracleAddress)
	}
	n.lendingMu.Lock()
	n.lendingParams = copyParams
	n.lendingMu.Unlock()
}

// LendingRiskParameters returns the currently configured lending risk limits.
func (n *Node) LendingRiskParameters() lending.RiskParameters {
	n.lendingMu.RLock()
	params := n.lendingParams
	n.lendingMu.RUnlock()
	if params.OracleAddress.Bytes() != nil {
		params.OracleAddress = cloneAddress(params.OracleAddress)
	}
	params.BorrowCaps = params.BorrowCaps.Clone()
	return params
}

// SetLendingAccrualConfig configures the interest model and fee splits used by the lending engine.
func (n *Node) SetLendingAccrualConfig(reserveBps, protocolFeeBps uint64, model *lending.InterestModel) {
	if n == nil {
		return
	}
	n.lendingMu.Lock()
	n.lendingReserveFactorBps = reserveBps
	n.lendingProtocolFeeBps = protocolFeeBps
	if model != nil {
		n.lendingInterestModel = model.Clone()
	} else {
		n.lendingInterestModel = nil
	}
	n.lendingMu.Unlock()
}

// LendingReserveFactorBps exposes the configured reserve factor basis points.
func (n *Node) LendingReserveFactorBps() uint64 {
	n.lendingMu.RLock()
	bps := n.lendingReserveFactorBps
	n.lendingMu.RUnlock()
	return bps
}

// LendingProtocolFeeBps exposes the configured protocol fee basis points.
func (n *Node) LendingProtocolFeeBps() uint64 {
	n.lendingMu.RLock()
	bps := n.lendingProtocolFeeBps
	n.lendingMu.RUnlock()
	return bps
}

// LendingInterestModel returns a cloned copy of the lending interest model.
func (n *Node) LendingInterestModel() *lending.InterestModel {
	n.lendingMu.RLock()
	model := n.lendingInterestModel
	n.lendingMu.RUnlock()
	if model != nil {
		return model.Clone()
	}
	return nil
}

// SetLendingDeveloperFee configures the developer fee parameters enforced by
// the lending module. The collector address is cloned to prevent external
// mutation of shared state.
func (n *Node) SetLendingDeveloperFee(bps uint64, collector crypto.Address) {
	if n == nil {
		return
	}
	cloned := cloneAddress(collector)
	n.lendingMu.Lock()
	n.lendingDeveloperFeeBps = bps
	n.lendingDeveloperFeeCollector = cloned
	n.lendingMu.Unlock()
}

// LendingDeveloperFeeConfig returns the currently configured developer fee
// basis points and collector address.
func (n *Node) LendingDeveloperFeeConfig() (uint64, crypto.Address) {
	n.lendingMu.RLock()
	bps := n.lendingDeveloperFeeBps
	collector := cloneAddress(n.lendingDeveloperFeeCollector)
	n.lendingMu.RUnlock()
	return bps, collector
}

// SetLendingCollateralRouting configures the collateral routing defaults applied
// when instantiating lending engines. The routing is cloned to avoid external
// mutation of shared state.
func (n *Node) SetLendingCollateralRouting(routing lending.CollateralRouting) {
	if n == nil {
		return
	}
	clone := routing.Clone()
	n.lendingMu.Lock()
	n.lendingCollateralRouting = clone
	n.lendingMu.Unlock()
}

// LendingCollateralRouting returns a copy of the currently configured
// collateral routing defaults.
func (n *Node) LendingCollateralRouting() lending.CollateralRouting {
	n.lendingMu.RLock()
	routing := n.lendingCollateralRouting.Clone()
	n.lendingMu.RUnlock()
	return routing
}

// IsTreasuryAllowListed reports whether the supplied address is present in the
// governance-controlled treasury allow list. An empty allow list permits all
// addresses so operators can opt-out of restrictions.
func (n *Node) IsTreasuryAllowListed(addr crypto.Address) bool {
	if n == nil {
		return false
	}
	bytes := addr.Bytes()
	if len(bytes) == 0 {
		return false
	}
	n.govPolicyMu.RLock()
	defer n.govPolicyMu.RUnlock()
	if len(n.govPolicy.TreasuryAllowList) == 0 {
		return true
	}
	var raw [20]byte
	copy(raw[:], bytes)
	for _, allowed := range n.govPolicy.TreasuryAllowList {
		if allowed == raw {
			return true
		}
	}
	return false
}

func (n *Node) recordSwapOracleHealth(ts time.Time) {
	n.swapStatusMu.Lock()
	n.swapOracleLast = ts.UTC().Unix()
	n.swapStatusMu.Unlock()
}

func cloneBigInt(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}

func cloneAddress(addr crypto.Address) crypto.Address {
	bytes := addr.Bytes()
	if len(bytes) == 0 {
		return crypto.Address{}
	}
	return crypto.NewAddress(addr.Prefix(), append([]byte(nil), bytes...))
}

func deriveModuleAddress(seed string, prefix crypto.AddressPrefix) crypto.Address {
	hash := ethcrypto.Keccak256([]byte(seed))
	raw := append([]byte(nil), hash[len(hash)-20:]...)
	return crypto.NewAddress(prefix, raw)
}

func (n *Node) emitSwapLimitAlert(alert events.SwapLimitAlert) {
	if n == nil {
		return
	}
	if evt := alert.Event(); evt != nil {
		n.state.AppendEvent(evt)
	}
}

func (n *Node) emitSwapVelocityAlert(alert events.SwapVelocityAlert) {
	if n == nil {
		return
	}
	if evt := alert.Event(); evt != nil {
		n.state.AppendEvent(evt)
	}
}

func (n *Node) emitSwapSanctionAlert(alert events.SwapSanctionAlert) {
	if n == nil {
		return
	}
	if evt := alert.Event(); evt != nil {
		n.state.AppendEvent(evt)
	}
}

func (n *Node) StartConsensus() {
	if n.bftEngine != nil {
		n.bftEngine.Start()
	}
}

// ProcessNetworkMessage is the central router for all incoming P2P messages.
func (n *Node) ProcessNetworkMessage(msg *p2p.Message) error {
	switch msg.Type {
	case p2p.MsgTypeTx:
		tx := new(types.Transaction)
		if err := json.Unmarshal(msg.Payload, tx); err != nil {
			return err
		}
		if err := n.AddTransaction(tx); err != nil {
			if errors.Is(err, ErrInvalidTransaction) {
				return fmt.Errorf("%w: %v", p2p.ErrInvalidPayload, err)
			}
			return err
		}

	case p2p.MsgTypeProposal:
		proposal := new(bft.SignedProposal)
		if err := json.Unmarshal(msg.Payload, proposal); err != nil {
			return err
		}
		if n.bftEngine != nil {
			return n.bftEngine.HandleProposal(proposal)
		}

	case p2p.MsgTypeVote:
		vote := new(bft.SignedVote)
		if err := json.Unmarshal(msg.Payload, vote); err != nil {
			return err
		}
		if n.bftEngine != nil {
			return n.bftEngine.HandleVote(vote)
		}
	}
	return nil
}

// HandleMessage satisfies the p2p.MessageHandler interface by forwarding to ProcessNetworkMessage.
func (n *Node) HandleMessage(msg *p2p.Message) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	return n.ProcessNetworkMessage(msg)
}

func (n *Node) AddTransaction(tx *types.Transaction) error {
	if n == nil || tx == nil {
		return fmt.Errorf("add transaction: invalid arguments")
	}
	if err := n.validateTransaction(tx); err != nil {
		return err
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()

	if tx.Type == types.TxTypeMint {
		voucher, _, err := decodeMintTransaction(tx.Data)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
		}
		if voucher == nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransaction, ErrMintInvalidPayload)
		}
		invoiceID := voucher.TrimmedInvoiceID()
		for _, existing := range n.mempool {
			if existing == nil || existing.Type != types.TxTypeMint {
				continue
			}
			existingVoucher, _, err := decodeMintTransaction(existing.Data)
			if err != nil || existingVoucher == nil {
				continue
			}
			if existingVoucher.TrimmedInvoiceID() == invoiceID {
				return ErrMintInvoiceUsed
			}
		}
	}

	if limit := n.mempoolLimit; limit > 0 && len(n.mempool) >= limit {
		return ErrMempoolFull
	}
	n.mempool = append(n.mempool, tx)
	return nil
}

func (n *Node) validateTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("add transaction: nil transaction")
	}
	if tx.ChainID == nil {
		return fmt.Errorf("%w: missing chain id", ErrInvalidTransaction)
	}
	if !types.IsValidChainID(tx.ChainID) {
		return fmt.Errorf("%w: unexpected chain id %s", ErrInvalidTransaction, tx.ChainID.String())
	}
	if tx.Type != types.TxTypeMint {
		if _, err := tx.From(); err != nil {
			return fmt.Errorf("%w: recover sender: %w", ErrInvalidTransaction, err)
		}
	}
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	if !n.transactionSimulationEnabled() {
		return nil
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	stateCopy, err := n.state.Copy()
	if err != nil {
		return err
	}
	stateCopy.events = nil
	stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())
	var blockHeight uint64
	if n.chain != nil {
		blockHeight = n.chain.GetHeight()
	}
	blockTime := n.currentTime()
	stateCopy.BeginBlock(blockHeight, blockTime)
	defer stateCopy.EndBlock()
	if _, err := stateCopy.ExecuteTransaction(tx); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}
	return nil
}

// SubmitTransaction enqueues the provided transaction for inclusion in a future block.
func (n *Node) SubmitTransaction(tx *types.Transaction) error {
	return n.AddTransaction(tx)
}

// --- Methods for bft.NodeInterface ---

func (n *Node) GetMempool() []*types.Transaction {
	if n == nil {
		return nil
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()

	if n.proposedTxs == nil {
		n.proposedTxs = make(map[string]struct{})
	}

	if len(n.mempool) > 0 {
		now := n.currentTime().Unix()
		filtered := n.mempool[:0]
		for _, tx := range n.mempool {
			if tx == nil {
				continue
			}
			if tx.Type == types.TxTypeMint {
				voucher, _, err := decodeMintTransaction(tx.Data)
				if err != nil || voucher == nil || voucher.Expiry <= now {
					if key, keyErr := transactionKey(tx); keyErr == nil {
						delete(n.proposedTxs, key)
					}
					continue
				}
			}
			filtered = append(filtered, tx)
		}
		for i := len(filtered); i < len(n.mempool); i++ {
			n.mempool[i] = nil
		}
		n.mempool = filtered
	}

	txs := make([]*types.Transaction, 0, len(n.mempool))
	for _, tx := range n.mempool {
		key, err := transactionKey(tx)
		if err != nil {
			continue
		}
		if _, alreadyProposed := n.proposedTxs[key]; alreadyProposed {
			continue
		}
		n.proposedTxs[key] = struct{}{}
		txs = append(txs, tx)
	}
	return txs
}

func transactionKey(tx *types.Transaction) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("nil transaction")
	}
	hash, err := tx.Hash()
	if err != nil {
		return "", err
	}
	if tx.Type == types.TxTypeMint {
		return hex.EncodeToString(hash), nil
	}
	from, err := tx.From()
	if err != nil {
		return "", err
	}
	key := make([]byte, len(hash)+len(from))
	copy(key, hash)
	copy(key[len(hash):], from)
	return hex.EncodeToString(key), nil
}

func (n *Node) requeueTransactions(txs []*types.Transaction) {
	if n == nil || len(txs) == 0 {
		return
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	if len(n.proposedTxs) == 0 {
		return
	}
	for _, tx := range txs {
		key, err := transactionKey(tx)
		if err != nil {
			continue
		}
		delete(n.proposedTxs, key)
	}
}

func (n *Node) markTransactionsCommitted(txs []*types.Transaction) {
	if n == nil || len(txs) == 0 {
		return
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	if len(n.mempool) == 0 && len(n.proposedTxs) == 0 {
		return
	}

	committed := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		key, err := transactionKey(tx)
		if err != nil {
			continue
		}
		committed[key] = struct{}{}
		delete(n.proposedTxs, key)
	}
	if len(committed) == 0 || len(n.mempool) == 0 {
		return
	}

	filtered := n.mempool[:0]
	for _, tx := range n.mempool {
		key, err := transactionKey(tx)
		if err != nil {
			filtered = append(filtered, tx)
			continue
		}
		if _, ok := committed[key]; ok {
			continue
		}
		filtered = append(filtered, tx)
	}
	for i := len(filtered); i < len(n.mempool); i++ {
		n.mempool[i] = nil
	}
	n.mempool = filtered
}

func (n *Node) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	blockTime := n.currentTime()
	timestamp := blockTime.Unix()

	var pruned []*types.Transaction
	if len(txs) > 0 {
		filtered := make([]*types.Transaction, 0, len(txs))
		for _, tx := range txs {
			if tx == nil {
				continue
			}
			if tx.Type == types.TxTypeMint {
				voucher, _, err := decodeMintTransaction(tx.Data)
				if err != nil || voucher == nil || voucher.Expiry <= timestamp {
					pruned = append(pruned, tx)
					continue
				}
			}
			filtered = append(filtered, tx)
		}
		txs = filtered
	}

	if len(pruned) > 0 {
		n.markTransactionsCommitted(pruned)
	}

	// Clamp the proposal to the configured transaction cap to avoid building
	// blocks that exceed the active limit. The slice header is adjusted
	// locally so callers (for example, the mempool) retain their full view of
	// pending transactions.
	maxTxs := n.globalConfigSnapshot().Blocks.MaxTxs
	if maxTxs > 0 && int64(len(txs)) > maxTxs {
		if maxTxs > int64(math.MaxInt) {
			maxTxs = int64(math.MaxInt)
		}
		txs = txs[:int(maxTxs)]
	}

	header := &types.BlockHeader{
		Height:    n.chain.GetHeight() + 1,
		Timestamp: timestamp,
		PrevHash:  n.chain.Tip(),
		Validator: n.validatorKey.PubKey().Address().Bytes(),
	}

	// Compute TxRoot over ordered transactions
	txRoot, err := ComputeTxRoot(txs)
	if err != nil {
		return nil, err
	}
	header.TxRoot = txRoot

	// Execute against a copy of StateDB to derive StateRoot
	n.stateMu.Lock()
	if err := n.refreshModulePauses(); err != nil {
		n.stateMu.Unlock()
		return nil, err
	}
	stateCopy, err := n.state.Copy()
	n.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	stateCopy.SetPauseView(n)
	stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())
	blockTime = time.Unix(header.Timestamp, 0).UTC()
	stateCopy.BeginBlock(header.Height, blockTime)
	defer stateCopy.EndBlock()
	for _, tx := range txs {
		if err := stateCopy.ApplyTransaction(tx); err != nil {
			return nil, err
		}
	}
	stateRoot := stateCopy.PendingRoot()
	header.StateRoot = stateRoot.Bytes()

	return types.NewBlock(header, txs), nil
}

func (n *Node) CommitBlock(b *types.Block) (err error) {
	var proposedTxs []*types.Transaction
	if b != nil {
		proposedTxs = b.Transactions
	}
	var prunedTxs []*types.Transaction
	defer func() {
		if len(proposedTxs) == 0 {
			return
		}
		if err != nil {
			if len(prunedTxs) == 0 {
				n.requeueTransactions(proposedTxs)
				return
			}
			dropped := make(map[string]struct{}, len(prunedTxs))
			for _, tx := range prunedTxs {
				key, keyErr := transactionKey(tx)
				if keyErr != nil {
					continue
				}
				dropped[key] = struct{}{}
			}
			if len(dropped) == 0 {
				n.requeueTransactions(proposedTxs)
				return
			}
			requeue := make([]*types.Transaction, 0, len(proposedTxs))
			for _, tx := range proposedTxs {
				key, keyErr := transactionKey(tx)
				if keyErr != nil {
					requeue = append(requeue, tx)
					continue
				}
				if _, skip := dropped[key]; skip {
					continue
				}
				requeue = append(requeue, tx)
			}
			n.requeueTransactions(requeue)
		} else {
			n.markTransactionsCommitted(proposedTxs)
		}
	}()

	// Verify TxRoot before executing
	txRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return err
	}
	if !bytes.Equal(txRoot, b.Header.TxRoot) {
		return fmt.Errorf("tx root mismatch")
	}

	if err := n.validateBlockTimestamp(b.Header.Timestamp); err != nil {
		return err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	// Remember parent root for rollback on any failure
	parentRoot := n.state.CurrentRoot()
	rollback := func() error {
		if err := n.state.ResetToRoot(parentRoot); err != nil {
			return fmt.Errorf("rollback to parent root: %w", err)
		}
		return nil
	}

	if err := n.refreshModulePauses(); err != nil {
		return err
	}

	blockTime := time.Unix(b.Header.Timestamp, 0).UTC()
	n.state.BeginBlock(b.Header.Height, blockTime)
	defer n.state.EndBlock()

	// Apply transactions; abort (and rollback) on the first failure
	for i, tx := range b.Transactions {
		if err := n.state.ApplyTransaction(tx); err != nil {
			fatalMint := isFatalMintError(err)
			if rbErr := rollback(); rbErr != nil {
				return fmt.Errorf("apply transaction %d: %v (rollback failed: %w)", i, err, rbErr)
			}
			if fatalMint {
				prunedTxs = append(prunedTxs, tx)
				n.markTransactionsCommitted([]*types.Transaction{tx})
			}
			return fmt.Errorf("apply transaction %d: %w", i, err)
		}
	}

	// Check derived StateRoot matches header (if header set) or fill it
	if err := n.state.ProcessBlockLifecycle(b.Header.Height, b.Header.Timestamp); err != nil {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("block lifecycle: %v (rollback failed: %w)", err, rbErr)
		}
		return fmt.Errorf("block lifecycle: %w", err)
	}

	if err := n.refreshModulePauses(); err != nil {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("refresh module pauses: %v (rollback failed: %w)", err, rbErr)
		}
		return fmt.Errorf("refresh module pauses: %w", err)
	}

	pendingRoot := n.state.PendingRoot()
	pendingBytes := pendingRoot.Bytes()
	if len(b.Header.StateRoot) == 0 {
		b.Header.StateRoot = pendingBytes
	} else if !bytes.Equal(b.Header.StateRoot, pendingBytes) {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("state root mismatch: %w", rbErr)
		}
		return fmt.Errorf("state root mismatch")
	}

	// Commit state at this height
	committedRoot, err := n.state.Commit(b.Header.Height)
	if err != nil {
		if rbErr := rollback(); rbErr != nil {
			return fmt.Errorf("state commit failed: %v (rollback failed: %w)", err, rbErr)
		}
		return fmt.Errorf("state commit failed: %w", err)
	}
	committedBytes := committedRoot.Bytes()
	if !bytes.Equal(b.Header.StateRoot, committedBytes) {
		return fmt.Errorf("state root mismatch after commit")
	}

	// Persist block to the chain
	if err := n.chain.AddBlock(b); err != nil {
		return err
	}
	if n.syncMgr != nil && b != nil && b.Header != nil {
		n.syncMgr.SetHeight(b.Header.Height)
	}
	return nil
}

func isFatalMintError(err error) bool {
	switch {
	case errors.Is(err, ErrMintExpired):
		return true
	case errors.Is(err, ErrMintInvalidChainID):
		return true
	case errors.Is(err, ErrMintInvalidPayload):
		return true
	case errors.Is(err, ErrMintInvalidSigner):
		return true
	case errors.Is(err, ErrMintInvoiceUsed):
		return true
	default:
		return false
	}
}

func (n *Node) GetValidatorSet() map[string]*big.Int {
	if n == nil || n.state == nil {
		return nil
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	if n.state.ValidatorSet == nil {
		return make(map[string]*big.Int)
	}

	snapshot := make(map[string]*big.Int, len(n.state.ValidatorSet))
	for addr, power := range n.state.ValidatorSet {
		if power != nil {
			snapshot[addr] = new(big.Int).Set(power)
		} else {
			snapshot[addr] = nil
		}
	}
	return snapshot
}
func (n *Node) GetHeight() uint64 { return n.chain.Height() }

// GetBlockByHeight retrieves the block stored at the requested height.
func (n *Node) GetBlockByHeight(height uint64) (*types.Block, error) {
	if n == nil || n.chain == nil {
		return nil, fmt.Errorf("blockchain not initialised")
	}
	return n.chain.GetBlockByHeight(height)
}

// PotsoSubmitEvidence validates and persists a misbehaviour report.
func (n *Node) PotsoSubmitEvidence(ev evidence.Evidence) (*evidence.Receipt, error) {
	if n == nil {
		return nil, fmt.Errorf("node not initialised")
	}
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return nil, err
	}
	if n.evidenceStore == nil {
		n.evidenceStore = evidence.NewStore(n.db)
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		return nil, err
	}
	currentHeight := uint64(0)
	if n.chain != nil {
		currentHeight = n.chain.Height()
	}
	maxAge := n.evidenceMaxAge
	if maxAge == 0 {
		maxAge = evidence.DefaultMaxAgeBlocks
	}
	heightLookup := func(height uint64) bool {
		if n.chain == nil {
			return false
		}
		_, err := n.chain.GetBlockByHeight(height)
		return err == nil
	}
	validationErr := evidence.ValidateEvidence(&ev, hash, currentHeight, maxAge, heightLookup)
	receipt := &evidence.Receipt{Hash: hash}
	if validationErr != nil {
		receipt.Status = evidence.ReceiptStatusRejected
		receipt.Reason = validationErr
		if evt := (events.PotsoEvidenceRejected{Reporter: ev.Reporter, Reason: string(validationErr.Reason)}).Event(); evt != nil {
			n.state.AppendEvent(evt)
		}
		return receipt, nil
	}
	record, created, err := n.evidenceStore.Put(hash, ev, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	receipt.Record = record
	if created {
		receipt.Status = evidence.ReceiptStatusAccepted
		minHeight := uint64(0)
		if record != nil {
			minHeight = record.MinHeight()
		}
		evt := events.PotsoEvidenceAccepted{
			Hash:         hash,
			EvidenceType: string(ev.Type),
			Offender:     ev.Offender,
			Height:       minHeight,
			Reporter:     ev.Reporter,
		}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
	} else {
		receipt.Status = evidence.ReceiptStatusIdempotent
	}
	return receipt, nil
}

// PotsoEvidenceByHash retrieves persisted evidence by canonical hash.
func (n *Node) PotsoEvidenceByHash(hash [32]byte) (*evidence.Record, bool, error) {
	if n == nil || n.evidenceStore == nil {
		return nil, false, fmt.Errorf("evidence store not initialised")
	}
	return n.evidenceStore.Get(hash)
}

// PotsoEvidenceList returns stored evidence filtered by the provided constraints.
func (n *Node) PotsoEvidenceList(filter evidence.Filter) ([]*evidence.Record, int, error) {
	if n == nil || n.evidenceStore == nil {
		return nil, 0, fmt.Errorf("evidence store not initialised")
	}
	return n.evidenceStore.List(filter)
}

// SyncManager exposes the fast-sync subsystem for RPC handlers.
func (n *Node) SyncManager() *syncmgr.Manager { return n.syncMgr }

// SnapshotExport produces a snapshot manifest in the supplied directory.
func (n *Node) SnapshotExport(ctx context.Context, outDir string) (*syncmgr.SnapshotManifest, error) {
	if n == nil || n.syncMgr == nil {
		return nil, fmt.Errorf("fast-sync manager not initialised")
	}
	root := n.state.CurrentRoot()
	var checkpointHash []byte
	height := n.chain.Height()
	if header := n.chain.CurrentHeader(); header != nil {
		height = header.Height
		if len(header.StateRoot) > 0 {
			root = common.BytesToHash(header.StateRoot)
		}
		if hash, err := header.Hash(); err == nil {
			checkpointHash = hash
		}
	}
	manifest, err := n.syncMgr.ExportSnapshot(ctx, height, root, outDir)
	if err != nil {
		return nil, err
	}
	if len(checkpointHash) > 0 {
		manifest.Checkpoint = append([]byte(nil), checkpointHash...)
		if manifest.Metadata == nil {
			manifest.Metadata = make(map[string]string)
		}
		manifest.Metadata["checkpointHeight"] = strconv.FormatUint(height, 10)
		manifest.Metadata["checkpointHash"] = hex.EncodeToString(checkpointHash)
	}
	return manifest, nil
}

// SnapshotImport verifies and installs a snapshot manifest/chunk set.
func (n *Node) SnapshotImport(ctx context.Context, manifest *syncmgr.SnapshotManifest, chunkDir string) (common.Hash, error) {
	if n == nil || n.syncMgr == nil {
		return common.Hash{}, fmt.Errorf("fast-sync manager not initialised")
	}
	if manifest.ChainID != 0 && manifest.ChainID != n.chain.ChainID() {
		return common.Hash{}, fmt.Errorf("snapshot chain mismatch: manifest=%d local=%d", manifest.ChainID, n.chain.ChainID())
	}
	root, err := n.syncMgr.ImportSnapshot(ctx, manifest, chunkDir)
	if err != nil {
		return common.Hash{}, err
	}
	n.stateMu.Lock()
	if err := n.state.ResetToRoot(root); err != nil {
		n.stateMu.Unlock()
		return common.Hash{}, err
	}
	if err := n.refreshModulePauses(); err != nil {
		n.stateMu.Unlock()
		return common.Hash{}, err
	}
	n.stateMu.Unlock()
	n.syncMgr.SetHeight(manifest.Height)
	n.refreshValidatorSet()
	return root, nil
}

func (n *Node) refreshValidatorSet() {
	if n == nil || n.syncMgr == nil {
		return
	}
	n.syncMgr.SetValidatorSet(buildValidatorSet(n.state.ValidatorSet))
}

func buildValidatorSet(source map[string]*big.Int) *syncmgr.ValidatorSet {
	validators := make([]syncmgr.Validator, 0, len(source))
	for key, power := range source {
		addr := []byte(key)
		validators = append(validators, syncmgr.Validator{
			Address: append([]byte(nil), addr...),
			Power:   validatorPower(power),
		})
	}
	return syncmgr.NewValidatorSet(validators)
}

func validatorPower(v *big.Int) uint64 {
	if v == nil || v.Sign() <= 0 {
		return 0
	}
	if v.BitLen() > 63 {
		return math.MaxUint64
	}
	return v.Uint64()
}
func (n *Node) GetAccount(addr []byte) (*types.Account, error) {
	return n.state.GetAccount(addr)
}

func (n *Node) EpochConfig() epoch.Config {
	return n.state.EpochConfig()
}

func (n *Node) SetEpochConfig(cfg epoch.Config) error {
	return n.state.SetEpochConfig(cfg)
}

func (n *Node) EpochSnapshot(epochNumber uint64) (*epoch.Snapshot, bool) {
	return n.state.EpochSnapshot(epochNumber)
}

func (n *Node) LatestEpochSnapshot() (*epoch.Snapshot, bool) {
	return n.state.LatestEpochSnapshot()
}

func (n *Node) LatestEpochSummary() (*epoch.Summary, bool) {
	return n.state.LatestEpochSummary()
}

func (n *Node) EpochSummary(epochNumber uint64) (*epoch.Summary, bool) {
	snapshot, ok := n.state.EpochSnapshot(epochNumber)
	if !ok {
		return nil, false
	}
	summary := snapshot.Summary()
	return &summary, true
}

func (n *Node) RewardConfig() rewards.Config {
	return n.state.RewardConfig()
}

func (n *Node) SetRewardConfig(cfg rewards.Config) error {
	return n.state.SetRewardConfig(cfg)
}

func (n *Node) PotsoRewardConfig() potso.RewardConfig {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.PotsoRewardConfig()
}

func (n *Node) SetPotsoRewardConfig(cfg potso.RewardConfig) error {
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return err
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if err := n.state.SetPotsoRewardConfig(cfg); err != nil {
		return err
	}
	n.potsoEngineMu.Lock()
	if n.potsoEngine != nil {
		n.potsoEngine.Reset()
	}
	n.potsoEngineMu.Unlock()
	return nil
}

func (n *Node) PotsoWeightConfig() potso.WeightParams {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.PotsoWeightConfig()
}

func (n *Node) SetPotsoWeightConfig(cfg potso.WeightParams) error {
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return err
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.SetPotsoWeightConfig(cfg)
}

// SetPotsoEngineParams overrides the runtime heartbeat engine parameters.
func (n *Node) SetPotsoEngineParams(params potso.EngineParams) error {
	n.potsoEngineMu.Lock()
	defer n.potsoEngineMu.Unlock()
	if n.potsoEngine == nil {
		engine, err := potso.NewEngine(potso.DefaultEngineParams())
		if err != nil {
			return err
		}
		n.potsoEngine = engine
	}
	return n.potsoEngine.SetParams(params)
}

func (n *Node) RewardEpochSettlement(epochNumber uint64) (*rewards.EpochSettlement, bool) {
	return n.state.RewardEpochSettlement(epochNumber)
}

func (n *Node) LatestRewardEpochSettlement() (*rewards.EpochSettlement, bool) {
	return n.state.LatestRewardEpochSettlement()
}

func (n *Node) PotsoLatestRewardEpoch() (uint64, bool, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	value, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	return value, true, nil
}

func (n *Node) PotsoRewardEpochInfo(epoch uint64) (*potso.RewardEpochMeta, bool, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	meta, ok, err := manager.PotsoRewardsGetMeta(epoch)
	if err != nil {
		return nil, false, err
	}
	if !ok || meta == nil {
		return nil, false, nil
	}
	cloned := meta.Clone()
	return &cloned, true, nil
}

func (n *Node) PotsoRewardEpochPayouts(epoch uint64, cursor *[20]byte, limit int) ([]potso.RewardPayout, error) {
	if limit <= 0 {
		limit = 50
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	winners, err := manager.PotsoRewardsListWinners(epoch)
	if err != nil {
		return nil, err
	}
	start := 0
	if cursor != nil {
		for i := range winners {
			if winners[i] == *cursor {
				start = i + 1
				break
			}
		}
	}
	if start >= len(winners) {
		return []potso.RewardPayout{}, nil
	}
	end := start + limit
	if end > len(winners) {
		end = len(winners)
	}
	result := make([]potso.RewardPayout, 0, end-start)
	for i := start; i < end; i++ {
		amount, ok, err := manager.PotsoRewardsGetPayout(epoch, winners[i])
		if err != nil {
			return nil, err
		}
		if !ok || amount == nil {
			amount = big.NewInt(0)
		} else {
			amount = new(big.Int).Set(amount)
		}
		result = append(result, potso.RewardPayout{
			Address: winners[i],
			Amount:  amount,
		})
	}
	return result, nil
}

// NetworkSeedsParam retrieves the on-chain network.seeds registry payload if present.
func (n *Node) NetworkSeedsParam() ([]byte, bool, error) {
	if n == nil || n.state == nil {
		return nil, false, fmt.Errorf("state unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	raw, ok, err := manager.ParamStoreGet("network.seeds")
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), raw...), true, nil
}

func (n *Node) PotsoRewardClaim(epoch uint64, addr [20]byte) (bool, *big.Int, error) {
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return false, nil, err
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	cfg := n.state.PotsoRewardConfig()
	if cfg.EffectivePayoutMode() != potso.RewardPayoutModeClaim {
		return false, nil, potso.ErrClaimingDisabled
	}

	manager := nhbstate.NewManager(n.state.Trie)
	claim, ok, err := manager.PotsoRewardsGetClaim(epoch, addr)
	if err != nil {
		return false, nil, err
	}
	if !ok || claim == nil {
		return false, nil, potso.ErrRewardNotFound
	}
	amount := big.NewInt(0)
	if claim.Amount != nil {
		amount = new(big.Int).Set(claim.Amount)
	}
	if claim.Claimed {
		return false, amount, nil
	}

	treasury, err := manager.GetAccount(cfg.TreasuryAddress[:])
	if err != nil {
		return false, nil, err
	}
	if treasury.BalanceZNHB == nil {
		treasury.BalanceZNHB = big.NewInt(0)
	}
	if treasury.BalanceZNHB.Cmp(amount) < 0 {
		return false, nil, potso.ErrInsufficientTreasury
	}

	account, err := manager.GetAccount(addr[:])
	if err != nil {
		return false, nil, err
	}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}

	treasury.BalanceZNHB = new(big.Int).Sub(treasury.BalanceZNHB, amount)
	account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)

	if err := manager.PutAccount(cfg.TreasuryAddress[:], treasury); err != nil {
		return false, nil, err
	}
	if err := manager.PutAccount(addr[:], account); err != nil {
		return false, nil, err
	}

	claim.Claimed = true
	claim.ClaimedAt = uint64(time.Now().UTC().Unix())
	if !claim.Mode.Valid() {
		claim.Mode = potso.RewardPayoutModeClaim
	} else {
		claim.Mode = claim.Mode.Normalise()
	}
	if err := manager.PotsoRewardsSetClaim(epoch, addr, claim); err != nil {
		return false, nil, err
	}
	if amount.Sign() > 0 {
		entry := potso.RewardHistoryEntry{Epoch: epoch, Amount: new(big.Int).Set(amount), Mode: claim.Mode}
		if err := manager.PotsoRewardsAppendHistory(addr, entry); err != nil {
			return false, nil, err
		}
		if evt := (events.PotsoRewardPaid{Epoch: epoch, Address: addr, Amount: new(big.Int).Set(amount), Mode: claim.Mode}).Event(); evt != nil {
			n.state.AppendEvent(evt)
		}
	}
	return true, amount, nil
}

func (n *Node) PotsoRewardsHistory(addr [20]byte, cursor string, limit int) ([]potso.RewardHistoryEntry, string, error) {
	if limit <= 0 {
		limit = 50
	}
	offset := 0
	if strings.TrimSpace(cursor) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(cursor))
		if err != nil || parsed < 0 {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		offset = parsed
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	history, err := manager.PotsoRewardsHistory(addr)
	if err != nil {
		return nil, "", err
	}
	if len(history) == 0 || offset >= len(history) {
		return []potso.RewardHistoryEntry{}, "", nil
	}

	// History is stored oldest to newest; serve newest-first slices.
	endIndex := len(history) - 1 - offset
	if endIndex < 0 {
		return []potso.RewardHistoryEntry{}, "", nil
	}
	startIndex := endIndex - limit + 1
	if startIndex < 0 {
		startIndex = 0
	}

	result := make([]potso.RewardHistoryEntry, 0, endIndex-startIndex+1)
	for i := endIndex; i >= startIndex; i-- {
		clone := history[i].Clone()
		result = append(result, clone)
	}

	nextOffset := offset + len(result)
	nextCursor := ""
	if nextOffset < len(history) {
		nextCursor = strconv.Itoa(nextOffset)
	}
	return result, nextCursor, nil
}

func (n *Node) PotsoExportEpoch(epoch uint64) ([]byte, *big.Int, int, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	data, total, winners, err := manager.PotsoRewardsBuildCSV(epoch)
	if err != nil {
		return nil, nil, 0, err
	}
	copied := append([]byte(nil), data...)
	if total == nil {
		total = big.NewInt(0)
	} else {
		total = new(big.Int).Set(total)
	}
	return copied, total, winners, nil
}
func (n *Node) PotsoLeaderboard(epoch uint64, offset, limit int) (uint64, uint64, []potso.StoredWeightEntry, error) {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	target := epoch
	if target == 0 {
		last, ok, err := manager.PotsoRewardsLastProcessedEpoch()
		if err != nil {
			return 0, 0, nil, err
		}
		if !ok {
			return 0, 0, []potso.StoredWeightEntry{}, nil
		}
		target = last
	}

	snapshot, ok, err := manager.PotsoMetricsGetSnapshot(target)
	if err != nil {
		return 0, 0, nil, err
	}
	if !ok || snapshot == nil {
		return target, 0, []potso.StoredWeightEntry{}, nil
	}
	entries := snapshot.Entries
	total := uint64(len(entries))
	if offset >= len(entries) {
		return target, total, []potso.StoredWeightEntry{}, nil
	}
	end := len(entries)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	subset := entries[offset:end]
	result := make([]potso.StoredWeightEntry, len(subset))
	for i := range subset {
		entry := subset[i]
		result[i] = potso.StoredWeightEntry{
			Address:            entry.Address,
			Stake:              new(big.Int).Set(entry.Stake),
			Engagement:         entry.Engagement,
			StakeShareBps:      entry.StakeShareBps,
			EngagementShareBps: entry.EngagementShareBps,
			WeightBps:          entry.WeightBps,
		}
	}
	return target, total, result, nil
}

func (n *Node) LoyaltyManager() *nhbstate.Manager {
	return nhbstate.NewManager(n.state.Trie)
}

func (n *Node) LoyaltyRegistry() *loyalty.Registry {
	registry := loyalty.NewRegistry(n.LoyaltyManager())
	registry.SetPauses(n)
	return registry
}

func (n *Node) LoyaltyBusinessByID(id loyalty.BusinessID) (*loyalty.Business, bool, error) {
	return n.state.LoyaltyBusinessByID(id)
}

func (n *Node) LoyaltyProgramByID(id loyalty.ProgramID) (*loyalty.Program, bool, error) {
	return n.state.LoyaltyProgramByID(id)
}

func (n *Node) LoyaltyProgramsByOwner(owner [20]byte) ([]loyalty.ProgramID, error) {
	return n.state.LoyaltyProgramsByOwner(owner)
}

var (
	// ErrEscrowNotFound is returned when an escrow record is missing from state.
	ErrEscrowNotFound = errors.New("escrow not found")
	// ErrTradeNotFound is returned when a trade record is missing from state.
	ErrTradeNotFound = errors.New("trade not found")
)

type escrowEventEmitter struct {
	node *Node
}

type eventWithPayload interface {
	Event() *types.Event
}

func (e escrowEventEmitter) Emit(evt events.Event) {
	if e.node == nil || evt == nil {
		return
	}
	payload, ok := evt.(eventWithPayload)
	if !ok {
		return
	}
	event := payload.Event()
	if event == nil {
		return
	}
	e.node.state.AppendEvent(event)
}

type creatorEventEmitter struct {
	node *Node
}

func (e creatorEventEmitter) Emit(evt events.Event) {
	if e.node == nil || evt == nil {
		return
	}
	payload, ok := evt.(eventWithPayload)
	if !ok {
		return
	}
	event := payload.Event()
	if event == nil {
		return
	}
	e.node.state.AppendEvent(event)
}

func (n *Node) newEscrowEngine(manager *nhbstate.Manager) *escrow.Engine {
	engine := escrow.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(escrowEventEmitter{node: n})
	engine.SetFeeTreasury(n.escrowTreasury)
	engine.SetPauses(n)
	return engine
}

func (n *Node) newTradeEngine(manager *nhbstate.Manager) *escrow.TradeEngine {
	escrowEngine := n.newEscrowEngine(manager)
	tradeEngine := escrow.NewTradeEngine(escrowEngine)
	tradeEngine.SetState(manager)
	tradeEngine.SetEmitter(escrowEventEmitter{node: n})
	tradeEngine.SetPauses(n)
	return tradeEngine
}

func (n *Node) newCreatorEngine(manager *nhbstate.Manager) *creator.Engine {
	engine := creator.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(creatorEventEmitter{node: n})
	engine.SetNowFunc(func() int64 { return n.currentTime().Unix() })
	var payoutVault [20]byte
	copy(payoutVault[:], n.creatorPayoutVaultAddr.Bytes())
	engine.SetPayoutVault(payoutVault)
	var rewardsTreasury [20]byte
	copy(rewardsTreasury[:], n.creatorRewardsTreasuryAddr.Bytes())
	engine.SetRewardsTreasury(rewardsTreasury)
	return engine
}

func (n *Node) CreatorPublish(creatorAddr [20]byte, id string, uri string, metadata string) (*creator.Content, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	return engine.PublishContent(creatorAddr, id, uri, metadata)
}

func (n *Node) CreatorTip(fan [20]byte, contentID string, amount *big.Int) (*creator.Tip, *creator.PayoutLedger, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	tip, err := engine.TipContent(fan, contentID, amount)
	if err != nil {
		return nil, nil, err
	}
	if tip == nil {
		return nil, nil, nil
	}
	ledger, err := engine.Payouts(tip.Creator)
	if err != nil {
		return nil, nil, err
	}
	return tip, ledger, nil
}

func (n *Node) CreatorStake(fan [20]byte, creatorAddr [20]byte, amount *big.Int) (*creator.Stake, *big.Int, *creator.PayoutLedger, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	stake, reward, err := engine.StakeCreator(fan, creatorAddr, amount)
	if err != nil {
		return nil, nil, nil, err
	}
	ledger, err := engine.Payouts(creatorAddr)
	if err != nil {
		return nil, nil, nil, err
	}
	return stake, reward, ledger, nil
}

func (n *Node) CreatorUnstake(fan [20]byte, creatorAddr [20]byte, amount *big.Int) (*creator.Stake, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	return engine.UnstakeCreator(fan, creatorAddr, amount)
}

func (n *Node) CreatorClaimPayouts(creatorAddr [20]byte) (*creator.PayoutLedger, *big.Int, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	return engine.ClaimPayouts(creatorAddr)
}

func (n *Node) CreatorPayouts(creatorAddr [20]byte) (*creator.PayoutLedger, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newCreatorEngine(manager)
	return engine.Payouts(creatorAddr)
}

func (n *Node) EscrowCreate(payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, nonce uint64, mediator *[20]byte, meta [32]byte, realm string) ([32]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	esc, err := engine.Create(payer, payee, token, amount, feeBps, deadline, nonce, mediator, meta, realm)
	if err != nil {
		return [32]byte{}, err
	}
	return esc.ID, nil
}

func (n *Node) EscrowFund(id [32]byte, from [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Fund(id, from)
}

func (n *Node) EscrowRelease(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Release(id, caller)
}

func (n *Node) EscrowRefund(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Refund(id, caller)
}

func (n *Node) EscrowExpire(id [32]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Expire(id, time.Now().Unix())
}

func (n *Node) EscrowDispute(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Dispute(id, caller)
}

func (n *Node) EscrowResolve(id [32]byte, caller [20]byte, outcome string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Resolve(id, caller, outcome)
}

func (n *Node) StakeDelegate(delegator [20]byte, amount *big.Int, validator *[20]byte) (*types.Account, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	var target []byte
	if validator != nil {
		// treat zero address as self-delegation
		zero := [20]byte{}
		if *validator != zero {
			target = validator[:]
		}
	}
	acct, err := n.state.StakeDelegate(delegator[:], target, amount)
	if err != nil {
		return nil, err
	}
	return acct, nil
}

func (n *Node) StakeUndelegate(delegator [20]byte, amount *big.Int) (*types.StakeUnbond, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	return n.state.StakeUndelegate(delegator[:], amount)
}

func (n *Node) StakeClaim(delegator [20]byte, unbondID uint64) (*types.StakeUnbond, error) {
	if unbondID == 0 {
		return nil, fmt.Errorf("unbondingId must be greater than zero")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	return n.state.StakeClaim(delegator[:], unbondID)
}

func (n *Node) EscrowGet(id [32]byte) (*escrow.Escrow, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	esc, ok := manager.EscrowGet(id)
	if !ok {
		return nil, ErrEscrowNotFound
	}
	return esc, nil
}

func (n *Node) EscrowVaultAddress(token string) ([20]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	return manager.EscrowVaultAddress(token)
}

// EscrowMilestoneCreate is a placeholder implementation used by the milestone
// RPC surface. The engine is not yet connected to state transitions.
func (n *Node) EscrowMilestoneCreate(project *escrow.MilestoneProject) (*escrow.MilestoneProject, error) {
	return nil, ErrMilestoneUnsupported
}

// EscrowMilestoneGet returns the current milestone project state when
// persistence is available.
func (n *Node) EscrowMilestoneGet(id [32]byte) (*escrow.MilestoneProject, error) {
	return nil, ErrMilestoneUnsupported
}

// EscrowMilestoneFund transitions a milestone leg into the funded state.
func (n *Node) EscrowMilestoneFund(id [32]byte, legID uint64, caller [20]byte) error {
	return ErrMilestoneUnsupported
}

// EscrowMilestoneRelease releases a funded milestone leg to the payee.
func (n *Node) EscrowMilestoneRelease(id [32]byte, legID uint64, caller [20]byte) error {
	return ErrMilestoneUnsupported
}

// EscrowMilestoneCancel cancels a milestone leg.
func (n *Node) EscrowMilestoneCancel(id [32]byte, legID uint64, caller [20]byte) error {
	return ErrMilestoneUnsupported
}

// EscrowMilestoneSubscriptionUpdate updates the subscription toggle for a
// milestone project.
func (n *Node) EscrowMilestoneSubscriptionUpdate(id [32]byte, caller [20]byte, active bool) (*escrow.MilestoneProject, error) {
	return nil, ErrMilestoneUnsupported
}

// ReputationVerifySkill validates the caller's verifier role and records a
// skill verification.
func (n *Node) ReputationVerifySkill(verifier, subject [20]byte, skill string, expiresAt int64) (*reputation.SkillVerification, error) {
	if n == nil {
		return nil, fmt.Errorf("reputation: node unavailable")
	}
	trimmedSkill := strings.TrimSpace(skill)
	if trimmedSkill == "" {
		return nil, fmt.Errorf("reputation: skill required")
	}
	issuedAt := n.currentTime().Unix()
	verification := &reputation.SkillVerification{
		Subject:   subject,
		Skill:     trimmedSkill,
		Verifier:  verifier,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}
	if err := verification.Validate(); err != nil {
		return nil, err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("reputation: state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	if !manager.HasRole(roleReputationVerifier, verifier[:]) {
		return nil, ErrReputationVerifierUnauthorized
	}
	ledger := reputation.NewLedger(manager)
	ledger.SetNowFunc(func() int64 { return n.currentTime().Unix() })
	if err := ledger.Put(verification); err != nil {
		return nil, err
	}
	n.state.AppendEvent(reputation.NewSkillVerifiedEvent(verification))
	return verification, nil
}

// ReputationRevokeSkill validates the caller's verifier role and revokes a
// previously issued attestation.
func (n *Node) ReputationRevokeSkill(verifier [20]byte, attestationID [32]byte, reason string) (*reputation.Revocation, error) {
	if n == nil {
		return nil, fmt.Errorf("reputation: node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("reputation: state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	if !manager.HasRole(roleReputationVerifier, verifier[:]) {
		return nil, ErrReputationVerifierUnauthorized
	}
	ledger := reputation.NewLedger(manager)
	ledger.SetNowFunc(func() int64 { return n.currentTime().Unix() })
	revocation, err := ledger.Revoke(attestationID, verifier, reason)
	if err != nil {
		return nil, err
	}
	n.state.AppendEvent(reputation.NewSkillRevokedEvent(revocation))
	return revocation, nil
}

func (n *Node) P2PCreateTrade(offerID string, buyer, seller [20]byte,
	baseToken string, baseAmt *big.Int,
	quoteToken string, quoteAmt *big.Int,
	deadline int64, slippageBps uint32) (tradeID [32]byte, escrowBaseID, escrowQuoteID [32]byte, err error) {

	trimmedOffer := strings.TrimSpace(offerID)
	if trimmedOffer == "" {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: offerId is required")
	}
	normalizedBase, err := escrow.NormalizeToken(baseToken)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	normalizedQuote, err := escrow.NormalizeToken(quoteToken)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	if baseAmt == nil || baseAmt.Sign() <= 0 {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: base amount must be positive")
	}
	if quoteAmt == nil || quoteAmt.Sign() <= 0 {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: quote amount must be positive")
	}
	now := time.Now().Unix()
	if deadline < now {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: deadline must be in the future")
	}

	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, fmt.Errorf("trade: failed to derive nonce: %w", err)
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	tradeEngine := n.newTradeEngine(manager)

	trade, err := tradeEngine.CreateTrade(trimmedOffer, buyer, seller, normalizedQuote, quoteAmt, normalizedBase, baseAmt, deadline, slippageBps, nonce)
	if err != nil {
		return [32]byte{}, [32]byte{}, [32]byte{}, err
	}
	return trade.ID, trade.EscrowBase, trade.EscrowQuote, nil
}

func (n *Node) P2PGetTrade(id [32]byte) (*escrow.Trade, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return nil, ErrTradeNotFound
	}
	return trade.Clone(), nil
}

func (n *Node) P2PSettle(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return ErrTradeNotFound
	}
	if caller != trade.Buyer && caller != trade.Seller {
		return fmt.Errorf("trade: caller not participant")
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.SettleAtomic(id)
}

func (n *Node) P2PDispute(id [32]byte, caller [20]byte, msg string) error {
	_ = msg

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	trade, ok := manager.TradeGet(id)
	if !ok {
		return ErrTradeNotFound
	}
	if caller != trade.Buyer && caller != trade.Seller {
		return fmt.Errorf("trade: caller not participant")
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.TradeDispute(id, caller)
}

func (n *Node) P2PResolve(id [32]byte, arbitrator [20]byte, outcome string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	if !n.state.HasRole("ROLE_ARBITRATOR", arbitrator[:]) {
		return fmt.Errorf("trade: caller lacks arbitrator role")
	}
	if _, ok := manager.TradeGet(id); !ok {
		return ErrTradeNotFound
	}
	tradeEngine := n.newTradeEngine(manager)
	return tradeEngine.TradeResolve(id, outcome)
}

func (n *Node) IdentitySetAlias(addr [20]byte, alias string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	previous, _ := manager.IdentityReverse(addr[:])
	if err := manager.IdentitySetAlias(addr[:], alias); err != nil {
		return err
	}
	current, ok := manager.IdentityReverse(addr[:])
	if !ok || current == "" {
		return fmt.Errorf("identity: failed to persist alias")
	}
	if previous == current {
		if previous == "" {
			evt := events.IdentityAliasSet{Alias: current, Address: addr}.Event()
			if evt != nil {
				n.state.AppendEvent(evt)
			}
		}
		return nil
	}
	if previous == "" {
		evt := events.IdentityAliasSet{Alias: current, Address: addr}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
	} else {
		evt := events.IdentityAliasRenamed{OldAlias: previous, NewAlias: current, Address: addr}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
	}
	return nil
}

func (n *Node) IdentitySetAvatar(addr [20]byte, avatarRef string) (*identity.AliasRecord, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	alias, ok := manager.IdentityReverse(addr[:])
	if !ok {
		return nil, identity.ErrAliasNotFound
	}
	record, err := manager.IdentitySetAvatar(alias, avatarRef, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	evt := events.IdentityAliasAvatarUpdated{Alias: record.Alias, Address: addr, AvatarRef: record.AvatarRef}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return record, nil
}

func (n *Node) IdentityResolve(alias string) (*identity.AliasRecord, bool) {
	manager := nhbstate.NewManager(n.state.Trie)
	return manager.IdentityResolve(alias)
}

func (n *Node) IdentityReverse(addr [20]byte) (string, bool) {
	manager := nhbstate.NewManager(n.state.Trie)
	return manager.IdentityReverse(addr[:])
}

func (n *Node) EngagementRegisterDevice(addr [20]byte, deviceID string) (string, error) {
	if n.engagementMgr == nil {
		return "", fmt.Errorf("engagement manager unavailable")
	}
	validator := n.validatorKey.PubKey().Address()
	if !bytes.Equal(addr[:], validator.Bytes()) {
		return "", fmt.Errorf("device must register validator address %s", validator.String())
	}
	return n.engagementMgr.RegisterDevice(addr, deviceID)
}

func (n *Node) EngagementSubmitHeartbeat(deviceID, token string, timestamp int64) (int64, error) {
	if n.engagementMgr == nil {
		return 0, fmt.Errorf("engagement manager unavailable")
	}
	ts, err := n.engagementMgr.SubmitHeartbeat(deviceID, token, timestamp)
	if err != nil {
		return 0, err
	}

	validator := n.validatorKey.PubKey().Address()
	payload := types.HeartbeatPayload{DeviceID: deviceID, Timestamp: ts}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	account, err := n.state.GetAccount(validator.Bytes())
	if err != nil {
		return 0, err
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(n.validatorKey.PrivateKey); err != nil {
		return 0, err
	}
	if err := n.AddTransaction(tx); err != nil {
		return 0, err
	}
	return ts, nil
}

func (n *Node) GovernancePropose(proposer [20]byte, kind, payload string, deposit *big.Int) (uint64, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newGovernanceEngine(manager)
	lockAmount := big.NewInt(0)
	if deposit != nil {
		if deposit.Sign() < 0 {
			return 0, fmt.Errorf("governance: deposit must not be negative")
		}
		lockAmount = new(big.Int).Set(deposit)
	}
	return engine.SubmitProposal(proposer, kind, payload, lockAmount)
}

func (n *Node) GovernanceVote(proposalID uint64, voter [20]byte, choice string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newGovernanceEngine(manager)
	return engine.CastVote(proposalID, voter, choice)
}

func (n *Node) GovernanceProposal(id uint64) (*governance.Proposal, bool, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	proposal, ok, err := manager.GovernanceGetProposal(id)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return proposal, true, nil
}

func (n *Node) GovernanceListProposals(cursor uint64, limit int) ([]*governance.Proposal, uint64, error) {
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	var latest uint64
	if _, err := manager.KVGet(nhbstate.GovernanceSequenceKey(), &latest); err != nil {
		return nil, 0, err
	}

	proposals := make([]*governance.Proposal, 0, limit)
	if latest == 0 {
		return proposals, 0, nil
	}

	start := latest
	if cursor > 0 {
		if cursor > latest {
			start = latest
		} else {
			start = cursor
		}
	}
	if start == 0 {
		return proposals, 0, nil
	}

	current := start
	for current >= 1 && len(proposals) < limit {
		proposal, ok, err := manager.GovernanceGetProposal(current)
		if err != nil {
			return nil, 0, err
		}
		if ok && proposal != nil {
			proposals = append(proposals, proposal)
		}
		current--
	}
	var nextCursor uint64
	if current >= 1 {
		nextCursor = current
	}
	return proposals, nextCursor, nil
}

func (n *Node) GovernanceFinalize(proposalID uint64) (*governance.Proposal, *governance.Tally, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newGovernanceEngine(manager)
	_, tally, err := engine.Finalize(proposalID)
	if err != nil {
		return nil, nil, err
	}
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		return nil, nil, err
	}
	if !ok || proposal == nil {
		return nil, nil, fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	return proposal, tally, nil
}

func (n *Node) GovernanceQueue(proposalID uint64) (*governance.Proposal, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newGovernanceEngine(manager)
	if err := engine.QueueExecution(proposalID); err != nil {
		return nil, err
	}
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if !ok || proposal == nil {
		return nil, fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	return proposal, nil
}

func (n *Node) GovernanceExecute(proposalID uint64) (*governance.Proposal, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newGovernanceEngine(manager)
	if err := engine.Execute(proposalID); err != nil {
		return nil, err
	}
	proposal, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if !ok || proposal == nil {
		return nil, fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	return proposal, nil
}

// PotsoHeartbeat records an authenticated heartbeat for the supplied participant.
func (n *Node) PotsoHeartbeat(addr [20]byte, blockHeight uint64, blockHash []byte, timestamp int64) (*potso.Meter, uint64, error) {
	if !potso.WithinTolerance(timestamp, time.Now()) {
		return nil, 0, fmt.Errorf("heartbeat timestamp outside tolerance")
	}
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return nil, 0, err
	}
	block, err := n.chain.GetBlockByHeight(blockHeight)
	if err != nil {
		return nil, 0, err
	}
	expectedHash, err := block.Header.Hash()
	if err != nil {
		return nil, 0, err
	}
	if !bytes.Equal(expectedHash, blockHash) {
		return nil, 0, fmt.Errorf("block hash mismatch")
	}

	day := time.Unix(timestamp, 0).UTC().Format(potso.DayFormat)

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	cfg := n.state.PotsoRewardConfig()
	epoch := uint64(0)
	if cfg.EpochLengthBlocks > 0 {
		epoch = blockHeight / cfg.EpochLengthBlocks
	}
	n.potsoEngineMu.Lock()
	engine := n.potsoEngine
	n.potsoEngineMu.Unlock()
	if engine != nil {
		if err := engine.Precheck(addr, epoch); err != nil {
			return nil, 0, err
		}
	}
	heartbeat, _, err := manager.PotsoGetHeartbeat(addr)
	if err != nil {
		return nil, 0, err
	}
	delta, accepted, err := heartbeat.ApplyHeartbeat(timestamp, blockHeight, blockHash)
	if err != nil {
		if errors.Is(err, potso.ErrHeartbeatTooSoon) {
			meter, _, loadErr := manager.PotsoGetMeter(addr, day)
			if loadErr != nil {
				return nil, 0, loadErr
			}
			meter.Day = day
			meter.RecomputeScore()
			return meter, 0, nil
		}
		return nil, 0, err
	}
	if !accepted {
		meter, _, loadErr := manager.PotsoGetMeter(addr, day)
		if loadErr != nil {
			return nil, 0, loadErr
		}
		meter.Day = day
		meter.RecomputeScore()
		return meter, 0, nil
	}

	if err := manager.PotsoPutHeartbeat(addr, heartbeat); err != nil {
		return nil, 0, err
	}

	meter, _, err := manager.PotsoGetMeter(addr, day)
	if err != nil {
		return nil, 0, err
	}
	meter.Day = day
	meter.UptimeSeconds += delta
	meter.RecomputeScore()
	if err := manager.PotsoPutMeter(addr, meter); err != nil {
		return nil, 0, err
	}
	if engine != nil {
		engine.Commit(addr, epoch, delta)
		if cfg.EmissionPerEpoch == nil || cfg.EmissionPerEpoch.Sign() <= 0 {
			engine.ObserveWashEngagement(addr, epoch)
		}
	}

	evt := events.PotsoHeartbeat{Address: addr, Timestamp: timestamp, BlockHeight: blockHeight, UptimeDelta: delta}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return meter, delta, nil
}

// PotsoUserMeters retrieves the meter for the given day and address.
func (n *Node) PotsoUserMeters(addr [20]byte, day string) (*potso.Meter, error) {
	if day == "" {
		day = time.Now().UTC().Format(potso.DayFormat)
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	meter, _, err := manager.PotsoGetMeter(addr, day)
	if err != nil {
		return nil, err
	}
	meter.Day = potso.NormaliseDay(day)
	meter.RecomputeScore()
	return meter, nil
}

// PotsoTop returns the top scoring participants for the given day.
func (n *Node) PotsoTop(day string, limit int) ([]PotsoLeaderboardEntry, error) {
	if day == "" {
		day = time.Now().UTC().Format(potso.DayFormat)
	}
	if limit <= 0 {
		limit = 10
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	participants, err := manager.PotsoListParticipants(day)
	if err != nil {
		return nil, err
	}
	entries := make([]PotsoLeaderboardEntry, 0, len(participants))
	for _, addr := range participants {
		meter, _, err := manager.PotsoGetMeter(addr, day)
		if err != nil {
			return nil, err
		}
		meter.Day = potso.NormaliseDay(day)
		meter.RecomputeScore()
		entries = append(entries, PotsoLeaderboardEntry{Address: addr, Meter: meter})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Meter.Score == entries[j].Meter.Score {
			if entries[i].Meter.RawScore == entries[j].Meter.RawScore {
				if entries[i].Meter.UptimeSeconds == entries[j].Meter.UptimeSeconds {
					return bytes.Compare(entries[i].Address[:], entries[j].Address[:]) < 0
				}
				return entries[i].Meter.UptimeSeconds > entries[j].Meter.UptimeSeconds
			}
			return entries[i].Meter.RawScore > entries[j].Meter.RawScore
		}
		return entries[i].Meter.Score > entries[j].Meter.Score
	})
	if limit < len(entries) {
		entries = entries[:limit]
	}
	return entries, nil
}

// PotsoStakeLock moves the requested amount of ZNHB into the staking vault and records a new lock.
func (n *Node) PotsoStakeLock(owner [20]byte, amount *big.Int) (uint64, *potso.StakeLock, error) {
	if amount == nil || amount.Sign() <= 0 {
		return 0, nil, fmt.Errorf("amount must be positive")
	}

	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return 0, nil, err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)

	ownerAcc, err := manager.GetAccount(owner[:])
	if err != nil {
		return 0, nil, err
	}
	if ownerAcc.BalanceZNHB.Cmp(amount) < 0 {
		return 0, nil, fmt.Errorf("insufficient ZNHB balance")
	}

	vaultAddr := manager.PotsoStakeVaultAddress()
	vaultAcc, err := manager.GetAccount(vaultAddr[:])
	if err != nil {
		return 0, nil, err
	}

	ownerAcc.BalanceZNHB = new(big.Int).Sub(ownerAcc.BalanceZNHB, amount)
	vaultAcc.BalanceZNHB = new(big.Int).Add(vaultAcc.BalanceZNHB, amount)

	if err := manager.PutAccount(owner[:], ownerAcc); err != nil {
		return 0, nil, err
	}
	if err := manager.PutAccount(vaultAddr[:], vaultAcc); err != nil {
		return 0, nil, err
	}

	nonce, err := manager.PotsoStakeAllocateNonce(owner)
	if err != nil {
		return 0, nil, err
	}
	now := uint64(time.Now().Unix())
	lock := &potso.StakeLock{
		Owner:     owner,
		Amount:    new(big.Int).Set(amount),
		CreatedAt: now,
	}
	if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
		return 0, nil, err
	}
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return 0, nil, err
	}
	nonces = append(nonces, nonce)
	if err := manager.PotsoStakePutLockNonces(owner, nonces); err != nil {
		return 0, nil, err
	}
	bonded, err := manager.PotsoStakeBondedTotal(owner)
	if err != nil {
		return 0, nil, err
	}
	bonded = new(big.Int).Add(bonded, amount)
	if err := manager.PotsoStakeSetBondedTotal(owner, bonded); err != nil {
		return 0, nil, err
	}

	evt := events.PotsoStakeLocked{Owner: owner, Amount: amount}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return nonce, lock, nil
}

// PotsoStakeUnbond starts the unbonding cooldown for the requested amount.
func (n *Node) PotsoStakeUnbond(owner [20]byte, amount *big.Int) (*big.Int, uint64, error) {
	if amount == nil || amount.Sign() <= 0 {
		return nil, 0, fmt.Errorf("amount must be positive")
	}

	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return nil, 0, err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	bonded, err := manager.PotsoStakeBondedTotal(owner)
	if err != nil {
		return nil, 0, err
	}
	if bonded.Cmp(amount) < 0 {
		return nil, 0, fmt.Errorf("insufficient bonded stake")
	}

	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return nil, 0, err
	}
	if len(nonces) == 0 {
		return nil, 0, fmt.Errorf("no active stake locks")
	}

	// Ensure the active locks cover the requested amount before mutating state.
	available := big.NewInt(0)
	for _, nonce := range nonces {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, nonce)
		if getErr != nil {
			return nil, 0, getErr
		}
		if !ok || lock == nil || lock.UnbondAt != 0 || lock.Amount == nil {
			continue
		}
		available = new(big.Int).Add(available, lock.Amount)
	}
	if available.Cmp(amount) < 0 {
		return nil, 0, fmt.Errorf("insufficient bonded stake")
	}

	now := uint64(time.Now().Unix())
	withdrawAt := now + potso.StakeUnbondSeconds
	newNonces := make([]uint64, 0, len(nonces)+1)
	remaining := new(big.Int).Set(amount)
	unbonded := big.NewInt(0)

	for _, nonce := range nonces {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, nonce)
		if getErr != nil {
			return nil, 0, getErr
		}
		if !ok || lock == nil {
			continue
		}
		newNonces = append(newNonces, nonce)
		if remaining.Sign() == 0 || lock.Amount == nil || lock.Amount.Sign() == 0 || lock.UnbondAt != 0 {
			continue
		}
		take := new(big.Int)
		if lock.Amount.Cmp(remaining) > 0 {
			take.Set(remaining)
			leftover := new(big.Int).Sub(lock.Amount, remaining)
			lock.Amount = new(big.Int).Set(remaining)
			lock.UnbondAt = now
			lock.WithdrawAt = withdrawAt
			if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
				return nil, 0, err
			}
			newNonce, allocErr := manager.PotsoStakeAllocateNonce(owner)
			if allocErr != nil {
				return nil, 0, allocErr
			}
			newLock := &potso.StakeLock{Owner: owner, Amount: leftover, CreatedAt: lock.CreatedAt}
			if err := manager.PotsoStakePutLock(owner, newNonce, newLock); err != nil {
				return nil, 0, err
			}
			newNonces = append(newNonces, newNonce)
		} else {
			take.Set(lock.Amount)
			lock.UnbondAt = now
			lock.WithdrawAt = withdrawAt
			if err := manager.PotsoStakePutLock(owner, nonce, lock); err != nil {
				return nil, 0, err
			}
		}
		ref := potso.WithdrawalRef{Owner: owner, Nonce: nonce, Amount: new(big.Int).Set(take)}
		if err := manager.PotsoStakeQueueAppend(potso.WithdrawDay(withdrawAt), ref); err != nil {
			return nil, 0, err
		}
		unbonded.Add(unbonded, take)
		remaining.Sub(remaining, take)
		if remaining.Sign() == 0 {
			break
		}
	}

	if err := manager.PotsoStakePutLockNonces(owner, newNonces); err != nil {
		return nil, 0, err
	}
	bonded.Sub(bonded, unbonded)
	if err := manager.PotsoStakeSetBondedTotal(owner, bonded); err != nil {
		return nil, 0, err
	}

	evt := events.PotsoStakeUnbonded{Owner: owner, Amount: unbonded, WithdrawAt: withdrawAt}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return unbonded, withdrawAt, nil
}

// PotsoStakeWithdraw releases any matured stake locks back to the owner account.
func (n *Node) PotsoStakeWithdraw(owner [20]byte) ([]potso.WithdrawResult, error) {
	if err := nativecommon.Guard(n, modulePotso); err != nil {
		return nil, err
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return nil, err
	}
	if len(nonces) == 0 {
		return []potso.WithdrawResult{}, nil
	}

	now := uint64(time.Now().Unix())
	keepNonces := make([]uint64, 0, len(nonces))
	withdrawn := make([]potso.WithdrawResult, 0)
	total := big.NewInt(0)
	pending := false

	for _, nonce := range nonces {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, nonce)
		if getErr != nil {
			return nil, getErr
		}
		if !ok || lock == nil {
			continue
		}
		if lock.UnbondAt == 0 {
			keepNonces = append(keepNonces, nonce)
			continue
		}
		amount := big.NewInt(0)
		if lock.Amount != nil {
			amount = new(big.Int).Set(lock.Amount)
		}
		if lock.WithdrawAt > now {
			keepNonces = append(keepNonces, nonce)
			if amount.Sign() > 0 {
				pending = true
			}
			continue
		}
		if amount.Sign() > 0 {
			withdrawn = append(withdrawn, potso.WithdrawResult{Nonce: nonce, Amount: amount})
			total.Add(total, amount)
		}
		if err := manager.PotsoStakeDeleteLock(owner, nonce); err != nil {
			return nil, err
		}
		if err := manager.PotsoStakeQueueRemove(potso.WithdrawDay(lock.WithdrawAt), owner, nonce); err != nil {
			return nil, err
		}
	}

	if err := manager.PotsoStakePutLockNonces(owner, keepNonces); err != nil {
		return nil, err
	}

	if total.Sign() == 0 {
		if pending {
			return nil, fmt.Errorf("no withdrawable locks yet")
		}
		return []potso.WithdrawResult{}, nil
	}

	ownerAcc, err := manager.GetAccount(owner[:])
	if err != nil {
		return nil, err
	}
	vaultAddr := manager.PotsoStakeVaultAddress()
	vaultAcc, err := manager.GetAccount(vaultAddr[:])
	if err != nil {
		return nil, err
	}
	if vaultAcc.BalanceZNHB.Cmp(total) < 0 {
		return nil, fmt.Errorf("staking vault underfunded")
	}
	ownerAcc.BalanceZNHB = new(big.Int).Add(ownerAcc.BalanceZNHB, total)
	vaultAcc.BalanceZNHB = new(big.Int).Sub(vaultAcc.BalanceZNHB, total)
	if err := manager.PutAccount(owner[:], ownerAcc); err != nil {
		return nil, err
	}
	if err := manager.PutAccount(vaultAddr[:], vaultAcc); err != nil {
		return nil, err
	}

	evt := events.PotsoStakeWithdrawn{Owner: owner, Amount: total}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return withdrawn, nil
}

// PotsoStakeInfo summarises the staking position for the owner.
func (n *Node) PotsoStakeInfo(owner [20]byte) (*potso.StakeAccountInfo, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	bonded, err := manager.PotsoStakeBondedTotal(owner)
	if err != nil {
		return nil, err
	}
	nonces, err := manager.PotsoStakeLockNonces(owner)
	if err != nil {
		return nil, err
	}
	info := &potso.StakeAccountInfo{
		Owner:          owner,
		Bonded:         new(big.Int).Set(bonded),
		PendingUnbond:  big.NewInt(0),
		Withdrawable:   big.NewInt(0),
		Locks:          make([]potso.StakeLockInfo, 0, len(nonces)),
		ComputedAtUnix: time.Now().Unix(),
	}
	now := uint64(time.Now().Unix())
	for _, nonce := range nonces {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, nonce)
		if getErr != nil {
			return nil, getErr
		}
		if !ok || lock == nil {
			continue
		}
		amount := big.NewInt(0)
		if lock.Amount != nil {
			amount = new(big.Int).Set(lock.Amount)
		}
		if lock.UnbondAt > 0 {
			if lock.WithdrawAt <= now {
				info.Withdrawable.Add(info.Withdrawable, amount)
			} else {
				info.PendingUnbond.Add(info.PendingUnbond, amount)
			}
		}
		info.Locks = append(info.Locks, potso.StakeLockInfo{
			Nonce:      nonce,
			Amount:     amount,
			CreatedAt:  lock.CreatedAt,
			UnbondAt:   lock.UnbondAt,
			WithdrawAt: lock.WithdrawAt,
		})
	}
	return info, nil
}

func (n *Node) ClaimableCreate(payer [20]byte, token string, amount *big.Int, hashLock [32]byte, deadline int64) ([32]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, err := manager.CreateClaimable(payer, token, amount, hashLock, deadline, [32]byte{})
	if err != nil {
		return [32]byte{}, err
	}
	evt := events.ClaimableCreated{
		ID:            record.ID,
		Payer:         record.Payer,
		Token:         record.Token,
		Amount:        record.Amount,
		RecipientHint: record.RecipientHint,
		Deadline:      record.Deadline,
		CreatedAt:     record.CreatedAt,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return record.ID, nil
}

func (n *Node) IdentityCreateClaimable(payer [20]byte, token string, amount *big.Int, hint [32]byte, deadline int64) (*claimable.Claimable, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	hash := ethcrypto.Keccak256(hint[:])
	var hashLock [32]byte
	copy(hashLock[:], hash)
	record, err := manager.CreateClaimable(payer, token, amount, hashLock, deadline, hint)
	if err != nil {
		return nil, err
	}
	evt := events.ClaimableCreated{
		ID:            record.ID,
		Payer:         record.Payer,
		Token:         record.Token,
		Amount:        record.Amount,
		RecipientHint: record.RecipientHint,
		Deadline:      record.Deadline,
		CreatedAt:     record.CreatedAt,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return record, nil
}

func (n *Node) ClaimableClaim(id [32]byte, preimage []byte, payee [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableClaim(id, preimage, payee)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableClaimed{
		ID:            record.ID,
		Payer:         record.Payer,
		Payee:         payee,
		Token:         record.Token,
		Amount:        record.Amount,
		RecipientHint: record.RecipientHint,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) IdentityClaim(id [32]byte, preimage []byte, payee [20]byte) (*claimable.Claimable, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.ClaimableGet(id)
	if !ok {
		return nil, claimable.ErrNotFound
	}
	if record.Status == claimable.ClaimStatusClaimed {
		return record, nil
	}
	if record.Status != claimable.ClaimStatusInit {
		return nil, claimable.ErrInvalidState
	}
	hint := record.RecipientHint
	if hint != ([32]byte{}) {
		if len(preimage) != len(hint) {
			return nil, claimable.ErrInvalidPreimage
		}
		if !bytes.Equal(hint[:], preimage) {
			return nil, claimable.ErrInvalidPreimage
		}
	}
	updated, changed, err := manager.ClaimableClaim(id, preimage, payee)
	if err != nil {
		return nil, err
	}
	if !changed {
		return updated, nil
	}
	evt := events.ClaimableClaimed{
		ID:            updated.ID,
		Payer:         updated.Payer,
		Payee:         payee,
		Token:         updated.Token,
		Amount:        updated.Amount,
		RecipientHint: updated.RecipientHint,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return updated, nil
}

func (n *Node) ClaimableCancel(id [32]byte, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableCancel(id, caller, time.Now().Unix())
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableCancelled{
		ID:     record.ID,
		Payer:  record.Payer,
		Token:  record.Token,
		Amount: record.Amount,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) ClaimableExpire(id [32]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, changed, err := manager.ClaimableExpire(id, time.Now().Unix())
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	evt := events.ClaimableExpired{
		ID:     record.ID,
		Payer:  record.Payer,
		Token:  record.Token,
		Amount: record.Amount,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return nil
}

func (n *Node) ClaimableGet(id [32]byte) (*claimable.Claimable, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.ClaimableGet(id)
	if !ok {
		return nil, claimable.ErrNotFound
	}
	return record, nil
}

// MintWithSignature now enqueues a mint transaction into the mempool. The
// voucher payload and signature are executed during block processing so all
// validators observe the same state transition.
func (n *Node) MintWithSignature(voucher *MintVoucher, signature []byte) (string, error) {
	if voucher == nil {
		return "", fmt.Errorf("voucher required")
	}
	if len(signature) == 0 {
		return "", fmt.Errorf("signature required")
	}
	if _, err := voucher.AmountBig(); err != nil {
		return "", err
	}
	if voucher.ChainID != MintChainID {
		return "", ErrMintInvalidChainID
	}
	if voucher.Expiry <= n.currentTime().Unix() {
		return "", ErrMintExpired
	}
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		return "", err
	}
	token := voucher.NormalizedToken()
	if token != "NHB" && token != "ZNHB" {
		return "", fmt.Errorf("unsupported token %q", voucher.Token)
	}
	if len(signature) != 65 {
		return "", fmt.Errorf("invalid signature length")
	}
	digest := ethcrypto.Keccak256(canonical)
	if _, err := ethcrypto.SigToPub(digest, signature); err != nil {
		return "", fmt.Errorf("recover signer: %w", err)
	}

	txHash, err := mintTransactionHash(voucher, signature)
	if err != nil {
		return "", err
	}
	payload, err := encodeMintTransaction(voucher, signature)
	if err != nil {
		return "", err
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	if err := n.AddTransaction(tx); err != nil {
		if errors.Is(err, ErrMintInvoiceUsed) {
			return "", ErrMintInvoiceUsed
		}
		if errors.Is(err, ErrMempoolFull) {
			return "", err
		}
		if errors.Is(err, ErrInvalidTransaction) {
			switch {
			case errors.Is(err, ErrMintInvalidSigner):
				return "", ErrMintInvalidSigner
			case errors.Is(err, ErrMintInvoiceUsed):
				return "", ErrMintInvoiceUsed
			case errors.Is(err, ErrMintExpired):
				return "", ErrMintExpired
			case errors.Is(err, ErrMintInvalidChainID):
				return "", ErrMintInvalidChainID
			case errors.Is(err, ErrMintInvalidPayload):
				return "", ErrMintInvalidPayload
			}
		}
		return "", err
	}

	return txHash, nil
}

func (n *Node) SwapSubmitVoucher(submission *swap.VoucherSubmission) (string, bool, error) {
	if submission == nil || submission.Voucher == nil {
		return "", false, fmt.Errorf("swap: voucher required")
	}
	if err := nativecommon.Guard(n, moduleSwap); err != nil {
		return "", false, err
	}
	voucher := submission.Voucher
	if strings.TrimSpace(voucher.Domain) != swap.VoucherDomainV1 {
		return "", false, ErrSwapInvalidDomain
	}
	if voucher.ChainID != n.chain.ChainID() {
		return "", false, ErrSwapInvalidChainID
	}
	if voucher.Expiry <= time.Now().Unix() {
		return "", false, ErrSwapExpired
	}
	if voucher.Amount == nil || voucher.Amount.Sign() <= 0 {
		return "", false, fmt.Errorf("swap: invalid amount")
	}
	if len(voucher.Nonce) == 0 {
		return "", false, fmt.Errorf("swap: nonce required")
	}
	if voucher.Recipient == ([20]byte{}) {
		return "", false, fmt.Errorf("swap: recipient required")
	}
	orderID := strings.TrimSpace(voucher.OrderID)
	if orderID == "" {
		return "", false, fmt.Errorf("swap: orderId required")
	}
	provider := strings.TrimSpace(submission.Provider)
	if provider == "" {
		return "", false, fmt.Errorf("swap: provider required")
	}
	providerTxID := strings.TrimSpace(submission.ProviderTxID)
	if providerTxID == "" {
		return "", false, fmt.Errorf("swap: providerTxId required")
	}
	signature := append([]byte(nil), submission.Signature...)
	if len(signature) == 0 {
		return "", false, ErrSwapInvalidSignature
	}
	priceProof := submission.PriceProof
	token := strings.ToUpper(strings.TrimSpace(voucher.Token))
	if token != "ZNHB" {
		return "", false, ErrSwapInvalidToken
	}
	hash := voucher.Hash()
	if len(hash) == 0 {
		return "", false, ErrSwapInvalidSignature
	}
	if len(signature) != 65 {
		return "", false, ErrSwapInvalidSignature
	}
	pubKey, err := ethcrypto.SigToPub(hash, signature)
	if err != nil {
		return "", false, fmt.Errorf("swap: recover signer: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)

	cfg := n.swapConfig()
	riskParams, err := cfg.Risk.Parameters()
	if err != nil {
		return "", false, err
	}
	if !cfg.IsFiatAllowed(voucher.Fiat) {
		return "", false, ErrSwapUnsupportedFiat
	}
	n.swapCfgMu.RLock()
	oracle := n.swapOracle
	n.swapCfgMu.RUnlock()
	if oracle == nil {
		return "", false, ErrSwapOracleUnavailable
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	riskEngine := swap.NewRiskEngine(manager)
	sanctionsLog := swap.NewSanctionsLog(manager)
	priceEngine := swap.NewPriceProofEngine(manager, cfg.MaxQuoteAge(), cfg.PriceProofMaxDeviationBps)
	quote, err := oracle.GetRate("USD", token)
	if err != nil {
		if errors.Is(err, swap.ErrNoFreshQuote) {
			return "", false, ErrSwapQuoteStale
		}
		return "", false, fmt.Errorf("swap: oracle: %w", err)
	}
	if priceProof == nil {
		rateStr := strings.TrimSpace(voucher.Rate)
		if rateStr == "" && quote.Rate != nil {
			rateStr = quote.Rate.FloatString(18)
		}
		providerID := "manual"
		if len(cfg.OraclePriority) > 0 {
			trimmed := strings.TrimSpace(cfg.OraclePriority[0])
			if trimmed != "" {
				providerID = trimmed
			}
		}
		pair := fmt.Sprintf("%s/USD", token)
		timestamp := quote.Timestamp.UTC().Unix()
		if timestamp == 0 {
			timestamp = time.Now().UTC().Unix()
		}
		fallback, err := swap.NewPriceProof(swap.PriceProofDomainV1, providerID, pair, rateStr, timestamp, nil)
		if err != nil {
			return "", false, fmt.Errorf("swap: price proof: %w", err)
		}
		priceProof = fallback
	}
	if len(priceProof.Signature) == 65 {
		if err := priceEngine.Verify(priceProof, provider, token); err != nil {
			switch {
			case errors.Is(err, swap.ErrPriceProofNil):
				return "", false, ErrSwapPriceProofRequired
			case errors.Is(err, swap.ErrPriceProofDomain),
				errors.Is(err, swap.ErrPriceProofPair),
				errors.Is(err, swap.ErrPriceProofProviderMismatch),
				errors.Is(err, swap.ErrPriceProofSignatureInvalid):
				return "", false, ErrSwapPriceProofInvalid
			case errors.Is(err, swap.ErrPriceProofSignerUnknown):
				return "", false, ErrSwapPriceProofSignerUnknown
			case errors.Is(err, swap.ErrPriceProofStale):
				return "", false, ErrSwapPriceProofStale
			case errors.Is(err, swap.ErrPriceProofDeviation):
				return "", false, ErrSwapPriceProofDeviation
			default:
				return "", false, fmt.Errorf("swap: price proof verify: %w", err)
			}
		}
	} else if priceProof == nil {
		return "", false, ErrSwapPriceProofRequired
	}
	proofID, err := priceProof.ID()
	if err != nil {
		return "", false, fmt.Errorf("swap: price proof id: %w", err)
	}
	if len(cfg.Providers.Allow) > 0 && !cfg.Providers.IsAllowed(provider) {
		n.emitSwapLimitAlert(events.SwapLimitAlert{
			Address:      voucher.Recipient,
			Provider:     provider,
			ProviderTxID: providerTxID,
			Limit:        "provider",
			Amount:       new(big.Int).Set(voucher.Amount),
		})
		return "", false, ErrSwapProviderNotAllowed
	}
	if riskParams.SanctionsCheckEnabled {
		checker := n.swapSanctionsChecker()
		if checker != nil && !checker(voucher.Recipient) {
			if err := sanctionsLog.RecordFailure(voucher.Recipient, provider, providerTxID); err != nil {
				return "", false, fmt.Errorf("swap: record sanctions failure: %w", err)
			}
			n.emitSwapSanctionAlert(events.SwapSanctionAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
			})
			return "", false, ErrSwapSanctioned
		}
	}
	violation, err := riskEngine.CheckLimits(voucher.Recipient, voucher.Amount, riskParams)
	if err != nil {
		return "", false, err
	}
	if violation != nil {
		switch violation.Code {
		case swap.RiskCodeVelocity:
			n.emitSwapVelocityAlert(events.SwapVelocityAlert{
				Address:       voucher.Recipient,
				Provider:      provider,
				ProviderTxID:  providerTxID,
				WindowSeconds: violation.WindowSeconds,
				ObservedCount: violation.Count,
				AllowedMints:  riskParams.VelocityMaxMints,
			})
			return "", false, ErrSwapVelocityExceeded
		case swap.RiskCodePerTxMin:
			n.emitSwapLimitAlert(events.SwapLimitAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
				Limit:        string(violation.Code),
				Amount:       new(big.Int).Set(voucher.Amount),
				LimitValue:   cloneBigInt(violation.Limit),
				CurrentValue: cloneBigInt(violation.Current),
			})
			return "", false, ErrSwapAmountBelowMinimum
		case swap.RiskCodePerTxMax:
			n.emitSwapLimitAlert(events.SwapLimitAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
				Limit:        string(violation.Code),
				Amount:       new(big.Int).Set(voucher.Amount),
				LimitValue:   cloneBigInt(violation.Limit),
				CurrentValue: cloneBigInt(violation.Current),
			})
			return "", false, ErrSwapAmountAboveMaximum
		case swap.RiskCodeDailyCap:
			n.emitSwapLimitAlert(events.SwapLimitAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
				Limit:        string(violation.Code),
				Amount:       new(big.Int).Set(voucher.Amount),
				LimitValue:   cloneBigInt(violation.Limit),
				CurrentValue: cloneBigInt(violation.Current),
			})
			return "", false, ErrSwapDailyCapExceeded
		case swap.RiskCodeMonthlyCap:
			n.emitSwapLimitAlert(events.SwapLimitAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
				Limit:        string(violation.Code),
				Amount:       new(big.Int).Set(voucher.Amount),
				LimitValue:   cloneBigInt(violation.Limit),
				CurrentValue: cloneBigInt(violation.Current),
			})
			return "", false, ErrSwapMonthlyCapExceeded
		default:
			n.emitSwapLimitAlert(events.SwapLimitAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
				Limit:        string(violation.Code),
				Amount:       new(big.Int).Set(voucher.Amount),
				LimitValue:   cloneBigInt(violation.Limit),
				CurrentValue: cloneBigInt(violation.Current),
			})
			return "", false, fmt.Errorf("swap: risk violation %s", violation.Code)
		}
	}
	tokenMeta, err := manager.Token(token)
	if err != nil {
		return "", false, err
	}
	if tokenMeta == nil {
		return "", false, ErrSwapInvalidToken
	}
	if tokenMeta.MintPaused {
		return "", false, ErrSwapMintPaused
	}
	if len(tokenMeta.MintAuthority) != 20 {
		return "", false, fmt.Errorf("swap: mint authority not configured")
	}
	if !bytes.Equal(tokenMeta.MintAuthority, recovered.Bytes()) {
		return "", false, ErrSwapInvalidSigner
	}
	if quote.Rate == nil {
		return "", false, fmt.Errorf("swap: oracle rate unavailable")
	}
	if priceProof.Rate == nil {
		return "", false, ErrSwapPriceProofInvalid
	}
	rateDiff := new(big.Rat).Sub(quote.Rate, priceProof.Rate)
	if rateDiff.Sign() < 0 {
		rateDiff.Neg(rateDiff)
	}
	if priceProof.Rate.Sign() == 0 {
		return "", false, ErrSwapPriceProofInvalid
	}
	ratio := new(big.Rat).Quo(rateDiff, priceProof.Rate)
	ratio.Mul(ratio, big.NewRat(10000, 1))
	threshold := new(big.Rat).SetInt64(int64(cfg.PriceProofMaxDeviationBps))
	if cfg.PriceProofMaxDeviationBps > 0 && ratio.Cmp(threshold) == 1 {
		return "", false, ErrSwapPriceProofDeviation
	}
	quote.Timestamp = priceProof.Timestamp
	quote.Source = strings.ToLower(strings.TrimSpace(priceProof.Provider))
	n.recordSwapOracleHealth(time.Now())
	twapWindow := cfg.TwapWindow()
	priceProofID := proofID
	var (
		twapRate          string
		twapCount         int
		twapWindowSeconds int64
		twapStart         int64
		twapEnd           int64
		oracleMedian      string
		oracleFeeders     []string
	)
	if twapOracle, ok := oracle.(swap.TWAPOracle); ok {
		snapshot, err := twapOracle.TWAP("USD", token, twapWindow)
		if err == nil && snapshot.Average != nil {
			twapRate = snapshot.Average.FloatString(18)
			twapCount = snapshot.Count
			if snapshot.Window > 0 {
				twapWindowSeconds = int64(snapshot.Window / time.Second)
			} else if twapWindow > 0 {
				twapWindowSeconds = int64(twapWindow / time.Second)
			}
			if !snapshot.Start.IsZero() {
				twapStart = snapshot.Start.UTC().Unix()
			}
			if !snapshot.End.IsZero() {
				twapEnd = snapshot.End.UTC().Unix()
			}
			if snapshot.Median != nil {
				oracleMedian = snapshot.Median.FloatString(18)
			}
			if len(snapshot.Feeders) > 0 {
				oracleFeeders = append([]string{}, snapshot.Feeders...)
			}
			if strings.TrimSpace(snapshot.ProofID) != "" {
				priceProofID = strings.TrimSpace(snapshot.ProofID)
			}
		}
	}
	maxAge := cfg.MaxQuoteAge()
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		if quote.Timestamp.IsZero() || quote.Timestamp.Before(cutoff) {
			return "", false, ErrSwapQuoteStale
		}
	}
	mintAmount, err := swap.ComputeMintAmount(voucher.FiatAmount, quote.Rate, tokenMeta.Decimals)
	if err != nil {
		return "", false, err
	}
	if mintAmount == nil || mintAmount.Sign() == 0 {
		return "", false, fmt.Errorf("swap: computed mint amount zero")
	}
	diff := new(big.Int).Sub(mintAmount, voucher.Amount)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	allowance := new(big.Int).SetUint64(cfg.SlippageBps)
	slippage := new(big.Int).Mul(diff, big.NewInt(10000))
	slippage.Div(slippage, mintAmount)
	if slippage.Cmp(allowance) == 1 {
		return "", false, ErrSwapSlippageExceeded
	}

	ledger := swap.NewLedger(manager)
	exists, err := ledger.Exists(providerTxID)
	if err != nil {
		return "", false, err
	}
	if exists {
		return "", false, ErrSwapDuplicateProviderTx
	}
	if manager.HasSeenSwapNonce(orderID) {
		return "", false, ErrSwapNonceUsed
	}
	if len(priceProof.Signature) == 65 {
		if err := priceEngine.Record(priceProof); err != nil {
			return "", false, fmt.Errorf("swap: record price proof: %w", err)
		}
	}
	if err := n.state.MintToken(token, voucher.Recipient[:], voucher.Amount); err != nil {
		return "", false, err
	}
	if err := manager.MarkSwapNonce(orderID); err != nil {
		return "", false, err
	}

	usdAmount := strings.TrimSpace(submission.USDAmount)
	if usdAmount == "" && strings.EqualFold(voucher.Fiat, "USD") {
		usdAmount = strings.TrimSpace(voucher.FiatAmount)
	}
	if priceProofID == "" {
		builder := strings.Builder{}
		builder.WriteString(strings.TrimSpace(providerTxID))
		builder.WriteString("|")
		builder.WriteString(strings.TrimSpace(token))
		builder.WriteString("|")
		if quote.Rate != nil {
			builder.WriteString(quote.Rate.FloatString(18))
		}
		builder.WriteString("|")
		builder.WriteString(strconv.FormatInt(quote.Timestamp.UTC().UnixNano(), 10))
		if twapRate != "" {
			builder.WriteString("|")
			builder.WriteString(twapRate)
		}
		if oracleMedian != "" {
			builder.WriteString("|")
			builder.WriteString(oracleMedian)
		}
		if len(oracleFeeders) > 0 {
			builder.WriteString("|")
			builder.WriteString(strings.Join(oracleFeeders, ","))
		}
		sum := sha256.Sum256([]byte(builder.String()))
		priceProofID = hex.EncodeToString(sum[:])
	}
	record := &swap.VoucherRecord{
		Provider:          provider,
		ProviderTxID:      providerTxID,
		FiatCurrency:      strings.ToUpper(strings.TrimSpace(voucher.Fiat)),
		FiatAmount:        strings.TrimSpace(voucher.FiatAmount),
		USD:               usdAmount,
		Rate:              quote.RateString(18),
		Token:             token,
		MintAmountWei:     new(big.Int).Set(voucher.Amount),
		Recipient:         voucher.Recipient,
		Username:          strings.TrimSpace(submission.Username),
		Address:           strings.TrimSpace(submission.Address),
		QuoteTimestamp:    quote.Timestamp.UTC().Unix(),
		OracleSource:      quote.Source,
		OracleMedian:      oracleMedian,
		OracleFeeders:     append([]string{}, oracleFeeders...),
		PriceProofID:      priceProofID,
		MinterSignature:   "0x" + hex.EncodeToString(signature),
		Status:            swap.VoucherStatusMinted,
		TwapRate:          twapRate,
		TwapObservations:  twapCount,
		TwapWindowSeconds: twapWindowSeconds,
		TwapStart:         twapStart,
		TwapEnd:           twapEnd,
	}
	if err := ledger.Put(record); err != nil {
		return "", false, err
	}

	if err := riskEngine.RecordMint(voucher.Recipient, voucher.Amount, riskParams.VelocityWindowSeconds); err != nil {
		return "", false, err
	}

	txHashBytes := ethcrypto.Keccak256(append(hash, signature...))
	txHash := "0x" + hex.EncodeToString(txHashBytes)

	evt := events.SwapMinted{
		OrderID:    orderID,
		Recipient:  voucher.Recipient,
		Amount:     new(big.Int).Set(voucher.Amount),
		Fiat:       voucher.Fiat,
		FiatAmount: voucher.FiatAmount,
		Rate:       voucher.Rate,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	proofEvt := events.SwapMintProof{
		ProviderTxID:      providerTxID,
		OrderID:           orderID,
		Token:             token,
		PriceProofID:      priceProofID,
		OracleSource:      quote.Source,
		OracleMedian:      oracleMedian,
		OracleFeeders:     append([]string{}, oracleFeeders...),
		QuoteTimestamp:    quote.Timestamp.UTC().Unix(),
		TwapRate:          twapRate,
		TwapObservations:  twapCount,
		TwapWindowSeconds: twapWindowSeconds,
		TwapStart:         twapStart,
		TwapEnd:           twapEnd,
	}.Event()
	if proofEvt != nil {
		n.state.AppendEvent(proofEvt)
	}

	return txHash, true, nil
}

// SwapGetVoucher returns the ledger record for the supplied provider
// transaction identifier.
func (n *Node) SwapGetVoucher(providerTxID string) (*swap.VoucherRecord, bool, error) {
	trimmed := strings.TrimSpace(providerTxID)
	if trimmed == "" {
		return nil, false, fmt.Errorf("swap: providerTxId required")
	}
	var (
		record *swap.VoucherRecord
		ok     bool
	)
	err := n.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		var err error
		record, ok, err = ledger.Get(trimmed)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return record.Copy(), true, nil
}

// SwapListVouchers paginates voucher records for the supplied time range.
func (n *Node) SwapListVouchers(startTs, endTs int64, cursor string, limit int) ([]*swap.VoucherRecord, string, error) {
	var (
		results    []*swap.VoucherRecord
		nextCursor string
	)
	err := n.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		records, cursorOut, err := ledger.List(startTs, endTs, cursor, limit)
		if err != nil {
			return err
		}
		nextCursor = cursorOut
		results = make([]*swap.VoucherRecord, 0, len(records))
		for _, record := range records {
			results = append(results, record.Copy())
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return results, nextCursor, nil
}

// SwapExportVouchers produces a base64 encoded CSV export and accompanying totals.
func (n *Node) SwapExportVouchers(startTs, endTs int64) (string, int, *big.Int, error) {
	var (
		encoded string
		count   int
		total   *big.Int
	)
	err := n.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		var err error
		encoded, count, total, err = ledger.ExportCSV(startTs, endTs)
		return err
	})
	if err != nil {
		return "", 0, nil, err
	}
	if total == nil {
		total = big.NewInt(0)
	}
	return encoded, count, total, nil
}

// SwapLimits returns the current risk counters for the provided address alongside the active parameters.
func (n *Node) SwapLimits(addr [20]byte) (*swap.RiskUsage, swap.RiskParameters, error) {
	cfg := n.swapConfig()
	params, err := cfg.Risk.Parameters()
	if err != nil {
		return nil, swap.RiskParameters{}, err
	}
	var usage *swap.RiskUsage
	err = n.WithState(func(m *nhbstate.Manager) error {
		engine := swap.NewRiskEngine(m)
		report, err := engine.Usage(addr)
		if err != nil {
			return err
		}
		usage = report
		return nil
	})
	if err != nil {
		return nil, swap.RiskParameters{}, err
	}
	if usage == nil {
		usage = &swap.RiskUsage{Address: addr}
	}
	return usage.Copy(), params, nil
}

// SwapProviderStatus summarises the provider allow list and oracle health metadata.
func (n *Node) SwapProviderStatus() swap.ProviderStatus {
	cfg := n.swapConfig()
	n.swapStatusMu.RLock()
	last := n.swapOracleLast
	n.swapStatusMu.RUnlock()
	feeds := []swap.OracleFeedStatus{}
	if healthOracle, ok := n.swapOracle.(interface{ Health() swap.OracleHealth }); ok && healthOracle != nil {
		health := healthOracle.Health()
		feeds = make([]swap.OracleFeedStatus, 0, len(health.Feeds))
		for _, feed := range health.Feeds {
			lastObs := feed.LastObserved.UTC().Unix()
			feeds = append(feeds, swap.OracleFeedStatus{
				Pair:            feed.Pair(),
				Base:            feed.Base,
				Quote:           feed.Quote,
				LastObservation: lastObs,
				Observations:    feed.Observations,
			})
		}
	}
	return swap.ProviderStatus{
		Allow:                 cfg.Providers.AllowList(),
		LastOracleHealthCheck: last,
		OracleFeeds:           feeds,
	}
}

// SwapReverseVoucher reverses a previously minted voucher and moves funds into the refund sink.
func (n *Node) SwapReverseVoucher(providerTxID string) error {
	if err := nativecommon.Guard(n, moduleSwap); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(providerTxID)
	if trimmed == "" {
		return fmt.Errorf("swap: providerTxId required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	ledger := swap.NewLedger(manager)
	record, ok, err := ledger.Get(trimmed)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("swap: voucher not found")
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case swap.VoucherStatusReversed:
		return ErrSwapVoucherAlreadyReversed
	case swap.VoucherStatusMinted:
		// proceed
	default:
		return ErrSwapVoucherNotMinted
	}
	if record.MintAmountWei == nil || record.MintAmountWei.Sign() <= 0 {
		return fmt.Errorf("swap: voucher amount invalid")
	}
	balance, err := manager.Balance(record.Recipient[:], record.Token)
	if err != nil {
		return err
	}
	if balance.Cmp(record.MintAmountWei) < 0 {
		return ErrSwapReversalInsufficientBalance
	}
	updatedRecipient := new(big.Int).Sub(balance, record.MintAmountWei)
	if err := manager.SetBalance(record.Recipient[:], record.Token, updatedRecipient); err != nil {
		return err
	}
	sink := n.swapRefundSink
	sinkBalance, err := manager.Balance(sink[:], record.Token)
	if err != nil {
		return err
	}
	updatedSink := new(big.Int).Add(sinkBalance, record.MintAmountWei)
	if err := manager.SetBalance(sink[:], record.Token, updatedSink); err != nil {
		return err
	}
	if err := ledger.MarkReversed(trimmed); err != nil {
		return err
	}
	return nil
}

// SwapMarkReconciled marks the supplied vouchers as reconciled in the ledger.
func (n *Node) SwapMarkReconciled(ids []string) error {
	trimmed := make([]string, 0, len(ids))
	for _, id := range ids {
		if t := strings.TrimSpace(id); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) == 0 {
		return nil
	}
	if err := nativecommon.Guard(n, moduleSwap); err != nil {
		return err
	}
	return n.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		if err := ledger.MarkReconciled(trimmed); err != nil {
			return err
		}
		evt := events.SwapTreasuryReconciled{
			VoucherIDs: trimmed,
			ObservedAt: time.Now().UTC().Unix(),
		}.Event()
		if evt != nil {
			n.state.AppendEvent(evt)
		}
		return nil
	})
}

// SwapRecordBurn persists a burn-for-redeem receipt and marks associated vouchers as reconciled.
func (n *Node) SwapRecordBurn(receipt *swap.BurnReceipt) error {
	if receipt == nil {
		return fmt.Errorf("swap: burn receipt required")
	}
	if err := nativecommon.Guard(n, moduleSwap); err != nil {
		return err
	}
	trimmedID := strings.TrimSpace(receipt.ReceiptID)
	if trimmedID == "" {
		return fmt.Errorf("swap: burn receiptId required")
	}
	return n.WithState(func(m *nhbstate.Manager) error {
		burnLedger := swap.NewBurnLedger(m)
		if err := burnLedger.Put(receipt); err != nil {
			return err
		}
		var proofIDs []string
		if len(receipt.VoucherIDs) > 0 {
			voucherLedger := swap.NewLedger(m)
			seen := make(map[string]struct{})
			for _, voucherID := range receipt.VoucherIDs {
				record, ok, err := voucherLedger.Get(voucherID)
				if err != nil {
					return err
				}
				if ok && record != nil {
					trimmed := strings.TrimSpace(record.PriceProofID)
					if trimmed != "" {
						if _, exists := seen[trimmed]; !exists {
							proofIDs = append(proofIDs, trimmed)
							seen[trimmed] = struct{}{}
						}
					}
				}
			}
			if err := voucherLedger.MarkReconciled(receipt.VoucherIDs); err != nil {
				return err
			}
		}
		observed := receipt.ObservedAt
		if observed <= 0 {
			observed = time.Now().UTC().Unix()
		}
		burnEvent := events.SwapBurnRecorded{
			ReceiptID:    trimmedID,
			ProviderTxID: strings.TrimSpace(receipt.ProviderTxID),
			Token:        strings.TrimSpace(receipt.Token),
			Amount:       cloneBigInt(receipt.AmountWei),
			BurnTx:       strings.TrimSpace(receipt.BurnTxHash),
			TreasuryTx:   strings.TrimSpace(receipt.TreasuryTxID),
			VoucherIDs:   append([]string{}, receipt.VoucherIDs...),
			ObservedAt:   observed,
		}.Event()
		if burnEvent != nil {
			n.state.AppendEvent(burnEvent)
		}
		if len(receipt.VoucherIDs) > 0 {
			recon := events.SwapTreasuryReconciled{
				VoucherIDs: append([]string{}, receipt.VoucherIDs...),
				ReceiptID:  trimmedID,
				ObservedAt: observed,
			}.Event()
			if recon != nil {
				n.state.AppendEvent(recon)
			}
		}
		if len(proofIDs) > 0 {
			proofEvt := events.SwapRedeemProof{
				ReceiptID:     trimmedID,
				ProviderTxID:  strings.TrimSpace(receipt.ProviderTxID),
				VoucherIDs:    append([]string{}, receipt.VoucherIDs...),
				PriceProofIDs: proofIDs,
				ObservedAt:    observed,
			}.Event()
			if proofEvt != nil {
				n.state.AppendEvent(proofEvt)
			}
		}
		return nil
	})
}

// SwapListBurnReceipts paginates burn receipts for audit consumption.
func (n *Node) SwapListBurnReceipts(startTs, endTs int64, cursor string, limit int) ([]*swap.BurnReceipt, string, error) {
	var (
		receipts []*swap.BurnReceipt
		next     string
	)
	err := n.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewBurnLedger(m)
		list, cursorOut, err := ledger.List(startTs, endTs, cursor, limit)
		if err != nil {
			return err
		}
		receipts = make([]*swap.BurnReceipt, 0, len(list))
		for _, receipt := range list {
			receipts = append(receipts, receipt.Copy())
		}
		next = cursorOut
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return receipts, next, nil
}

func (n *Node) ResolveUsername(username string) ([]byte, bool) {
	return n.state.ResolveUsername(username)
}

func (n *Node) HasRole(role string, addr []byte) bool {
	return n.state.HasRole(role, addr)
}

func (n *Node) Events() []types.Event {
	if n == nil || n.state == nil {
		return nil
	}
	return n.state.Events()
}

func (n *Node) WithState(fn func(*nhbstate.Manager) error) error {
	if fn == nil {
		return fmt.Errorf("state callback required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	return fn(manager)
}

func (n *Node) QueryState(namespace, key string) (*QueryResult, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	result, err := n.state.QueryState(namespace, key)
	if errors.Is(err, ErrQueryNotSupported) {
		return n.queryStateFallback(namespace, key)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (n *Node) QueryPrefix(namespace, prefix string) ([]QueryRecord, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	records, err := n.state.QueryPrefix(namespace, prefix)
	if errors.Is(err, ErrQueryNotSupported) {
		return n.queryPrefixFallback(namespace, prefix)
	}
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (n *Node) SimulateTx(txBytes []byte) (*SimulationResult, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	if len(txBytes) == 0 {
		return nil, fmt.Errorf("simulate: tx bytes required")
	}
	var protoTx consensusv1.Transaction
	if err := proto.Unmarshal(txBytes, &protoTx); err != nil {
		return nil, fmt.Errorf("simulate: decode transaction: %w", err)
	}
	tx, err := codec.TransactionFromProto(&protoTx)
	if err != nil {
		return nil, err
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	stateCopy, err := n.state.Copy()
	if err != nil {
		return nil, err
	}
	stateCopy.events = nil
	stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())
	blockHeight := n.chain.GetHeight()
	blockTime := n.currentTime()
	stateCopy.BeginBlock(blockHeight, blockTime)
	defer stateCopy.EndBlock()
	result, err := stateCopy.ExecuteTransaction(tx)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (n *Node) queryStateFallback(namespace, key string) (*QueryResult, error) {
	ns := strings.TrimSpace(strings.ToLower(namespace))
	path := strings.TrimSpace(key)
	switch ns {
	case "swap":
		if path == "oracles" {
			status := n.SwapProviderStatus()
			payload, err := json.Marshal(status)
			if err != nil {
				return nil, err
			}
			return &QueryResult{Value: payload}, nil
		}
	case "gov", "governance":
		manager := nhbstate.NewManager(n.state.Trie)
		switch {
		case path == "params":
			policy := n.governancePolicy()
			params := make(map[string]string)
			keys := append([]string{}, policy.AllowedParams...)
			if !containsString(keys, governance.ParamKeyMinimumValidatorStake) {
				keys = append(keys, governance.ParamKeyMinimumValidatorStake)
			}
			seen := make(map[string]struct{})
			for _, name := range keys {
				trimmed := strings.TrimSpace(name)
				if trimmed == "" {
					continue
				}
				if _, ok := seen[trimmed]; ok {
					continue
				}
				seen[trimmed] = struct{}{}
				raw, ok, err := manager.ParamStoreGet(trimmed)
				if err != nil {
					return nil, err
				}
				if ok {
					params[trimmed] = string(raw)
				}
			}
			response := struct {
				Policy governance.ProposalPolicy `json:"policy"`
				Params map[string]string         `json:"params"`
			}{
				Policy: policy,
				Params: params,
			}
			payload, err := json.Marshal(response)
			if err != nil {
				return nil, err
			}
			return &QueryResult{Value: payload}, nil
		case path == "proposals/latest":
			var latest uint64
			if _, err := manager.KVGet(nhbstate.GovernanceSequenceKey(), &latest); err != nil {
				return nil, err
			}
			payload, err := json.Marshal(struct {
				Latest uint64 `json:"latest"`
			}{Latest: latest})
			if err != nil {
				return nil, err
			}
			return &QueryResult{Value: payload}, nil
		case strings.HasPrefix(path, "tallies/"):
			idText := strings.TrimSpace(strings.TrimPrefix(path, "tallies/"))
			if idText == "" {
				return nil, fmt.Errorf("gov: proposal id required")
			}
			proposalID, err := strconv.ParseUint(idText, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("gov: invalid proposal id: %w", err)
			}
			proposal, ok, err := manager.GovernanceGetProposal(proposalID)
			if err != nil {
				return nil, err
			}
			if !ok || proposal == nil {
				payload, marshalErr := json.Marshal(struct {
					ProposalID uint64 `json:"proposal_id"`
				}{ProposalID: proposalID})
				if marshalErr != nil {
					return nil, marshalErr
				}
				return &QueryResult{Value: payload}, nil
			}
			votes, err := manager.GovernanceListVotes(proposalID)
			if err != nil {
				return nil, err
			}
			engine := n.newGovernanceEngine(manager)
			tally, status, err := engine.ComputeTally(proposal, votes)
			if err != nil {
				return nil, err
			}
			response := struct {
				ProposalID uint64            `json:"proposal_id"`
				Status     string            `json:"status"`
				Tally      *governance.Tally `json:"tally"`
			}{
				ProposalID: proposalID,
				Status:     status.StatusString(),
				Tally:      tally,
			}
			payload, err := json.Marshal(response)
			if err != nil {
				return nil, err
			}
			return &QueryResult{Value: payload}, nil
		}
	}
	return nil, ErrQueryNotSupported
}

func (n *Node) queryPrefixFallback(namespace, prefix string) ([]QueryRecord, error) {
	ns := strings.TrimSpace(strings.ToLower(namespace))
	scope := strings.TrimSpace(prefix)
	switch ns {
	case "gov", "governance":
		if scope == "params" {
			manager := nhbstate.NewManager(n.state.Trie)
			policy := n.governancePolicy()
			keys := append([]string{}, policy.AllowedParams...)
			if !containsString(keys, governance.ParamKeyMinimumValidatorStake) {
				keys = append(keys, governance.ParamKeyMinimumValidatorStake)
			}
			seen := make(map[string]struct{})
			records := make([]QueryRecord, 0, len(keys))
			for _, name := range keys {
				trimmed := strings.TrimSpace(name)
				if trimmed == "" {
					continue
				}
				if _, ok := seen[trimmed]; ok {
					continue
				}
				seen[trimmed] = struct{}{}
				raw, ok, err := manager.ParamStoreGet(trimmed)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				records = append(records, QueryRecord{Key: trimmed, Value: append([]byte(nil), raw...)})
			}
			return records, nil
		}
	}
	return nil, ErrQueryNotSupported
}

func containsString(list []string, target string) bool {
	trimmedTarget := strings.TrimSpace(target)
	for _, entry := range list {
		if strings.TrimSpace(entry) == trimmedTarget {
			return true
		}
	}
	return false
}

// --- Both accessors are needed by different subsystems ---

// GenesisHash exposes the canonical genesis hash for the local chain.
func (n *Node) GenesisHash() []byte {
	return n.chain.GenesisHash()
}

// ChainID exposes the chain identifier (used by P2P authenticated handshake).
func (n *Node) ChainID() uint64 {
	return n.chain.ChainID()
}

// GetLastCommitHash returns a commit hash/seed (used by BFT proposer selection).
func (n *Node) GetLastCommitHash() []byte {
	return n.chain.Tip()
}

// Chain returns a reference to the node's blockchain object.
func (n *Node) Chain() *Blockchain {
	return n.chain
}
