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
	"log/slog"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhbchain/config"
	"nhbchain/consensus"
	"nhbchain/consensus/bft"
	"nhbchain/consensus/codec"
	"nhbchain/consensus/potso/evidence"
	"nhbchain/consensus/potso/penalty"
	"nhbchain/core/claimable"
	"nhbchain/core/engagement"
	"nhbchain/core/epoch"
	"nhbchain/core/events"
	"nhbchain/core/genesis"
	"nhbchain/core/identity"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	syncmgr "nhbchain/core/sync"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/mempool"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/creator"
	"nhbchain/native/escrow"
	"nhbchain/native/fees"
	govcfg "nhbchain/native/gov"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/loyalty"
	nativeparams "nhbchain/native/params"
	"nhbchain/native/pos"
	"nhbchain/native/potso"
	"nhbchain/native/reputation"
	"nhbchain/native/subscriptions"
	swap "nhbchain/native/swap"
	"nhbchain/observability"
	"nhbchain/p2p"
	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/storage"
	"nhbchain/storage/trie"

	statebank "nhbchain/state/bank"
	statepotso "nhbchain/state/potso"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"google.golang.org/protobuf/proto"
)

// Node is the central controller, wiring all components together.
type Node struct {
	db                    storage.Database
	state                 *StateProcessor
	chain                 *Blockchain
	syncMgr               *syncmgr.Manager
	validatorKey          *crypto.PrivateKey
	mempool               []*types.Transaction
	mempoolMu             sync.Mutex
	proposedTxs           map[string]struct{}
	mempoolLimit          int
	allowUnlimitedMempool bool
	senderUsage           map[string]*senderQuotaUsage
	senderNonces          map[string]map[uint64]time.Time
	pendingNonces         map[string]nonceRecord
	posArrival            map[string]time.Time
	txValidationMu        sync.RWMutex
	txSimulationEnabled   bool
	bftEngine             *bft.Engine
	stateMu               sync.RWMutex
	// selfProposedHash is the header hash of the block CreateBlock most
	// recently built from the current, possibly-drifted n.state (guarded by
	// stateMu). ValidateBlock/commitBlock skip the committed-head drift
	// check when the block they're processing matches this hash, since it
	// means n.state's current content -- including any pending direct-state
	// RPC writes (lending, escrow) folded in by WithState -- is exactly what
	// produced that block's declared root, not unrelated local drift. Any
	// other block (a peer's proposal, a catch-up/synced block) did not come
	// from this state, so the drift check still applies to it. See
	// docs/issue30.md item 2 / task #32.
	selfProposedHash             []byte
	// quorumCertActivationHeight gates NHB-TRIAGE-C1's quorum-certificate
	// check on the untrusted P2P block-sync path (commitSyncedBlock):
	// blocks at or below this height are accepted without one (so already
	// -committed, pre-fix blocks -- which never carry a QuorumCert -- keep
	// syncing to new/catching-up nodes), blocks above it are rejected
	// unless they carry a QuorumCert that verifies against the current
	// validator set. NewNode defaults this to math.MaxUint64 (effectively
	// disabled -- every existing caller, including the whole test suite,
	// keeps today's behavior unchanged). Enabling this is an explicit,
	// deploy-time decision, not automatic: SetQuorumCertActivationHeight
	// must be called with the chain's current tip height, on every
	// validator, as part of one coordinated upgrade -- deliberately NOT
	// auto-detected from chain state, since that could let one validator
	// silently enforce while its peers don't.
	quorumCertActivationHeight uint64
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
	paymasterLimits              PaymasterLimits
	paymasterTopUpPolicy         PaymasterAutoTopUpPolicy
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
	feesMu                       sync.RWMutex
	feesPolicy                   fees.Policy
	transferGasPolicy            TransferGasPolicy
	potsoEngineMu                sync.Mutex
	potsoEngine                  *potso.Engine
	potsoLedger                  *statepotso.Ledger
	globalCfgMu                  sync.RWMutex
	globalCfg                    config.Global
	networkMode                  string
	networkBroadcaster           p2p.Broadcaster
	blockSyncMu                  sync.Mutex
	// externalCommitNotifier, if set, is called after a block committed via
	// the peer-sync path (handleNetworkBlocks/commitSyncedBlock) so the BFT
	// engine can immediately abandon a stale in-flight round for a height
	// the network already finalized, instead of discovering it lazily up
	// to a full round-timeout later. Wired by cmd/nhb/main.go to the BFT
	// engine's NotifyExternalCommit once both are constructed, mirroring
	// the SetNetworkBroadcaster wiring pattern below.
	externalCommitNotifier func()

	posStreamMu      sync.RWMutex
	posStreamSeq     uint64
	posStreamNextID  uint64
	posStreamSubs    map[uint64]chan POSFinalityUpdate
	posStreamHistory []POSFinalityUpdate
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
	moduleTransferNHB      = "transfer_nhb"
	moduleTransferZNHB     = "transfer_znhb"
	moduleStaking          = "staking"
	moduleSubscriptions    = "subscriptions"
	// moduleSwapRedeem gates only new TxTypeRedeemNHB burns
	// (applyRedeemNHB). It deliberately does NOT gate
	// applyAttestRedemption, so an off-chain payout that has already burned
	// its NHB can still be attested paid/failed while new burns are paused
	// -- see applyRedeemNHB's guard call in core/state_transition.go.
	moduleSwapRedeem = "swap_redeem"
	// moduleMarket must match native/market/engine.go's own moduleName
	// constant exactly ("market") -- nativecommon.Guard is keyed by this
	// string, and the market engine calls it internally via
	// engine.SetPauses(sp.pauses), not via a call site in this package.
	moduleMarket = "market"
)

var ErrPaymasterUnauthorized = errors.New("paymaster: caller lacks ROLE_PAYMASTER_ADMIN")

// ErrReputationVerifierUnauthorized is returned when a caller lacks the
// required verifier role to issue skill attestations.
var ErrReputationVerifierUnauthorized = errors.New("reputation: caller lacks verifier role")

// ErrMempoolFull is returned when the node's mempool has reached its configured capacity.
var ErrMempoolFull = errors.New("mempool: transaction limit reached")

// ErrInvalidTransaction marks transactions that fail basic validation or cannot be executed.
var ErrInvalidTransaction = errors.New("mempool: invalid transaction")

// ErrMempoolByteLimit indicates the aggregate mempool byte capacity has been reached.
var ErrMempoolByteLimit = errors.New("mempool: byte capacity reached")

// ErrMempoolSenderLimit indicates a sender-specific cap has been exceeded.
var ErrMempoolSenderLimit = errors.New("mempool: sender capacity reached")

// ErrMempoolNonceDuplicate guards against rapid nonce replays from the same sender.
var ErrMempoolNonceDuplicate = errors.New("mempool: nonce recently used")

// ErrMempoolQuotaExceeded indicates the caller exhausted their governance-configured quota window.
var ErrMempoolQuotaExceeded = errors.New("mempool: sender quota exceeded")

// DefaultBlockTimestampTolerance bounds how far ahead of the local clock a
// block timestamp may drift before it is rejected.
const DefaultBlockTimestampTolerance = 5 * time.Second

const (
	senderNonceTTL        = 15 * time.Minute
	senderUsagePruneAfter = 48 * time.Hour
	mempoolMaxSenderTx    = 32
	mempoolMaxSenderBytes = 256 << 10 // 256 KiB per sender window
)

type senderQuotaUsage struct {
	epoch    uint64
	counters nativecommon.QuotaNow
	updated  time.Time
}

type nonceRecord struct {
	sender string
	nonce  uint64
}

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

func (n *Node) validateBlockTimestamp(ts int64, allowHistorical bool) error {
	if n == nil || n.chain == nil {
		return fmt.Errorf("%w: chain unavailable", ErrBlockTimestampOutOfWindow)
	}
	prev := n.chain.LastTimestamp()
	min := prev
	if !allowHistorical {
		tolerance := n.blockTimestampTolerance()
		now := n.currentTime()
		max := now.Add(tolerance).Unix()
		if ts > max {
			return fmt.Errorf("%w: timestamp %d exceeds maximum %d (now=%d tolerance=%s)", ErrBlockTimestampOutOfWindow, ts, max, now.Unix(), tolerance)
		}
	}
	if ts < min {
		return fmt.Errorf("%w: timestamp %d precedes minimum %d", ErrBlockTimestampOutOfWindow, ts, min)
	}
	return nil
}

func (n *Node) rebuildStateProcessorLocked(root common.Hash) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	if n.db == nil {
		return fmt.Errorf("database unavailable")
	}
	var rootBytes []byte
	if root != (common.Hash{}) {
		rootBytes = root.Bytes()
	}
	stateTrie, err := trie.NewTrie(n.db, rootBytes)
	if err != nil {
		return err
	}
	if err := nhbstate.EnsureStateVersion(stateTrie, true); err != nil {
		return err
	}
	stateProcessor, err := NewStateProcessor(stateTrie)
	if err != nil {
		return err
	}
	stateProcessor.SetEscrowFeeTreasury(n.escrowTreasury)
	stateProcessor.SetPauseView(n)
	stateProcessor.SetQuotaConfig(n.moduleQuotaSnapshot())
	stateProcessor.SetPaymasterEnabled(n.paymasterEnabled)
	stateProcessor.SetPaymasterLimits(n.paymasterLimits)
	stateProcessor.SetPaymasterAutoTopUpPolicy(n.paymasterTopUpPolicy)
	stateProcessor.SetFeePolicy(n.feesPolicy)
	stateProcessor.SetTransferGasPolicy(n.transferGasPolicy)
	stateProcessor.SetLendingAddresses(n.lendingModuleAddr, n.lendingCollateralAddr)
	stateProcessor.SetLendingRiskParameters(n.lendingParams)
	stateProcessor.SetLendingAccrualConfig(n.lendingReserveFactorBps, n.lendingProtocolFeeBps, n.lendingInterestModel)
	stateProcessor.SetLendingDeveloperFee(n.lendingDeveloperFeeBps, n.lendingDeveloperFeeCollector)
	stateProcessor.SetLendingCollateralRouting(n.lendingCollateralRouting)
	stateProcessor.SetGovernancePolicy(n.governancePolicy())
	stateProcessor.SetSwapPayoutAuthorities(n.swapConfig().PayoutAuthorities)
	stateProcessor.SetSwapConfig(n.swapConfig())
	if n.chain != nil {
		stateProcessor.SetSwapVoucherChainID(n.chain.ChainID())
	}
	if err := stateProcessor.SetEngagementConfig(n.state.EngagementConfig()); err != nil {
		return err
	}
	if err := stateProcessor.SetEpochConfig(n.state.EpochConfig()); err != nil {
		return err
	}
	if err := stateProcessor.SetRewardConfig(n.state.RewardConfig()); err != nil {
		return err
	}
	if err := stateProcessor.SetPotsoRewardConfig(n.state.PotsoRewardConfig()); err != nil {
		return err
	}
	if err := stateProcessor.SetPotsoWeightConfig(n.state.PotsoWeightConfig()); err != nil {
		return err
	}
	if err := stateProcessor.ensureValidatorSetLiveness(n.currentTime()); err != nil {
		return err
	}
	n.state = stateProcessor
	if err := n.refreshModulePauses(); err != nil {
		return err
	}
	n.refreshValidatorSet()
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

