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

	"nhbchain/consensus/bft"
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
	"nhbchain/native/escrow"
	"nhbchain/native/governance"
	"nhbchain/native/loyalty"
	"nhbchain/native/potso"
	swap "nhbchain/native/swap"
	"nhbchain/p2p"
	"nhbchain/storage"
	"nhbchain/storage/trie"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// Node is the central controller, wiring all components together.
type Node struct {
	db                 storage.Database
	state              *StateProcessor
	chain              *Blockchain
	syncMgr            *syncmgr.Manager
	validatorKey       *crypto.PrivateKey
	mempool            []*types.Transaction
	bftEngine          *bft.Engine
	p2pSrv             *p2p.Server
	stateMu            sync.Mutex
	escrowTreasury     [20]byte
	engagementMgr      *engagement.Manager
	govPolicy          governance.ProposalPolicy
	govPolicyMu        sync.RWMutex
	swapCfgMu          sync.RWMutex
	swapCfg            swap.Config
	swapOracle         swap.PriceOracle
	swapManual         *swap.ManualOracle
	swapSanctions      swap.SanctionsChecker
	swapStatusMu       sync.RWMutex
	swapOracleLast     int64
	swapRefundSink     [20]byte
	evidenceStore      *evidence.Store
	evidenceMaxAge     uint64
	paymasterMu        sync.RWMutex
	paymasterEnabled   bool
	timestampTolerance time.Duration
	timeConfigMu       sync.RWMutex
	timeSource         func() time.Time
}

const rolePaymasterAdmin = "ROLE_PAYMASTER_ADMIN"

var ErrPaymasterUnauthorized = errors.New("paymaster: caller lacks ROLE_PAYMASTER_ADMIN")

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

	node := &Node{
		db:                 db,
		state:              stateProcessor,
		chain:              chain,
		validatorKey:       key,
		mempool:            make([]*types.Transaction, 0),
		escrowTreasury:     treasury,
		engagementMgr:      engagement.NewManager(stateProcessor.EngagementConfig()),
		swapSanctions:      swap.DefaultSanctionsChecker,
		swapRefundSink:     treasury,
		evidenceStore:      evidence.NewStore(db),
		evidenceMaxAge:     evidence.DefaultMaxAgeBlocks,
		paymasterEnabled:   stateProcessor.PaymasterEnabled(),
		timestampTolerance: DefaultBlockTimestampTolerance,
		timeSource:         func() time.Time { return time.Now().UTC() },
	}

	// Initialise fast-sync manager.
	if trieDB := stateTrie.TrieDB(); trieDB != nil {
		mgr := syncmgr.NewManager(chain.ChainID(), chain.Height(), trieDB)
		mgr.SetValidatorSet(buildValidatorSet(stateProcessor.ValidatorSet))
		node.syncMgr = mgr
	}

	return node, nil
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

// SetP2PServer records the running P2P server for query purposes.
func (n *Node) SetP2PServer(server *p2p.Server) {
	n.p2pSrv = server
}

// P2PServer exposes the underlying P2P server if available.
func (n *Node) P2PServer() *p2p.Server {
	return n.p2pSrv
}

func (n *Node) StartConsensus() {
	if n.bftEngine != nil {
		n.bftEngine.Start()
	}
}