func NewNode(db storage.Database, key *crypto.PrivateKey, genesisPath string, allowAutogenesis bool, allowMigrate bool) (*Node, error) {
	validatorAddr := key.PubKey().Address()
	fmt.Printf("Starting node with validator address: %s\n", validatorAddr.String())

	chain, err := NewBlockchain(db, genesisPath, allowAutogenesis)
	if err != nil {
		return nil, err
	}
	// One-time, idempotent: populates the transaction-hash index (see
	// blockchain.go's AddBlock/FindTransactionHeight) for every block that
	// existed before this index did. No-ops instantly on every startup
	// after the first, and on any chain that's already fully indexed
	// (including a fresh test/dev chain with nothing to backfill).
	if indexed, backfillErr := chain.BackfillTransactionIndex(); backfillErr != nil {
		fmt.Printf("Warning: transaction hash index backfill failed: %v (nhb_getTransaction/nhb_getTransactionReceipt fall back to a bounded scan regardless)\n", backfillErr)
	} else if indexed > 0 {
		fmt.Printf("Backfilled transaction hash index: %d transactions indexed.\n", indexed)
	}

	// Load current state root from the chain tip (if any), then open the trie.
	var root []byte
	if header := chain.CurrentHeader(); header != nil {
		root = header.StateRoot
	}
	stateTrie, err := trie.NewTrie(db, root)
	if err != nil {
		if shouldAttemptGenesisRebuild(err) && len(root) > 0 && strings.TrimSpace(genesisPath) != "" {
			fmt.Println("State trie missing nodes for stored genesis; attempting to rebuild from genesis file.")
			if rebuildErr := rebuildGenesisState(db, genesisPath); rebuildErr != nil {
				return nil, fmt.Errorf("rebuild genesis state: %w (original error: %v)", rebuildErr, err)
			}
			stateTrie, err = trie.NewTrie(db, root)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := nhbstate.EnsureStateVersion(stateTrie, allowMigrate); err != nil {
		if errors.Is(err, nhbstate.ErrStateVersionMismatch) {
			return nil, fmt.Errorf("%w; pass --allow-migrate to bypass the guard", err)
		}
		return nil, err
	}

	stateProcessor, err := NewStateProcessor(stateTrie)
	if err != nil {
		return nil, err
	}
	if err := stateProcessor.ensureValidatorSetLiveness(time.Now().UTC()); err != nil {
		return nil, err
	}

	var treasury [20]byte
	copy(treasury[:], validatorAddr.Bytes())

	// The genesis-declared admin wallet (if the loaded genesis configures
	// one) is the canonical treasury -- fee revenue, escrow refunds, and the
	// transfer-gas fee collector all derive from it instead of defaulting to
	// the validator's own address. hasAdminWallet tracks whether a *real*
	// admin wallet was configured (genesis field or explicit env override),
	// as distinct from the validator-address fallback -- code that must
	// never silently operate against the fallback (e.g. the ZNHB purchase
	// flow) checks this instead of just using treasury directly.
	var hasAdminWallet bool
	if adminAddr, ok := chain.AdminWallet(); ok {
		copy(treasury[:], adminAddr[:])
		hasAdminWallet = true
	}

	if masterTreasury := strings.TrimSpace(os.Getenv("NHB_MASTER_TREASURY")); masterTreasury != "" {
		if addr, err := genesis.ParseBech32Account(masterTreasury); err == nil {
			copy(treasury[:], addr[:])
			hasAdminWallet = true
		} else {
			fmt.Printf("Warning: Failed to parse NHB_MASTER_TREASURY, falling back to validator: %v\n", err)
		}
	}

	stateProcessor.SetEscrowFeeTreasury(treasury)
	stateProcessor.SetAdminWallet(treasury, hasAdminWallet)
	// Deliberately NOT calling EnsureZNHBPoolsBootstrapped() here. A prior
	// version of this code did, and it caused a real production incident:
	// this call happens before ensurePendingStateMatchesCommittedHeadLocked
	// ("startup") runs further down NewNode, so its writes were still
	// pending/uncommitted when that drift-reset compared PendingRoot()
	// against the last committed header and silently discarded them --
	// the node came up looking healthy with no error, but the pools were
	// never actually bootstrapped. EnsureZNHBPoolsBootstrapped is instead
	// called from ProcessBlockLifecycle (core/epochs.go), so its writes are
	// folded into a real committed block on every validator identically,
	// the same way CheckZNHBSupplyInvariant's adjacent call already is.
	if hasAdminWallet {
		// Activates the halving-schedule validator/staking reward emission
		// (core/rewards/halving.go) backed by the ZNHB Reward Pool. This is a
		// pure function of built-in constants (not chain state), so it's safe
		// to recompute identically on every process start -- rewardConfig
		// itself is in-memory only, never persisted. 20/50/30 validator/
		// staker/engagement split matches this codebase's existing test
		// convention (core/rewards_logic_test.go). Not currently governance
		// -adjustable -- rewards.Config has no ParamStore hook, unlike
		// staking's APR/payout-period knobs (see config.toml's
		// AllowedParams). Changing the split requires a code change today.
		rewardCfg := rewards.HalvingScheduleConfig(2000, 5000, 3000, 2000)
		if err := stateProcessor.SetRewardConfig(rewardCfg); err != nil {
			return nil, fmt.Errorf("activate ZNHB reward halving schedule: %w", err)
		}
	}

	// The treasury buyback engine's reference-price signer quorum is
	// genesis-immutable (core/blockchain.go's BuybackSigners, sourced from
	// genesis.BuybackSignerConfig) -- unlike the bps parameters below, it is
	// never touched by governance, so a captured vote can never redirect
	// buyback authority to colluding signers. fee_share_bps/discount_bps/
	// safety_margin_bps are deliberately conservative launch defaults
	// (20% of fee revenue, 5% below curve price, 5% below reference price)
	// intended to become governance-adjustable via a future
	// policy.buybackParams proposal kind -- the signer set itself never will.
	if signers, threshold, ok := chain.BuybackSigners(); ok {
		buybackCfg := buyback.Config{
			FeeShareBps:     2000,
			DiscountBps:     500,
			SafetyMarginBps: 500,
			SignerThreshold: threshold,
			Signers:         signers,
		}
		if err := stateProcessor.SetBuybackConfig(buybackCfg); err != nil {
			return nil, fmt.Errorf("configure ZNHB treasury buyback engine: %w", err)
		}
		stateProcessor.SetBuybackAccrualAddress(deriveModuleAddress("module/tokenomics/buybackAccrual", crypto.NHBPrefix))
	}

	moduleAddr := deriveModuleAddress("module/lending/treasury", crypto.NHBPrefix)
	collateralAddr := deriveModuleAddress("module/lending/collateral", crypto.ZNHBPrefix)
	creatorVaultAddr := deriveModuleAddress("module/creator/payout", crypto.NHBPrefix)
	creatorRewardsAddr := deriveModuleAddress("module/creator/rewards", crypto.NHBPrefix)

	potsoEngine, err := potso.NewEngine(potso.DefaultEngineParams())
	if err != nil {
		return nil, err
	}

	defaultSwapCfg := swap.Config{}.Normalise()
	stateProcessor.SetSwapPayoutAuthorities(defaultSwapCfg.PayoutAuthorities)
	stateProcessor.SetSwapConfig(defaultSwapCfg)
	stateProcessor.SetSwapVoucherChainID(chain.ChainID())

	pLedger, _ := statepotso.NewLedger(nil, nil)

	node := &Node{
		db:                         db,
		state:                      stateProcessor,
		chain:                      chain,
		validatorKey:               key,
		mempool:                    make([]*types.Transaction, 0),
		proposedTxs:                make(map[string]struct{}),
		posArrival:                 make(map[string]time.Time),
		senderUsage:                make(map[string]*senderQuotaUsage),
		senderNonces:               make(map[string]map[uint64]time.Time),
		pendingNonces:              make(map[string]nonceRecord),
		escrowTreasury:             treasury,
		engagementMgr:              engagement.NewManager(stateProcessor.EngagementConfig()),
		swapCfg:                    defaultSwapCfg,
		swapSanctions:              swap.DefaultSanctionsChecker,
		swapRefundSink:             treasury,
		evidenceStore:              evidence.NewStore(db),
		evidenceMaxAge:             evidence.DefaultMaxAgeBlocks,
		paymasterEnabled:           stateProcessor.PaymasterEnabled(),
		paymasterLimits:            PaymasterLimits{},
		paymasterTopUpPolicy:       PaymasterAutoTopUpPolicy{Token: "ZNHB"},
		timestampTolerance:         DefaultBlockTimestampTolerance,
		timeSource:                 func() time.Time { return time.Now().UTC() },
		// Disabled by default (see the field doc comment): every existing
		// caller of NewNode, including the whole test suite, gets today's
		// unchanged behavior unless SetQuorumCertActivationHeight is
		// called explicitly to opt in.
		quorumCertActivationHeight: math.MaxUint64,
		lendingModuleAddr:          moduleAddr,
		lendingCollateralAddr:      collateralAddr,
		creatorPayoutVaultAddr:     creatorVaultAddr,
		creatorRewardsTreasuryAddr: creatorRewardsAddr,
		modulePauses:               make(map[string]bool),
		moduleQuotas:               make(map[string]nativecommon.Quota),
		feesPolicy:                 fees.Policy{Domains: map[string]fees.DomainPolicy{}},
		transferGasPolicy: TransferGasPolicy{
			Enabled: true,
			// 1000 NHB (18 decimals), not 1000 base units -- the founder's
			// "$1000 free tier" spec, matching NHB's 1:1 USD peg. The old
			// literal big.NewInt(1000) was 0.000000000000001 NHB, exhausting
			// the free tier on a wallet's first transaction.
			FreeSpendLimitWei: new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
			Window:            TransferGasWindowLifetime,
			FeeCollector:      treasury,
			// 20 bps (0.20%) once the free tier is exhausted -- see
			// docs/issue30.md item 7b.
			FeeBps: 20,
			// 10 bps (0.10%) once the free tier is exhausted -- ZNHB's own,
			// separately configurable rate, lower than NHB's 20 bps since
			// ZNHB is a lower-priced asset with its own use case.
			FeeBpsZNHB: 10,
		},
		potsoEngine:         potsoEngine,
		potsoLedger:         pLedger,
		txSimulationEnabled: true,
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
			Mempool: config.Mempool{MaxBytes: 16 << 20, POSReservationBPS: consensus.DefaultPOSReservationBPS},
			Blocks:  config.Blocks{MaxTxs: 500},
			Staking: config.Staking{
				AprBps:                1250,
				PayoutPeriodDays:      30,
				UnbondingDays:         7,
				MinStakeWei:           "0",
				MaxEmissionPerYearWei: "5000000000000000000",
				RewardAsset:           "ZNHB",
			},
			Fees: config.Fees{
				FreeTierTxPerMonth: config.DefaultFreeTierTxPerMonth,
				MDRBasisPoints:     config.DefaultMDRBasisPoints,
				// 1000 NHB at 18 decimals -- see docs/issue30.md item 7.
				TransferFreeTierSpendWei: "1000000000000000000000",
				TransferFreeTierWindow:   TransferGasWindowLifetime,
				// 20 bps -- see docs/issue30.md item 7b.
				TransferFeeBps: 20,
				// 10 bps -- ZNHB's own, lower rate.
				TransferFeeBpsZNHB: 10,
			},
		},
		networkMode:      strings.TrimSpace(os.Getenv("NHB_ENV")),
		posStreamSubs:    make(map[uint64]chan POSFinalityUpdate),
		posStreamHistory: make([]POSFinalityUpdate, 0, posFinalityHistoryLimit),
	}

	if node.networkMode == "" {
		node.networkMode = "prod"
	}

	stateProcessor.SetQuotaConfig(node.moduleQuotas)
	stateProcessor.SetPaymasterLimits(node.paymasterLimits)
	stateProcessor.SetPaymasterAutoTopUpPolicy(node.paymasterTopUpPolicy)
	stateProcessor.SetFeePolicy(node.feesPolicy)
	stateProcessor.SetTransferGasPolicy(node.transferGasPolicy)
	stateProcessor.SetLendingAddresses(node.lendingModuleAddr, node.lendingCollateralAddr)
	stateProcessor.SetLendingRiskParameters(node.lendingParams)
	stateProcessor.SetLendingAccrualConfig(node.lendingReserveFactorBps, node.lendingProtocolFeeBps, node.lendingInterestModel)
	stateProcessor.SetLendingDeveloperFee(node.lendingDeveloperFeeBps, node.lendingDeveloperFeeCollector)
	stateProcessor.SetLendingCollateralRouting(node.lendingCollateralRouting)
	stateProcessor.SetGovernancePolicy(node.governancePolicy())

	node.SetModulePauses(config.Pauses{})
	node.stateMu.Lock()
	err = node.refreshModulePauses()
	node.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := node.SetGlobalConfig(node.globalCfg); err != nil {
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

	// Startup is the one time comparing pending state against the last
	// committed header is actually correct: a hard crash can leave the
	// on-disk trie store out of sync with the last persisted block, and
	// there are no legitimate pending mutations yet (nothing has run).
	// See ensurePendingStateMatchesCommittedHeadLocked for why this same
	// check must NOT run during normal operation.
	if err := node.ensurePendingStateMatchesCommittedHeadLocked("startup"); err != nil {
		return nil, err
	}

	return node, nil
}

func shouldAttemptGenesisRebuild(err error) bool {
	var missing *gethtrie.MissingNodeError
	return errors.As(err, &missing)
}

func normalizeModuleName(module string) string {
	return strings.ToLower(strings.TrimSpace(module))
}

// ensurePendingStateMatchesCommittedHeadLocked resets pending state to the
// last committed block's declared root if the two disagree. Only two things
// call this: NewNode at startup (unconditionally -- a hard crash can leave
// the on-disk trie store out of sync with the last persisted block, and
// there are no legitimate pending mutations yet), and
// resetDriftUnlessSelfProposedLocked (conditionally -- see below). It must
// never run unconditionally inside CreateBlock/ValidateBlock/CommitBlock/
// WithState during normal operation: n.state is deliberately mutated
// directly by direct-state RPCs (lending, escrow, and formerly staking) via
// WithState ahead of block inclusion, so its pending root legitimately --
// and expectedly -- differs from the last committed head between blocks.
// Running this unconditionally there previously silently discarded every
// such write the instant any block operation ran next, with only a WARN log
// to show for it (see docs/issue30.md item 2 / task #32).
func (n *Node) ensurePendingStateMatchesCommittedHeadLocked(context string) error {
	if n == nil || n.state == nil || n.chain == nil {
		return nil
	}
	header := n.chain.CurrentHeader()
	if header == nil || len(header.StateRoot) == 0 {
		return nil
	}
	expectedRoot := common.BytesToHash(header.StateRoot)
	pendingRoot := n.state.PendingRoot()
	if pendingRoot == expectedRoot {
		return nil
	}
	slog.Warn(
		"state root drift detected; resetting to committed chain head",
		slog.String("context", strings.TrimSpace(context)),
		slog.Uint64("height", header.Height),
		slog.String("expected_state_root", fmt.Sprintf("%x", expectedRoot.Bytes())),
		slog.String("pending_state_root", fmt.Sprintf("%x", pendingRoot.Bytes())),
	)
	if err := n.state.ResetToRoot(expectedRoot); err != nil {
		return fmt.Errorf("reset drifted state root: %w", err)
	}
	return nil
}

// resetDriftUnlessSelfProposedLocked is the guard ValidateBlock and
// commitBlock actually use. b is the block about to be validated/committed.
// If it's the exact block this node's own CreateBlock most recently built
// from the current n.state (tracked via selfProposedHash), n.state's
// pending content -- including any WithState writes folded in ahead of that
// proposal -- is exactly what produced b's declared root, so there is
// nothing to reset: doing so anyway would wipe those writes and then fail
// this same block's own state-root check for a completely spurious reason.
// For any other block (a peer's competing/later proposal, a catch-up/synced
// block, or anything from a prior, now-superseded round), n.state's current
// content did not contribute to it and must be reset to the last committed
// head first, exactly like ensurePendingStateMatchesCommittedHeadLocked
// always did -- otherwise this validator could never validate or adopt
// another block again once it had any pending local drift of its own.
// Callers must hold stateMu.
func (n *Node) resetDriftUnlessSelfProposedLocked(b *types.Block, context string) error {
	if n == nil {
		return nil
	}
	if b != nil && b.Header != nil && len(n.selfProposedHash) > 0 {
		if hash, err := b.Header.Hash(); err == nil && bytes.Equal(hash, n.selfProposedHash) {
			return nil
		}
	}
	return n.ensurePendingStateMatchesCommittedHeadLocked(context)
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

// StakingPauseOnChain reports the currently persisted on-chain value of the
// staking module pause flag -- a read-only query, replacing the old
// ensureStakingPauseCleared's direct trie write (cmd/nhb/main.go,
// cmd/consensusd/main.go, byte-identical duplicates, both removed). That
// function unilaterally overwrote this consensus-shared value at process
// startup whenever it disagreed with the operator's own local config: since
// refreshModulePauses (above) re-reads this same on-chain value on every
// single block validate/commit/create cycle (it is the sole source of truth
// nativecommon.Guard actually enforces against, moments after startup and
// forever after), a validator whose local config happened to say
// "unpaused" while the network's last real governance action said
// "paused" would silently force its own copy of shared state to match its
// local expectation -- exactly the kind of unilateral consensus-state
// override the governance/CreatePool/POTSO-stake fixes elsewhere this
// session close for the RPC surface. Callers should log a mismatch and let
// the operator resolve it through a real governance action, never
// overwrite this value directly.
func (n *Node) StakingPauseOnChain() (bool, error) {
	if n == nil {
		return false, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil || n.state.Trie == nil {
		return false, fmt.Errorf("state unavailable")
	}
	store := nativeparams.NewStore(nhbstate.NewManager(n.state.Trie))
	pauses, err := store.Pauses()
	if err != nil {
		return false, fmt.Errorf("load staking pause state: %w", err)
	}
	return pauses.Staking, nil
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
	n.modulePauses[moduleTransferNHB] = pauses.TransferNHB
	n.modulePauses[moduleTransferZNHB] = pauses.TransferZNHB
	n.modulePauses[moduleStaking] = pauses.Staking
	n.modulePauses[moduleSubscriptions] = pauses.Subscriptions
	n.modulePauses[moduleSwapRedeem] = pauses.SwapRedeem
	n.modulePauses[moduleMarket] = pauses.Market
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

// SetFeePolicy updates the configured fee policy and synchronises it with the state processor.
func (n *Node) SetFeePolicy(policy fees.Policy) {
	if n == nil {
		return
	}
	clone := policy.Clone()
	if clone.Domains == nil {
		clone.Domains = make(map[string]fees.DomainPolicy)
	}
	n.feesMu.Lock()
	n.feesPolicy = clone
	n.feesMu.Unlock()
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetFeePolicy(clone)
	}
	n.stateMu.Unlock()
}

// FeePolicy returns a copy of the active fee policy snapshot.
func (n *Node) FeePolicy() fees.Policy {
	if n == nil {
		return fees.Policy{}
	}
	n.feesMu.RLock()
	snapshot := n.feesPolicy.Clone()
	n.feesMu.RUnlock()
	return snapshot
}

// SetTransferGasPolicy updates the NHB transfer gas sponsorship policy.
func (n *Node) SetTransferGasPolicy(policy TransferGasPolicy) {
	if n == nil {
		return
	}
	clone := policy.Clone()
	n.feesMu.Lock()
	n.transferGasPolicy = clone
	n.feesMu.Unlock()
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetTransferGasPolicy(clone)
	}
	n.stateMu.Unlock()
}

// TransferGasPolicy returns the active NHB transfer gas sponsorship policy.
func (n *Node) TransferGasPolicy() TransferGasPolicy {
	if n == nil {
		return TransferGasPolicy{}
	}
	n.feesMu.RLock()
	snapshot := n.transferGasPolicy.Clone()
	n.feesMu.RUnlock()
	return snapshot
}

// FeesTotals lists the aggregated fee totals for the supplied domain grouped by wallet.
func (n *Node) FeesTotals(domain string) ([]fees.Totals, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	var totals []fees.Totals
	err := n.WithState(func(m *nhbstate.Manager) error {
		records, err := m.FeesListTotals(domain)
		if err != nil {
			return err
		}
		totals = make([]fees.Totals, len(records))
		for i := range records {
			totals[i] = records[i]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return totals, nil
}

// FeesMonthlyStatus exposes the aggregate free-tier usage snapshot for the active month.
func (n *Node) FeesMonthlyStatus() (nhbstate.FeeMonthlyStatus, error) {
	if n == nil {
		return nhbstate.FeeMonthlyStatus{}, fmt.Errorf("node unavailable")
	}
	var status nhbstate.FeeMonthlyStatus
	err := n.WithState(func(m *nhbstate.Manager) error {
		snapshot, err := m.FeesMonthlyStatus()
		if err != nil {
			return err
		}
		status = snapshot
		return nil
	})
	if err != nil {
		return nhbstate.FeeMonthlyStatus{}, err
	}
	return status, nil
}

// TransferGasStatus returns the active NHB transfer gas sponsorship status for
// the supplied wallet.
func (n *Node) TransferGasStatus(addr []byte) (nhbstate.TransferGasSpendStatus, error) {
	if n == nil {
		return nhbstate.TransferGasSpendStatus{}, fmt.Errorf("node unavailable")
	}
	if len(addr) != 20 {
		return nhbstate.TransferGasSpendStatus{}, fmt.Errorf("address must be 20 bytes")
	}
	policy := n.TransferGasPolicy()
	var status nhbstate.TransferGasSpendStatus
	err := n.WithState(func(m *nhbstate.Manager) error {
		var wallet [20]byte
		copy(wallet[:], addr)
		window := nhbstate.TransferGasWindowLifetime
		if normalizeTransferGasWindow(policy.Window) == TransferGasWindowMonthly {
			window = nhbstate.TransferGasWindowMonthly
		}
		snapshot, err := m.TransferGasSpendStatus(wallet, window, n.state.blockTimestamp(), policy.FreeSpendLimitWei, "NHB")
		if err != nil {
			return err
		}
		status = snapshot
		return nil
	})
	if err != nil {
		return nhbstate.TransferGasSpendStatus{}, err
	}
	return status, nil
}

// TransferGasStatusForAsset returns the active transfer gas sponsorship
// status for the supplied wallet, scoped to asset ("NHB" or "ZNHB"). Unlike
// TransferGasStatus (which is always scoped to NHB for backward
// compatibility), this allows callers to check ZNHB free-tier eligibility as
// well, since NHB and ZNHB spend are tracked independently.
func (n *Node) TransferGasStatusForAsset(addr []byte, asset string) (nhbstate.TransferGasSpendStatus, error) {
	if n == nil {
		return nhbstate.TransferGasSpendStatus{}, fmt.Errorf("node unavailable")
	}
	if len(addr) != 20 {
		return nhbstate.TransferGasSpendStatus{}, fmt.Errorf("address must be 20 bytes")
	}
	policy := n.TransferGasPolicy()
	var status nhbstate.TransferGasSpendStatus
	err := n.WithState(func(m *nhbstate.Manager) error {
		var wallet [20]byte
		copy(wallet[:], addr)
		window := nhbstate.TransferGasWindowLifetime
		if normalizeTransferGasWindow(policy.Window) == TransferGasWindowMonthly {
			window = nhbstate.TransferGasWindowMonthly
		}
		snapshot, err := m.TransferGasSpendStatus(wallet, window, n.state.blockTimestamp(), policy.FreeSpendLimitWei, asset)
		if err != nil {
			return err
		}
		status = snapshot
		return nil
	})
	if err != nil {
		return nhbstate.TransferGasSpendStatus{}, err
	}
	return status, nil
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

func (n *Node) devNetwork() bool {
	mode := strings.ToLower(strings.TrimSpace(n.networkMode))
	switch mode {
	case "dev", "development", "local", "test", "testing", "staging":
		return true
	default:
		return false
	}
}

func (n *Node) pruneSenderUsageLocked(now time.Time) {
	if len(n.senderUsage) == 0 {
		return
	}
	for key, usage := range n.senderUsage {
		if usage == nil {
			delete(n.senderUsage, key)
			continue
		}
		if now.Sub(usage.updated) > senderUsagePruneAfter {
			delete(n.senderUsage, key)
		}
	}
}

func (n *Node) pruneSenderNoncesLocked(now time.Time) {
	if len(n.senderNonces) == 0 {
		return
	}
	for sender, nonces := range n.senderNonces {
		if len(nonces) == 0 {
			delete(n.senderNonces, sender)
			continue
		}
		for nonce, expiry := range nonces {
			if now.After(expiry) {
				delete(nonces, nonce)
			}
		}
		if len(nonces) == 0 {
			delete(n.senderNonces, sender)
		}
	}
}

func (n *Node) registerSenderNonceLocked(sender string, nonce uint64, now time.Time) error {
	if sender == "" || nonce == 0 {
		return nil
	}
	n.pruneSenderNoncesLocked(now)
	if n.senderNonces == nil {
		n.senderNonces = make(map[string]map[uint64]time.Time)
	}
	table := n.senderNonces[sender]
	if table == nil {
		table = make(map[uint64]time.Time)
		n.senderNonces[sender] = table
	}
	if expiry, ok := table[nonce]; ok && now.Before(expiry) {
		return ErrMempoolNonceDuplicate
	}
	table[nonce] = now.Add(senderNonceTTL)
	return nil
}

func (n *Node) applySenderQuotaLocked(sender string, quota nativecommon.Quota, now time.Time) error {
	if sender == "" || (quota.MaxRequestsPerMin == 0 && quota.MaxNHBPerEpoch == 0) {
		return nil
	}
	epoch := quotaEpochForConfig(quota, now)
	if n.senderUsage == nil {
		n.senderUsage = make(map[string]*senderQuotaUsage)
	}
	usage, ok := n.senderUsage[sender]
	if !ok || usage == nil || usage.epoch != epoch {
		usage = &senderQuotaUsage{epoch: epoch, counters: nativecommon.QuotaNow{EpochID: epoch}}
		n.senderUsage[sender] = usage
	}
	next, err := nativecommon.CheckQuota(quota, epoch, usage.counters, 1, 0)
	if err != nil {
		return err
	}
	usage.counters = next
	usage.updated = now
	return nil
}

func (n *Node) trackTransactionLocked(key string, sender []byte, nonce uint64, now time.Time) {
	if key == "" {
		return
	}
	if n.pendingNonces == nil {
		n.pendingNonces = make(map[string]nonceRecord)
	}
	record := nonceRecord{}
	if len(sender) > 0 && nonce > 0 {
		record.sender = hex.EncodeToString(sender)
		record.nonce = nonce
	}
	n.pendingNonces[key] = record
}

func (n *Node) untrackTransactionLocked(key string) {
	if key == "" || len(n.pendingNonces) == 0 {
		return
	}
	record, ok := n.pendingNonces[key]
	if !ok {
		return
	}
	delete(n.pendingNonces, key)
	if record.sender == "" || record.nonce == 0 {
		return
	}
	table, ok := n.senderNonces[record.sender]
	if !ok {
		return
	}
	delete(table, record.nonce)
	if len(table) == 0 {
		delete(n.senderNonces, record.sender)
	}
}

func quotaFromConfig(q config.Quota) nativecommon.Quota {
	return nativecommon.Quota{
		MaxRequestsPerMin: q.MaxRequestsPerMin,
		MaxNHBPerEpoch:    q.MaxNHBPerEpoch,
		EpochSeconds:      q.EpochSeconds,
	}
}

func quotaEpochForConfig(q nativecommon.Quota, ts time.Time) uint64 {
	seconds := q.EpochSeconds
	if seconds == 0 {
		seconds = 60
	}
	if seconds <= 0 {
		return 0
	}
	unix := ts.Unix()
	if unix < 0 {
		unix = 0
	}
	return uint64(unix) / uint64(seconds)
}

func transactionSize(tx *types.Transaction) (int, error) {
	msg, err := codec.TransactionToProto(tx)
	if err != nil {
		return 0, err
	}
	if msg == nil {
		return 0, fmt.Errorf("transaction proto nil")
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

// SetMempoolUnlimitedOptIn toggles acceptance of an unbounded mempool.
func (n *Node) SetMempoolUnlimitedOptIn(allow bool) {
	if n == nil {
		return
	}
	if allow && !n.devNetwork() {
		allow = false
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
		if len(removed) > 0 {
			for _, tx := range removed {
				if key, err := transactionKey(tx); err == nil {
					if len(n.proposedTxs) > 0 {
						delete(n.proposedTxs, key)
					}
					if n.posArrival != nil {
						delete(n.posArrival, key)
					}
					n.untrackTransactionLocked(key)
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
	if n.state != nil {
		n.state.SetGovernancePolicy(copyPolicy)
	}
}

// SetGlobalConfig records the last validated global configuration to use when
// preflighting governance policy proposals. Callers must ensure the
// configuration has been validated before invoking this method.
func (n *Node) SetGlobalConfig(cfg config.Global) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}

	if strings.EqualFold(strings.TrimSpace(n.networkMode), "prod") &&
		cfg.Loyalty.Dynamic.EnforceProRate && !cfg.Loyalty.Dynamic.EnableProRate {
		return fmt.Errorf("loyalty: pro-rate mode is enforced in production (loyalty.prorate.locked); set global.loyalty.Dynamic.EnforceProRate=false to override in non-production environments")
	}

	n.globalCfgMu.Lock()
	n.globalCfg = cfg
	n.globalCfgMu.Unlock()
	feePolicy, err := buildFeePolicyFromConfig(cfg.Fees)
	if err != nil {
		return err
	}
	n.SetFeePolicy(feePolicy)
	transferPolicy, err := buildTransferGasPolicyFromConfig(cfg.Fees, n.escrowTreasury)
	if err != nil {
		return err
	}
	n.SetTransferGasPolicy(transferPolicy)
	return nil
}

func buildFeePolicyFromConfig(cfg config.Fees) (fees.Policy, error) {
	policy := fees.Policy{
		Version: 1,
		Domains: map[string]fees.DomainPolicy{},
	}

	var ownerWallet [20]byte
	if wallet := strings.TrimSpace(cfg.OwnerWallet); wallet != "" {
		addr, err := crypto.DecodeAddress(wallet)
		if err != nil {
			return fees.Policy{}, fmt.Errorf("fees: invalid owner wallet: %w", err)
		}
		copy(ownerWallet[:], addr.Bytes())
	}

	assets := make(map[string]fees.AssetPolicy, len(cfg.Assets))
	for _, asset := range cfg.Assets {
		name := fees.NormalizeAsset(asset.Asset)
		if name == "" {
			continue
		}
		routeWallet := ownerWallet
		if wallet := strings.TrimSpace(asset.OwnerWallet); wallet != "" {
			addr, err := crypto.DecodeAddress(wallet)
			if err != nil {
				return fees.Policy{}, fmt.Errorf("fees: invalid route wallet for %s: %w", name, err)
			}
			copy(routeWallet[:], addr.Bytes())
		}
		assets[name] = fees.AssetPolicy{
			MDRBasisPoints: asset.MDRBasisPoints,
			OwnerWallet:    routeWallet,
		}
	}

	domainPolicy := fees.DomainPolicy{
		FreeTierTxPerMonth:    cfg.FreeTierTxPerMonth,
		FreeTierTxPerMonthSet: true,
		MDRBasisPoints:        cfg.MDRBasisPoints,
		OwnerWallet:           ownerWallet,
		Assets:                assets,
	}
	for _, domain := range []string{fees.DomainPOS, "p2p", "otc"} {
		policy.Domains[domain] = domainPolicy
	}
	return policy, nil
}

func buildTransferGasPolicyFromConfig(cfg config.Fees, defaultCollector [20]byte) (TransferGasPolicy, error) {
	policy := TransferGasPolicy{
		Enabled:      true,
		Window:       normalizeTransferGasWindow(cfg.TransferFreeTierWindow),
		FeeCollector: defaultCollector,
	}
	if wallet := strings.TrimSpace(cfg.TransferFeeCollector); wallet != "" {
		addr, err := crypto.DecodeAddress(wallet)
		if err != nil {
			return TransferGasPolicy{}, fmt.Errorf("fees: invalid transfer fee collector: %w", err)
		}
		copy(policy.FeeCollector[:], addr.Bytes())
	}
	if limit := strings.TrimSpace(cfg.TransferFreeTierSpendWei); limit != "" {
		amount, err := config.ParseAmount(limit)
		if err != nil {
			return TransferGasPolicy{}, fmt.Errorf("fees: invalid transfer free tier spend limit: %w", err)
		}
		policy.FreeSpendLimitWei = amount
	} else {
		policy.FreeSpendLimitWei = big.NewInt(0)
	}
	if policy.FreeSpendLimitWei == nil {
		policy.FreeSpendLimitWei = big.NewInt(0)
	}
	policy.FeeBps = cfg.TransferFeeBps
	policy.FeeBpsZNHB = cfg.TransferFeeBpsZNHB
	if policy.FreeSpendLimitWei.Sign() <= 0 {
		policy.Enabled = false
	}
	return policy, nil
}

// SyncStakingParams refreshes the runtime staking configuration by loading any
// governance-managed overrides from the parameter store. This bootstrap path
// must remain read-only with respect to canonical chain state; on-chain staking
// reward state is advanced only during normal block execution.
func (n *Node) SyncStakingParams() error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}

	base := n.globalConfigSnapshot().Staking
	n.stateMu.Lock()
	if n.state == nil {
		n.stateMu.Unlock()
		return fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	merged := base

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingAprBps); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingAprBps, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseUint(trimmed, 10, 32)
			if parseErr != nil {
				n.stateMu.Unlock()
				return fmt.Errorf("parse %s: %w", governance.ParamKeyStakingAprBps, parseErr)
			}
			merged.AprBps = uint32(value)
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingPayoutPeriodDays); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingPayoutPeriodDays, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseUint(trimmed, 10, 32)
			if parseErr != nil {
				n.stateMu.Unlock()
				return fmt.Errorf("parse %s: %w", governance.ParamKeyStakingPayoutPeriodDays, parseErr)
			}
			merged.PayoutPeriodDays = uint32(value)
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingUnbondingDays); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingUnbondingDays, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseUint(trimmed, 10, 32)
			if parseErr != nil {
				n.stateMu.Unlock()
				return fmt.Errorf("parse %s: %w", governance.ParamKeyStakingUnbondingDays, parseErr)
			}
			merged.UnbondingDays = uint32(value)
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingMinStakeWei); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingMinStakeWei, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			merged.MinStakeWei = trimmed
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingMaxEmissionPerYearWei); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingMaxEmissionPerYearWei, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			merged.MaxEmissionPerYearWei = trimmed
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingRewardAsset); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingRewardAsset, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			merged.RewardAsset = trimmed
		}
	}

	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingCompoundDefault); err != nil {
		n.stateMu.Unlock()
		return fmt.Errorf("load %s: %w", governance.ParamKeyStakingCompoundDefault, err)
	} else if ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			value, parseErr := strconv.ParseBool(strings.ToLower(trimmed))
			if parseErr != nil {
				n.stateMu.Unlock()
				return fmt.Errorf("parse %s: %w", governance.ParamKeyStakingCompoundDefault, parseErr)
			}
			merged.CompoundDefault = value
		}
	}

	n.state.stakeRewardAPR = uint64(merged.AprBps)
	n.stateMu.Unlock()

	n.globalCfgMu.Lock()
	n.globalCfg.Staking = merged
	n.globalCfgMu.Unlock()

	return nil
}

// ValidateStakingConfig checks that config.toml's [global.Staking].MinStakeWei
// parses as a valid base-10 integer, if the runtime governance param store
// has not already had staking.minimumValidatorStake explicitly set.
//
// IMPORTANT: despite the historical name this replaced (SyncValidatorThresholds),
// this function only validates format -- it never writes MinStakeWei (or
// anything else) into the governance param store. It cannot: a per-node
// config.toml value is not guaranteed identical across validators, so using
// it to seed a consensus-relevant threshold at runtime would risk different
// nodes disagreeing on the actual eligibility floor and diverging on state
// root. The real default (used whenever the param store is empty) is
// governance.DefaultMinimumValidatorStake(); the real way to change it after
// genesis is a passed governance proposal, not a local config edit.
func (n *Node) ValidateStakingConfig() error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	if _, ok, err := manager.ParamStoreGet(governance.ParamKeyMinimumValidatorStake); err != nil {
		return fmt.Errorf("load %s: %w", governance.ParamKeyMinimumValidatorStake, err)
	} else if !ok {
		if cfgMinStake := strings.TrimSpace(n.globalConfigSnapshot().Staking.MinStakeWei); cfgMinStake != "" {
			if _, valid := new(big.Int).SetString(cfgMinStake, 10); !valid {
				return fmt.Errorf("invalid configured minimum validator stake %q", cfgMinStake)
			}
		}
	}
	return nil
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
		Mempool: config.Mempool{MaxBytes: n.globalCfg.Mempool.MaxBytes, POSReservationBPS: n.globalCfg.Mempool.POSReservationBPS},
		Blocks:  config.Blocks{MaxTxs: n.globalCfg.Blocks.MaxTxs},
		Staking: config.Staking{
			AprBps:                n.globalCfg.Staking.AprBps,
			PayoutPeriodDays:      n.globalCfg.Staking.PayoutPeriodDays,
			UnbondingDays:         n.globalCfg.Staking.UnbondingDays,
			MinStakeWei:           n.globalCfg.Staking.MinStakeWei,
			MaxEmissionPerYearWei: n.globalCfg.Staking.MaxEmissionPerYearWei,
			RewardAsset:           n.globalCfg.Staking.RewardAsset,
			CompoundDefault:       n.globalCfg.Staking.CompoundDefault,
		},
		Pauses: config.Pauses{
			Lending:      n.globalCfg.Pauses.Lending,
			Swap:         n.globalCfg.Pauses.Swap,
			Escrow:       n.globalCfg.Pauses.Escrow,
			Trade:        n.globalCfg.Pauses.Trade,
			Loyalty:      n.globalCfg.Pauses.Loyalty,
			POTSO:        n.globalCfg.Pauses.POTSO,
			TransferNHB:  n.globalCfg.Pauses.TransferNHB,
			TransferZNHB: n.globalCfg.Pauses.TransferZNHB,
			Staking:      n.globalCfg.Pauses.Staking,
		},
		Quotas: config.Quotas{
			Lending: n.globalCfg.Quotas.Lending,
			Swap:    n.globalCfg.Quotas.Swap,
			Escrow:  n.globalCfg.Quotas.Escrow,
			Trade:   n.globalCfg.Quotas.Trade,
			Loyalty: n.globalCfg.Quotas.Loyalty,
			POTSO:   n.globalCfg.Quotas.POTSO,
		},
		Fees: config.Fees{
			FreeTierTxPerMonth:       n.globalCfg.Fees.FreeTierTxPerMonth,
			MDRBasisPoints:           n.globalCfg.Fees.MDRBasisPoints,
			OwnerWallet:              n.globalCfg.Fees.OwnerWallet,
			TransferFreeTierSpendWei: n.globalCfg.Fees.TransferFreeTierSpendWei,
			TransferFreeTierWindow:   n.globalCfg.Fees.TransferFreeTierWindow,
			TransferFeeCollector:     n.globalCfg.Fees.TransferFeeCollector,
			TransferFeeBps:           n.globalCfg.Fees.TransferFeeBps,
			TransferFeeBpsZNHB:       n.globalCfg.Fees.TransferFeeBpsZNHB,
			Assets:                   append([]config.FeeAsset{}, n.globalCfg.Fees.Assets...),
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

func (n *Node) SetNetworkBroadcaster(broadcaster p2p.Broadcaster) {
	if n == nil {
		return
	}
	n.networkBroadcaster = broadcaster
}

// SetExternalCommitNotifier installs a callback invoked after this node
// commits a block that arrived via peer sync rather than this node's own
// BFT round. See the field doc on externalCommitNotifier for why this
// exists.
func (n *Node) SetExternalCommitNotifier(notify func()) {
	if n == nil {
		return
	}
	n.externalCommitNotifier = notify
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
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetSwapPayoutAuthorities(normalised.PayoutAuthorities)
		n.state.SetSwapConfig(normalised)
	}
	n.stateMu.Unlock()
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

// PaymasterLimits returns the currently configured sponsorship caps.
func (n *Node) PaymasterLimits() PaymasterLimits {
	if n == nil {
		return PaymasterLimits{}
	}
	n.paymasterMu.RLock()
	defer n.paymasterMu.RUnlock()
	return n.paymasterLimits.Clone()
}

// PaymasterAutoTopUpPolicy returns the configured automatic top-up policy.
func (n *Node) PaymasterAutoTopUpPolicy() PaymasterAutoTopUpPolicy {
	if n == nil {
		return PaymasterAutoTopUpPolicy{}
	}
	n.paymasterMu.RLock()
	defer n.paymasterMu.RUnlock()
	return n.paymasterTopUpPolicy.Clone()
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

// SetPaymasterLimits updates the sponsorship caps enforced for sponsored transactions.
func (n *Node) SetPaymasterLimits(limits PaymasterLimits) {
	if n == nil {
		return
	}
	clone := limits.Clone()
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetPaymasterLimits(clone)
	}
	n.stateMu.Unlock()
	n.paymasterMu.Lock()
	n.paymasterLimits = clone
	n.paymasterMu.Unlock()
}

// SetPaymasterAutoTopUpPolicy installs the automatic top-up policy applied to paymaster accounts.
func (n *Node) SetPaymasterAutoTopUpPolicy(policy PaymasterAutoTopUpPolicy) {
	if n == nil {
		return
	}
	clone := policy.Clone()
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetPaymasterAutoTopUpPolicy(clone)
	}
	n.stateMu.Unlock()
	n.paymasterMu.Lock()
	n.paymasterTopUpPolicy = clone
	n.paymasterMu.Unlock()
}

// PaymasterCounters returns the current sponsorship usage metrics for the provided scopes.
func (n *Node) PaymasterCounters(merchant, device, day string) (*PaymasterCounters, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	snapshot, err := n.state.PaymasterCounters(merchant, device, day)
	if err != nil {
		return nil, err
	}
	return snapshot.Clone(), nil
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
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetLendingRiskParameters(copyParams)
	}
	n.stateMu.Unlock()
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
	stateModel := cloneLendingInterestModel(n.lendingInterestModel)
	n.lendingMu.Unlock()
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetLendingAccrualConfig(reserveBps, protocolFeeBps, stateModel)
	}
	n.stateMu.Unlock()
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
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetLendingDeveloperFee(bps, cloned)
	}
	n.stateMu.Unlock()
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
	n.stateMu.Lock()
	if n.state != nil {
		n.state.SetLendingCollateralRouting(clone)
	}
	n.stateMu.Unlock()
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
	return crypto.MustNewAddress(addr.Prefix(), append([]byte(nil), bytes...))
}

func deriveModuleAddress(seed string, prefix crypto.AddressPrefix) crypto.Address {
	hash := ethcrypto.Keccak256([]byte(seed))
	raw := append([]byte(nil), hash[len(hash)-20:]...)
	return crypto.MustNewAddress(prefix, raw)
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
		// broadcast=false: this transaction arrived FROM a peer -- rebroadcasting
		// it would just echo it straight back with no other peers to reach in
		// today's small topology (and risks a flood loop once the network grows
		// beyond two nodes, absent real multi-hop relay/dedup semantics).
		if err := n.addTransaction(tx, false); err != nil {
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

	case p2p.MsgTypeGetStatus:
		return n.handleNetworkGetStatus()

	case p2p.MsgTypeStatus:
		var status p2p.StatusPayload
		if err := json.Unmarshal(msg.Payload, &status); err != nil {
			return err
		}
		return n.handleNetworkStatus(status)

	case p2p.MsgTypeGetBlocks:
		var payload p2p.GetBlocksPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return err
		}
		return n.handleNetworkGetBlocks(payload)

	case p2p.MsgTypeBlocks:
		var payload p2p.BlocksPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return err
		}
		return n.handleNetworkBlocks(payload.Blocks)

	case p2p.MsgTypeBlock:
		block := new(types.Block)
		if err := json.Unmarshal(msg.Payload, block); err != nil {
			return err
		}
		return n.handleNetworkBlocks([]*types.Block{block})
	}
	return nil
}

const networkBlockSyncBatchSize = 128

func (n *Node) handleNetworkGetStatus() error {
	if n == nil || n.networkBroadcaster == nil {
		return nil
	}
	msg, err := p2p.NewStatusMessage(n.GetHeight())
	if err != nil {
		return err
	}
	return n.networkBroadcaster.Broadcast(msg)
}

func (n *Node) handleNetworkStatus(status p2p.StatusPayload) error {
	if n == nil {
		return nil
	}
	localHeight := n.GetHeight()
	if status.Height <= localHeight {
		return nil
	}
	return n.requestBlockSync(localHeight + 1)
}

func (n *Node) handleNetworkGetBlocks(payload p2p.GetBlocksPayload) error {
	if n == nil || n.chain == nil || n.networkBroadcaster == nil {
		return nil
	}
	from := payload.From
	if from == 0 {
		from = 1
	}
	latest := n.GetHeight()
	if from > latest {
		return nil
	}
	to := from + networkBlockSyncBatchSize - 1
	if to < from || to > latest {
		to = latest
	}
	blocks := make([]*types.Block, 0, to-from+1)
	for height := from; height <= to; height++ {
		block, err := n.chain.GetBlockByHeight(height)
		if err != nil || block == nil {
			break
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return nil
	}
	msg, err := p2p.NewBlocksMessage(blocks)
	if err != nil {
		return err
	}
	return n.networkBroadcaster.Broadcast(msg)
}

func (n *Node) handleNetworkBlocks(blocks []*types.Block) error {
	if n == nil || len(blocks) == 0 {
		return nil
	}
	n.blockSyncMu.Lock()
	defer n.blockSyncMu.Unlock()

	applied := 0
	for _, block := range blocks {
		if block == nil || block.Header == nil {
			continue
		}
		localHeight := n.GetHeight()
		switch {
		case block.Header.Height <= localHeight:
			continue
		case block.Header.Height > localHeight+1:
			return n.requestBlockSync(localHeight + 1)
		}
		if err := n.commitSyncedBlock(block); err != nil {
			return fmt.Errorf("commit synced block %d: %w", block.Header.Height, err)
		}
		applied++
		if n.externalCommitNotifier != nil {
			n.externalCommitNotifier()
		}
	}

	if applied > 0 && len(blocks) >= networkBlockSyncBatchSize {
		return n.requestBlockSync(n.GetHeight() + 1)
	}
	return nil
}

func (n *Node) requestBlockSync(from uint64) error {
	if n == nil || n.networkBroadcaster == nil {
		return nil
	}
	msg, err := p2p.NewGetBlocksMessage(from)
	if err != nil {
		return err
	}
	return n.networkBroadcaster.Broadcast(msg)
}

// HandleMessage satisfies the p2p.MessageHandler interface by forwarding to ProcessNetworkMessage.
func (n *Node) HandleMessage(msg *p2p.Message) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	return n.ProcessNetworkMessage(msg)
}

// AddTransaction enqueues a locally-originated transaction (from an RPC
// call, the validator's own heartbeat loop, a mint voucher submission, etc.)
// into the local mempool and, on success, gossips it to connected peers so a
// validator other than this one can eventually include it in a block. Use
// addTransaction(tx, false) instead for a transaction that arrived FROM a
// peer via ProcessNetworkMessage -- rebroadcasting it would just echo it
// straight back with no other peers to reach in today's small topology.
func (n *Node) AddTransaction(tx *types.Transaction) error {
	return n.addTransaction(tx, true)
}

func (n *Node) addTransaction(tx *types.Transaction, broadcast bool) error {
	if n == nil || tx == nil {
		return fmt.Errorf("add transaction: invalid arguments")
	}
	if err := n.validateTransaction(tx); err != nil {
		return err
	}
	if tx.Type > 0 {
		if tx.MaxBlockHeight > 0 && n.chain != nil && n.chain.GetHeight() > tx.MaxBlockHeight {
			return fmt.Errorf("%w: max block height %d exceeded (current: %d)", ErrInvalidTransaction, tx.MaxBlockHeight, n.chain.GetHeight())
		}
		if tx.IntentExpiry > 0 && uint64(time.Now().Unix()) > tx.IntentExpiry {
			return fmt.Errorf("%w: transaction expired", ErrInvalidTransaction)
		}
	}
	snapshot := n.globalConfigSnapshot()
	txSize, err := transactionSize(tx)
	if err != nil {
		return fmt.Errorf("%w: encode transaction: %w", ErrInvalidTransaction, err)
	}
	var sender []byte
	var senderKey string
	var nonce uint64
	if types.RequiresSignature(tx.Type) {
		sender, err = tx.From()
		if err != nil {
			return fmt.Errorf("%w: recover sender: %w", ErrInvalidTransaction, err)
		}
		senderKey = hex.EncodeToString(sender)
		nonce = tx.Nonce
		account, accountErr := n.GetAccount(sender)
		expectedNonce := uint64(0)
		if accountErr == nil && account != nil {
			expectedNonce = account.Nonce
		}

		if tx.Type > 0 { // Strict sequencing for Native V3 Executions
			if nonce != expectedNonce {
				return fmt.Errorf("%w: strict nonce sequence breached: expected %d, got %d", ErrInvalidTransaction, expectedNonce, nonce)
			}
		} else { // Legacy EVM
			if nonce < expectedNonce {
				return fmt.Errorf("%w: nonce %d has already been used; current account nonce is %d", ErrInvalidTransaction, nonce, expectedNonce)
			}
		}
	}
	now := n.currentTime()
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	n.pruneSenderUsageLocked(now)
	n.pruneSenderNoncesLocked(now)

	if tx.Type > 0 && types.RequiresSignature(tx.Type) {
		// V3 Replace-by-Fee Logic
		for i, existing := range n.mempool {
			if existing != nil && existing.Type > 0 && types.RequiresSignature(existing.Type) && existing.Nonce == tx.Nonce {
				if existingSender, err := existing.From(); err == nil && bytes.Equal(existingSender, sender) {
					var existingFee, newFee *big.Int
					if existing.GasPrice != nil {
						existingFee = new(big.Int).Mul(existing.GasPrice, new(big.Int).SetUint64(existing.GasLimit))
					} else {
						existingFee = big.NewInt(0)
					}
					if tx.GasPrice != nil {
						newFee = new(big.Int).Mul(tx.GasPrice, new(big.Int).SetUint64(tx.GasLimit))
					} else {
						newFee = big.NewInt(0)
					}

					if newFee.Cmp(existingFee) > 0 {
						n.mempool[i] = tx
						if key, keyErr := transactionKey(tx); keyErr == nil {
							n.trackTransactionLocked(key, sender, nonce, now)
						}
						n.gossipTransactionLocked(tx, broadcast)
						return nil // Replaced! Do not append.
					} else {
						return fmt.Errorf("%w: transaction with nonce %d already exists and fee is not higher", ErrInvalidTransaction, nonce)
					}
				}
			}
		}
	}

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

	// Cheap, same-mempool duplicate rejection for TxTypeSwapVoucherMint,
	// mirroring TxTypeMint's invoiceID dedup above. This only catches a
	// duplicate resubmission that is CURRENTLY resident in this validator's
	// own mempool -- it cannot catch the cross-validator race (two
	// validators each independently synthesize a distinct priceProof for
	// the same voucher, producing two different transaction hashes that
	// never sit in the same mempool at the same time). That race is what
	// applySwapVoucherMintTransaction's ledger.Exists/HasSeenSwapNonce
	// checks (a real, network-wide-agreed check once state is synced) and
	// classifyProposalError below actually guard against.
	if tx.Type == types.TxTypeSwapVoucherMint {
		submission, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
		}
		if submission == nil || submission.Voucher == nil {
			return fmt.Errorf("%w: %w", ErrInvalidTransaction, ErrSwapVoucherInvalidPayload)
		}
		providerTxID := strings.TrimSpace(submission.ProviderTxID)
		orderID := strings.TrimSpace(submission.Voucher.OrderID)
		for _, existing := range n.mempool {
			if existing == nil || existing.Type != types.TxTypeSwapVoucherMint {
				continue
			}
			existingSubmission, err := decodeSwapVoucherMintTransaction(existing.Data)
			if err != nil || existingSubmission == nil || existingSubmission.Voucher == nil {
				continue
			}
			if providerTxID != "" && strings.TrimSpace(existingSubmission.ProviderTxID) == providerTxID {
				return ErrSwapDuplicateProviderTx
			}
			if orderID != "" && strings.TrimSpace(existingSubmission.Voucher.OrderID) == orderID {
				return ErrSwapNonceUsed
			}
		}
	}

	if limit := n.mempoolLimit; limit > 0 && len(n.mempool) >= limit {
		return ErrMempoolFull
	}
	var currentBytes int64
	var senderCount int
	var senderBytes int64
	maxBytes := snapshot.Mempool.MaxBytes
	if maxBytes > 0 || senderKey != "" {
		for _, existing := range n.mempool {
			if existing == nil {
				continue
			}
			sz, sizeErr := transactionSize(existing)
			if sizeErr != nil {
				continue
			}
			if maxBytes > 0 {
				currentBytes += int64(sz)
			}
			if senderKey != "" && types.RequiresSignature(existing.Type) {
				existingFrom, fromErr := existing.From()
				if fromErr == nil && hex.EncodeToString(existingFrom) == senderKey {
					senderCount++
					senderBytes += int64(sz)
				}
			}
		}
	}
	if maxBytes > 0 && currentBytes+int64(txSize) > maxBytes {
		return ErrMempoolByteLimit
	}
	if senderKey != "" {
		if senderCount >= mempoolMaxSenderTx || senderBytes+int64(txSize) > mempoolMaxSenderBytes {
			return ErrMempoolSenderLimit
		}
	}
	if senderKey != "" {
		if err := n.registerSenderNonceLocked(senderKey, nonce, now); err != nil {
			return err
		}
		if err := n.applySenderQuotaLocked(senderKey, quotaFromConfig(snapshot.Quotas.Trade), now); err != nil {
			return fmt.Errorf("%w: %w", ErrMempoolQuotaExceeded, err)
		}
	}
	if mempool.IsPOSLaneEligible(tx) {
		if key, err := transactionKey(tx); err == nil {
			if n.posArrival == nil {
				n.posArrival = make(map[string]time.Time)
			}
			if _, exists := n.posArrival[key]; !exists {
				n.posArrival[key] = n.currentTime()
				if metrics := observability.Mempool(); metrics != nil {
					metrics.RecordPOSEnqueued()
				}
			}
		}
	}
	key, keyErr := transactionKey(tx)
	if keyErr != nil {
		return fmt.Errorf("%w: derive transaction key: %w", ErrInvalidTransaction, keyErr)
	}
	n.mempool = append(n.mempool, tx)
	n.trackTransactionLocked(key, sender, nonce, now)
	n.gossipTransactionLocked(tx, broadcast)
	if mempool.IsPOSLaneEligible(tx) {
		if hash, err := tx.Hash(); err == nil {
			n.publishPOSFinality(POSFinalityUpdate{
				IntentRef: append([]byte(nil), tx.IntentRef...),
				TxHash:    append([]byte(nil), hash...),
				Status:    POSFinalityStatusPending,
				Timestamp: n.currentTime().Unix(),
			})
		}
	}
	return nil
}

// gossipTransactionLocked broadcasts tx to connected peers so a validator
// other than this one can include it in a block -- without this, a
// transaction submitted to any validator that isn't currently the block
// proposer can never be mined, since building a block only ever draws from
// the proposer's own local mempool. Called with n.mempoolMu already held
// (matching addTransaction's callers), which is safe: Broadcast only takes a
// brief peer-list read-lock and enqueues onto each peer's bounded send
// channel, it doesn't block on network I/O.
func (n *Node) gossipTransactionLocked(tx *types.Transaction, broadcast bool) {
	if !broadcast || n == nil || n.networkBroadcaster == nil {
		return
	}
	msg, err := p2p.NewTxMessage(tx)
	if err != nil {
		slog.Warn("Failed to encode transaction for gossip", slog.Any("error", err))
		return
	}
	if err := n.networkBroadcaster.Broadcast(msg); err != nil {
		slog.Warn("Failed to gossip transaction to peers", slog.Any("error", err))
	}
}

func (n *Node) validateTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("add transaction: nil transaction")
	}
	if err := tx.ValidateBasic(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}
	if tx.ChainID == nil {
		return fmt.Errorf("%w: missing chain id", ErrInvalidTransaction)
	}
	if !types.IsValidChainID(tx.ChainID) {
		return fmt.Errorf("%w: unexpected chain id %s", ErrInvalidTransaction, tx.ChainID.String())
	}
	if types.RequiresSignature(tx.Type) {
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
		blockHeight = n.chain.GetHeight() + 1
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

// SubmitTxEnvelope enqueues a transaction derived from the provided signed envelope.
func (n *Node) SubmitTxEnvelope(envelope *consensusv1.SignedTxEnvelope) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	tx, err := codec.TransactionFromEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("submit envelope: %w", err)
	}
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

	var (
		lanes   mempool.Lanes
		ordered []*types.Transaction
	)
	if len(n.mempool) > 0 {
		now := n.currentTime().Unix()
		original := n.mempool
		filtered := original[:0]
		lanes = mempool.Lanes{POS: make([]*types.Transaction, 0, len(original)), Normal: make([]*types.Transaction, 0, len(original))}
		for _, tx := range original {
			if tx == nil {
				continue
			}
			if tx.Type == types.TxTypeMint {
				voucher, _, err := decodeMintTransaction(tx.Data)
				if err != nil || voucher == nil || voucher.Expiry <= now {
					if key, keyErr := transactionKey(tx); keyErr == nil {
						delete(n.proposedTxs, key)
						if n.posArrival != nil {
							delete(n.posArrival, key)
						}
					}
					continue
				}
			}
			if tx.Type == types.TxTypeSwapVoucherMint {
				submission, err := decodeSwapVoucherMintTransaction(tx.Data)
				if err != nil || submission == nil || submission.Voucher == nil || submission.Voucher.Expiry <= now {
					if key, keyErr := transactionKey(tx); keyErr == nil {
						delete(n.proposedTxs, key)
						if n.posArrival != nil {
							delete(n.posArrival, key)
						}
					}
					continue
				}
			}
			filtered = append(filtered, tx)
			if mempool.IsPOSLaneEligible(tx) {
				lanes.POS = append(lanes.POS, tx)
			} else {
				lanes.Normal = append(lanes.Normal, tx)
			}
		}
		for i := len(filtered); i < len(original); i++ {
			original[i] = nil
		}
		n.mempool = filtered
	}

	if len(n.mempool) == 0 {
		if metrics := observability.Mempool(); metrics != nil {
			metrics.RecordPOSLaneFill(mempool.Usage{})
		}
		return nil
	}

	snapshot := n.globalConfigSnapshot()
	maxTxs := snapshot.Blocks.MaxTxs
	if maxTxs <= 0 || maxTxs > int64(math.MaxInt) {
		maxTxs = int64(len(n.mempool))
	}
	planner := consensus.POSQuota{ReservationBPS: snapshot.Mempool.POSReservationBPS}
	ordered, usage := mempool.Schedule(lanes, int(maxTxs), planner)
	if metrics := observability.Mempool(); metrics != nil {
		metrics.RecordPOSLaneFill(usage)
	}

	txs := make([]*types.Transaction, 0, len(ordered))
	for _, tx := range ordered {
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

// MempoolSize returns the number of transactions currently retained in the
// mempool without mutating proposal bookkeeping.
func (n *Node) MempoolSize() int {
	if n == nil {
		return 0
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	return len(n.mempool)
}

// HasPendingTransactionHash reports whether the current mempool contains a
// transaction matching the provided canonical or 0x-prefixed hash.
func (n *Node) HasPendingTransactionHash(hash string) bool {
	if n == nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(hash), "0x"))
	if normalized == "" {
		return false
	}

	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()

	for _, tx := range n.mempool {
		if tx == nil {
			continue
		}
		txHash, err := tx.Hash()
		if err != nil {
			continue
		}
		if strings.EqualFold(hex.EncodeToString(txHash), normalized) {
			return true
		}
	}

	return false
}

func transactionKey(tx *types.Transaction) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("nil transaction")
	}
	hash, err := tx.Hash()
	if err != nil {
		return "", err
	}
	// Senderless/envelope-unsigned transaction types (TxTypeMint,
	// TxTypeSwapVoucherMint) have no recoverable envelope signature -- keying
	// solely on hash matches RequiresSignature's classification instead of
	// hardcoding each type here, so any future senderless type is covered
	// automatically instead of silently calling tx.From() on a transaction
	// that was never signed.
	if !types.RequiresSignature(tx.Type) {
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

// RequeueTransactions releases proposal bookkeeping for transactions that were
// selected into a round but not finalized, allowing them to be proposed again
// in a later round.
func (n *Node) RequeueTransactions(txs []*types.Transaction) {
	n.requeueTransactions(txs)
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
		n.untrackTransactionLocked(key)
		if mempool.IsPOSLaneEligible(tx) && n.posArrival != nil {
			if enqueuedAt, ok := n.posArrival[key]; ok {
				latency := n.currentTime().Sub(enqueuedAt)
				if metrics := observability.Mempool(); metrics != nil {
					metrics.ObservePOSFinality(latency)
				}
				delete(n.posArrival, key)
			}
		}
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
			n.untrackTransactionLocked(key)
			continue
		}
		filtered = append(filtered, tx)
	}
	for i := len(filtered); i < len(n.mempool); i++ {
		n.mempool[i] = nil
	}
	n.mempool = filtered
}