// HandleMessage is the central router for all incoming P2P messages.
// It satisfies the p2p.MessageHandler interface.
func (n *Node) HandleMessage(msg *p2p.Message) error {
	switch msg.Type {
	case p2p.MsgTypeTx:
		tx := new(types.Transaction)
		if err := json.Unmarshal(msg.Payload, tx); err != nil {
			return err
		}
		n.AddTransaction(tx)

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

func (n *Node) AddTransaction(tx *types.Transaction) {
	n.mempool = append(n.mempool, tx)
}

// --- Methods for bft.NodeInterface ---

func (n *Node) GetMempool() []*types.Transaction {
	txs := make([]*types.Transaction, len(n.mempool))
	copy(txs, n.mempool)
	n.mempool = []*types.Transaction{} // drain after read
	return txs
}

func (n *Node) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	header := &types.BlockHeader{
		Height:    n.chain.GetHeight() + 1,
		Timestamp: n.currentTime().Unix(),
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
	stateCopy, err := n.state.Copy()
	if err != nil {
		return nil, err
	}
	blockTime := time.Unix(header.Timestamp, 0).UTC()
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

func (n *Node) CommitBlock(b *types.Block) error {
	// Remember parent root for rollback on any failure
	parentRoot := n.state.CurrentRoot()
	rollback := func() error {
		if err := n.state.ResetToRoot(parentRoot); err != nil {
			return fmt.Errorf("rollback to parent root: %w", err)
		}
		return nil
	}

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

	blockTime := time.Unix(b.Header.Timestamp, 0).UTC()
	n.state.BeginBlock(b.Header.Height, blockTime)
	defer n.state.EndBlock()

	// Apply transactions; abort (and rollback) on the first failure
	for i, tx := range b.Transactions {
		if err := n.state.ApplyTransaction(tx); err != nil {
			if rbErr := rollback(); rbErr != nil {
				return fmt.Errorf("apply transaction %d: %v (rollback failed: %w)", i, err, rbErr)
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

func (n *Node) GetValidatorSet() map[string]*big.Int { return n.state.ValidatorSet }
func (n *Node) GetHeight() uint64                    { return n.chain.Height() }

// PotsoSubmitEvidence validates and persists a misbehaviour report.
func (n *Node) PotsoSubmitEvidence(ev evidence.Evidence) (*evidence.Receipt, error) {
	if n == nil {
		return nil, fmt.Errorf("node not initialised")
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
	if err := n.state.ResetToRoot(root); err != nil {
		return common.Hash{}, err
	}
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
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.SetPotsoRewardConfig(cfg)
}

func (n *Node) PotsoWeightConfig() potso.WeightParams {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.PotsoWeightConfig()
}

func (n *Node) SetPotsoWeightConfig(cfg potso.WeightParams) error {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.SetPotsoWeightConfig(cfg)
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
	return loyalty.NewRegistry(n.LoyaltyManager())
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

func (n *Node) newEscrowEngine(manager *nhbstate.Manager) *escrow.Engine {
	engine := escrow.NewEngine()
	engine.SetState(manager)
	engine.SetEmitter(escrowEventEmitter{node: n})
	engine.SetFeeTreasury(n.escrowTreasury)
	return engine
}

func (n *Node) newTradeEngine(manager *nhbstate.Manager) *escrow.TradeEngine {
	escrowEngine := n.newEscrowEngine(manager)
	tradeEngine := escrow.NewTradeEngine(escrowEngine)
	tradeEngine.SetState(manager)
	tradeEngine.SetEmitter(escrowEventEmitter{node: n})
	return tradeEngine
}

func (n *Node) EscrowCreate(payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, mediator *[20]byte, meta [32]byte, realm string) ([32]byte, error) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)
	engine := n.newEscrowEngine(manager)
	esc, err := engine.Create(payer, payee, token, amount, feeBps, deadline, mediator, meta, realm)
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

func (n *Node) P2PCreateTrade(offerID string, buyer, seller [20]byte,
	baseToken string, baseAmt *big.Int,
	quoteToken string, quoteAmt *big.Int,
	deadline int64) (tradeID [32]byte, escrowBaseID, escrowQuoteID [32]byte, err error) {

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

	trade, err := tradeEngine.CreateTrade(trimmedOffer, buyer, seller, normalizedQuote, quoteAmt, normalizedBase, baseAmt, deadline, nonce)
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
	n.mempool = append(n.mempool, tx)
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

// MintWithSignature credits funds to the recipient described in the voucher after
// verifying the off-chain signature and replay protection fields.
func (n *Node) MintWithSignature(voucher *MintVoucher, signature []byte) (string, error) {
	if voucher == nil {
		return "", fmt.Errorf("voucher required")
	}
	amount, err := voucher.AmountBig()
	if err != nil {
		return "", err
	}
	if voucher.ChainID != MintChainID {
		return "", ErrMintInvalidChainID
	}
	if voucher.Expiry <= time.Now().Unix() {
		return "", ErrMintExpired
	}
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := ethcrypto.Keccak256(canonical)
	if len(signature) != 65 {
		return "", fmt.Errorf("invalid signature length")
	}
	pubKey, err := ethcrypto.SigToPub(digest, signature)
	if err != nil {
		return "", fmt.Errorf("recover signer: %w", err)
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
		return "", fmt.Errorf("unsupported token %q", voucher.Token)
	}

	invoiceID := voucher.TrimmedInvoiceID()
	if invoiceID == "" {
		return "", fmt.Errorf("invoiceId required")
	}
	recipientRef := voucher.TrimmedRecipient()
	if recipientRef == "" {
		return "", fmt.Errorf("recipient required")
	}

	txHashBytes := ethcrypto.Keccak256(append([]byte{}, append(canonical, signature...)...))
	txHash := "0x" + strings.ToLower(hex.EncodeToString(txHashBytes))

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	manager := nhbstate.NewManager(n.state.Trie)

	if !manager.HasRole(requiredRole, recoveredBytes[:]) {
		return "", ErrMintInvalidSigner
	}

	key := nhbstate.MintInvoiceKey(invoiceID)
	var used bool
	if ok, err := manager.KVGet(key, &used); err != nil {
		return "", err
	} else if ok && used {
		return "", ErrMintInvoiceUsed
	}

	var recipient [20]byte
	if decoded, err := crypto.DecodeAddress(recipientRef); err == nil {
		copy(recipient[:], decoded.Bytes())
	} else {
		resolved, ok := manager.IdentityResolve(recipientRef)
		if !ok || resolved == nil {
			return "", fmt.Errorf("recipient not found: %s", recipientRef)
		}
		recipient = resolved.Primary
	}

	account, err := manager.GetAccount(recipient[:])
	if err != nil {
		return "", err
	}
	switch token {
	case "NHB":
		account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
	if err := manager.PutAccount(recipient[:], account); err != nil {
		return "", err
	}
	if err := manager.KVPut(key, true); err != nil {
		return "", err
	}

	evt := events.MintSettled{
		InvoiceID: invoiceID,
		Recipient: recipient,
		Token:     token,
		Amount:    amount,
		TxHash:    txHash,
	}.Event()
	if evt != nil {
		n.state.AppendEvent(evt)
	}

	return txHash, nil
}

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
	token := strings.ToUpper(strings.TrimSpace(voucher.Token))
	if token != "ZNHB" && token != "NHB" {
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
	quote, err := oracle.GetRate("USD", token)
	if err != nil {
		if errors.Is(err, swap.ErrNoFreshQuote) {
			return "", false, ErrSwapQuoteStale
		}
		return "", false, fmt.Errorf("swap: oracle: %w", err)
	}
	n.recordSwapOracleHealth(time.Now())
	twapWindow := cfg.TwapWindow()
	var (
		twapRate          string
		twapCount         int
		twapWindowSeconds int64
		twapStart         int64
		twapEnd           int64
		oracleMedian      string
		oracleFeeders     []string
		priceProofID      string
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

	if err := riskEngine.RecordMint(voucher.Recipient, voucher.Amount); err != nil {
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