func (n *Node) dropTransactionsFromMempool(txs []*types.Transaction) {
	if n == nil || len(txs) == 0 {
		return
	}
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()

	dropped := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		key, err := transactionKey(tx)
		if err != nil {
			continue
		}
		dropped[key] = struct{}{}
		delete(n.proposedTxs, key)
		if n.posArrival != nil {
			delete(n.posArrival, key)
		}
		n.untrackTransactionLocked(key)
	}
	if len(dropped) == 0 || len(n.mempool) == 0 {
		return
	}

	filtered := n.mempool[:0]
	for _, tx := range n.mempool {
		key, err := transactionKey(tx)
		if err != nil {
			filtered = append(filtered, tx)
			continue
		}
		if _, ok := dropped[key]; ok {
			continue
		}
		filtered = append(filtered, tx)
	}
	for i := len(filtered); i < len(n.mempool); i++ {
		n.mempool[i] = nil
	}
	n.mempool = filtered
}

// proposalTxDisposition is classifyProposalError's verdict for a transaction
// that failed stateCopy.ApplyTransaction while CreateBlock's buildProposalState
// is speculatively assembling a block.
type proposalTxDisposition int

const (
	// proposalDispositionAbort is the zero value and the safe default for
	// any error classifyProposalError does not explicitly recognize: the
	// whole in-progress proposal attempt fails and CreateBlock returns the
	// error to its caller. This is deliberately what "unclassified" means
	// -- adding a new disposition always requires a positive, reviewed
	// decision (see the ABORT-is-correct discussion below); silence must
	// never be read as "safe to skip or prune".
	proposalDispositionAbort proposalTxDisposition = iota
	// proposalDispositionPrune means the transaction is permanently
	// unexecutable -- a pure function of its own immutable payload, or of
	// monotonic state that can never revert once true -- so it is dropped
	// from the mempool immediately (n.dropTransactionsFromMempool) instead
	// of being left resident to fail identically every subsequent round.
	proposalDispositionPrune
	// proposalDispositionSkip means the transaction failed for a reason
	// that depends on mutable state shared with other transactions in the
	// SAME proposal attempt (a rolling cap, a governance pause, a signer
	// registry) or on ordering within this attempt -- a later attempt
	// (next round, a different candidate set, state that has since
	// changed) can genuinely succeed. It is excluded from THIS attempt's
	// block but its mempool "in-flight" mark is released
	// (n.requeueTransactions) so it is reconsidered next round; it is
	// never removed from n.mempool.
	proposalDispositionSkip
)

// classifyProposalError reports the disposition for a transaction that
// failed during proposal building (stateCopy.ApplyTransaction in
// CreateBlock's buildProposalState). Getting this wrong in either direction
// is dangerous: classifying a transiently-failing error as PRUNE silently
// and permanently destroys a transaction that would have succeeded later;
// leaving a routinely-occurring error unclassified (ABORT) lets it block
// EVERY OTHER pending transaction, for EVERY subsequent round, until the
// offending transaction's own expiry -- a validator-wide liveness stall, not
// just a loss for one submitter. This function (and the SKIP disposition
// specifically) exists because that second failure mode was found to be far
// more common, and far more severe, than originally assumed -- see the
// module-pause discussion below.
//
// This matters most for TxTypeSwapVoucherMint: when the same fiat voucher
// reaches two validators nearly simultaneously (each independently
// synthesizes its own price proof when the caller omits one, producing
// distinct transaction hashes local mempool dedup can never catch across
// nodes), only one can ever commit -- correct, that's the whole point of
// routing this through consensus. But the LOSING validator's copy of the
// voucher transaction must be prunable, or CreateBlock aborts the entire
// proposal (not just this one transaction) every round it tries to include
// the now-permanently-invalid duplicate, blocking that validator from
// proposing ANY block, for ANY transaction, until the voucher's own expiry.
// On a small validator set that can stall block production network-wide.
//
// == PRUNE: pure function of the transaction's own immutable payload, or of
// monotonic state that can never revert once true ==
//
// Decode failure, domain/chainId/token mismatch, unrecoverable signature, a
// provider transaction ID or order nonce already consumed, a voucher past
// its fixed expiry: re-proposing the exact same transaction bytes can never
// succeed later either.
//
//   - ErrSwapPriceProofRequired fires only when the transaction's OWN
//     embedded price proof is absent, or its signature is absent, and this
//     transaction type unconditionally requires both (see RequireSignature
//     (true) in applySwapVoucherMintTransaction -- hardcoded, not read from
//     mutable operator config). A resubmission of the identical transaction
//     bytes can never later carry a price proof or signature it does not
//     already contain, so this can never succeed later either.
//   - ErrSwapPriceProofStale fires when the block timestamp has advanced
//     more than swap.Config.MaxQuoteAgeSeconds past the price proof's own
//     fixed signing timestamp. Block time (n.currentTime(), read fresh on
//     every CreateBlock attempt) only moves forward, so once a specific
//     proof is stale relative to it, it can only ever become MORE stale on
//     every later retry -- exactly ErrSwapExpired's reasoning, just applied
//     to the quote's freshness window instead of the voucher's own expiry.
//     Round 1/2's reported liveness bug (an entirely ordinary voucher that
//     sits in the mempool past the default 120s quote window permanently
//     wedges the proposing validator) lives here. Note: native/swap/engine.
//     go's Verify also raises this same sentinel for a price-proof
//     timestamp implausibly far in the FUTURE (>30s ahead of block time,
//     e.g. signer/validator clock skew); that sub-case is not strictly
//     monotonic (it can self-resolve once block time catches up), but it
//     requires a fixed operational clock-skew anomaly to trigger at all, and
//     pruning it only costs the submitter a fresh resubmission with a
//     current price proof -- not a validator-wide liveness stall, which is
//     the far more severe failure mode this whole function exists to avoid.
//   - ErrNonceTooLow (tx.Nonce < account.Nonce): account.Nonce only ever
//     increases, so a stale/replayed nonce can never become valid again.
//   - ErrHeartbeatTooSoon covers applyHeartbeat's rate-limit and replay
//     rejections. Those are timing-ordering problems specific to a single
//     heartbeat transaction (see ErrHeartbeatTooSoon's doc comment in
//     state_transition.go) -- a validator's own liveness ping should never
//     be able to abort an entire block proposal for everyone else's
//     transactions just because it arrived slightly too soon. Pruning it
//     here is a defense-in-depth backstop: normally a well-behaved
//     submitter (see EngagementValidatorHeartbeatDue and
//     pendingHeartbeatFee) never produces a heartbeat transaction that
//     reaches this check already doomed, and the admission-time simulation
//     in validateTransaction filters most bad ones out before they ever
//     enter the mempool -- but that simulation can be disabled
//     (SetTransactionSimulationEnabled), so CreateBlock must not assume it
//     always ran. Both of ErrHeartbeatTooSoon's sub-cases compare a fixed
//     payload.Timestamp against a monotonically-increasing on-chain
//     EngagementLastHeartbeat, so -- unlike the nonce split above -- no
//     further split is needed here; both sub-cases are equally permanent.
//   - ErrUnknownTransactionType: within one running binary the set of
//     recognized TxType values is a compiled constant, so retrying the
//     identical bytes can never succeed for the lifetime of this process.
//     This only drops the transaction from THIS validator's local mempool;
//     a peer running newer software that recognizes the type keeps its own
//     copy and can still gossip/re-propagate it, and if this validator
//     later upgrades it re-learns the transaction via gossip like any
//     other.
//   - The TxTypeMint analogues of the swap payload/monotonic-state PRUNE
//     errors: ErrMintInvoiceUsed (monotonic dedup, same pattern as
//     ErrSwapDuplicateProviderTx/ErrSwapNonceUsed), ErrMintInvalidChainID,
//     ErrMintExpired, and ErrMintInvalidPayload (all pure functions of the
//     voucher's own immutable payload, same pattern as their swap
//     equivalents).
//
// == SKIP: depends on mutable state shared across transactions in this
// attempt, or on ordering within this attempt -- a later attempt can
// genuinely succeed ==
//
//   - ErrSwapDailyCapExceeded / ErrSwapMonthlyCapExceeded: per-recipient
//     rolling totals that reset at day/month boundaries. Two vouchers to
//     the same recipient in one proposal attempt can trip the cap for the
//     second even though each independently passed admission-time
//     simulation; a later attempt (different candidate set, or after the
//     window rolls over) can succeed.
//   - ErrSwapVelocityExceeded: a rolling count within
//     RiskParameters.VelocityWindowSeconds -- strictly order/timing
//     dependent within the same block-building pass.
//   - ErrSwapPriceProofDeviation: compares the proof's rate to a
//     live-updating reference; a later attempt (rate moved back in range,
//     or a fresher proof arrives) can succeed.
//   - ErrSwapSlippageExceeded: compares the computed mint amount to
//     voucher.Amount using the CURRENT price proof's rate -- depends on
//     which price proof made it through, itself order/timing dependent.
//   - ErrSwapInvalidSigner, ErrSwapPriceProofSignerUnknown,
//     ErrSwapMintPaused, ErrSwapUnsupportedFiat, ErrSwapProviderNotAllowed,
//     ErrSwapAmountBelowMinimum, ErrSwapAmountAboveMaximum, ErrSwapSanctioned:
//     all depend on mutable operator config or registries that genuinely
//     can change (a signer gets registered, a pause lifts, an allow-list is
//     edited, a sanctions entry is delisted) -- a resubmission-free later
//     attempt of the exact same transaction bytes can succeed once the
//     config changes. ErrSwapSanctioned specifically: harmlessly re-skipping
//     a sanctioned voucher every round is strictly better than letting it
//     freeze every OTHER user's transactions; it is bounded by the same
//     worst-case argument as every other SKIP entry (see "Termination"
//     below). Note ErrSwapPriceProofInvalid is deliberately NOT included
//     here -- see the ABORT section.
//   - nativecommon.ErrModulePaused (returned directly by
//     applySwapVoucherMintTransaction's nativecommon.Guard(sp.pauses,
//     moduleSwap) call), ErrStakePaused (the wrapped form every moduleStaking
//     Guard call site in state_transition.go returns), ErrTransferNHBPaused,
//     ErrTransferZNHBPaused: pausing a module is a normal, designed
//     governance/admin action (maintenance, incident response), not a rare
//     edge case, and un-pausing is equally normal and expected soon after.
//     TxTypeTransfer (ordinary NHB transfers) is almost certainly the
//     single most common transaction type on this chain. Before this
//     disposition existed, none of these were classified at all (fell to
//     ABORT by default): if governance paused NHB transfers (or ZNHB
//     transfers, staking, or swap) while even one matching transaction sat
//     in a validator's mempool, that validator could not propose ANY block
//     for ANY sender until the pause lifted or that one transaction was
//     otherwise evicted -- a strictly worse, strictly more likely-to-trigger
//     version of the swap-cap liveness bug this whole mechanism exists to
//     fix.
//   - nativecommon.ErrQuotaRequestsExceeded, ErrQuotaNHBCapExceeded,
//     ErrQuotaCounterOverflow (reached via applyQuota, which wraps with
//     fmt.Errorf("quota: %s: %w", ...) -- errors.Is still matches through
//     the %w): per-sender, per-epoch rolling counters that reset over time,
//     structurally identical to the swap daily/monthly caps. Reached by
//     escrow, trade, staking (via POTSO's quota, not the staking pause),
//     and every TxTypeLending* transaction type.
//   - ErrMintPaused, ErrMintInvalidSigner, ErrMintEmissionCapExceeded: the
//     TxTypeMint analogues of the module-pause and signer-registry SKIP
//     entries above, plus a per-year emission cap that resets yearly and is
//     directly order-dependent (two mint vouchers processed in the same
//     proposal attempt can contend for remaining headroom) -- the identical
//     failure mode described for swap caps, just for the plain NHB/ZNHB
//     mint path instead.
//   - ErrNonceTooHigh (tx.Nonce > account.Nonce): a lower-nonce transaction
//     from the same sender hasn't landed yet. Today addTransaction enforces
//     strict admission-time nonce sequencing for every dispatchable tx type
//     (nonce != expectedNonce is rejected at admission), so this is
//     believed unreachable in practice via the current mempool -- but
//     classifying it SKIP rather than relying on that invariant is cheap
//     defense-in-depth against a future admission-time relaxation (e.g.
//     nonce-queuing for UX) or a scheduler reordering bug.
//
// == ABORT: deliberately still unclassified ==
//
//   - ErrSwapPriceProofInvalid: its signature-mismatch case is checked
//     against the SAME mutable SwapPriceSigner registry as
//     ErrSwapPriceProofSignerUnknown (recovered-pubkey-vs-currently-
//     registered-signer), so a correction to a stale/incorrect signer
//     registration could make a later resubmission succeed -- SKIP-shaped
//     reasoning. But it ALSO covers pure-payload causes (domain/pair
//     mismatch, a non-positive rate) that are PRUNE-shaped, and Go's
//     errors.Is cannot distinguish which branch fired. The conservative,
//     correct choice for an ambiguous sentinel is to leave it unclassified
//     (ABORT) rather than risk either silently, permanently dropping a
//     transaction that would have succeeded (wrong PRUNE), or endlessly
//     retrying real corruption forever masked as "try again later" (wrong
//     SKIP). This is intentionally NOT the same risk profile as a routine
//     operational event like a pause or a cap -- it requires a genuinely
//     malformed or adversarial payload to trigger, so ABORT's liveness cost
//     is bounded to that rare case rather than to ordinary usage.
//   - Two node-infrastructure failure classes that run in buildProposalState
//     before or after the per-tx loop, not inside it, and are therefore
//     never seen by this function at all: n.refreshModulePauses() /
//     n.state.Copy() failures (a precondition for building ANY block,
//     including an empty one -- no transaction-exclusion strategy can fix
//     an inability to read config or snapshot state), and
//     stateCopy.ProcessBlockLifecycle / n.processPendingEvidenceForState
//     failures (whole-block epoch rollover and slashing-evidence
//     processing, not attributable to any single pending transaction).
//     Both are surfaced as CreateBlock errors so the round fails visibly
//     and another validator's block production can cover it.
//
// == Termination ==
//
// The per-tx loop in buildProposalState never stops scanning on a PRUNE or
// SKIP verdict (it `continue`s), so every candidate is attempted exactly
// once per retry. CreateBlock's outer loop retries only when the candidate
// set strictly shrank (at least one PRUNE or SKIP hit), so it terminates in
// at most len(original txs)+1 iterations -- the last one either succeeding
// (trivially, on an empty set if every transaction was excluded) or
// hard-erroring via an ABORT-classified error. Worst case, EVERY mempool
// transaction is SKIP-classified (e.g. a burst that trips a shared cap, or
// a module pause hitting a fully-transfer-heavy mempool): the candidate set
// shrinks to empty within that same bound, computeDependencyGraph(nil)
// succeeds trivially, and buildProposalState returns a valid, successful,
// EMPTY *StateProcessor -- CreateBlock returns an empty block, not an
// error, not a hang. The validator stays live and proposes an empty block
// instead of dropping out of the round entirely. See
// TestCreateBlockAllSkippableTransactionsProducesEmptyBlockNotHang for a
// test that exercises exactly this worst case with a large synthetic
// mempool.
func classifyProposalError(err error) proposalTxDisposition {
	// *swap.RedeemRiskViolation is a struct type, not a sentinel value, so it
	// needs errors.As rather than errors.Is -- same disposition as the
	// mint-side cap-violation sentinels below (a redeem risk cap can free up
	// on a later attempt, e.g. the next day, or after other same-block
	// transactions apply, so this is skippable, not prunable).
	var redeemViolation *swap.RedeemRiskViolation
	if errors.As(err, &redeemViolation) {
		return proposalDispositionSkip
	}
	switch {
	case errors.Is(err, ErrNonceTooLow),
		errors.Is(err, ErrHeartbeatTooSoon),
		errors.Is(err, ErrUnknownTransactionType),
		errors.Is(err, ErrSwapDuplicateProviderTx),
		errors.Is(err, ErrSwapNonceUsed),
		errors.Is(err, ErrSwapExpired),
		errors.Is(err, ErrSwapVoucherInvalidPayload),
		errors.Is(err, ErrSwapInvalidDomain),
		errors.Is(err, ErrSwapInvalidChainID),
		errors.Is(err, ErrSwapInvalidToken),
		errors.Is(err, ErrSwapInvalidSignature),
		errors.Is(err, ErrSwapPriceProofRequired),
		errors.Is(err, ErrSwapPriceProofStale),
		errors.Is(err, ErrMintInvoiceUsed),
		errors.Is(err, ErrMintInvalidChainID),
		errors.Is(err, ErrMintExpired),
		errors.Is(err, ErrMintInvalidPayload),
		errors.Is(err, ErrRedeemRequestExists),
		errors.Is(err, nhbstate.ErrRedeemRequestNotPending),
		errors.Is(err, ErrRedeemInvalidPayload):
		return proposalDispositionPrune
	case errors.Is(err, ErrNonceTooHigh),
		errors.Is(err, ErrSwapDailyCapExceeded),
		errors.Is(err, ErrSwapMonthlyCapExceeded),
		errors.Is(err, ErrSwapVelocityExceeded),
		errors.Is(err, ErrSwapPriceProofDeviation),
		errors.Is(err, ErrSwapSlippageExceeded),
		errors.Is(err, ErrSwapInvalidSigner),
		errors.Is(err, ErrSwapPriceProofSignerUnknown),
		errors.Is(err, ErrSwapMintPaused),
		errors.Is(err, ErrSwapUnsupportedFiat),
		errors.Is(err, ErrSwapProviderNotAllowed),
		errors.Is(err, ErrSwapAmountBelowMinimum),
		errors.Is(err, ErrSwapAmountAboveMaximum),
		errors.Is(err, ErrSwapSanctioned),
		errors.Is(err, nativecommon.ErrModulePaused),
		errors.Is(err, ErrStakePaused),
		errors.Is(err, ErrTransferNHBPaused),
		errors.Is(err, ErrTransferZNHBPaused),
		errors.Is(err, nativecommon.ErrQuotaRequestsExceeded),
		errors.Is(err, nativecommon.ErrQuotaNHBCapExceeded),
		errors.Is(err, nativecommon.ErrQuotaCounterOverflow),
		errors.Is(err, ErrMintPaused),
		errors.Is(err, ErrMintInvalidSigner),
		errors.Is(err, ErrMintEmissionCapExceeded),
		errors.Is(err, ErrMintRecipientUnresolved),
		errors.Is(err, ErrRedeemInsufficientBalance),
		errors.Is(err, ErrRedeemUnauthorizedAttestor),
		// A lending health/MaxLTV outcome can now depend on a same-sender
		// fixed-term borrow/repay applied earlier in the SAME proposal
		// attempt (see native/lending's combinedDebtWei) -- a later attempt
		// (different ordering, or after other same-attempt transactions
		// apply) can genuinely change the outcome, so this is skippable,
		// not prunable, same reasoning as the swap caps above.
		errors.Is(err, lending.ErrHealthCheckFailed),
		errors.Is(err, lending.ErrMaxLTVExceeded),
		// A Withdraw's same-block-as-supply guard (see
		// native/lending.ErrWithdrawSameBlockAsSupply's doc comment) depends
		// on whether a same-sender Supply already applied earlier in this
		// SAME proposal attempt -- a later attempt can genuinely change the
		// outcome, same reasoning as the two lending errors above.
		errors.Is(err, lending.ErrWithdrawSameBlockAsSupply):
		return proposalDispositionSkip
	}
	return proposalDispositionAbort
}

func (n *Node) CreateBlock(txs []*types.Transaction) (block *types.Block, err error) {
	proposedTxs := append([]*types.Transaction(nil), txs...)
	var prunedTxs []*types.Transaction
	// skippedTxs accumulates every transaction excluded from this attempt via
	// proposalDispositionSkip across every buildProposalState retry within
	// this single CreateBlock call. Unlike prunedTxs it is never a mempool
	// structure and never persisted -- it exists only for the duration of
	// this call, exactly like prunedTxs already does.
	var skippedTxs []*types.Transaction
	defer func() {
		// Unconditional release, regardless of whether CreateBlock ultimately
		// succeeds or fails: buildProposalState already calls
		// n.requeueTransactions(attemptSkipped) immediately upon detecting a
		// SKIP disposition (see below), releasing the mempool "in-flight"
		// mark as early as correctness allows. This second call is
		// deliberately redundant -- requeueTransactions only ever does
		// delete(n.proposedTxs, key), so deleting an already-deleted key is a
		// safe no-op -- and exists as a structural guarantee: on the SUCCESS
		// path (err == nil) the rest of this defer never runs (see the next
		// check), so without this unconditional release here, a
		// successfully-skipped transaction's "in-flight" mark would never be
		// cleared, permanently hiding it from every future GetMempool() call
		// even though it is still physically resident in n.mempool -- a
		// silent, latent "phantom prune" bug distinct from, but just as bad
		// as, calling dropTransactionsFromMempool on it.
		if len(skippedTxs) > 0 {
			n.requeueTransactions(skippedTxs)
		}
		if err == nil || len(proposedTxs) == 0 {
			return
		}
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
				continue
			}
			if _, skip := dropped[key]; skip {
				continue
			}
			requeue = append(requeue, tx)
		}
		n.requeueTransactions(requeue)
	}()

	blockTime := n.currentTime()
	timestamp := blockTime.Unix()

	if len(txs) > 0 {
		filtered := make([]*types.Transaction, 0, len(txs))
		for _, tx := range txs {
			if tx == nil {
				continue
			}
			if tx.Type == types.TxTypeMint {
				voucher, _, err := decodeMintTransaction(tx.Data)
				if err != nil || voucher == nil || voucher.Expiry <= timestamp {
					prunedTxs = append(prunedTxs, tx)
					continue
				}
			}
			if tx.Type == types.TxTypeSwapVoucherMint {
				submission, err := decodeSwapVoucherMintTransaction(tx.Data)
				if err != nil || submission == nil || submission.Voucher == nil || submission.Voucher.Expiry <= timestamp {
					prunedTxs = append(prunedTxs, tx)
					continue
				}
			}
			filtered = append(filtered, tx)
		}
		txs = filtered
	}

	if len(prunedTxs) > 0 {
		n.markTransactionsCommitted(prunedTxs)
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

	height := n.chain.GetHeight() + 1
	prevHash := n.chain.Tip()
	validator := n.validatorKey.PubKey().Address().Bytes()

	buildProposalState := func(candidateTxs []*types.Transaction) (*StateProcessor, []*types.Transaction, []byte, error) {
		orderedTxs, executionGraphRoot, err := computeDependencyGraph(candidateTxs)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("canonical scheduler failed: %w", err)
		}

		n.stateMu.Lock()
		if err := n.refreshModulePauses(); err != nil {
			n.stateMu.Unlock()
			return nil, nil, nil, err
		}
		stateCopy, err := n.state.Copy()
		n.stateMu.Unlock()
		if err != nil {
			return nil, nil, nil, err
		}
		stateCopy.SetPauseView(n)
		stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())
		blockTime = time.Unix(timestamp, 0).UTC()
		stateCopy.BeginBlock(height, blockTime)

		// Must run before this block's own transactions, not just before
		// ProcessBlockLifecycle further below -- ProcessBlockLifecycle only
		// runs AFTER the tx-application loop, so relying on it alone leaves
		// a window where a TxTypeRedeemNHB burn in this very block executes
		// before the genesis supply is seeded and underflows (the exact
		// 2026-08-24 incident this seed exists to fix, now reproducible as a
		// full block-production abort instead of one rejected tx). Idempotent
		// and cheap after its first real run, so calling it here in addition
		// to its existing call inside ProcessBlockLifecycle is safe.
		if err := stateCopy.SeedGenesisNHBSupplyOnce(); err != nil {
			stateCopy.EndBlock()
			return nil, nil, nil, fmt.Errorf("seed genesis NHB supply: %w", err)
		}

		keptTxs := make([]*types.Transaction, 0, len(orderedTxs))
		attemptPruned := make([]*types.Transaction, 0)
		// attemptSkipped collects this attempt's SKIP-classified failures --
		// see classifyProposalError's proposalDispositionSkip doc for why
		// these must NOT be treated like attemptPruned (mempool-removed) or
		// silently left in-flight (never reconsidered again).
		attemptSkipped := make([]*types.Transaction, 0)
		for _, tx := range orderedTxs {
			if err := stateCopy.ApplyTransaction(tx); err != nil {
				switch classifyProposalError(err) {
				case proposalDispositionPrune:
					attemptPruned = append(attemptPruned, tx)
					continue
				case proposalDispositionSkip:
					attemptSkipped = append(attemptSkipped, tx)
					continue
				default: // proposalDispositionAbort
					stateCopy.EndBlock()
					return nil, nil, nil, err
				}
			}
			keptTxs = append(keptTxs, tx)
		}
		if len(attemptPruned) > 0 || len(attemptSkipped) > 0 {
			prunedTxs = append(prunedTxs, attemptPruned...)
			skippedTxs = append(skippedTxs, attemptSkipped...)
			// PRUNE txs are permanently removed from the real mempool.
			n.dropTransactionsFromMempool(attemptPruned)
			// SKIP txs are released from "in-flight" bookkeeping immediately
			// (as early as correctness allows, matching
			// dropTransactionsFromMempool's timing above) so the next
			// GetMempool() call can offer them again -- but n.mempool itself
			// is deliberately left untouched: requeueTransactions only ever
			// deletes from n.proposedTxs, never from n.mempool. See
			// CreateBlock's top-level defer for the unconditional,
			// idempotent backstop release of the same set.
			n.requeueTransactions(attemptSkipped)
			stateCopy.EndBlock()
			return nil, keptTxs, nil, nil
		}
		if err := stateCopy.ProcessBlockLifecycle(height, timestamp); err != nil {
			stateCopy.EndBlock()
			return nil, nil, nil, err
		}
		if err := n.processPendingEvidenceForState(stateCopy, height); err != nil {
			stateCopy.EndBlock()
			return nil, nil, nil, err
		}
		stateCopy.FinalizeBlock()
		return stateCopy, keptTxs, executionGraphRoot, nil
	}

	var (
		stateCopy          *StateProcessor
		executionGraphRoot []byte
	)
	for {
		stateCopy, txs, executionGraphRoot, err = buildProposalState(txs)
		if err != nil {
			return nil, err
		}
		if stateCopy != nil {
			break
		}
	}
	defer stateCopy.EndBlock()

	header := &types.BlockHeader{
		Height:             height,
		Timestamp:          timestamp,
		PrevHash:           prevHash,
		Validator:          validator,
		ExecutionGraphRoot: executionGraphRoot,
	}

	txRoot, err := ComputeTxRoot(txs)
	if err != nil {
		return nil, err
	}
	header.TxRoot = txRoot
	header.StateRoot = stateCopy.PendingRoot().Bytes()

	block = types.NewBlock(header, txs)
	if hash, hashErr := header.Hash(); hashErr == nil {
		n.stateMu.Lock()
		n.selfProposedHash = hash
		n.stateMu.Unlock()
	}
	return block, nil
}

func (n *Node) CommitBlock(b *types.Block) error {
	return n.commitBlock(b, false)
}

func (n *Node) ValidateBlock(b *types.Block) error {
	if b == nil {
		return fmt.Errorf("block cannot be nil")
	}
	if b.Header == nil {
		return fmt.Errorf("block header missing")
	}
	if n == nil || n.chain == nil {
		return fmt.Errorf("blockchain not initialised")
	}

	txRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return err
	}
	if !bytes.Equal(txRoot, b.Header.TxRoot) {
		return fmt.Errorf("tx root mismatch")
	}
	if err := n.validateBlockTimestamp(b.Header.Timestamp, false); err != nil {
		return err
	}

	n.stateMu.RLock()
	currentHeight := n.chain.Height()
	n.stateMu.RUnlock()
	expectedHeight := currentHeight + 1
	if b.Header.Height != expectedHeight {
		return fmt.Errorf("block height mismatch: got %d want %d", b.Header.Height, expectedHeight)
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if err := n.resetDriftUnlessSelfProposedLocked(b, "validate block"); err != nil {
		return err
	}
	if err := n.refreshModulePauses(); err != nil {
		return err
	}
	stateCopy, err := n.state.Copy()
	if err != nil {
		return err
	}
	stateCopy.SetPauseView(n)
	stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())

	blockTime := time.Unix(b.Header.Timestamp, 0).UTC()
	stateCopy.BeginBlock(b.Header.Height, blockTime)
	defer stateCopy.EndBlock()
	// Always traced, not just for empty blocks: a real incident (buyback
	// ref-price transaction inclusion causing a state-root mismatch on
	// every validator, every round) needed exactly this per-stage
	// breakdown to diagnose and the gate meant it was blank precisely when
	// it mattered -- the one case with a non-empty block.
	traceStateRoots := true
	hexRoot := func(root common.Hash) string {
		return fmt.Sprintf("%x", root.Bytes())
	}
	rootAfterBegin := ""
	rootAfterLifecycle := ""
	rootAfterEvidence := ""
	rootAfterFinalize := ""
	if traceStateRoots {
		rootAfterBegin = hexRoot(stateCopy.PendingRoot())
	}

	orderedTxs, executionGraphRoot, err := computeDependencyGraph(b.Transactions)
	if err != nil {
		return fmt.Errorf("canonical scheduler failed: %w", err)
	}
	if len(b.Header.ExecutionGraphRoot) == 0 {
		return fmt.Errorf("execution graph root missing")
	}
	if !bytes.Equal(b.Header.ExecutionGraphRoot, executionGraphRoot) {
		return fmt.Errorf("execution graph root mismatch")
	}
	// See the matching comment at the buildProposalState call site -- must
	// run before this block's own transactions, not just before
	// ProcessBlockLifecycle below, so a TxTypeRedeemNHB burn in this same
	// block can't underflow the not-yet-seeded genesis supply counter.
	if err := stateCopy.SeedGenesisNHBSupplyOnce(); err != nil {
		return fmt.Errorf("seed genesis NHB supply: %w", err)
	}
	for i, tx := range orderedTxs {
		if err := stateCopy.ApplyTransaction(tx); err != nil {
			return fmt.Errorf("apply transaction %d: %w", i, err)
		}
	}
	if err := stateCopy.ProcessBlockLifecycle(b.Header.Height, b.Header.Timestamp); err != nil {
		return fmt.Errorf("block lifecycle: %w", err)
	}
	if traceStateRoots {
		rootAfterLifecycle = hexRoot(stateCopy.PendingRoot())
	}
	if err := n.processPendingEvidenceForState(stateCopy, b.Header.Height); err != nil {
		return fmt.Errorf("process evidence: %w", err)
	}
	if traceStateRoots {
		rootAfterEvidence = hexRoot(stateCopy.PendingRoot())
	}
	stateCopy.FinalizeBlock()
	if traceStateRoots {
		rootAfterFinalize = hexRoot(stateCopy.PendingRoot())
	}
	if pendingRoot := stateCopy.PendingRoot().Bytes(); !bytes.Equal(b.Header.StateRoot, pendingRoot) {
		return fmt.Errorf(
			"state root mismatch: header=%x pending=%x root_after_begin=%s root_after_lifecycle=%s root_after_evidence=%s root_after_finalize=%s",
			b.Header.StateRoot,
			pendingRoot,
			rootAfterBegin,
			rootAfterLifecycle,
			rootAfterEvidence,
			rootAfterFinalize,
		)
	}
	return nil
}

func (n *Node) commitSyncedBlock(b *types.Block) error {
	return n.commitBlock(b, true)
}

func (n *Node) commitBlock(b *types.Block, allowHistoricalTimestamp bool) (err error) {
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

	if b == nil {
		return fmt.Errorf("block cannot be nil")
	}
	if b.Header == nil {
		return fmt.Errorf("block header missing")
	}
	if n == nil || n.chain == nil {
		return fmt.Errorf("blockchain not initialised")
	}

	// NHB-TRIAGE-C1: a block reaching this function via the untrusted P2P
	// sync path (allowHistoricalTimestamp==true -- see commitSyncedBlock)
	// used to be accepted with no check at all that any real validator
	// quorum ever voted for it; Header.Validator is only a claimed
	// proposer address, not a proof. A locally-produced block
	// (allowHistoricalTimestamp==false, called from bft.Engine.commit())
	// does not need this check here -- the BFT engine already verified a
	// real 2/3+ precommit quorum before ever calling CommitBlock, which is
	// exactly what QuorumCert.Verify independently re-checks for a synced
	// block that skipped that live round entirely.
	if allowHistoricalTimestamp && b.Header.Height > n.quorumCertActivationHeight {
		headerHash, hashErr := b.Header.Hash()
		if hashErr != nil {
			return fmt.Errorf("hash header for quorum verification: %w", hashErr)
		}
		// The validator set active while this block was being voted on is
		// whatever was committed as of its PARENT's state -- not
		// necessarily whatever this node's current/latest validator set
		// happens to be. Using the parent height's own historical set
		// (validatorSetAtHeight) rather than n.GetValidatorSet() makes this
		// correct even for a long-range resync spanning a validator-set
		// change, not just ordinary block-by-block sync. Height 0 (genesis)
		// has no parent; its own state is the base case.
		var parentHeight uint64
		if b.Header.Height > 0 {
			parentHeight = b.Header.Height - 1
		}
		validatorPower, vsErr := n.validatorSetAtHeight(parentHeight)
		if vsErr != nil {
			// Fail closed, not open: if the historical validator set can't
			// be resolved, reject the block rather than silently falling
			// back to a weaker check (e.g. the current set, which may not
			// be who was actually active at this height).
			return fmt.Errorf("resolve validator set for quorum verification: %w", vsErr)
		}
		if err := b.QuorumCert.Verify(headerHash, validatorPower); err != nil {
			return fmt.Errorf("quorum certificate verification failed: %w", err)
		}
	}

	// Verify TxRoot before executing
	txRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return err
	}
	if !bytes.Equal(txRoot, b.Header.TxRoot) {
		return fmt.Errorf("tx root mismatch")
	}

	if err := n.validateBlockTimestamp(b.Header.Timestamp, allowHistoricalTimestamp); err != nil {
		return err
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if err := n.resetDriftUnlessSelfProposedLocked(b, "commit block"); err != nil {
		return err
	}

	currentHeight := n.chain.Height()
	if b.Header.Height <= currentHeight {
		existing, existingErr := n.chain.GetBlockByHeight(b.Header.Height)
		if existingErr == nil && existing != nil && existing.Header != nil {
			existingHash, hashErr := existing.Header.Hash()
			if hashErr == nil {
				incomingHash, incomingErr := b.Header.Hash()
				if incomingErr == nil && bytes.Equal(existingHash, incomingHash) {
					return nil
				}
			}
		}
		return fmt.Errorf("block height mismatch: got %d want %d", b.Header.Height, currentHeight+1)
	}
	expectedHeight := currentHeight + 1
	if b.Header.Height != expectedHeight {
		return fmt.Errorf("block height mismatch: got %d want %d", b.Header.Height, expectedHeight)
	}

	if err := n.refreshModulePauses(); err != nil {
		return err
	}

	stateCopy, err := n.state.Copy()
	if err != nil {
		return err
	}
	stateCopy.SetPauseView(n)
	stateCopy.SetQuotaConfig(n.moduleQuotaSnapshot())

	blockTime := time.Unix(b.Header.Timestamp, 0).UTC()
	stateCopy.BeginBlock(b.Header.Height, blockTime)
	defer stateCopy.EndBlock()
	// Always traced -- see the matching comment in ValidateBlock above.
	traceStateRoots := true
	hexRoot := func(root common.Hash) string {
		return fmt.Sprintf("%x", root.Bytes())
	}
	var rootAfterBegin string
	var rootAfterLifecycle string
	var rootAfterEvidence string
	var rootAfterFinalize string
	var rootAfterRefresh string
	if traceStateRoots {
		rootAfterBegin = hexRoot(stateCopy.PendingRoot())
	}

	// Compute V3 Canonical Conflict DAG
	orderedTxs, executionGraphRoot, dagErr := computeDependencyGraph(b.Transactions)
	if dagErr != nil {
		return fmt.Errorf("canonical scheduler failed: %w", dagErr)
	}

	// Verify or Assign Execution Graph Root
	if len(b.Header.ExecutionGraphRoot) == 0 {
		b.Header.ExecutionGraphRoot = executionGraphRoot
	} else if !bytes.Equal(b.Header.ExecutionGraphRoot, executionGraphRoot) {
		return fmt.Errorf("execution graph root mismatch")
	}

	b.Transactions = orderedTxs

	// See the matching comment at the buildProposalState call site -- must
	// run before this block's own transactions, not just before
	// ProcessBlockLifecycle below, so a TxTypeRedeemNHB burn in this same
	// block can't underflow the not-yet-seeded genesis supply counter.
	if err := stateCopy.SeedGenesisNHBSupplyOnce(); err != nil {
		return fmt.Errorf("seed genesis NHB supply: %w", err)
	}

	// Apply transactions deterministically in the canonical topological order
	for i, tx := range b.Transactions {
		if err := stateCopy.ApplyTransaction(tx); err != nil {
			fatalMint := isFatalMintError(err)
			if fatalMint {
				prunedTxs = append(prunedTxs, tx)
				n.markTransactionsCommitted([]*types.Transaction{tx})
			}
			return fmt.Errorf("apply transaction %d: %w", i, err)
		}
	}

	// Check derived StateRoot matches header (if header set) or fill it
	if err := stateCopy.ProcessBlockLifecycle(b.Header.Height, b.Header.Timestamp); err != nil {
		return fmt.Errorf("block lifecycle: %w", err)
	}
	if traceStateRoots {
		rootAfterLifecycle = hexRoot(stateCopy.PendingRoot())
	}

	if err := n.processPendingEvidenceForState(stateCopy, b.Header.Height); err != nil {
		return fmt.Errorf("process evidence: %w", err)
	}
	if traceStateRoots {
		rootAfterEvidence = hexRoot(stateCopy.PendingRoot())
	}

	stateCopy.FinalizeBlock()
	if traceStateRoots {
		rootAfterFinalize = hexRoot(stateCopy.PendingRoot())
	}
	if traceStateRoots {
		rootAfterRefresh = hexRoot(stateCopy.PendingRoot())
	}

	pendingRoot := stateCopy.PendingRoot()
	pendingBytes := pendingRoot.Bytes()
	if len(b.Header.StateRoot) == 0 {
		b.Header.StateRoot = pendingBytes
	} else if !bytes.Equal(b.Header.StateRoot, pendingBytes) {
		if traceStateRoots {
			slog.Warn(
				"commit state root mismatch",
				slog.Uint64("height", b.Header.Height),
				slog.Int64("timestamp", b.Header.Timestamp),
				slog.String("header_state_root", fmt.Sprintf("%x", b.Header.StateRoot)),
				slog.String("pending_state_root", fmt.Sprintf("%x", pendingBytes)),
				slog.String("root_after_begin", rootAfterBegin),
				slog.String("root_after_lifecycle", rootAfterLifecycle),
				slog.String("root_after_evidence", rootAfterEvidence),
				slog.String("root_after_finalize", rootAfterFinalize),
				slog.String("root_after_refresh", rootAfterRefresh),
			)
		}
		return fmt.Errorf("state root mismatch")
	}

	// Commit state at this height
	committedRoot, err := stateCopy.Commit(b.Header.Height)
	if err != nil {
		return fmt.Errorf("state commit failed: %w", err)
	}
	committedBytes := committedRoot.Bytes()
	if !bytes.Equal(b.Header.StateRoot, committedBytes) {
		return fmt.Errorf("state root mismatch after commit")
	}

	// Persist block to the chain
	var prevTimestamp int64
	if n.chain != nil {
		prevTimestamp = n.chain.LastTimestamp()
	}
	if err := n.chain.AddBlock(b); err != nil {
		return err
	}
	n.state = stateCopy
	if err := n.refreshModulePauses(); err != nil {
		return fmt.Errorf("refresh module pauses: %w", err)
	}
	n.refreshValidatorSet()
	if metrics := observability.Consensus(); metrics != nil {
		prevTime := time.Unix(prevTimestamp, 0).UTC()
		currentTime := time.Unix(b.Header.Timestamp, 0).UTC()
		metrics.RecordBlockInterval(currentTime.Sub(prevTime))
	}
	if n.syncMgr != nil && b != nil && b.Header != nil {
		n.syncMgr.SetHeight(b.Header.Height)
	}
	n.publishPOSFinalityFinalized(b)
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
	case errors.Is(err, ErrMintEmissionCapExceeded):
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

// validatorSetAtHeight reconstructs the validator set exactly as it existed
// in the state produced by the block at the given height -- i.e. the set
// that was actually active while the NEXT block (height+1) was being voted
// on. Read-only: builds a throwaway StateProcessor over a historical trie
// root and never touches n.state (unlike rebuildStateProcessorLocked,
// which replaces it -- do not reuse that function here, it would corrupt
// the live node's current state view).
//
// This relies on nhbchain's trie storage never pruning old nodes:
// storage/trie/trie.go's Commit only ever calls the underlying
// hashdb.Database's Commit (which flushes dirty nodes to disk and never
// deletes anything) -- Cap/Dereference, go-ethereum's only node-eviction
// APIs, are never called anywhere in this codebase. Confirmed directly
// against go-ethereum v1.16.3's triedb/hashdb source, not assumed: every
// historical state root this chain has ever committed remains fully
// readable. If a future change ever introduces pruning, this function
// (and NHB-TRIAGE-C1's quorum-certificate verification on the P2P sync
// path, its only caller) would need to be revisited together with
// whatever retention window that pruning adopts.
func (n *Node) validatorSetAtHeight(height uint64) (map[string]*big.Int, error) {
	if n == nil || n.chain == nil || n.db == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	block, err := n.chain.GetBlockByHeight(height)
	if err != nil {
		return nil, fmt.Errorf("validator set at height %d: %w", height, err)
	}
	if block == nil || block.Header == nil || len(block.Header.StateRoot) == 0 {
		return nil, fmt.Errorf("validator set at height %d: missing state root", height)
	}
	stateTrie, err := trie.NewTrie(n.db, block.Header.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("validator set at height %d: open historical trie: %w", height, err)
	}
	sp, err := NewStateProcessor(stateTrie)
	if err != nil {
		return nil, fmt.Errorf("validator set at height %d: %w", height, err)
	}
	if err := sp.loadValidatorSet(); err != nil {
		return nil, fmt.Errorf("validator set at height %d: %w", height, err)
	}
	return sp.ValidatorSet, nil
}

// SetQuorumCertActivationHeight configures the height at and below which
// commitBlock's NHB-TRIAGE-C1 quorum-certificate check is skipped on the
// P2P sync path -- see the field doc comment on Node.quorumCertActivationHeight.
// A live chain being upgraded to enforce this check MUST call this with
// its current tip height as part of a coordinated deploy (every validator
// needs the same activation height, applied before any of them starts
// producing new, QC-bearing blocks) -- otherwise already-committed history
// with no QuorumCert would be rejected by nodes still catching up or
// resyncing from genesis.
func (n *Node) SetQuorumCertActivationHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.quorumCertActivationHeight = height
}

// RemoveValidatorFromSet permanently removes addr from the active and
// eligible validator sets and persists the change to the trie (staged, not
// yet committed to a block). This is an operator recovery primitive for a
// validator that has been permanently decommissioned and will never vote
// again, so its previously-registered power no longer counts toward BFT's
// 2/3 quorum threshold and blocks the remaining validator(s) from ever
// reaching it. It does not touch genesis, stake/account balances, or any
// other state. The caller is responsible for finalizing the staged change
// into a real block via CreateBlock/CommitBlock.
func (n *Node) RemoveValidatorFromSet(addr [20]byte) (removed bool, remainingPower *big.Int, err error) {
	if n == nil || n.state == nil {
		return false, nil, fmt.Errorf("node not initialised")
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	key := string(addr[:])
	if _, ok := n.state.ValidatorSet[key]; !ok {
		return false, nil, nil
	}
	delete(n.state.ValidatorSet, key)
	delete(n.state.EligibleValidators, key)

	remaining := big.NewInt(0)
	for _, power := range n.state.ValidatorSet {
		if power != nil {
			remaining.Add(remaining, power)
		}
	}

	if err := n.state.persistValidatorSet(); err != nil {
		return false, nil, fmt.Errorf("persist validator set: %w", err)
	}
	if err := n.state.persistEligibleValidatorSet(); err != nil {
		return false, nil, fmt.Errorf("persist eligible validator set: %w", err)
	}
	return true, remaining, nil
}

// ReplaceValidatorSet atomically replaces the entire active and eligible
// validator sets with newSet and persists the change (staged, not yet
// committed to a block). This is an operator recovery primitive for
// correcting consensus state to match reality -- for example after a
// validator hot-key rotation that was never reflected in the persisted
// validator set, leaving the running node signing with a key the chain
// does not recognize as a validator, so its own votes can never count
// toward BFT quorum no matter how many rounds elapse. It is not a
// mechanism for normal validator onboarding/offboarding, which should go
// through the regular staking/epoch-rollover path whenever the chain is
// healthy enough to process transactions.
func (n *Node) ReplaceValidatorSet(newSet map[string]*big.Int) error {
	if n == nil || n.state == nil {
		return fmt.Errorf("node not initialised")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	cleaned := make(map[string]*big.Int, len(newSet))
	for k, v := range newSet {
		if v == nil {
			continue
		}
		cleaned[k] = new(big.Int).Set(v)
	}
	n.state.ValidatorSet = cleaned
	n.state.EligibleValidators = make(map[string]*big.Int, len(cleaned))
	for k, v := range cleaned {
		n.state.EligibleValidators[k] = new(big.Int).Set(v)
	}

	if err := n.state.persistValidatorSet(); err != nil {
		return fmt.Errorf("persist validator set: %w", err)
	}
	if err := n.state.persistEligibleValidatorSet(); err != nil {
		return fmt.Errorf("persist eligible validator set: %w", err)
	}
	return nil
}

// PendingStateRoot returns the state root that would result from the
// node's current in-memory pending state, without requiring a block to be
// created or committed first.
func (n *Node) PendingStateRoot() []byte {
	if n == nil || n.state == nil {
		return nil
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	root := n.state.PendingRoot()
	return root.Bytes()
}

// PatchTipStateRoot corrects the declared StateRoot of the current tip
// block to newStateRoot. See Blockchain.PatchTipStateRoot for details on
// when and why this operator recovery primitive is needed.
func (n *Node) PatchTipStateRoot(newStateRoot []byte) error {
	if n == nil || n.chain == nil {
		return fmt.Errorf("node not initialised")
	}
	return n.chain.PatchTipStateRoot(newStateRoot)
}

// SelfValidatorAddress returns the address derived from this node's own
// configured validator key.
func (n *Node) SelfValidatorAddress() [20]byte {
	var out [20]byte
	if n == nil || n.validatorKey == nil {
		return out
	}
	copy(out[:], n.validatorKey.PubKey().Address().Bytes())
	return out
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

// processPendingEvidence loops over all unprocessed POTSO evidence and applies them through the penalty engine.
func (n *Node) processPendingEvidence(currentHeight uint64) error {
	if n == nil {
		return nil
	}
	return n.processPendingEvidenceForState(n.state, currentHeight)
}

func (n *Node) processPendingEvidenceForState(state *StateProcessor, currentHeight uint64) error {
	if n == nil || state == nil || n.evidenceStore == nil {
		return nil
	}

	cfg := penalty.DefaultConfig()
	cfg.SlashEnabled = true
	cfg.EquivocationSlashBps = 10000 // 100% slashing on equivocation

	catalog, err := penalty.BuildCatalog(cfg)
	if err != nil {
		return fmt.Errorf("build penalty catalog: %w", err)
	}

	manager := nhbstate.NewManager(state.Trie)
	slasher := statebank.NewValidatorSlasher(manager)
	engine := penalty.NewEngine(catalog, n.potsoLedger, slasher)

	fromHeight := uint64(0)
	if currentHeight > n.evidenceMaxAge {
		fromHeight = currentHeight - n.evidenceMaxAge
	}

	filter := evidence.Filter{
		FromHeight: &fromHeight,
		Limit:      evidence.DefaultPageLimit,
	}

	for {
		records, nextOffset, err := n.evidenceStore.List(filter)
		if err != nil {
			return fmt.Errorf("list evidence: %w", err)
		}

		for _, rec := range records {
			ctx := penalty.Context{
				BlockHeight:  currentHeight,
				MissedEpochs: 0,
			}
			res, err := engine.Apply(rec, ctx)
			if err != nil {
				return fmt.Errorf("apply penalty for %x: %w", rec.Hash, err)
			}
			if !res.Idempotent && res.Event != nil {
				state.AppendEvent(res.Event)
			}
		}

		if nextOffset < 0 {
			break
		}
		filter.Offset = nextOffset
	}

	return nil
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
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	return n.state.GetAccount(addr)
}

// SweepExpiredPOSAuthorizations triggers an on-demand sweep of expired POS authorizations.
func (n *Node) SweepExpiredPOSAuthorizations(now time.Time) (int, error) {
	if n == nil {
		return 0, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return 0, fmt.Errorf("state unavailable")
	}
	return n.state.SweepExpiredPOSAuthorizations(now)
}

// GetPOSAuthorization returns the authorization record for the given ID.
func (n *Node) GetPOSAuthorization(id [32]byte) (*pos.Authorization, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	return n.state.GetPOSAuthorization(id)
}

// GetPOSAuthorizationByIntentRef resolves the authorization created for the
// given client-supplied intent reference, if any.
func (n *Node) GetPOSAuthorizationByIntentRef(intentRef []byte) (*pos.Authorization, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	return n.state.GetPOSAuthorizationByIntentRef(intentRef)
}

func (n *Node) EpochConfig() epoch.Config {
	if n == nil {
		return epoch.Config{}
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return epoch.Config{}
	}
	return n.state.EpochConfig()
}

func (n *Node) SetEpochConfig(cfg epoch.Config) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	return n.state.SetEpochConfig(cfg)
}

func (n *Node) EpochSnapshot(epochNumber uint64) (*epoch.Snapshot, bool) {
	if n == nil {
		return nil, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, false
	}
	return n.state.EpochSnapshot(epochNumber)
}

func (n *Node) LatestEpochSnapshot() (*epoch.Snapshot, bool) {
	if n == nil {
		return nil, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, false
	}
	return n.state.LatestEpochSnapshot()
}

func (n *Node) LatestEpochSummary() (*epoch.Summary, bool) {
	if n == nil {
		return nil, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, false
	}
	return n.state.LatestEpochSummary()
}

func (n *Node) EpochSummary(epochNumber uint64) (*epoch.Summary, bool) {
	snapshot, ok := n.EpochSnapshot(epochNumber)
	if !ok {
		return nil, false
	}
	summary := snapshot.Summary()
	return &summary, true
}

func (n *Node) RewardConfig() rewards.Config {
	if n == nil {
		return rewards.Config{}
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return rewards.Config{}
	}
	return n.state.RewardConfig()
}

// GlobalConfig returns a defensive copy of the validated runtime policy.
func (n *Node) GlobalConfig() config.Global {
	if n == nil {
		return config.Global{}
	}
	return n.globalConfigSnapshot()
}

func (n *Node) SetRewardConfig(cfg rewards.Config) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	return n.state.SetRewardConfig(cfg)
}

func (n *Node) PotsoRewardConfig() potso.RewardConfig {
	if n == nil {
		return potso.RewardConfig{}
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return potso.RewardConfig{}
	}
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
	if n == nil {
		return potso.WeightParams{}
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return potso.WeightParams{}
	}
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
	if n == nil {
		return nil, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, false
	}
	return n.state.RewardEpochSettlement(epochNumber)
}

func (n *Node) LatestRewardEpochSettlement() (*rewards.EpochSettlement, bool) {
	if n == nil {
		return nil, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, false
	}
	return n.state.LatestRewardEpochSettlement()
}

// RewardHistoryEntry describes a single account's payout within one settled
// epoch-emission reward distribution (validator/staker/engagement split).
type RewardHistoryEntry struct {
	Epoch      uint64
	Height     uint64
	ClosedAt   int64
	Total      *big.Int
	Validators *big.Int
	Stakers    *big.Int
	Engagement *big.Int
}

// RewardHistoryForAddress returns every payout entry for addr across the
// currently retained epoch-emission reward settlements (bounded by the
// node's configured reward-history retention window, see
// StateProcessor.RewardHistory). This is a read-only walk over state that
// every validator already computed and persisted identically inside
// ProcessBlockLifecycle -> settleEpochRewards: it introduces no new trie
// writes and no new determinism surface, since RewardHistory() only returns
// data that is already part of consensus state.
func (n *Node) RewardHistoryForAddress(addr []byte) []RewardHistoryEntry {
	if n == nil || len(addr) == 0 {
		return nil
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil
	}
	var out []RewardHistoryEntry
	for _, settlement := range n.state.RewardHistory() {
		for i := range settlement.Payouts {
			payout := settlement.Payouts[i]
			if !bytes.Equal(payout.Account, addr) {
				continue
			}
			out = append(out, RewardHistoryEntry{
				Epoch:      settlement.Epoch,
				Height:     settlement.Height,
				ClosedAt:   settlement.ClosedAt,
				Total:      payout.Total,
				Validators: payout.Validators,
				Stakers:    payout.Stakers,
				Engagement: payout.Engagement,
			})
		}
	}
	return out
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

// SubscriptionsManager returns a fresh state.Manager over the node's
// current trie -- every accessor below constructs one per call, matching
// LoyaltyManager's convention (cheap: two pointers + an interface).
func (n *Node) SubscriptionsManager() *nhbstate.Manager {
	return nhbstate.NewManager(n.state.Trie)
}

// SubscriptionsRegistry returns a fresh Registry wired to the node's
// current pause state, matching LoyaltyRegistry's convention.
func (n *Node) SubscriptionsRegistry() *subscriptions.Registry {
	registry := subscriptions.NewRegistry(n.SubscriptionsManager())
	registry.SetPauses(n)
	return registry
}

// SubscriptionPlanByID is a read-only lookup -- no auth required, mirrors
// every other public on-chain-state RPC read in this codebase.
func (n *Node) SubscriptionPlanByID(id subscriptions.PlanID) (*subscriptions.Plan, bool) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().GetPlan(id)
}

func (n *Node) SubscriptionPlansByMerchant(merchant [20]byte) ([]subscriptions.PlanID, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().ListPlansByMerchant(merchant)
}

func (n *Node) SubscriptionByID(id subscriptions.SubscriptionID) (*subscriptions.Subscription, bool) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().GetSubscription(id)
}

func (n *Node) SubscriptionsByPayer(payer [20]byte) ([]subscriptions.SubscriptionID, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().ListSubscriptionsByPayer(payer)
}

func (n *Node) SubscriptionsByMerchant(merchant [20]byte) ([]subscriptions.SubscriptionID, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().ListSubscriptionsByMerchant(merchant)
}

func (n *Node) SubscriptionCharges(id subscriptions.SubscriptionID) ([]subscriptions.Charge, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.SubscriptionsRegistry().ListCharges(id)
}

// SubscriptionsEngineConfig exposes the deployment-configured management
// fee/retry parameters read-only, so the portal dashboard can display the
// real fee rate rather than a hardcoded guess.
func (n *Node) SubscriptionsEngineConfig() (subscriptions.Config, bool) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return subscriptions.Config{}, false
	}
	return n.state.SubscriptionsConfig()
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

func (n *Node) EscrowDispute(id [32]byte, caller [20]byte, reason string) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	return engine.Dispute(id, caller, reason)
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

// Node.StakeClaimRewards (the direct-state-trie write that used to back
// rpc/stake_handlers.go's handleStakeClaimRewards, mutating state under
// n.stateMu.Lock() completely outside CreateBlock/ApplyTransaction/
// ValidateBlock) has been removed. Reward claims now go through a real
// signed TxTypeStakeClaimRewards transaction, applied via
// StateProcessor.applyStakeClaimRewards -> StateProcessor.StakeClaimRewards
// (core/state_transition.go) from the standard consensus tx-dispatch path,
// so every validator applies the payout identically instead of just the
// one handling the RPC call.

// StakePreviewClaim estimates the staking reward that would be minted if the
// caller claimed rewards at the provided timestamp. The state is not mutated.
func (n *Node) StakePreviewClaim(addr [20]byte, at time.Time) (*big.Int, uint64, error) {
	if n == nil {
		return nil, 0, fmt.Errorf("node unavailable")
	}

	snapshotTime := at
	if snapshotTime.IsZero() {
		snapshotTime = n.currentTime()
	}
	snapshotTime = snapshotTime.UTC()

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	if n.state == nil || n.state.Trie == nil {
		return nil, 0, fmt.Errorf("state unavailable")
	}

	manager := nhbstate.NewManager(n.state.Trie)
	account, err := manager.GetAccount(addr[:])
	if err != nil {
		return nil, 0, err
	}

	globalIndex, err := manager.StakingGlobalIndex()
	if err != nil {
		return nil, 0, err
	}
	if globalIndex == nil {
		globalIndex = big.NewInt(0)
	}

	payoutDays := uint64(30)
	if raw, ok, err := manager.ParamStoreGet(governance.ParamKeyStakingPayoutPeriodDays); err == nil && ok {
		trimmed := strings.TrimSpace(string(raw))
		if trimmed != "" {
			if value, parseErr := strconv.ParseUint(trimmed, 10, 64); parseErr == nil && value > 0 {
				payoutDays = value
			}
		}
	}

	const secondsPerDay = uint64(86400)
	periodSeconds := payoutDays * secondsPerDay

	lastPayout := account.StakeLastPayoutTs
	nextPayout := lastPayout
	if periodSeconds > 0 {
		nextPayout = lastPayout + periodSeconds
	}

	payable := big.NewInt(0)
	if periodSeconds > 0 && account.StakeShares != nil && account.StakeShares.Sign() > 0 {
		nowTs := uint64(snapshotTime.Unix())
		if nowTs > lastPayout {
			elapsed := nowTs - lastPayout
			if elapsed >= periodSeconds {
				periods := elapsed / periodSeconds
				if periods > 0 {
					deltaIndex := new(big.Int).Sub(globalIndex, account.StakeLastIndex)
					if deltaIndex.Sign() > 0 {
						eligibleSeconds := periods * periodSeconds
						if eligibleSeconds > 0 {
							eligibleIndexDelta := new(big.Int).Mul(deltaIndex, new(big.Int).SetUint64(eligibleSeconds))
							eligibleIndexDelta.Quo(eligibleIndexDelta, new(big.Int).SetUint64(elapsed))
							if eligibleIndexDelta.Sign() > 0 {
								payable = new(big.Int).Mul(eligibleIndexDelta, account.StakeShares)
								nextPayout = nowTs + periodSeconds
							}
						}
					}
				}
			}
		}
	}

	return payable, nextPayout, nil
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

// milestoneStorageKey generates a namespaced state key for milestone projects.
func milestoneStorageKey(id [32]byte) []byte {
	return append([]byte("milestone-proj-"), id[:]...)
}

func putMilestoneProject(manager *nhbstate.Manager, project *escrow.MilestoneProject) error {
	if manager == nil || project == nil {
		return fmt.Errorf("milestone: project persistence unavailable")
	}
	encoded, err := json.Marshal(project)
	if err != nil {
		return fmt.Errorf("marshal milestone: %w", err)
	}
	return manager.KVPut(milestoneStorageKey(project.ID), encoded)
}

func getMilestoneProject(manager *nhbstate.Manager, id [32]byte) (*escrow.MilestoneProject, bool, error) {
	if manager == nil {
		return nil, false, fmt.Errorf("milestone: project persistence unavailable")
	}
	var payload []byte
	ok, err := manager.KVGet(milestoneStorageKey(id), &payload)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	var project escrow.MilestoneProject
	if err := json.Unmarshal(payload, &project); err != nil {
		return nil, false, fmt.Errorf("decode milestone: %w", err)
	}
	return &project, true, nil
}

func milestoneProjectFingerprint(project *escrow.MilestoneProject, nonce int64) []byte {
	type legFingerprint struct {
		ID          uint64
		Type        uint8
		Title       string
		Description string
		Token       string
		Amount      string
		Deadline    int64
	}
	type subscriptionFingerprint struct {
		IntervalSeconds int64
		NextReleaseAt   int64
		Active          bool
	}
	type projectFingerprint struct {
		Payer        [20]byte
		Payee        [20]byte
		RealmID      string
		MetadataHex  string
		CreatedAt    int64
		Nonce        int64
		Legs         []legFingerprint
		Subscription *subscriptionFingerprint
	}
	fp := projectFingerprint{
		Payer:       project.Payer,
		Payee:       project.Payee,
		RealmID:     strings.TrimSpace(project.RealmID),
		MetadataHex: hex.EncodeToString(project.Metadata),
		CreatedAt:   project.CreatedAt,
		Nonce:       nonce,
		Legs:        make([]legFingerprint, 0, len(project.Legs)),
	}
	for _, leg := range project.Legs {
		if leg == nil {
			continue
		}
		amount := "0"
		if leg.Amount != nil {
			amount = leg.Amount.String()
		}
		fp.Legs = append(fp.Legs, legFingerprint{
			ID:          leg.ID,
			Type:        uint8(leg.Type),
			Title:       strings.TrimSpace(leg.Title),
			Description: strings.TrimSpace(leg.Description),
			Token:       strings.ToUpper(strings.TrimSpace(leg.Token)),
			Amount:      amount,
			Deadline:    leg.Deadline,
		})
	}
	if project.Subscription != nil {
		fp.Subscription = &subscriptionFingerprint{
			IntervalSeconds: project.Subscription.IntervalSeconds,
			NextReleaseAt:   project.Subscription.NextReleaseAt,
			Active:          project.Subscription.Active,
		}
	}
	encoded, _ := json.Marshal(fp)
	return encoded
}

func milestoneVaultPrefix(token string) crypto.AddressPrefix {
	if strings.EqualFold(strings.TrimSpace(token), "ZNHB") {
		return crypto.ZNHBPrefix
	}
	return crypto.NHBPrefix
}

func milestoneVaultAddress(projectID [32]byte, legID uint64, token string) crypto.Address {
	seed := fmt.Sprintf("module/milestone/%x/%d/%s", projectID[:], legID, strings.ToUpper(strings.TrimSpace(token)))
	return deriveModuleAddress(seed, milestoneVaultPrefix(token))
}

func cloneCoreAccount(account *types.Account) *types.Account {
	if account == nil {
		return &types.Account{
			BalanceNHB:        big.NewInt(0),
			BalanceZNHB:       big.NewInt(0),
			Stake:             big.NewInt(0),
			StakeShares:       big.NewInt(0),
			StakeLastIndex:    big.NewInt(0),
			LockedZNHB:        big.NewInt(0),
			CollateralBalance: big.NewInt(0),
			DebtPrincipal:     big.NewInt(0),
			SupplyShares:      big.NewInt(0),
		}
	}
	clone := *account
	clone.BalanceNHB = cloneBigInt(account.BalanceNHB)
	clone.BalanceZNHB = cloneBigInt(account.BalanceZNHB)
	clone.Stake = cloneBigInt(account.Stake)
	clone.StakeShares = cloneBigInt(account.StakeShares)
	clone.StakeLastIndex = cloneBigInt(account.StakeLastIndex)
	clone.LockedZNHB = cloneBigInt(account.LockedZNHB)
	clone.CollateralBalance = cloneBigInt(account.CollateralBalance)
	clone.DebtPrincipal = cloneBigInt(account.DebtPrincipal)
	clone.SupplyShares = cloneBigInt(account.SupplyShares)
	if account.LendingSnapshot.SupplyIndex != nil {
		clone.LendingSnapshot.SupplyIndex = new(big.Int).Set(account.LendingSnapshot.SupplyIndex)
	} else {
		clone.LendingSnapshot.SupplyIndex = big.NewInt(0)
	}
	if account.LendingSnapshot.BorrowIndex != nil {
		clone.LendingSnapshot.BorrowIndex = new(big.Int).Set(account.LendingSnapshot.BorrowIndex)
	} else {
		clone.LendingSnapshot.BorrowIndex = big.NewInt(0)
	}
	if account.StakingRewards.AccruedZNHB != nil {
		clone.StakingRewards.AccruedZNHB = new(big.Int).Set(account.StakingRewards.AccruedZNHB)
	} else {
		clone.StakingRewards.AccruedZNHB = big.NewInt(0)
	}
	if len(account.DelegatedValidator) > 0 {
		clone.DelegatedValidator = append([]byte(nil), account.DelegatedValidator...)
	}
	if len(account.PendingUnbonds) > 0 {
		clone.PendingUnbonds = make([]types.StakeUnbond, len(account.PendingUnbonds))
		copy(clone.PendingUnbonds, account.PendingUnbonds)
	}
	if len(account.CodeHash) > 0 {
		clone.CodeHash = append([]byte(nil), account.CodeHash...)
	}
	if len(account.StorageRoot) > 0 {
		clone.StorageRoot = append([]byte(nil), account.StorageRoot...)
	}
	return &clone
}

func milestoneUnauthorized(actor string) error {
	return fmt.Errorf("escrow: milestone unauthorized %s caller", actor)
}

func (n *Node) milestoneMoveTokenLocked(manager *nhbstate.Manager, from, to [20]byte, token string, amount *big.Int) (func() error, error) {
	if manager == nil {
		return nil, fmt.Errorf("milestone: state manager unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		return func() error { return nil }, nil
	}
	normalized := strings.ToUpper(strings.TrimSpace(token))
	switch normalized {
	case "NHB", "ZNHB":
		fromAcc, err := manager.GetAccount(from[:])
		if err != nil {
			return nil, err
		}
		toAcc, err := manager.GetAccount(to[:])
		if err != nil {
			return nil, err
		}
		originalFrom := cloneCoreAccount(fromAcc)
		originalTo := cloneCoreAccount(toAcc)
		fromAcc = cloneCoreAccount(fromAcc)
		toAcc = cloneCoreAccount(toAcc)

		var (
			subRollback func()
			addRollback func()
		)
		if normalized == "NHB" {
			subRollback, err = nhbstate.MustSubBalance(fromAcc.BalanceNHB, amount)
			if err != nil {
				return nil, err
			}
			addRollback, err = nhbstate.MustAddBalance(toAcc.BalanceNHB, amount)
			if err != nil {
				subRollback()
				return nil, err
			}
		} else {
			subRollback, err = nhbstate.MustSubBalance(fromAcc.BalanceZNHB, amount)
			if err != nil {
				return nil, err
			}
			addRollback, err = nhbstate.MustAddBalance(toAcc.BalanceZNHB, amount)
			if err != nil {
				subRollback()
				return nil, err
			}
		}
		if err := manager.PutAccount(from[:], fromAcc); err != nil {
			addRollback()
			subRollback()
			return nil, err
		}
		if err := manager.PutAccount(to[:], toAcc); err != nil {
			_ = manager.PutAccount(from[:], originalFrom)
			addRollback()
			subRollback()
			return nil, err
		}
		return func() error {
			if err := manager.PutAccount(from[:], originalFrom); err != nil {
				return err
			}
			return manager.PutAccount(to[:], originalTo)
		}, nil
	default:
		fromBalance, err := manager.Balance(from[:], normalized)
		if err != nil {
			return nil, err
		}
		toBalance, err := manager.Balance(to[:], normalized)
		if err != nil {
			return nil, err
		}
		if fromBalance.Cmp(amount) < 0 {
			return nil, fmt.Errorf("milestone: insufficient %s balance", normalized)
		}
		newFrom := new(big.Int).Sub(new(big.Int).Set(fromBalance), amount)
		newTo := new(big.Int).Add(new(big.Int).Set(toBalance), amount)
		if err := manager.SetBalance(from[:], normalized, newFrom); err != nil {
			return nil, err
		}
		if err := manager.SetBalance(to[:], normalized, newTo); err != nil {
			_ = manager.SetBalance(from[:], normalized, fromBalance)
			return nil, err
		}
		return func() error {
			if err := manager.SetBalance(from[:], normalized, fromBalance); err != nil {
				return err
			}
			return manager.SetBalance(to[:], normalized, toBalance)
		}, nil
	}
}

func (n *Node) emitMilestoneEvent(event *types.Event) {
	if n == nil || n.state == nil || event == nil {
		return
	}
	n.state.AppendEvent(event)
}

func (n *Node) sweepMilestoneDueLegsLocked(manager *nhbstate.Manager, project *escrow.MilestoneProject) error {
	if manager == nil || project == nil {
		return nil
	}
	engine := escrow.NewMilestoneEngine(func() time.Time { return n.currentTime() })
	for {
		leg := project.NextDueLeg(n.currentTime().Unix())
		if leg == nil {
			return nil
		}
		originalProject := project.Clone()
		vault := milestoneVaultAddress(project.ID, leg.ID, leg.Token)
		var vaultAddr [20]byte
		copy(vaultAddr[:], vault.Bytes())
		rollback, err := n.milestoneMoveTokenLocked(manager, vaultAddr, project.Payer, leg.Token, leg.Amount)
		if err != nil {
			return err
		}
		expired := engine.ExpireDueLeg(project)
		if expired == nil {
			if rollback != nil {
				_ = rollback()
			}
			return nil
		}
		if err := putMilestoneProject(manager, project); err != nil {
			*project = *originalProject
			if rollback != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
			}
			return fmt.Errorf("persist milestone: %w", err)
		}
		n.emitMilestoneEvent(escrow.NewMilestoneDueEvent(project, expired))
	}
}

// EscrowMilestoneCreate persists a new milestone project to state.
func (n *Node) EscrowMilestoneCreate(project *escrow.MilestoneProject) (*escrow.MilestoneProject, error) {
	if n == nil || n.state == nil {
		return nil, fmt.Errorf("node or state unavailable")
	}
	if project == nil {
		return nil, fmt.Errorf("escrow: milestone project required")
	}
	if project.Payer == ([20]byte{}) {
		return nil, fmt.Errorf("escrow: milestone payer required")
	}
	if project.Payee == ([20]byte{}) {
		return nil, fmt.Errorf("escrow: milestone payee required")
	}
	engine := escrow.NewMilestoneEngine(func() time.Time { return n.currentTime() })
	if err := engine.CreateProject(project); err != nil {
		return nil, err
	}

	// Generate a collision-resistant identifier incorporating project contents and a creation nonce.
	project.ID = sha256.Sum256(milestoneProjectFingerprint(project, n.currentTime().UnixNano()))

	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	if err := putMilestoneProject(manager, project); err != nil {
		return nil, fmt.Errorf("persist milestone: %w", err)
	}
	n.emitMilestoneEvent(escrow.NewMilestoneCreatedEvent(project))
	return project, nil
}

// EscrowMilestoneGet returns the current milestone project state from persistence.
func (n *Node) EscrowMilestoneGet(id [32]byte) (*escrow.MilestoneProject, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	project, ok, err := getMilestoneProject(manager, id)
	if err != nil {
		return nil, fmt.Errorf("read milestone: %w", err)
	}
	if !ok {
		return nil, escrow.ErrMilestoneNotFound
	}
	if err := n.sweepMilestoneDueLegsLocked(manager, project); err != nil {
		return nil, err
	}
	return project, nil
}

// EscrowMilestoneFund transitions a milestone leg into the funded state.
func (n *Node) EscrowMilestoneFund(id [32]byte, legID uint64, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	project, ok, err := getMilestoneProject(manager, id)
	if err != nil {
		return fmt.Errorf("read milestone: %w", err)
	}
	if !ok {
		return escrow.ErrMilestoneNotFound
	}
	if project.Payer != caller {
		return milestoneUnauthorized("payer")
	}
	if err := n.sweepMilestoneDueLegsLocked(manager, project); err != nil {
		return err
	}
	leg := project.FindLeg(legID)
	if leg == nil {
		return escrow.ErrMilestoneNotFound
	}
	if leg.Deadline <= n.currentTime().Unix() {
		return fmt.Errorf("escrow: milestone leg deadline passed")
	}
	originalProject := project.Clone()
	vault := milestoneVaultAddress(project.ID, leg.ID, leg.Token)
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())
	rollback, err := n.milestoneMoveTokenLocked(manager, project.Payer, vaultAddr, leg.Token, leg.Amount)
	if err != nil {
		return err
	}

	engine := escrow.NewMilestoneEngine(func() time.Time { return n.currentTime() })
	if err := engine.FundLeg(project, legID); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	if err := putMilestoneProject(manager, project); err != nil {
		*project = *originalProject
		if rollback != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(fmt.Errorf("persist milestone: %w", err), rollbackErr)
			}
		}
		return fmt.Errorf("persist milestone: %w", err)
	}
	n.emitMilestoneEvent(escrow.NewMilestoneFundedEvent(project, project.FindLeg(legID)))
	return nil
}

// EscrowMilestoneRelease releases a funded milestone leg to the payee.
func (n *Node) EscrowMilestoneRelease(id [32]byte, legID uint64, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	project, ok, err := getMilestoneProject(manager, id)
	if err != nil {
		return fmt.Errorf("read milestone: %w", err)
	}
	if !ok {
		return escrow.ErrMilestoneNotFound
	}
	if project.Payer != caller {
		return milestoneUnauthorized("payer")
	}
	if err := n.sweepMilestoneDueLegsLocked(manager, project); err != nil {
		return err
	}
	leg := project.FindLeg(legID)
	if leg == nil {
		return escrow.ErrMilestoneNotFound
	}
	originalProject := project.Clone()
	vault := milestoneVaultAddress(project.ID, leg.ID, leg.Token)
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())
	rollback, err := n.milestoneMoveTokenLocked(manager, vaultAddr, project.Payee, leg.Token, leg.Amount)
	if err != nil {
		return err
	}

	engine := escrow.NewMilestoneEngine(func() time.Time { return n.currentTime() })
	if err := engine.ReleaseLeg(project, legID); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	if err := putMilestoneProject(manager, project); err != nil {
		*project = *originalProject
		if rollback != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(fmt.Errorf("persist milestone: %w", err), rollbackErr)
			}
		}
		return fmt.Errorf("persist milestone: %w", err)
	}
	n.emitMilestoneEvent(escrow.NewMilestoneReleasedEvent(project, project.FindLeg(legID)))
	return nil
}

// EscrowMilestoneCancel cancels a milestone leg.
func (n *Node) EscrowMilestoneCancel(id [32]byte, legID uint64, caller [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	project, ok, err := getMilestoneProject(manager, id)
	if err != nil {
		return fmt.Errorf("read milestone: %w", err)
	}
	if !ok {
		return escrow.ErrMilestoneNotFound
	}
	if project.Payer != caller {
		return milestoneUnauthorized("payer")
	}
	if err := n.sweepMilestoneDueLegsLocked(manager, project); err != nil {
		return err
	}
	leg := project.FindLeg(legID)
	if leg == nil {
		return escrow.ErrMilestoneNotFound
	}
	originalProject := project.Clone()
	var rollback func() error
	if leg.Status == escrow.MilestoneLegFunded {
		vault := milestoneVaultAddress(project.ID, leg.ID, leg.Token)
		var vaultAddr [20]byte
		copy(vaultAddr[:], vault.Bytes())
		rollback, err = n.milestoneMoveTokenLocked(manager, vaultAddr, project.Payer, leg.Token, leg.Amount)
		if err != nil {
			return err
		}
	}

	engine := escrow.NewMilestoneEngine(func() time.Time { return n.currentTime() })
	if err := engine.CancelLeg(project, legID); err != nil {
		if rollback != nil {
			_ = rollback()
		}
		return err
	}
	if err := putMilestoneProject(manager, project); err != nil {
		*project = *originalProject
		if rollback != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(fmt.Errorf("persist milestone: %w", err), rollbackErr)
			}
		}
		return fmt.Errorf("persist milestone: %w", err)
	}
	n.emitMilestoneEvent(escrow.NewMilestoneCancelledEvent(project, project.FindLeg(legID)))
	return nil
}

// EscrowMilestoneSubscriptionUpdate updates the subscription toggle for a
// milestone project.
func (n *Node) EscrowMilestoneSubscriptionUpdate(id [32]byte, caller [20]byte, active bool) (*escrow.MilestoneProject, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	manager := nhbstate.NewManager(n.state.Trie)
	project, ok, err := getMilestoneProject(manager, id)
	if err != nil {
		return nil, fmt.Errorf("read milestone: %w", err)
	}
	if !ok {
		return nil, escrow.ErrMilestoneNotFound
	}
	if project.Payer != caller {
		return nil, milestoneUnauthorized("payer")
	}
	if err := n.sweepMilestoneDueLegsLocked(manager, project); err != nil {
		return nil, err
	}

	if project.Subscription == nil {
		return nil, fmt.Errorf("escrow: project does not have a subscription")
	}
	project.Subscription.Active = active
	project.UpdatedAt = n.currentTime().Unix()

	if err := putMilestoneProject(manager, project); err != nil {
		return nil, fmt.Errorf("persist milestone: %w", err)
	}
	return project, nil
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
	// RPC-direct write, not consensus block execution -- 0 falls back to
	// time.Now(), which is fine here (see IdentitySetAlias's doc comment).
	if err := manager.IdentitySetAlias(addr[:], alias, 0); err != nil {
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

func aliasRecordHasAddress(record *identity.AliasRecord, addr [20]byte) bool {
	if record == nil {
		return false
	}
	for _, existing := range record.Addresses {
		if existing == addr {
			return true
		}
	}
	return false
}

func aliasRecordOwnedBy(record *identity.AliasRecord, owner [20]byte) bool {
	if record == nil {
		return false
	}
	if record.Owner == owner {
		return true
	}
	if record.Owner == ([20]byte{}) && record.Primary == owner {
		return true
	}
	return false
}

func (n *Node) IdentityAddAddress(owner [20]byte, alias string, addr [20]byte) (*identity.AliasRecord, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.IdentityResolve(alias)
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if !aliasRecordOwnedBy(record, owner) {
		return nil, identity.ErrNotAliasOwner
	}
	alreadyLinked := aliasRecordHasAddress(record, addr)
	updated, err := manager.IdentityAddAddress(record.Alias, addr[:], time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if alreadyLinked {
		return updated, nil
	}
	evt := events.IdentityAliasAddressLinked{Alias: updated.Alias, Address: addr}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return updated, nil
}

func (n *Node) IdentityRemoveAddress(owner [20]byte, alias string, addr [20]byte) (*identity.AliasRecord, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.IdentityResolve(alias)
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if !aliasRecordOwnedBy(record, owner) {
		return nil, identity.ErrNotAliasOwner
	}
	updated, err := manager.IdentityRemoveAddress(record.Alias, addr[:], time.Now().Unix())
	if err != nil {
		return nil, err
	}
	evt := events.IdentityAliasAddressRemoved{Alias: updated.Alias, Address: addr}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return updated, nil
}

func (n *Node) IdentitySetPrimary(owner [20]byte, alias string, addr [20]byte) (*identity.AliasRecord, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.IdentityResolve(alias)
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if !aliasRecordOwnedBy(record, owner) {
		return nil, identity.ErrNotAliasOwner
	}
	if record.Primary == addr {
		return record, nil
	}
	updated, err := manager.IdentitySetPrimary(record.Alias, addr[:], time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if updated.Primary != addr {
		return updated, nil
	}
	evt := events.IdentityAliasPrimaryUpdated{Alias: updated.Alias, Address: addr}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return updated, nil
}

func (n *Node) IdentityRename(owner [20]byte, alias string, newAlias string) (*identity.AliasRecord, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.IdentityResolve(alias)
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if !aliasRecordOwnedBy(record, owner) {
		return nil, identity.ErrNotAliasOwner
	}
	previousAlias := record.Alias
	updated, err := manager.IdentityRename(record.Alias, newAlias, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if previousAlias == updated.Alias {
		return updated, nil
	}
	evt := events.IdentityAliasRenamed{OldAlias: previousAlias, NewAlias: updated.Alias, Address: owner}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return updated, nil
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

// HeartbeatSubmissionMargin is added on top of the configured
// engagement.Config.HeartbeatInterval before EngagementValidatorHeartbeatDue
// will report that another heartbeat submission is due. A periodic ticker
// whose nominal period exactly equals the enforced minimum interval races
// ordinary scheduling/processing jitter: at second-granularity, "elapsed
// since last heartbeat" frequently rounds down to one second less than the
// interval, producing spurious "heartbeat rate limited" rejections purely
// from timing noise rather than any real problem. This margin is small
// relative to the 15-minute validator-readiness grace window
// (validatorReadinessMinGrace in epochs.go), so it costs nothing in
// practice while making the interval comparison immune to jitter on that
// scale.
const HeartbeatSubmissionMargin = 15 * time.Second

// EngagementHeartbeatInterval returns the minimum spacing enforced between
// heartbeats, as configured on the node's engagement manager -- which is
// itself constructed from the same engagement.Config the StateProcessor
// uses for applyHeartbeat's on-chain rate check (see NewNode's
// engagement.NewManager(stateProcessor.EngagementConfig()) call), so the
// two never drift apart. Exposed so callers deciding "is it time to submit
// another heartbeat" against real chain state don't need to hard-code a
// duplicate constant.
func (n *Node) EngagementHeartbeatInterval() time.Duration {
	if n == nil || n.engagementMgr == nil {
		return engagement.DefaultConfig().HeartbeatInterval
	}
	if interval := n.engagementMgr.HeartbeatInterval(); interval > 0 {
		return interval
	}
	return engagement.DefaultConfig().HeartbeatInterval
}

// EngagementValidatorHeartbeatDue reports whether enough time has elapsed,
// per the authoritative on-chain EngagementLastHeartbeat recorded against
// addr's account, to attempt another heartbeat submission as of now.
//
// This exists specifically for the automatic validator heartbeat loop
// (cmd/nhb/main.go's startValidatorHeartbeatLoop). That loop used to decide
// "is it time yet" purely from core/engagement.Manager's local, in-memory
// per-device bookkeeping, which resets to empty on every process restart --
// while the authoritative on-chain EngagementLastHeartbeat does not. That
// mismatch let a freshly-restarted process immediately queue a heartbeat
// transaction that the mempool's deterministic admission-time re-check
// (validateTransaction's scratch-state simulation of applyHeartbeat, which
// evaluates against the real, still-recent on-chain state) would then
// reject as too soon, wasting the attempt. Consulting the real on-chain
// value here instead closes that gap: a restart can never fool this check
// into thinking no heartbeat has happened recently when one actually has.
//
// The local engagement manager's device-registration/token bookkeeping is
// untouched by this and continues to serve its own purpose (authenticating
// the caller and rejecting literal timestamp replay) for every caller,
// including this one and the RPC-exposed manual submission path.
//
// A HeartbeatSubmissionMargin is added on top of the configured interval so
// ordinary scheduling/processing jitter around a periodic ticker can never
// cause the comparison to be satisfied or rejected by second-rounding
// alone -- see HeartbeatSubmissionMargin's doc comment for the underlying
// race this avoids.
func (n *Node) EngagementValidatorHeartbeatDue(addr []byte, now time.Time) (bool, error) {
	if n == nil {
		return false, fmt.Errorf("node unavailable")
	}
	account, err := n.GetAccount(addr)
	if err != nil {
		return false, err
	}
	if account == nil || account.EngagementLastHeartbeat == 0 {
		// No on-chain heartbeat has ever been recorded for this account --
		// nothing to rate-limit against yet, matching applyHeartbeat's own
		// "EngagementLastHeartbeat != 0" guard.
		return true, nil
	}
	minElapsed := n.EngagementHeartbeatInterval() + HeartbeatSubmissionMargin
	elapsed := now.UTC().Unix() - int64(account.EngagementLastHeartbeat)
	return elapsed >= int64(minElapsed.Seconds()), nil
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

	account, err := n.GetAccount(validator.Bytes())
	if err != nil {
		return 0, err
	}
	if account == nil {
		return 0, fmt.Errorf("validator account not found")
	}

	// If a heartbeat at this nonce is still sitting unmined in the mempool
	// (the account nonce has not advanced past it yet), resubmitting at the
	// same gas price is rejected outright by the mempool's replace-by-fee
	// rule ("transaction with nonce %d already exists and fee is not
	// higher"), permanently stranding validator liveness after any missed
	// block since every later tick reconstructs an identical tx. Bump the
	// fee above the pending transaction's so a retry can actually replace
	// it instead of failing forever.
	gasPrice := big.NewInt(1)
	if pendingFee := n.pendingHeartbeatFee(validator.Bytes(), account.Nonce); pendingFee != nil {
		pendingGasPrice := new(big.Int).Div(pendingFee, big.NewInt(21000))
		gasPrice = new(big.Int).Add(pendingGasPrice, big.NewInt(1))
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 21000,
		GasPrice: gasPrice,
	}
	if err := tx.Sign(n.validatorKey.PrivateKey); err != nil {
		return 0, err
	}
	if err := n.AddTransaction(tx); err != nil {
		return 0, err
	}
	return ts, nil
}

// pendingHeartbeatFee returns the total fee (gasPrice * gasLimit) of an
// existing mempool transaction from addr at nonce, if one is already
// pending, so a heartbeat retry can bump its fee instead of being rejected
// by the mempool's replace-by-fee rule.
//
// This must scan the raw mempool (n.mempool, under n.mempoolMu) directly
// rather than going through GetMempool(). GetMempool() exists for BFT
// proposal-building (consensus/bft/bft.go) and the consensus gRPC service
// (consensus/service/server.go's GetMempool handler), and it has a side
// effect that makes it unsuitable as a passive peek: every transaction it
// returns gets marked in n.proposedTxs and is excluded from every
// subsequent call until the block that (attempted to) include it is
// resolved via CreateBlock's own bookkeeping. A validator that isn't
// currently the active block proposer never resolves that bookkeeping
// through its own CreateBlock, so once anything -- an actual proposal
// attempt, or the consensus gRPC endpoint if something polls it -- calls
// GetMempool() once and observes the still-pending heartbeat transaction,
// every later call here would see it as already excluded and return nil.
// That makes gasPrice silently fall back to the default of 1, which
// collides with the still-pending transaction's own price of 1
// ("already exists and fee is not higher"), stranding the account's nonce
// indefinitely -- this was confirmed as the actual root cause of the
// multi-hour stuck-nonce pattern observed in production. Reading
// n.mempool directly has no such side effect and always reflects exactly
// what is physically queued right now, proposed or not.
func (n *Node) pendingHeartbeatFee(addr []byte, nonce uint64) *big.Int {
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	for _, existing := range n.mempool {
		if existing == nil || existing.Nonce != nonce {
			continue
		}
		sender, err := existing.From()
		if err != nil || !bytes.Equal(sender, addr) {
			continue
		}
		if existing.GasPrice == nil {
			return big.NewInt(0)
		}
		return new(big.Int).Mul(existing.GasPrice, new(big.Int).SetUint64(existing.GasLimit))
	}
	return nil
}

// GovernancePropose/GovernanceVote/GovernanceFinalize/GovernanceQueue/
// GovernanceExecute (direct-state-trie writes, bypassing consensus/gossip
// entirely, with no cryptographic proof tying a caller-supplied
// proposer/voter address to the actual caller) have been removed. Their
// replacement is real, signed, consensus-routed transactions -- see
// TxTypeGovPropose/Vote/Finalize/Queue/Execute (core/types/transaction.go)
// and their apply functions (core/governance_tx.go), submitted the same way
// every other signed native transaction type is (nhb_sendTransaction), not
// a bespoke per-feature RPC method.

// governanceAttachLiveTally decorates proposal with a live, on-demand vote
// tally when it is still accepting votes (ProposalStatusVotingPeriod) and
// has not been finalized yet (proposal.Tally == nil). It mirrors exactly
// what governance.Engine.Finalize itself computes via ComputeTally --
// side-effect free, read-only -- so the result reflects votes cast so far
// without ever being persisted: only the in-memory proposal object handed
// back to the RPC caller is mutated. Finalized proposals already carry
// their real, persisted tally (Engine.Finalize attaches it before the final
// GovernancePutProposal, see native/governance/engine.go), so this is a
// no-op for them, and for proposals that haven't entered voting yet
// (deposit_period) or have no state at all.
func (n *Node) governanceAttachLiveTally(manager *nhbstate.Manager, engine *governance.Engine, proposal *governance.Proposal) error {
	if proposal == nil || proposal.Tally != nil || proposal.Status != governance.ProposalStatusVotingPeriod {
		return nil
	}
	votes, err := manager.GovernanceListVotes(proposal.ID)
	if err != nil {
		return err
	}
	tally, _, err := engine.ComputeTally(proposal, votes)
	if err != nil {
		return err
	}
	proposal.Tally = tally
	return nil
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
	engine := n.newGovernanceEngine(manager)
	if err := n.governanceAttachLiveTally(manager, engine, proposal); err != nil {
		return nil, false, err
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

	engine := n.newGovernanceEngine(manager)
	current := start
	for current >= 1 && len(proposals) < limit {
		proposal, ok, err := manager.GovernanceGetProposal(current)
		if err != nil {
			return nil, 0, err
		}
		if ok && proposal != nil {
			if err := n.governanceAttachLiveTally(manager, engine, proposal); err != nil {
				return nil, 0, err
			}
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

// GovernanceFinalize/GovernanceQueue/GovernanceExecute were removed for the
// same reason as GovernancePropose/GovernanceVote above -- see that comment.

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
	// Epoch-scoped leg of the same uptime delta -- see updatePotsoActivity's
	// comment in core/state_transition.go. epoch here is derived from
	// blockHeight (already hash-verified above against the finalized block),
	// never wall-clock time, so this remains a deterministic input to
	// processPotsoRewardEpoch even if this heartbeat is processed late.
	if cfg.EpochLengthBlocks > 0 {
		if err := manager.PotsoMetricsAddEngagement(epoch, addr, 0, 0, delta); err != nil {
			return nil, 0, err
		}
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

// PotsoStakeLock/PotsoStakeUnbond/PotsoStakeWithdraw (direct n.state.Trie
// writes under n.stateMu.Lock(), outside CreateBlock/ApplyTransaction
// entirely) were removed -- stake actions are now real signed transactions
// (TxTypePotsoStakeLock/Unbond/Withdraw, core/potso_stake_tx.go), applied
// identically by every validator via consensus. See that file's doc comment
// for the full rationale, including why the bespoke sha256/secp256k1
// signature + authNonce scheme the old RPC methods verified owner identity
// with (rpc/potso_stake_handlers.go, also removed) is now fully redundant
// with the standard envelope signature and standard account nonce.

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
	chainID := fmt.Sprintf("%d", n.ChainID())
	record, err := manager.CreateClaimable(payer, token, amount, hashLock, deadline, [32]byte{}, chainID, claimable.RecipientKindNone)
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

func (n *Node) IdentityCreateClaimable(payer [20]byte, token string, amount *big.Int, hint [32]byte, deadline int64, recipientKind claimable.RecipientKind) (*claimable.Claimable, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	hash := ethcrypto.Keccak256(hint[:])
	var hashLock [32]byte
	copy(hashLock[:], hash)
	chainID := fmt.Sprintf("%d", n.ChainID())
	record, err := manager.CreateClaimable(payer, token, amount, hashLock, deadline, hint, chainID, recipientKind)
	if err != nil {
		return nil, err
	}
	// NHB-TRIAGE-C6: only publish the hint on-chain when it's already
	// public by construction (an alias-derived pointer -- anyone who knows
	// the target username can compute it themselves, so this discloses
	// nothing). When it's an opaque bearer secret the payer meant to share
	// privately (e.g. by email), broadcasting it here in cleartext would
	// let anyone watching the event log claim it before the intended
	// recipient ever sees it -- so it's omitted for that case.
	publicHint := [32]byte{}
	if record.RecipientKind == claimable.RecipientKindAlias {
		publicHint = record.RecipientHint
	}
	evt := events.ClaimableCreated{
		ID:            record.ID,
		Payer:         record.Payer,
		Token:         record.Token,
		Amount:        record.Amount,
		RecipientHint: publicHint,
		Deadline:      record.Deadline,
		CreatedAt:     record.CreatedAt,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}
	return record, nil
}

// authorizeClaimablePayee is the single authorization gate every claim path
// must pass through before the funds move, closing NHB-TRIAGE-C6 (the
// generic claimable_claim RPC used to call straight into
// Manager.ClaimableClaim with no identity check at all -- strictly weaker
// than identity_claim's already-partial check, and a caller who just wanted
// to steal an alias-addressed claimable could simply use this endpoint
// instead).
//
// For an alias-derived record (RecipientKind == RecipientKindAlias),
// knowledge of the preimage proves nothing -- it's a public hash of the
// recipient's username, not a secret -- so authorization can ONLY come from
// the claimer actually owning that alias, checked against the CURRENT
// registry (not whatever was true when the claimable was created). If the
// alias still isn't registered, the correct outcome is "not yet claimable",
// not "fall back to a check that grants access to whoever happens to know
// a public hash". For every other record (RecipientKindNone: no hint, or a
// genuine opaque bearer secret), no identity check applies -- the hashlock
// alone is, by design, the whole authorization.
func (n *Node) authorizeClaimablePayee(manager *nhbstate.Manager, record *claimable.Claimable, payee [20]byte) error {
	if record == nil || record.RecipientKind != claimable.RecipientKindAlias {
		return nil
	}
	alias, ok := manager.IdentityAliasByID(record.RecipientHint)
	if !ok {
		return fmt.Errorf("%w: recipient alias not registered yet", claimable.ErrUnauthorized)
	}
	resolved, ok := manager.IdentityResolve(alias)
	if !ok || resolved == nil {
		return fmt.Errorf("%w: recipient alias not registered yet", claimable.ErrUnauthorized)
	}
	if resolved.Primary == payee {
		return nil
	}
	for _, addr := range resolved.Addresses {
		if addr == payee {
			return nil
		}
	}
	return claimable.ErrUnauthorized
}

func (n *Node) ClaimableClaim(id [32]byte, preimage []byte, payee [20]byte) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	record, ok := manager.ClaimableGet(id)
	if !ok {
		return claimable.ErrNotFound
	}
	if err := n.authorizeClaimablePayee(manager, record, payee); err != nil {
		return err
	}
	updated, changed, err := manager.ClaimableClaim(id, preimage, payee, time.Now().Unix())
	if err != nil {
		return err
	}
	if !changed {
		return nil
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
	if err := n.authorizeClaimablePayee(manager, record, payee); err != nil {
		return nil, err
	}
	updated, changed, err := manager.ClaimableClaim(id, preimage, payee, time.Now().Unix())
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
	// Cheap, immediate rejection mirroring applyMintTransaction's
	// unconditional ZNHB check (core/state_transition.go) -- this is purely
	// a fast-fail UX convenience so a ZNHB mint attempt gets an instant,
	// descriptive error instead of round-tripping through mempool admission
	// first. It is NOT the enforcement mechanism: applyMintTransaction
	// rejects ZNHB unconditionally regardless of this early return, so the
	// invariant holds even if this node-level convenience check is ever
	// bypassed (e.g. a future caller constructs and submits the TxTypeMint
	// transaction directly instead of going through this method).
	if token == "ZNHB" {
		return "", ErrMintZNHBNotMintable
	}
	if token != "NHB" {
		return "", fmt.Errorf("unsupported token %q", voucher.Token)
	}
	if len(signature) != 65 {
		return "", fmt.Errorf("invalid signature length")
	}
	digest := ethcrypto.Keccak256(canonical)
	if _, err := ethcrypto.SigToPub(digest, signature); err != nil {
		return "", fmt.Errorf("recover signer: %w", err)
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
	hashBytes, err := tx.Hash()
	if err != nil {
		return "", err
	}
	txHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
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

// SwapSubmitVoucher enqueues a fiat-gateway-attested ZNHB mint voucher as a
// signed TxTypeSwapVoucherMint transaction and returns immediately -- it does
// NOT wait for the transaction to be included in a block. This mirrors
// MintWithSignature's contract, and the otc-gateway's existing awaitMinted
// polling loop (services/otc-gateway/server/sign_submit.go) is written to
// tolerate exactly that: it already handles minted=false by polling
// swap_voucher_get / this method's ledger record until the voucher
// transitions to Minted.
//
// CORRECTION (round 3): an earlier version of this comment claimed the
// otc-gateway needed "zero gateway-side changes" for this RPC-shape
// contract. That is false and pre-dates this whole effort (present in
// round 1, round 2, and the original pre-conversion code alike): the
// gateway's request-building code (services/otc-gateway/server/
// sign_submit.go, via swaprpc/client.go's SubmitMintVoucher) actually
// builds and sends a core.MintVoucher-shaped JSON payload -- the shape for
// the unrelated mint_with_sig RPC -- to swap_submitVoucher, whose handler
// (rpc/swap_handlers.go's handleSwapSubmitVoucher) decodes into a
// swap.VoucherV1, a different shape with required domain/orderId/nonce
// fields that MintVoucher's encoding never produces. Every real submission
// from the actual gateway therefore fails immediately at JSON-decode with
// "voucher: domain required", before any of the transaction-type or
// price-proof logic above is ever reached. This is a separate,
// pre-existing, unrelated payload-shape bug in the otc-gateway itself --
// out of scope for the swap-voucher-mint TxType conversion this file
// implements -- and still needs its own fix before the fiat-onramp flow
// works end-to-end in production.
//
// Prior to this, SwapSubmitVoucher performed a direct, non-consensus
// state-trie write inside this RPC handler: its duplicate-submission check
// (ledger.Exists / HasSeenSwapNonce) only ever saw ONE validator's own local
// state, so the same fiat voucher reaching two validators independently
// could mint ZNHB twice. Routing through AddTransaction -> mempool -> gossip
// -> ApplyTransaction makes the duplicate check a real, network-wide-agreed
// check: see applySwapVoucherMintTransaction in swap_voucher_tx.go for the
// deterministic execution path every validator now runs identically.
//
// Only shallow, stateless shape validation happens here (domain, chain id,
// basic field presence, signature length) -- exactly mirroring
// MintWithSignature's own shallow pre-checks. Every stateful check (fiat
// allow-list, provider allow-list, mint-authority signer match, mandatory
// price-proof signature verification, slippage, risk limits, sanctions,
// duplicate providerTxID/orderID) lives solely in
// applySwapVoucherMintTransaction so there is exactly one implementation of
// the business rules -- AddTransaction synchronously simulates that same
// deterministic path via validateTransaction before admitting the
// transaction to the mempool, so malformed/invalid submissions still fail
// synchronously from the caller's point of view; only successful
// submissions become asynchronous (enqueued, not yet minted).
func (n *Node) SwapSubmitVoucher(submission *swap.VoucherSubmission) (string, bool, error) {
	if submission == nil || submission.Voucher == nil {
		return "", false, fmt.Errorf("swap: voucher required")
	}
	voucher := submission.Voucher
	if strings.TrimSpace(voucher.Domain) != swap.VoucherDomainV1 {
		return "", false, ErrSwapInvalidDomain
	}
	if voucher.ChainID != n.chain.ChainID() {
		return "", false, ErrSwapInvalidChainID
	}
	if voucher.Expiry <= n.currentTime().Unix() {
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
	if len(submission.Signature) != 65 {
		return "", false, ErrSwapInvalidSignature
	}
	token := strings.ToUpper(strings.TrimSpace(voucher.Token))
	if token != "ZNHB" {
		return "", false, ErrSwapInvalidToken
	}

	payload, err := encodeSwapVoucherMintTransaction(submission)
	if err != nil {
		return "", false, err
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapVoucherMint,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	hashBytes, err := tx.Hash()
	if err != nil {
		return "", false, err
	}
	txHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
	if err := n.AddTransaction(tx); err != nil {
		// Every ErrSwap* sentinel raised by applySwapVoucherMintTransaction
		// (via AddTransaction's synchronous simulation, or the mempool-level
		// duplicate check below) propagates through errors.Is regardless of
		// how many times it was wrapped, so callers checking
		// errors.Is(err, core.ErrSwapXxx) -- e.g. rpc/swap_handlers.go's
		// existing error-to-HTTP-status switch -- keep working unchanged.
		return "", false, err
	}

	return txHash, false, nil
}

// buybackRefPricePayload mirrors, field-for-field and in the same order,
// the anonymous decode-side struct in core/buyback_tx.go's
// applyBuybackRefPrice -- RLP encodes/decodes structs positionally, so this
// shape must never drift from that one. Unexported: constructing this is an
// internal encoding detail of SubmitBuybackRefPrice, not part of the Node
// API surface.
type buybackRefPricePayload struct {
	RateNum    *big.Int
	RateDenom  *big.Int
	Epoch      uint64
	Timestamp  uint64
	Signatures [][]byte
}

// CurrentBuybackEpoch returns the treasury buyback engine's current open
// epoch bucket -- the epoch a reference-price submission must target right
// now to land before that epoch's settlement (see
// core/buyback_settlement.go's currentBuybackEpoch doc comment). The second
// return value is false if epoch scheduling isn't enabled on this network
// (EpochLengthBlocks == 0) or the chain hasn't produced its first block yet.
//
// Deliberately does NOT delegate to StateProcessor.currentBuybackEpoch(),
// which reads sp.execContext.height -- a value only populated transiently
// while a block is actively being processed (BeginBlock/EndBlock), and zero
// otherwise. A standalone status read like this one needs the same stable,
// externally-observable height AddTransaction's own synchronous simulation
// validates a submission against: n.chain.GetHeight()+1 (see
// validateTransaction). Using any other height source here would let this
// method report a different "current epoch" than what a real submission
// actually gets checked against.
func (n *Node) CurrentBuybackEpoch() (uint64, bool) {
	if n == nil {
		return 0, false
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return 0, false
	}
	length := n.state.epochConfig.Length
	if length == 0 {
		return 0, false
	}
	var height uint64
	if n.chain != nil {
		height = n.chain.GetHeight() + 1
	}
	if height == 0 {
		return 0, false
	}
	return (height + length - 1) / length, true
}

// BuybackRefPriceStatus is a JSON-safe snapshot of whatever reference-price
// record (if any) is currently on file for the requested epoch. All
// big.Int-scale fields are decimal strings for the same reason
// ZNHBTokenomicsState's are: JSON's float64 numbers silently lose precision
// on values this large.
type BuybackRefPriceStatus struct {
	Epoch       uint64 `json:"epoch"`
	HasRefPrice bool   `json:"hasRefPrice"`
	RateNum     string `json:"rateNum,omitempty"`
	RateDenom   string `json:"rateDenom,omitempty"`
	TimestampAt uint64 `json:"timestampAt,omitempty"`
	SignerCount int    `json:"signerCount,omitempty"`
}

// BuybackRefPriceStatus reports whether epoch already has a verified
// reference price on file -- a submission service should check this before
// signing and submitting, both to avoid a wasted submission (only the first
// per epoch is ever accepted, see applyBuybackRefPrice) and to confirm a
// prior submission actually landed.
func (n *Node) BuybackRefPriceStatusForEpoch(epoch uint64) (*BuybackRefPriceStatus, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	rec, ok, err := manager.BuybackRefPriceForEpoch(epoch)
	if err != nil {
		return nil, fmt.Errorf("buyback: load reference price status: %w", err)
	}
	status := &BuybackRefPriceStatus{Epoch: epoch, HasRefPrice: ok}
	if ok && rec != nil {
		if rec.RateNum != nil {
			status.RateNum = rec.RateNum.String()
		}
		if rec.RateDenom != nil {
			status.RateDenom = rec.RateDenom.String()
		}
		status.TimestampAt = rec.TimestampAt
		status.SignerCount = len(rec.Signers)
	}
	return status, nil
}

// SubmitBuybackRefPrice constructs and injects a TxTypeBuybackRefPrice
// transaction carrying the supplied M-of-N signature bundle -- mirroring
// SwapSubmitVoucher's pattern exactly, since both are senderless,
// envelope-unsigned transaction types whose real authorization lives
// entirely inside tx.Data (see core/buyback_tx.go's applyBuybackRefPrice
// doc comment). Signature verification against the genesis-immutable
// signer quorum happens there, synchronously, during AddTransaction's
// simulation -- this method does no verification of its own and trusts
// nothing about the signatures beyond what that consensus code checks.
func (n *Node) SubmitBuybackRefPrice(rateNum, rateDenom *big.Int, epoch, timestamp uint64, signatures [][]byte) (string, error) {
	if rateNum == nil || rateNum.Sign() <= 0 || rateDenom == nil || rateDenom.Sign() <= 0 {
		return "", fmt.Errorf("buyback: rate must be a positive fraction")
	}
	if epoch == 0 {
		return "", fmt.Errorf("buyback: epoch required")
	}
	if timestamp == 0 {
		return "", fmt.Errorf("buyback: timestamp required")
	}
	if len(signatures) == 0 {
		return "", fmt.Errorf("buyback: at least one signature required")
	}
	payload, err := rlp.EncodeToBytes(buybackRefPricePayload{
		RateNum:    new(big.Int).Set(rateNum),
		RateDenom:  new(big.Int).Set(rateDenom),
		Epoch:      epoch,
		Timestamp:  timestamp,
		Signatures: signatures,
	})
	if err != nil {
		return "", fmt.Errorf("buyback: encode payload: %w", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeBuybackRefPrice,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	hashBytes, err := tx.Hash()
	if err != nil {
		return "", fmt.Errorf("buyback: hash transaction: %w", err)
	}
	txHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
	if err := n.AddTransaction(tx); err != nil {
		return "", err
	}
	return txHash, nil
}

// lendingRefPricePayload mirrors, field-for-field and in the same order, the
// anonymous decode-side struct in core/lending_tx.go's
// applyLendingRefPriceTransaction -- RLP encodes/decodes structs
// positionally, so this shape must never drift from that one. Unlike
// buybackRefPricePayload there is no Epoch: lending's oracle price is not
// epoch-gated (see core/lending_tx.go's domain-separation doc comment).
// Unexported: constructing this is an internal encoding detail of
// SubmitLendingRefPrice, not part of the Node API surface.
type lendingRefPricePayload struct {
	RateNum    *big.Int
	RateDenom  *big.Int
	Timestamp  uint64
	Signatures [][]byte
}

// LendingRefPriceStatus is a JSON-safe snapshot of the most recently
// accepted lending oracle reference price, if any submission has ever been
// recorded. All big.Int-scale fields are decimal strings for the same
// reason BuybackRefPriceStatus's are.
type LendingRefPriceStatus struct {
	HasRefPrice  bool   `json:"hasRefPrice"`
	RateNum      string `json:"rateNum,omitempty"`
	RateDenom    string `json:"rateDenom,omitempty"`
	Timestamp    uint64 `json:"timestamp,omitempty"`
	SignerCount  int    `json:"signerCount,omitempty"`
	AppliedBlock uint64 `json:"appliedBlock,omitempty"`
	MarketCount  uint64 `json:"marketCount,omitempty"`
}

// LendingRefPriceStatus reports the most recently accepted lending oracle
// reference price -- a submission service should check this before signing
// and submitting, both to confirm a prior submission actually landed and to
// read back the Timestamp a new submission's own Timestamp must exceed (see
// applyLendingRefPriceTransaction's replay-protection check).
func (n *Node) LendingRefPriceStatus() (*LendingRefPriceStatus, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	rec, ok, err := manager.LendingRefPriceLast()
	if err != nil {
		return nil, fmt.Errorf("lending: load reference price status: %w", err)
	}
	status := &LendingRefPriceStatus{HasRefPrice: ok}
	if ok && rec != nil {
		if rec.RateNum != nil {
			status.RateNum = rec.RateNum.String()
		}
		if rec.RateDenom != nil {
			status.RateDenom = rec.RateDenom.String()
		}
		status.Timestamp = rec.Timestamp
		status.SignerCount = len(rec.Signers)
		status.AppliedBlock = rec.AppliedBlock
		status.MarketCount = rec.MarketCount
	}
	return status, nil
}

// SubmitLendingRefPrice constructs and injects a TxTypeLendingRefPrice
// transaction carrying the supplied M-of-N signature bundle -- mirroring
// SubmitBuybackRefPrice's pattern exactly, since both are senderless,
// envelope-unsigned transaction types whose real authorization lives
// entirely inside tx.Data (see core/lending_tx.go's
// applyLendingRefPriceTransaction doc comment). Signature verification
// against the genesis-immutable signer quorum happens there, synchronously,
// during AddTransaction's simulation -- this method does no verification of
// its own and trusts nothing about the signatures beyond what that
// consensus code checks.
func (n *Node) SubmitLendingRefPrice(rateNum, rateDenom *big.Int, timestamp uint64, signatures [][]byte) (string, error) {
	if rateNum == nil || rateNum.Sign() <= 0 || rateDenom == nil || rateDenom.Sign() <= 0 {
		return "", fmt.Errorf("lendingRefPrice: rate must be a positive fraction")
	}
	if timestamp == 0 {
		return "", fmt.Errorf("lendingRefPrice: timestamp required")
	}
	if len(signatures) == 0 {
		return "", fmt.Errorf("lendingRefPrice: at least one signature required")
	}
	payload, err := rlp.EncodeToBytes(lendingRefPricePayload{
		RateNum:    new(big.Int).Set(rateNum),
		RateDenom:  new(big.Int).Set(rateDenom),
		Timestamp:  timestamp,
		Signatures: signatures,
	})
	if err != nil {
		return "", fmt.Errorf("lendingRefPrice: encode payload: %w", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingRefPrice,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	hashBytes, err := tx.Hash()
	if err != nil {
		return "", fmt.Errorf("lendingRefPrice: hash transaction: %w", err)
	}
	txHash := "0x" + strings.ToLower(hex.EncodeToString(hashBytes))
	if err := n.AddTransaction(tx); err != nil {
		return "", err
	}
	return txHash, nil
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

// SwapRiskParams returns the currently-effective circuit-breaker caps for
// the swap redeem direction (swap-out burn, TxTypeRedeemNHB): the governance
// param store's value if a policy.swapRiskParams proposal has ever executed,
// otherwise the conservative built-in default -- see
// native/swap/redeem_risk.go's Default* constants, read fresh from state on
// every call. Backs the public swap_getRiskParams RPC method: unlike
// SwapLimits (which is address-specific and gated behind requireAuthInto),
// this reports only network-wide parameters, so any caller -- including a
// governance UI drafting a proposal -- can see the current values.
//
// There is no mint-side (fiat-gateway voucher mint, TxTypeSwapVoucherMint)
// equivalent: those vouchers draw ZNHB from a fixed, pre-allocated genesis
// treasury Sale Pool rather than minting new supply (see
// core/swap_voucher_tx.go's applySwapVoucherMintTransaction), so they carry
// no external financial risk needing a governance-adjustable circuit
// breaker -- only the NHB-custody-backed redeem direction does.
func (n *Node) SwapRiskParams() (swap.RedeemRiskParameters, error) {
	var redeem swap.RedeemRiskParameters
	err := n.WithState(func(m *nhbstate.Manager) error {
		resolved, err := n.state.effectiveRedeemRiskParameters(m)
		if err != nil {
			return err
		}
		redeem = resolved
		return nil
	})
	if err != nil {
		return swap.RedeemRiskParameters{}, err
	}
	return redeem, nil
}

// LendingFixedTermRateSchedule returns the currently-effective tenure->rate
// table for fixed-term lending borrows: the governance param store's value
// if a policy.lendingRateSchedule proposal has ever executed, otherwise the
// conservative built-in default -- see native/lending.DefaultFixedTermRateSchedule,
// read fresh from state on every call. Mirrors SwapRiskParams's precedent
// exactly: network-wide only, no account-specific data, safe to expose
// publicly so a governance UI can display the current schedule before
// drafting a proposal.
func (n *Node) LendingFixedTermRateSchedule() (lending.TenureRateSchedule, error) {
	var schedule lending.TenureRateSchedule
	err := n.WithState(func(m *nhbstate.Manager) error {
		resolved, err := n.state.effectiveFixedTermRateSchedule(m)
		if err != nil {
			return err
		}
		schedule = resolved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return schedule, nil
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

// ListPendingRedemptions returns every currently pending TxTypeRedeemNHB
// swap-out request, backing the swap_listPendingRedemptions RPC method. This
// is how an off-chain watcher (payments-gateway) discovers burns awaiting an
// off-chain USDT payout -- see core/state/redemption.go's pending-request
// index and rpc/swap_redemption_handlers.go.
func (n *Node) ListPendingRedemptions() ([]*nhbstate.StoredRedemptionRequest, error) {
	var requests []*nhbstate.StoredRedemptionRequest
	err := n.WithState(func(m *nhbstate.Manager) error {
		list, err := m.PendingRedemptionRequests()
		if err != nil {
			return err
		}
		requests = list
		return nil
	})
	if err != nil {
		return nil, err
	}
	return requests, nil
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

// WithStateView runs fn against a disposable copy of the current state trie.
// Any writes fn performs land only in that copy and are discarded when it
// returns -- the live pending state (which feeds the next self-proposed
// block's root) is never touched. Use this for read-only RPC handlers that
// need to compute derived/migrated views of state (e.g. lazily replaying
// legacy records) without risking a state-root fork: a write made through
// WithState from a query handler becomes part of this validator's next
// block proposal even though no other validator executed the query, so
// their independently computed root would diverge from this one's.
func (n *Node) WithStateView(fn func(*nhbstate.Manager) error) error {
	if fn == nil {
		return fmt.Errorf("state callback required")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	view, err := n.state.Copy()
	if err != nil {
		return err
	}
	manager := nhbstate.NewManager(view.Trie)
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

// AdminWallet exposes the genesis-declared admin/treasury wallet address, if
// the loaded genesis file configured one.
func (n *Node) AdminWallet() ([20]byte, bool) {
	return n.chain.AdminWallet()
}

// ConfigureAdminWalletForTests wires up a genesis-equivalent admin/treasury
// wallet for tests in other packages that need to exercise ZNHB-purchase or
// swap-voucher-mint paths gated on the admin wallet, without constructing a
// full genesis file. It sets the in-memory admin wallet, credits it with the
// full 1,000,000,000 ZNHB genesis supply, and runs the one-time Sale/Reward
// Pool bootstrap split -- mirroring exactly what NewNode does when a real
// genesis file declares an admin wallet. Production code never calls this;
// it always derives the admin wallet from genesis (see NewNode).
func (n *Node) ConfigureAdminWalletForTests(addr [20]byte) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	n.state.SetAdminWallet(addr, true)
	// Keep the Blockchain-level copy in sync too -- a real genesis load
	// populates both (see blockchain.go's genesis-processing paths), but
	// this ephemeral test-only path only touched the state processor's
	// copy until this fix, leaving Blockchain.AdminWallet() (what RPC
	// history-building reads) permanently unconfigured under this helper.
	n.chain.SetAdminWalletForTests(addr)
	manager := nhbstate.NewManager(n.state.Trie)
	account := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}
	if err := manager.PutAccount(addr[:], account); err != nil {
		return fmt.Errorf("seed admin wallet balance: %w", err)
	}
	return n.state.EnsureZNHBPoolsBootstrapped()
}

// SetSubscriptionsConfig wires native/subscriptions' management-fee rate,
// retry/dunning schedule, and treasury address into the state processor.
// Called once from cmd/nhb/main.go/cmd/consensusd/main.go, reading
// config.toml's [subscriptions] section -- mirrors SetLendingRiskParameters'
// role for the lending module.
func (n *Node) SetSubscriptionsConfig(cfg subscriptions.Config) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	return n.state.SetSubscriptionsConfig(cfg)
}

// ConfigureBuybackForTests wires up a genesis-equivalent treasury buyback
// signer quorum for tests in other packages that need to exercise
// SubmitBuybackRefPrice / BuybackRefPriceStatusForEpoch without constructing
// a full genesis file. Production code never calls this; it always derives
// the signer quorum from genesis (see NewNode's BuybackSignerConfig read).
func (n *Node) ConfigureBuybackForTests(cfg buyback.Config) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	return n.state.SetBuybackConfig(cfg)
}

// ConfigureEpochLengthForTests sets the epoch length (in blocks) for tests
// in other packages that need CurrentBuybackEpoch/the buyback engine to see
// a real open epoch without constructing a full genesis file or config.toml
// (both of which is where EpochLengthBlocks normally comes from -- see
// NewNode's config.EpochLengthBlocks read). Production code never calls
// this.
func (n *Node) ConfigureEpochLengthForTests(length uint64) error {
	if n == nil {
		return fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil {
		return fmt.Errorf("state unavailable")
	}
	cfg := n.state.EpochConfig()
	cfg.Length = length
	return n.state.SetEpochConfig(cfg)
}

// ZNHBTokenomicsState is the public, read-only snapshot of the Genesis
// Treasury Distribution Curve's live state -- everything needed to
// independently recompute the current treasury price without trusting
// anyone's word for it. Exposed via the znhb_getTokenomicsState RPC method.
type ZNHBTokenomicsState struct {
	// CurrentTranchePrice is the exact spot price, in USD-equivalent NHB,
	// of the tranche the treasury is currently selling from -- formatted
	// as a decimal string (never a float) to preserve exactness.
	CurrentTranchePrice string `json:"currentTranchePrice"`
	// CurrentTrancheIndex is which of the 16,000 tranches is currently
	// active, and FullySoldOut is true once the Sale Pool is exhausted
	// (CurrentTranchePrice then reports the terminal price instead).
	CurrentTrancheIndex uint64 `json:"currentTrancheIndex"`
	FullySoldOut        bool   `json:"fullySoldOut"`
	// CumulativeSaleDistributed is the exact attoZNHB counter the curve is
	// priced against (0 to the Sale Pool's 800,000,000 ZNHB cap).
	CumulativeSaleDistributed string `json:"cumulativeSaleDistributedWei"`
	SalePoolBalanceWei        string `json:"salePoolBalanceWei"`
	RewardPoolBalanceWei      string `json:"rewardPoolBalanceWei"`
	BuybackAccrualBalanceWei  string `json:"buybackAccrualBalanceWei"`
}

// GetZNHBTokenomicsState returns the live, independently-verifiable state
// of the Genesis Treasury Distribution Curve. Returns zero-valued fields,
// not an error, if the ZNHB pools have never been bootstrapped (e.g. no
// admin wallet configured on this network).
func (n *Node) GetZNHBTokenomicsState() (*ZNHBTokenomicsState, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		return nil, fmt.Errorf("load cumulative sale distributed: %w", err)
	}
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		return nil, fmt.Errorf("load sale pool balance: %w", err)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		return nil, fmt.Errorf("load reward pool balance: %w", err)
	}
	buybackAccrual, err := manager.ZNHBBuybackAccrualBalance()
	if err != nil {
		return nil, fmt.Errorf("load buyback accrual balance: %w", err)
	}

	params := curve.Default()
	state := &ZNHBTokenomicsState{
		CumulativeSaleDistributed: cumulative.String(),
		SalePoolBalanceWei:        salePool.String(),
		RewardPoolBalanceWei:      rewardPool.String(),
		BuybackAccrualBalanceWei:  buybackAccrual.String(),
	}
	if cumulative.Cmp(params.SalePoolCapWei()) >= 0 {
		state.FullySoldOut = true
		state.CurrentTrancheIndex = params.TrancheCount
		state.CurrentTranchePrice = params.TerminalPrice().FloatString(curve.Decimals)
		return state, nil
	}
	idx := params.TrancheIndexFor(cumulative)
	price, err := params.TranchePrice(idx)
	if err != nil {
		return nil, fmt.Errorf("compute current tranche price: %w", err)
	}
	state.CurrentTrancheIndex = idx
	state.CurrentTranchePrice = price.FloatString(curve.Decimals)
	return state, nil
}

// ZNHBBuyQuote is the public, read-only quote for buying a specific amount
// of ZNHB from the treasury Sale Pool: the exact NHB cost applyBuyZNHB
// would charge right now, computed with the same curve.Cost math the
// on-chain transition itself uses. Callers (e.g. nhbportal) use this to
// build a buyZNHB transaction's MaxNHBAmount without ever having to
// replicate the Genesis Treasury Distribution Curve client-side.
type ZNHBBuyQuote struct {
	ZNHBAmountWei string `json:"znhbAmountWei"`
	// NHBCostWei is rounded the same protocol-favoring direction (up)
	// applyBuyZNHB itself uses (curve.RoundCostUp) -- it is the exact
	// amount that transaction will charge if submitted immediately, not
	// an approximation.
	NHBCostWei string `json:"nhbCostWei"`
	// EffectiveRate is NHBCostWei/ZNHBAmountWei as a decimal string, purely
	// for display -- callers must use NHBCostWei for the actual on-chain
	// slippage cap, not a value recomputed from this rate.
	EffectiveRate string `json:"effectiveRate"`
}

// QuoteBuyZNHB returns the live cost of buying znhbAmount (attoZNHB) from
// the treasury Sale Pool at the curve's current position. Returns
// curve.ErrExceedsSalePool (unwrapped, check with errors.Is) if the
// requested amount exceeds the Sale Pool's remaining inventory.
func (n *Node) QuoteBuyZNHB(znhbAmount *big.Int) (*ZNHBBuyQuote, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	if znhbAmount == nil || znhbAmount.Sign() <= 0 {
		return nil, fmt.Errorf("znhbAmount must be positive")
	}
	n.stateMu.RLock()
	defer n.stateMu.RUnlock()
	if n.state == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	manager := nhbstate.NewManager(n.state.Trie)
	c0, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		return nil, fmt.Errorf("load cumulative sale distributed: %w", err)
	}
	c1 := new(big.Int).Add(c0, znhbAmount)

	params := curve.Default()
	costRat, err := params.Cost(c0, c1)
	if err != nil {
		return nil, err
	}
	nhbCost := curve.RoundCostUp(costRat)
	effectiveRate := new(big.Rat).SetFrac(nhbCost, znhbAmount)
	return &ZNHBBuyQuote{
		ZNHBAmountWei: znhbAmount.String(),
		NHBCostWei:    nhbCost.String(),
		EffectiveRate: effectiveRate.FloatString(curve.Decimals),
	}, nil
}

// GetLastCommitHash returns a commit hash/seed (used by BFT proposer selection).
func (n *Node) GetLastCommitHash() []byte {
	return n.chain.Tip()
}

// Chain returns a reference to the node's blockchain object.
func (n *Node) Chain() *Blockchain {
	return n.chain
}
