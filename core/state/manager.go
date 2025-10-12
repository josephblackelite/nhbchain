package state

import (
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/identity"
	"nhbchain/crypto"
	"nhbchain/native/creator"
	"nhbchain/native/escrow"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/loyalty"
	"nhbchain/native/potso"
	swap "nhbchain/native/swap"
	"nhbchain/storage/trie"
)

// Manager provides a minimal interface for reading and writing state data during
// genesis initialisation.
type Manager struct {
	trie *trie.Trie
}

// NewManager creates a state manager operating on the provided trie.
func NewManager(tr *trie.Trie) *Manager {
	return &Manager{trie: tr}
}

type TokenMetadata struct {
	Symbol        string
	Name          string
	Decimals      uint8
	MintAuthority []byte
	MintPaused    bool
}

var (
	tokenPrefix                    = []byte("token:")
	tokenListKey                   = ethcrypto.Keccak256([]byte("token-list"))
	balancePrefix                  = []byte("balance:")
	rolePrefix                     = []byte("role:")
	loyaltyGlobalKeyBytes          = ethcrypto.Keccak256([]byte("loyalty:global"))
	loyaltyDynamicStateKeyBytes    = ethcrypto.Keccak256([]byte("loyalty:dynamic-state"))
	loyaltyDailyPrefix             = []byte("loyalty-meter:base-daily:")
	loyaltyTotalPrefix             = []byte("loyalty-meter:base-total:")
        loyaltyProgramDailyPrefix      = []byte("loyalty-meter:program-daily:")
        loyaltyProgramDailyTotalPrefix = []byte("loyalty-meter:program-daily-total:")
        loyaltyProgramEpochPrefix      = []byte("loyalty-meter:program-epoch:")
        loyaltyProgramIssuancePrefix   = []byte("loyalty-meter:program-issuance:")
        loyaltyBusinessPrefix          = []byte("loyalty/business/")
        loyaltyBusinessOwnerPrefix     = []byte("loyalty/business-owner/")
        loyaltyMerchantIndexPrefix     = []byte("loyalty/merchant-index/")
        loyaltyBusinessCounterKey      = []byte("loyalty/business/counter")
        loyaltyOwnerPaymasterPref      = []byte("loyalty/owner-paymaster/")
        loyaltyDayPrefix               = []byte("loyalty/day/")
	escrowRecordPrefix             = []byte("escrow/record/")
	escrowVaultPrefix              = []byte("escrow/vault/")
	escrowModuleSeedPrefix         = "module/escrow/vault/"
	escrowRealmPrefix              = []byte("escrow/realm/")
	escrowFrozenPolicyPrefix       = []byte("escrow/frozen/")
	creatorContentPrefix           = []byte("creator/content/")
	creatorStakePrefix             = []byte("creator/stake/")
	creatorLedgerPrefix            = []byte("creator/ledger/")
	creatorRateLimitPrefix         = []byte("creator/rate-limit")
	claimableRecordPrefix          = []byte("claimable/record/")
	claimableNoncePrefix           = []byte("claimable/nonce/")
	tradeRecordPrefix              = []byte("trade/record/")
	tradeEscrowIndexPrefix         = []byte("trade/index/escrow/")
	identityAliasPrefix            = []byte("identity/alias/")
	identityAliasIDPrefix          = []byte("identity/alias-id/")
	identityReversePrefix          = []byte("identity/reverse/")
	mintInvoicePrefix              = []byte("mint/invoice/")
	swapOrderPrefix                = []byte("swap/order/")
	swapPriceSignerPrefix          = []byte("swap/oracle/signer/")
	swapPriceProofPrefix           = []byte("swap/oracle/last/")
	potsoHeartbeatPrefix           = []byte("potso/heartbeat/")
	potsoMeterPrefix               = []byte("potso/meter/")
	potsoDayIndexPrefix            = []byte("potso/day-index/")
	potsoStakeTotalPrefix          = []byte("potso/stake/")
	potsoStakeNoncePrefix          = []byte("potso/stake/nonce/")
	potsoStakeAuthNoncePrefix      = []byte("potso/stake/authnonce/")
	potsoStakeLocksPrefix          = []byte("potso/stake/locks/")
	potsoStakeLockIndexPrefix      = []byte("potso/stake/locks/index/")
	potsoStakeQueuePrefix          = []byte("potso/stake/unbondq/")
	potsoStakeModuleSeedPrefix     = "module/potso/stake/vault"
	potsoStakeOwnerIndexKey        = []byte("potso/stake/owners")
	potsoRewardLastProcessed       = []byte("potso/rewards/lastProcessed")
	potsoRewardMetaKeyFormat       = "potso/rewards/epoch/%d/meta"
	potsoRewardWinnersFormat       = "potso/rewards/epoch/%d/winners"
	potsoRewardPayoutFormat        = "potso/rewards/epoch/%d/payout/%x"
	potsoRewardClaimFormat         = "potso/rewards/epoch/%d/claim/%x"
	potsoRewardHistoryFormat       = "potso/rewards/history/%x"
	potsoMetricsMeterPrefix        = []byte("potso/metrics/meter/")
	potsoMetricsIndexPrefix        = []byte("potso/metrics/index/")
	potsoMetricsSnapshotPrefix     = []byte("potso/metrics/snapshot/")
	governanceProposalPrefix       = []byte("gov/proposals/")
	governanceVotePrefix           = []byte("gov/votes/")
	governanceVoteIndexPrefix      = []byte("gov/vote-index/")
	governanceSequenceKey          = []byte("gov/seq")
	governanceAuditPrefix          = []byte("gov/audit/")
	governanceAuditSequenceKey     = []byte("gov/audit-seq")
	governanceEscrowPrefix         = []byte("gov/escrow/")
	paramsNamespacePrefix          = []byte("params/")
	snapshotPotsoPrefix            = []byte("snapshots/potso/")
	lendingMarketPrefix            = []byte("lending/market/")
	lendingFeeAccrualPrefix        = []byte("lending/fees/")
	lendingUserPrefix              = []byte("lending/user/")
	lendingPoolIndexKey            = []byte("lending/pools/index")
	feesCounterPrefix              = []byte("fees/counter/")
	feesTotalsPrefix               = []byte("fees/totals/")
	feesTotalsIndexPrefix          = []byte("fees/totals/index/")
)

// StakingGlobalIndexKey returns the deterministic storage key for the global
// staking index accumulator. The value stored at this key represents the
// protocol-wide reward index used to calculate user shares.
func StakingGlobalIndexKey() []byte {
	return append([]byte(nil), stakingGlobalIndexKeyBytes...)
}

// StakingLastIndexUpdateTsKey resolves the key storing the block timestamp
// (uint64 seconds) when the global staking index was most recently updated.
func StakingLastIndexUpdateTsKey() []byte {
	return append([]byte(nil), stakingLastIndexUpdateTsKeyByte...)
}

// StakingEmissionYTDKey constructs the year-scoped key for tracking staking
// emissions produced within a calendar year. The year is encoded in
// zero-padded decimal form to keep lexicographic ordering stable across
// clients.
func StakingEmissionYTDKey(year uint32) []byte {
	return []byte(fmt.Sprintf(stakingEmissionYTDKeyFormat, year))
}

// StakingAcctKey composes the storage key holding a snapshot of a delegator's
// staking metadata. Account addresses are appended verbatim to avoid encoding
// ambiguity across clients.
func StakingAcctKey(addr []byte) []byte {
	key := make([]byte, len(stakingAccountPrefix)+len(addr))
	copy(key, stakingAccountPrefix)
	copy(key[len(stakingAccountPrefix):], addr)
	return key
}

// GetGlobalIndex retrieves the persisted protocol-wide staking index metadata.
// When no snapshot has been recorded yet the function returns a zeroed
// structure with default big.Int instances to avoid shared references.
func (m *Manager) GetGlobalIndex() (*GlobalIndex, error) {
	if m == nil {
		return (&storedGlobalIndex{}).toGlobalIndex(), nil
	}
	data, err := m.trie.Get(StakingGlobalIndexKey())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return (&storedGlobalIndex{}).toGlobalIndex(), nil
	}
	var stored storedGlobalIndex
	if err := rlp.DecodeBytes(data, &stored); err != nil {
		legacy := new(big.Int).SetBytes(data)
		if legacy == nil {
			return (&storedGlobalIndex{}).toGlobalIndex(), nil
		}
		stored.UQ128x128 = encodeUQ128x128(legacy)
		return stored.toGlobalIndex(), nil
	}
	return stored.toGlobalIndex(), nil
}

// PutGlobalIndex persists the supplied staking index snapshot.
func (m *Manager) PutGlobalIndex(idx *GlobalIndex) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	stored := newStoredGlobalIndex(idx)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(StakingGlobalIndexKey(), encoded)
}

// GetStakingSnap loads the staking snapshot for the supplied address. Missing
// records return zeroed structures with default big.Int fields.
func (m *Manager) GetStakingSnap(addr []byte) (*AccountSnap, error) {
	if m == nil {
		return (&storedAccountSnap{}).toAccountSnap(), nil
	}
	key := StakingAcctKey(addr)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return (&storedAccountSnap{}).toAccountSnap(), nil
	}
	var stored storedAccountSnap
	if err := rlp.DecodeBytes(data, &stored); err != nil {
		return nil, err
	}
	return stored.toAccountSnap(), nil
}

// PutStakingSnap writes the staking account snapshot for the provided address.
func (m *Manager) PutStakingSnap(addr []byte, snap *AccountSnap) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	stored := newStoredAccountSnap(snap)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(StakingAcctKey(addr), encoded)
}

// StakingGlobalIndex returns the protocol-wide staking reward index accumulator.
// Callers should treat the returned value as read-only.
func (m *Manager) StakingGlobalIndex() (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), nil
	}
	value, err := m.loadBigInt(StakingGlobalIndexKey())
	if err != nil {
		return nil, err
	}
	if value == nil {
		return big.NewInt(0), nil
	}
	return value, nil
}

// SetStakingGlobalIndex overwrites the global staking reward index accumulator.
func (m *Manager) SetStakingGlobalIndex(value *big.Int) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	return m.writeBigInt(StakingGlobalIndexKey(), value)
}

// StakingEmissionYTD fetches the cumulative staking rewards minted within the
// provided calendar year. When no record is present the function returns zero.
func (m *Manager) StakingEmissionYTD(year uint32) (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), nil
	}
	return m.loadBigInt(StakingEmissionYTDKey(year))
}

// SetStakingEmissionYTD overwrites the recorded year-to-date staking emission
// total for the supplied calendar year.
func (m *Manager) SetStakingEmissionYTD(year uint32, total *big.Int) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	return m.writeBigInt(StakingEmissionYTDKey(year), total)
}

// IncrementStakingEmissionYTD adds the provided delta to the stored
// year-to-date staking emission counter and returns the updated total.
func (m *Manager) IncrementStakingEmissionYTD(year uint32, delta *big.Int) (*big.Int, error) {
	if m == nil {
		return big.NewInt(0), fmt.Errorf("state manager unavailable")
	}
	if delta == nil {
		delta = big.NewInt(0)
	}
	if delta.Sign() < 0 {
		return nil, fmt.Errorf("emission delta must be non-negative")
	}
	current, err := m.StakingEmissionYTD(year)
	if err != nil {
		return nil, err
	}
	updated := new(big.Int).Add(current, delta)
	if err := m.SetStakingEmissionYTD(year, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// GovernanceProposalKey constructs the storage key for the proposal metadata
// record under the governance namespace. Proposal identifiers are formatted in
// decimal to align with human-facing tooling and avoid accidental prefix
// collisions.
func GovernanceProposalKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", governanceProposalPrefix, id))
}

// GovernanceVoteKey builds the composite key for a vote entry beneath a
// proposal. Voter addresses are appended in hexadecimal form to provide stable
// ordering across clients regardless of Bech32 prefix usage.
func GovernanceVoteKey(id uint64, voter []byte) []byte {
	return []byte(fmt.Sprintf("%s%d/%x", governanceVotePrefix, id, voter))
}

func governanceVoteIndexKey(id uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", governanceVoteIndexPrefix, id))
}

// GovernanceSequenceKey returns the auto-increment sequence key used to mint
// proposal identifiers within the governance module.
func GovernanceSequenceKey() []byte {
	return append([]byte(nil), governanceSequenceKey...)
}

// GovernanceEscrowKey resolves the deposit escrow bucket for a governance
// participant. Escrow balances are denominated in ZNHB and tracked per account.
func GovernanceEscrowKey(addr []byte) []byte {
	key := make([]byte, len(governanceEscrowPrefix)+len(addr))
	copy(key, governanceEscrowPrefix)
	copy(key[len(governanceEscrowPrefix):], addr)
	return key
}

func governanceAuditKey(seq uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", governanceAuditPrefix, seq))
}

type storedGovernanceProposal struct {
	ID             uint64
	Title          string
	Summary        string
	MetadataURI    string
	Submitter      [20]byte
	Status         uint8
	Deposit        *big.Int
	SubmitTime     uint64
	VotingStart    uint64
	VotingEnd      uint64
	TimelockEnd    uint64
	Target         string
	ProposedChange string
	Queued         bool
}

type storedGovernanceVote struct {
	ProposalID uint64
	Voter      [20]byte
	Choice     string
	PowerBps   uint32
	Timestamp  uint64
}

type storedGovernanceAudit struct {
	Sequence   uint64
	Timestamp  uint64
	Event      string
	ProposalID uint64
	Actor      string
	Details    string
}

func newStoredGovernanceAudit(r *governance.AuditRecord) *storedGovernanceAudit {
	if r == nil {
		return nil
	}
	stored := &storedGovernanceAudit{
		Sequence:   r.Sequence,
		Event:      string(r.Event),
		ProposalID: r.ProposalID,
		Actor:      strings.TrimSpace(string(r.Actor)),
		Details:    strings.TrimSpace(r.Details),
	}
	if !r.Timestamp.IsZero() {
		stored.Timestamp = uint64(r.Timestamp.Unix())
	}
	return stored
}

func (s *storedGovernanceAudit) toAuditRecord() (*governance.AuditRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("governance: nil audit record")
	}
	record := &governance.AuditRecord{
		Sequence:   s.Sequence,
		Event:      governance.AuditEvent(strings.TrimSpace(s.Event)),
		ProposalID: s.ProposalID,
		Actor:      governance.AddressText(strings.TrimSpace(s.Actor)),
		Details:    strings.TrimSpace(s.Details),
	}
	if s.Timestamp != 0 {
		record.Timestamp = time.Unix(int64(s.Timestamp), 0).UTC()
	}
	return record, nil
}

func newStoredGovernanceProposal(p *governance.Proposal) *storedGovernanceProposal {
	if p == nil {
		return nil
	}
	deposit := big.NewInt(0)
	if p.Deposit != nil {
		deposit = new(big.Int).Set(p.Deposit)
	}
	var submitter [20]byte
	if bytes := p.Submitter.Bytes(); len(bytes) == 20 {
		copy(submitter[:], bytes)
	}
	return &storedGovernanceProposal{
		ID:             p.ID,
		Title:          p.Title,
		Summary:        p.Summary,
		MetadataURI:    p.MetadataURI,
		Submitter:      submitter,
		Status:         uint8(p.Status),
		Deposit:        deposit,
		SubmitTime:     uint64(p.SubmitTime.Unix()),
		VotingStart:    uint64(p.VotingStart.Unix()),
		VotingEnd:      uint64(p.VotingEnd.Unix()),
		TimelockEnd:    uint64(p.TimelockEnd.Unix()),
		Target:         p.Target,
		ProposedChange: p.ProposedChange,
		Queued:         p.Queued,
	}
}

func (s *storedGovernanceProposal) toGovernanceProposal() (*governance.Proposal, error) {
	if s == nil {
		return nil, fmt.Errorf("governance: nil proposal record")
	}
	status := governance.ProposalStatus(s.Status)
	if !validProposalStatus(status) {
		return nil, fmt.Errorf("governance: invalid proposal status")
	}
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.Submitter[:]...))
	deposit := big.NewInt(0)
	if s.Deposit != nil {
		deposit = new(big.Int).Set(s.Deposit)
	}
	proposal := &governance.Proposal{
		ID:             s.ID,
		Title:          s.Title,
		Summary:        s.Summary,
		MetadataURI:    s.MetadataURI,
		Submitter:      submitter,
		Status:         status,
		Deposit:        deposit,
		SubmitTime:     time.Unix(int64(s.SubmitTime), 0).UTC(),
		VotingStart:    time.Unix(int64(s.VotingStart), 0).UTC(),
		VotingEnd:      time.Unix(int64(s.VotingEnd), 0).UTC(),
		TimelockEnd:    time.Unix(int64(s.TimelockEnd), 0).UTC(),
		Target:         s.Target,
		ProposedChange: s.ProposedChange,
		Queued:         s.Queued,
	}
	return proposal, nil
}

func newStoredGovernanceVote(v *governance.Vote) *storedGovernanceVote {
	if v == nil {
		return nil
	}
	var voter [20]byte
	if bytes := v.Voter.Bytes(); len(bytes) == 20 {
		copy(voter[:], bytes)
	}
	return &storedGovernanceVote{
		ProposalID: v.ProposalID,
		Voter:      voter,
		Choice:     v.Choice.String(),
		PowerBps:   v.PowerBps,
		Timestamp:  uint64(v.Timestamp.Unix()),
	}
}

func (s *storedGovernanceVote) toGovernanceVote() (*governance.Vote, error) {
	if s == nil {
		return nil, fmt.Errorf("governance: nil vote record")
	}
	choice := governance.VoteChoice(s.Choice)
	if !choice.Valid() {
		return nil, fmt.Errorf("governance: invalid vote choice %q", s.Choice)
	}
	voter := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.Voter[:]...))
	vote := &governance.Vote{
		ProposalID: s.ProposalID,
		Voter:      voter,
		Choice:     choice,
		PowerBps:   s.PowerBps,
		Timestamp:  time.Unix(int64(s.Timestamp), 0).UTC(),
	}
	return vote, nil
}

func validProposalStatus(status governance.ProposalStatus) bool {
	switch status {
	case governance.ProposalStatusUnspecified,
		governance.ProposalStatusDepositPeriod,
		governance.ProposalStatusVotingPeriod,
		governance.ProposalStatusPassed,
		governance.ProposalStatusRejected,
		governance.ProposalStatusFailed,
		governance.ProposalStatusExpired,
		governance.ProposalStatusExecuted:
		return true
	default:
		return false
	}
}

// GovernanceEscrowBalance returns the current escrowed deposit balance for the
// supplied address. When no balance is present the method returns zero without
// an error.
func (m *Manager) GovernanceEscrowBalance(addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("governance: address must not be empty")
	}
	key := GovernanceEscrowKey(addr)
	balance := new(big.Int)
	ok, err := m.KVGet(key, balance)
	if err != nil {
		return nil, err
	}
	if !ok {
		return big.NewInt(0), nil
	}
	return balance, nil
}

// GovernanceEscrowLock adds the provided amount to the participant's escrow
// bucket and returns the updated balance. Negative amounts are rejected to
// protect against accidental unlock attempts.
func (m *Manager) GovernanceEscrowLock(addr []byte, amount *big.Int) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("governance: address must not be empty")
	}
	lockAmount := big.NewInt(0)
	if amount != nil {
		if amount.Sign() < 0 {
			return nil, fmt.Errorf("governance: lock amount must not be negative")
		}
		lockAmount = new(big.Int).Set(amount)
	}
	current, err := m.GovernanceEscrowBalance(addr)
	if err != nil {
		return nil, err
	}
	updated := new(big.Int).Add(current, lockAmount)
	if err := m.KVPut(GovernanceEscrowKey(addr), updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// GovernanceEscrowUnlock subtracts the provided amount from the participant's
// escrow bucket. Unlock attempts that exceed the current balance are rejected.
func (m *Manager) GovernanceEscrowUnlock(addr []byte, amount *big.Int) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("governance: address must not be empty")
	}
	unlockAmount := big.NewInt(0)
	if amount != nil {
		if amount.Sign() < 0 {
			return nil, fmt.Errorf("governance: unlock amount must not be negative")
		}
		unlockAmount = new(big.Int).Set(amount)
	}
	current, err := m.GovernanceEscrowBalance(addr)
	if err != nil {
		return nil, err
	}
	if current.Cmp(unlockAmount) < 0 {
		return nil, fmt.Errorf("governance: unlock exceeds escrow balance")
	}
	updated := new(big.Int).Sub(current, unlockAmount)
	if err := m.KVPut(GovernanceEscrowKey(addr), updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// GovernanceNextProposalID increments and returns the next proposal identifier.
// The sequence is stored as a uint64 counter beneath the governance namespace.
func (m *Manager) GovernanceNextProposalID() (uint64, error) {
	key := GovernanceSequenceKey()
	var current uint64
	ok, err := m.KVGet(key, &current)
	if err != nil {
		return 0, err
	}
	if ok && current == math.MaxUint64 {
		return 0, fmt.Errorf("governance: proposal sequence overflow")
	}
	next := current + 1
	if err := m.KVPut(key, next); err != nil {
		return 0, err
	}
	return next, nil
}

// GovernancePutProposal stores the provided proposal metadata under the
// canonical governance namespace.
func (m *Manager) GovernancePutProposal(p *governance.Proposal) error {
	if p == nil {
		return fmt.Errorf("governance: proposal must not be nil")
	}
	if !validProposalStatus(p.Status) {
		return fmt.Errorf("governance: invalid proposal status")
	}
	record := newStoredGovernanceProposal(p)
	if record == nil {
		return fmt.Errorf("governance: unable to store proposal")
	}
	return m.KVPut(GovernanceProposalKey(p.ID), record)
}

// GovernancePutVote stores or updates the recorded vote for the proposal.
func (m *Manager) GovernancePutVote(v *governance.Vote) error {
	if v == nil {
		return fmt.Errorf("governance: vote must not be nil")
	}
	if !v.Choice.Valid() {
		return fmt.Errorf("governance: invalid vote choice")
	}
	record := newStoredGovernanceVote(v)
	if record == nil {
		return fmt.Errorf("governance: unable to store vote")
	}
	voterBytes := v.Voter.Bytes()
	if len(voterBytes) != 20 {
		return fmt.Errorf("governance: voter address must be 20 bytes")
	}
	if err := m.KVPut(GovernanceVoteKey(v.ProposalID, voterBytes), record); err != nil {
		return err
	}
	indexKey := governanceVoteIndexKey(v.ProposalID)
	var existing []storedGovernanceVote
	ok, err := m.KVGet(indexKey, &existing)
	if err != nil {
		return err
	}
	if ok {
		replaced := false
		for i := range existing {
			if existing[i].Voter == record.Voter {
				existing[i] = *record
				replaced = true
				break
			}
		}
		if !replaced {
			existing = append(existing, *record)
		}
	} else {
		existing = append(existing, *record)
	}
	return m.KVPut(indexKey, existing)
}

func (m *Manager) GovernanceAppendAudit(r *governance.AuditRecord) (*governance.AuditRecord, error) {
	if r == nil {
		return nil, fmt.Errorf("governance: audit record must not be nil")
	}
	var current uint64
	ok, err := m.KVGet(governanceAuditSequenceKey, &current)
	if err != nil {
		return nil, err
	}
	if ok && current == math.MaxUint64 {
		return nil, fmt.Errorf("governance: audit sequence overflow")
	}
	next := current + 1
	r.Sequence = next
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	stored := newStoredGovernanceAudit(r)
	if stored == nil {
		return nil, fmt.Errorf("governance: unable to persist audit entry")
	}
	if err := m.KVPut(governanceAuditKey(next), stored); err != nil {
		return nil, err
	}
	if err := m.KVPut(governanceAuditSequenceKey, next); err != nil {
		return nil, err
	}
	return stored.toAuditRecord()
}

// GovernanceListVotes returns all stored votes for the proposal identifier.
func (m *Manager) GovernanceListVotes(id uint64) ([]*governance.Vote, error) {
	var stored []storedGovernanceVote
	ok, err := m.KVGet(governanceVoteIndexKey(id), &stored)
	if err != nil {
		return nil, err
	}
	if !ok || len(stored) == 0 {
		return nil, nil
	}
	votes := make([]*governance.Vote, 0, len(stored))
	for i := range stored {
		vote, err := stored[i].toGovernanceVote()
		if err != nil {
			return nil, err
		}
		votes = append(votes, vote)
	}
	return votes, nil
}

// GovernanceGetVote retrieves the stored vote for the given proposal and voter.
func (m *Manager) GovernanceGetVote(id uint64, voter []byte) (*governance.Vote, bool, error) {
	key := GovernanceVoteKey(id, voter)
	var stored storedGovernanceVote
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	vote, err := stored.toGovernanceVote()
	if err != nil {
		return nil, false, err
	}
	return vote, true, nil
}

// GovernanceGetProposal retrieves the stored proposal metadata if present. The
// boolean return indicates whether the proposal exists in state.
func (m *Manager) GovernanceGetProposal(id uint64) (*governance.Proposal, bool, error) {
	key := GovernanceProposalKey(id)
	var stored storedGovernanceProposal
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	proposal, err := stored.toGovernanceProposal()
	if err != nil {
		return nil, false, err
	}
	return proposal, true, nil
}

// ParamStoreKey provides the canonical namespace for governable on-chain
// parameters. Keys are stored verbatim to simplify compatibility with
// higher-level configuration tooling.
func ParamStoreKey(name string) []byte {
	key := make([]byte, len(paramsNamespacePrefix)+len(name))
	copy(key, paramsNamespacePrefix)
	copy(key[len(paramsNamespacePrefix):], name)
	return key
}

// ParamStoreSet writes the provided value under the parameter namespace. Values
// are stored verbatim (RLP byte slices) to preserve caller encoding such as
// JSON literals used by governance proposals.
func (m *Manager) ParamStoreSet(name string, value []byte) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("params: key must not be empty")
	}
	stored := append([]byte(nil), value...)
	return m.KVPut(ParamStoreKey(trimmed), stored)
}

// ParamStoreGet retrieves the stored value for the provided parameter key. A
// boolean return of false indicates the parameter has not been initialised.
func (m *Manager) ParamStoreGet(name string) ([]byte, bool, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, false, fmt.Errorf("params: key must not be empty")
	}
	var stored []byte
	ok, err := m.KVGet(ParamStoreKey(trimmed), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), stored...), true, nil
}

// SetMinimumValidatorStake persists the governed minimum validator stake value
// under the parameter store. Values must be strictly positive.
func (m *Manager) SetMinimumValidatorStake(value *big.Int) error {
	if value == nil {
		return fmt.Errorf("params: minimum validator stake must not be nil")
	}
	if value.Sign() <= 0 {
		return fmt.Errorf("params: minimum validator stake must be positive")
	}
	return m.ParamStoreSet(governance.ParamKeyMinimumValidatorStake, []byte(value.String()))
}

// MinimumValidatorStake retrieves the governed minimum validator stake value
// falling back to the legacy default when unset for backwards compatibility.
func (m *Manager) MinimumValidatorStake() (*big.Int, error) {
	raw, ok, err := m.ParamStoreGet(governance.ParamKeyMinimumValidatorStake)
	if err != nil {
		return nil, err
	}
	if !ok {
		return governance.DefaultMinimumValidatorStake(), nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return governance.DefaultMinimumValidatorStake(), nil
	}
	parsed, success := new(big.Int).SetString(trimmed, 10)
	if !success {
		return nil, fmt.Errorf("params: minimum validator stake must be a base-10 integer")
	}
	if parsed.Sign() <= 0 {
		return nil, fmt.Errorf("params: minimum validator stake must be positive")
	}
	return parsed, nil
}

// SnapshotPotsoWeightsKey exposes the read-only snapshot handle for POTSO
// weight checkpoints generated by the epoch module.
func SnapshotPotsoWeightsKey(epoch uint64) []byte {
	return []byte(fmt.Sprintf("%s%d/weights", snapshotPotsoPrefix, epoch))
}

// SnapshotPotsoWeights retrieves the stored composite weights snapshot for the epoch.
func (m *Manager) SnapshotPotsoWeights(epoch uint64) (*potso.StoredWeightSnapshot, bool, error) {
	var snapshot potso.StoredWeightSnapshot
	ok, err := m.KVGet(SnapshotPotsoWeightsKey(epoch), &snapshot)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &snapshot, true, nil
}

func LoyaltyGlobalStorageKey() []byte {
	return append([]byte(nil), loyaltyGlobalKeyBytes...)
}

func LoyaltyDynamicStateKey() []byte {
	return append([]byte(nil), loyaltyDynamicStateKeyBytes...)
}

func LoyaltyBaseDailyMeterKey(addr []byte, day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(loyaltyDailyPrefix)+len(trimmed)+1+len(addr))
	copy(buf, loyaltyDailyPrefix)
	copy(buf[len(loyaltyDailyPrefix):], trimmed)
	buf[len(loyaltyDailyPrefix)+len(trimmed)] = ':'
	copy(buf[len(loyaltyDailyPrefix)+len(trimmed)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyBaseTotalMeterKey(addr []byte) []byte {
	buf := make([]byte, len(loyaltyTotalPrefix)+len(addr))
	copy(buf, loyaltyTotalPrefix)
	copy(buf[len(loyaltyTotalPrefix):], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyProgramDailyMeterKey(id loyalty.ProgramID, addr []byte, day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)+1+len(addr))
	copy(buf, loyaltyProgramDailyPrefix)
	copy(buf[len(loyaltyProgramDailyPrefix):], id[:])
	buf[len(loyaltyProgramDailyPrefix)+len(id)] = ':'
	copy(buf[len(loyaltyProgramDailyPrefix)+len(id)+1:], trimmed)
	buf[len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)] = ':'
	copy(buf[len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyProgramDailyTotalKey(id loyalty.ProgramID, day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(loyaltyProgramDailyTotalPrefix)+len(id)+1+len(trimmed))
	copy(buf, loyaltyProgramDailyTotalPrefix)
	copy(buf[len(loyaltyProgramDailyTotalPrefix):], id[:])
	buf[len(loyaltyProgramDailyTotalPrefix)+len(id)] = ':'
	copy(buf[len(loyaltyProgramDailyTotalPrefix)+len(id)+1:], trimmed)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyProgramEpochKey(id loyalty.ProgramID, epoch uint64) []byte {
	epochStr := strconv.FormatUint(epoch, 10)
	buf := make([]byte, len(loyaltyProgramEpochPrefix)+len(id)+1+len(epochStr))
	copy(buf, loyaltyProgramEpochPrefix)
	copy(buf[len(loyaltyProgramEpochPrefix):], id[:])
	buf[len(loyaltyProgramEpochPrefix)+len(id)] = ':'
	copy(buf[len(loyaltyProgramEpochPrefix)+len(id)+1:], epochStr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyProgramIssuanceKey(id loyalty.ProgramID, addr []byte) []byte {
	buf := make([]byte, len(loyaltyProgramIssuancePrefix)+len(id)+1+len(addr))
	copy(buf, loyaltyProgramIssuancePrefix)
	copy(buf[len(loyaltyProgramIssuancePrefix):], id[:])
	buf[len(loyaltyProgramIssuancePrefix)+len(id)] = ':'
	copy(buf[len(loyaltyProgramIssuancePrefix)+len(id)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyBusinessKey(id loyalty.BusinessID) []byte {
	key := make([]byte, len(loyaltyBusinessPrefix)+len(id))
	copy(key, loyaltyBusinessPrefix)
	copy(key[len(loyaltyBusinessPrefix):], id[:])
	return key
}

func LoyaltyBusinessOwnerKey(owner []byte) []byte {
	key := make([]byte, len(loyaltyBusinessOwnerPrefix)+len(owner))
	copy(key, loyaltyBusinessOwnerPrefix)
	copy(key[len(loyaltyBusinessOwnerPrefix):], owner)
	return key
}

func LoyaltyMerchantIndexKey(addr []byte) []byte {
	key := make([]byte, len(loyaltyMerchantIndexPrefix)+len(addr))
	copy(key, loyaltyMerchantIndexPrefix)
	copy(key[len(loyaltyMerchantIndexPrefix):], addr)
	return key
}

func LoyaltyBusinessCounterKey() []byte {
	return append([]byte(nil), loyaltyBusinessCounterKey...)
}

func LoyaltyOwnerPaymasterKey(owner []byte) []byte {
	key := make([]byte, len(loyaltyOwnerPaymasterPref)+len(owner))
	copy(key, loyaltyOwnerPaymasterPref)
	copy(key[len(loyaltyOwnerPaymasterPref):], owner)
	return key
}

func tokenMetadataKey(symbol string) []byte {
	buf := make([]byte, len(tokenPrefix)+len(symbol))
	copy(buf, tokenPrefix)
	copy(buf[len(tokenPrefix):], symbol)
	return ethcrypto.Keccak256(buf)
}

func balanceKey(addr []byte, symbol string) []byte {
	buf := make([]byte, len(balancePrefix)+len(symbol)+1+len(addr))
	copy(buf, balancePrefix)
	copy(buf[len(balancePrefix):], symbol)
	buf[len(balancePrefix)+len(symbol)] = ':'
	copy(buf[len(balancePrefix)+len(symbol)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func (m *Manager) loadBigInt(key []byte) (*big.Int, error) {
	data, err := m.trie.Get(key)
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

func (m *Manager) writeBigInt(key []byte, amount *big.Int) error {
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
	return m.trie.Update(key, encoded)
}

func roleKey(role string) []byte {
	buf := make([]byte, len(rolePrefix)+len(role))
	copy(buf, rolePrefix)
	copy(buf[len(rolePrefix):], role)
	return ethcrypto.Keccak256(buf)
}

func kvKey(key []byte) []byte {
	return ethcrypto.Keccak256(key)
}

// MintInvoiceKey derives the storage key used to track processed invoice identifiers.
func MintInvoiceKey(invoiceID string) []byte {
	trimmed := strings.TrimSpace(invoiceID)
	buf := make([]byte, len(mintInvoicePrefix)+len(trimmed))
	copy(buf, mintInvoicePrefix)
	copy(buf[len(mintInvoicePrefix):], trimmed)
	return buf
}

func swapOrderKey(orderID string) []byte {
	trimmed := strings.TrimSpace(orderID)
	buf := make([]byte, len(swapOrderPrefix)+len(trimmed))
	copy(buf, swapOrderPrefix)
	copy(buf[len(swapOrderPrefix):], trimmed)
	return buf
}

func swapPriceSignerKey(provider string) []byte {
	trimmed := strings.ToLower(strings.TrimSpace(provider))
	buf := make([]byte, len(swapPriceSignerPrefix)+len(trimmed))
	copy(buf, swapPriceSignerPrefix)
	copy(buf[len(swapPriceSignerPrefix):], trimmed)
	return buf
}

func swapPriceProofKey(base string) []byte {
	trimmed := strings.ToUpper(strings.TrimSpace(base))
	buf := make([]byte, len(swapPriceProofPrefix)+len(trimmed))
	copy(buf, swapPriceProofPrefix)
	copy(buf[len(swapPriceProofPrefix):], trimmed)
	return buf
}

func potsoHeartbeatKey(addr []byte) []byte {
	buf := make([]byte, len(potsoHeartbeatPrefix)+len(addr))
	copy(buf, potsoHeartbeatPrefix)
	copy(buf[len(potsoHeartbeatPrefix):], addr)
	return kvKey(buf)
}

func potsoMeterKey(day string, addr []byte) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(potsoMeterPrefix)+len(trimmed)+1+len(addr))
	copy(buf, potsoMeterPrefix)
	copy(buf[len(potsoMeterPrefix):], trimmed)
	buf[len(potsoMeterPrefix)+len(trimmed)] = ':'
	copy(buf[len(potsoMeterPrefix)+len(trimmed)+1:], addr)
	return kvKey(buf)
}

func potsoDayIndexKey(day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(potsoDayIndexPrefix)+len(trimmed))
	copy(buf, potsoDayIndexPrefix)
	copy(buf[len(potsoDayIndexPrefix):], trimmed)
	return kvKey(buf)
}

func lendingMarketKey(poolID string) []byte {
	trimmed := strings.TrimSpace(poolID)
	buf := make([]byte, len(lendingMarketPrefix)+len(trimmed))
	copy(buf, lendingMarketPrefix)
	copy(buf[len(lendingMarketPrefix):], trimmed)
	return buf
}

func lendingFeeAccrualKey(poolID string) []byte {
	trimmed := strings.TrimSpace(poolID)
	buf := make([]byte, len(lendingFeeAccrualPrefix)+len(trimmed))
	copy(buf, lendingFeeAccrualPrefix)
	copy(buf[len(lendingFeeAccrualPrefix):], trimmed)
	return buf
}

func lendingUserKey(poolID string, addr []byte) []byte {
	trimmed := strings.TrimSpace(poolID)
	buf := make([]byte, len(lendingUserPrefix)+len(trimmed)+1+len(addr))
	copy(buf, lendingUserPrefix)
	copy(buf[len(lendingUserPrefix):], trimmed)
	buf[len(lendingUserPrefix)+len(trimmed)] = ':'
	copy(buf[len(lendingUserPrefix)+len(trimmed)+1:], addr)
	return buf
}

func normalizePoolID(poolID string) (string, error) {
	trimmed := strings.TrimSpace(poolID)
	if trimmed == "" {
		return "", fmt.Errorf("lending: pool id required")
	}
	return trimmed, nil
}

type storedLendingMarket struct {
	PoolID             string
	DeveloperOwner     [20]byte
	DeveloperCollector [20]byte
	DeveloperFeeBps    uint64
	TotalNHBSupplied   *big.Int
	TotalSupplyShares  *big.Int
	TotalNHBBorrowed   *big.Int
	SupplyIndex        *big.Int
	BorrowIndex        *big.Int
	LastUpdateBlock    uint64
	ReserveFactor      uint64
}

type storedLendingFees struct {
	ProtocolFeesWei  *big.Int
	DeveloperFeesWei *big.Int
}

func newStoredLendingMarket(market *lending.Market) *storedLendingMarket {
	if market == nil {
		return nil
	}
	stored := &storedLendingMarket{
		PoolID:          strings.TrimSpace(market.PoolID),
		LastUpdateBlock: market.LastUpdateBlock,
		ReserveFactor:   market.ReserveFactor,
		DeveloperFeeBps: market.DeveloperFeeBps,
	}
	if market.DeveloperOwner.Bytes() != nil {
		copy(stored.DeveloperOwner[:], market.DeveloperOwner.Bytes())
	}
	if market.DeveloperFeeCollector.Bytes() != nil {
		copy(stored.DeveloperCollector[:], market.DeveloperFeeCollector.Bytes())
	}
	if market.TotalNHBSupplied != nil {
		stored.TotalNHBSupplied = new(big.Int).Set(market.TotalNHBSupplied)
	}
	if market.TotalSupplyShares != nil {
		stored.TotalSupplyShares = new(big.Int).Set(market.TotalSupplyShares)
	}
	if market.TotalNHBBorrowed != nil {
		stored.TotalNHBBorrowed = new(big.Int).Set(market.TotalNHBBorrowed)
	}
	if market.SupplyIndex != nil {
		stored.SupplyIndex = new(big.Int).Set(market.SupplyIndex)
	}
	if market.BorrowIndex != nil {
		stored.BorrowIndex = new(big.Int).Set(market.BorrowIndex)
	}
	return stored
}

func (s *storedLendingMarket) toMarket() *lending.Market {
	if s == nil {
		return nil
	}
	market := &lending.Market{
		PoolID:          strings.TrimSpace(s.PoolID),
		LastUpdateBlock: s.LastUpdateBlock,
		ReserveFactor:   s.ReserveFactor,
		DeveloperFeeBps: s.DeveloperFeeBps,
	}
	var zeroAddr [20]byte
	if !bytes.Equal(s.DeveloperOwner[:], zeroAddr[:]) {
		market.DeveloperOwner = crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.DeveloperOwner[:]...))
	}
	if !bytes.Equal(s.DeveloperCollector[:], zeroAddr[:]) {
		market.DeveloperFeeCollector = crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.DeveloperCollector[:]...))
	}
	if s.TotalNHBSupplied != nil {
		market.TotalNHBSupplied = new(big.Int).Set(s.TotalNHBSupplied)
	}
	if s.TotalSupplyShares != nil {
		market.TotalSupplyShares = new(big.Int).Set(s.TotalSupplyShares)
	}
	if s.TotalNHBBorrowed != nil {
		market.TotalNHBBorrowed = new(big.Int).Set(s.TotalNHBBorrowed)
	}
	if s.SupplyIndex != nil {
		market.SupplyIndex = new(big.Int).Set(s.SupplyIndex)
	}
	if s.BorrowIndex != nil {
		market.BorrowIndex = new(big.Int).Set(s.BorrowIndex)
	}
	return market
}

func newStoredLendingFees(fees *lending.FeeAccrual) *storedLendingFees {
	if fees == nil {
		return nil
	}
	stored := &storedLendingFees{}
	if fees.ProtocolFeesWei != nil {
		stored.ProtocolFeesWei = new(big.Int).Set(fees.ProtocolFeesWei)
	}
	if fees.DeveloperFeesWei != nil {
		stored.DeveloperFeesWei = new(big.Int).Set(fees.DeveloperFeesWei)
	}
	return stored
}

func (s *storedLendingFees) toFeeAccrual() *lending.FeeAccrual {
	if s == nil {
		return nil
	}
	fees := &lending.FeeAccrual{}
	if s.ProtocolFeesWei != nil {
		fees.ProtocolFeesWei = new(big.Int).Set(s.ProtocolFeesWei)
	}
	if s.DeveloperFeesWei != nil {
		fees.DeveloperFeesWei = new(big.Int).Set(s.DeveloperFeesWei)
	}
	return fees
}

type storedLendingUser struct {
	Address        [20]byte
	CollateralZNHB *big.Int
	SupplyShares   *big.Int
	DebtNHB        *big.Int
	ScaledDebt     *big.Int
}

func newStoredLendingUser(account *lending.UserAccount) *storedLendingUser {
	if account == nil {
		return nil
	}
	stored := &storedLendingUser{}
	copy(stored.Address[:], account.Address.Bytes())
	if account.CollateralZNHB != nil {
		stored.CollateralZNHB = new(big.Int).Set(account.CollateralZNHB)
	}
	if account.SupplyShares != nil {
		stored.SupplyShares = new(big.Int).Set(account.SupplyShares)
	}
	if account.DebtNHB != nil {
		stored.DebtNHB = new(big.Int).Set(account.DebtNHB)
	}
	if account.ScaledDebt != nil {
		stored.ScaledDebt = new(big.Int).Set(account.ScaledDebt)
	}
	return stored
}

func (s *storedLendingUser) toUserAccount() *lending.UserAccount {
	if s == nil {
		return nil
	}
	account := &lending.UserAccount{
		Address: crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.Address[:]...)),
	}
	if s.CollateralZNHB != nil {
		account.CollateralZNHB = new(big.Int).Set(s.CollateralZNHB)
	}
	if s.SupplyShares != nil {
		account.SupplyShares = new(big.Int).Set(s.SupplyShares)
	}
	if s.DebtNHB != nil {
		account.DebtNHB = new(big.Int).Set(s.DebtNHB)
	}
	if s.ScaledDebt != nil {
		account.ScaledDebt = new(big.Int).Set(s.ScaledDebt)
	}
	return account
}

func (m *Manager) lendingLoadPoolIDs() ([]string, error) {
	var ids []string
	ok, err := m.KVGet(lendingPoolIndexKey, &ids)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []string{}, nil
	}
	return append([]string(nil), ids...), nil
}

func (m *Manager) lendingSavePoolIDs(ids []string) error {
	normalized := make([]string, len(ids))
	for i, id := range ids {
		normalized[i] = strings.TrimSpace(id)
	}
	return m.KVPut(lendingPoolIndexKey, normalized)
}

func (m *Manager) lendingEnsurePoolIndexed(poolID string) error {
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return err
	}
	ids, err := m.lendingLoadPoolIDs()
	if err != nil {
		return err
	}
	for _, existing := range ids {
		if existing == normalized {
			return nil
		}
	}
	ids = append(ids, normalized)
	return m.lendingSavePoolIDs(ids)
}

// LendingListPoolIDs returns the set of pool identifiers currently persisted in
// state.
func (m *Manager) LendingListPoolIDs() ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("state manager unavailable")
	}
	ids, err := m.lendingLoadPoolIDs()
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// LendingListMarkets loads all configured lending markets.
func (m *Manager) LendingListMarkets() ([]*lending.Market, error) {
	ids, err := m.LendingListPoolIDs()
	if err != nil {
		return nil, err
	}
	markets := make([]*lending.Market, 0, len(ids))
	for _, id := range ids {
		market, ok, err := m.LendingGetMarket(id)
		if err != nil {
			return nil, err
		}
		if ok && market != nil {
			markets = append(markets, market)
		}
	}
	return markets, nil
}

// LendingGetMarket loads the lending market state for the provided pool if it
// has been initialised.
func (m *Manager) LendingGetMarket(poolID string) (*lending.Market, bool, error) {
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return nil, false, err
	}
	var stored storedLendingMarket
	ok, err := m.KVGet(lendingMarketKey(normalized), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	market := stored.toMarket()
	if market != nil {
		market.PoolID = normalized
	}
	return market, true, nil
}

// LendingPutMarket persists the supplied lending market snapshot.
func (m *Manager) LendingPutMarket(poolID string, market *lending.Market) error {
	if market == nil {
		return fmt.Errorf("lending: market must not be nil")
	}
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return err
	}
	market.PoolID = normalized
	if err := m.KVPut(lendingMarketKey(normalized), newStoredLendingMarket(market)); err != nil {
		return err
	}
	return m.lendingEnsurePoolIndexed(normalized)
}

// LendingGetFeeAccrual loads the current lending fee accrual totals if present
// for the supplied pool identifier.
func (m *Manager) LendingGetFeeAccrual(poolID string) (*lending.FeeAccrual, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("state manager unavailable")
	}
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return nil, false, err
	}
	var stored storedLendingFees
	ok, err := m.KVGet(lendingFeeAccrualKey(normalized), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return stored.toFeeAccrual(), true, nil
}

// LendingPutFeeAccrual persists the provided lending fee accrual snapshot for
// the supplied pool.
func (m *Manager) LendingPutFeeAccrual(poolID string, fees *lending.FeeAccrual) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	if fees == nil {
		return fmt.Errorf("lending: fee accrual must not be nil")
	}
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return err
	}
	return m.KVPut(lendingFeeAccrualKey(normalized), newStoredLendingFees(fees))
}

// LendingGetUserAccount loads the lending position tracked for the supplied
// address within the provided pool.
func (m *Manager) LendingGetUserAccount(poolID string, addr [20]byte) (*lending.UserAccount, bool, error) {
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return nil, false, err
	}
	key := lendingUserKey(normalized, addr[:])
	var stored storedLendingUser
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return stored.toUserAccount(), true, nil
}

// LendingPutUserAccount stores the lending position for the provided address
// within the supplied pool.
func (m *Manager) LendingPutUserAccount(poolID string, account *lending.UserAccount) error {
	if account == nil {
		return fmt.Errorf("lending: user account must not be nil")
	}
	addr := account.Address
	if addr.Bytes() == nil {
		return fmt.Errorf("lending: user address must be set")
	}
	normalized, err := normalizePoolID(poolID)
	if err != nil {
		return err
	}
	return m.KVPut(lendingUserKey(normalized, addr.Bytes()), newStoredLendingUser(account))
}

func potsoStakeTotalKey(owner []byte) []byte {
	buf := make([]byte, len(potsoStakeTotalPrefix)+len(owner))
	copy(buf, potsoStakeTotalPrefix)
	copy(buf[len(potsoStakeTotalPrefix):], owner)
	return kvKey(buf)
}

func potsoStakeNonceKey(owner []byte) []byte {
	buf := make([]byte, len(potsoStakeNoncePrefix)+len(owner))
	copy(buf, potsoStakeNoncePrefix)
	copy(buf[len(potsoStakeNoncePrefix):], owner)
	return kvKey(buf)
}

func potsoStakeAuthNonceKey(owner []byte) []byte {
	buf := make([]byte, len(potsoStakeAuthNoncePrefix)+len(owner))
	copy(buf, potsoStakeAuthNoncePrefix)
	copy(buf[len(potsoStakeAuthNoncePrefix):], owner)
	return kvKey(buf)
}

func potsoStakeLockIndexKey(owner []byte) []byte {
	buf := make([]byte, len(potsoStakeLockIndexPrefix)+len(owner))
	copy(buf, potsoStakeLockIndexPrefix)
	copy(buf[len(potsoStakeLockIndexPrefix):], owner)
	return kvKey(buf)
}

func potsoStakeLockKey(owner []byte, nonce uint64) []byte {
	nonceStr := strconv.FormatUint(nonce, 10)
	buf := make([]byte, len(potsoStakeLocksPrefix)+len(owner)+1+len(nonceStr))
	copy(buf, potsoStakeLocksPrefix)
	copy(buf[len(potsoStakeLocksPrefix):], owner)
	buf[len(potsoStakeLocksPrefix)+len(owner)] = ':'
	copy(buf[len(potsoStakeLocksPrefix)+len(owner)+1:], nonceStr)
	return kvKey(buf)
}

func potsoStakeQueueKey(day string) []byte {
	trimmed := potso.NormaliseDay(day)
	buf := make([]byte, len(potsoStakeQueuePrefix)+len(trimmed))
	copy(buf, potsoStakeQueuePrefix)
	copy(buf[len(potsoStakeQueuePrefix):], trimmed)
	return kvKey(buf)
}

func potsoMetricsMeterKey(epoch uint64, addr []byte) []byte {
	epochStr := strconv.FormatUint(epoch, 10)
	buf := make([]byte, len(potsoMetricsMeterPrefix)+len(epochStr)+1+len(addr))
	copy(buf, potsoMetricsMeterPrefix)
	copy(buf[len(potsoMetricsMeterPrefix):], epochStr)
	buf[len(potsoMetricsMeterPrefix)+len(epochStr)] = ':'
	copy(buf[len(potsoMetricsMeterPrefix)+len(epochStr)+1:], addr)
	return kvKey(buf)
}

func potsoMetricsIndexKey(epoch uint64) []byte {
	epochStr := strconv.FormatUint(epoch, 10)
	buf := make([]byte, len(potsoMetricsIndexPrefix)+len(epochStr))
	copy(buf, potsoMetricsIndexPrefix)
	copy(buf[len(potsoMetricsIndexPrefix):], epochStr)
	return kvKey(buf)
}

func potsoMetricsSnapshotKey(epoch uint64) []byte {
	epochStr := strconv.FormatUint(epoch, 10)
	buf := make([]byte, len(potsoMetricsSnapshotPrefix)+len(epochStr))
	copy(buf, potsoMetricsSnapshotPrefix)
	copy(buf[len(potsoMetricsSnapshotPrefix):], epochStr)
	return kvKey(buf)
}

func potsoRewardMetaKey(epoch uint64) []byte {
	return []byte(fmt.Sprintf(potsoRewardMetaKeyFormat, epoch))
}

func potsoRewardWinnersKey(epoch uint64) []byte {
	return []byte(fmt.Sprintf(potsoRewardWinnersFormat, epoch))
}

func potsoRewardPayoutKey(epoch uint64, addr []byte) []byte {
	return []byte(fmt.Sprintf(potsoRewardPayoutFormat, epoch, addr))
}

func potsoRewardClaimKey(epoch uint64, addr []byte) []byte {
	return []byte(fmt.Sprintf(potsoRewardClaimFormat, epoch, addr))
}

func potsoRewardHistoryKey(addr []byte) []byte {
	return []byte(fmt.Sprintf(potsoRewardHistoryFormat, addr))
}

// HasSeenSwapNonce reports whether the provided swap order identifier has been processed.
func (m *Manager) HasSeenSwapNonce(orderID string) bool {
	trimmed := strings.TrimSpace(orderID)
	if trimmed == "" {
		return false
	}
	var used bool
	ok, err := m.KVGet(swapOrderKey(trimmed), &used)
	if err != nil {
		return false
	}
	return ok && used
}

// MarkSwapNonce records the supplied order identifier to prevent future replays.
func (m *Manager) MarkSwapNonce(orderID string) error {
	trimmed := strings.TrimSpace(orderID)
	if trimmed == "" {
		return fmt.Errorf("swap: order id must not be empty")
	}
	return m.KVPut(swapOrderKey(trimmed), true)
}

// SwapSetPriceSigner registers the trusted signer address for the provider price proofs.
func (m *Manager) SwapSetPriceSigner(provider string, addr [20]byte) error {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return fmt.Errorf("swap: provider id must not be empty")
	}
	return m.KVPut(swapPriceSignerKey(trimmed), addr[:])
}

// SwapPriceSigner retrieves the configured signer address for the provider price proofs.
func (m *Manager) SwapPriceSigner(provider string) ([20]byte, bool, error) {
	var signer [20]byte
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return signer, false, nil
	}
	var raw []byte
	ok, err := m.KVGet(swapPriceSignerKey(trimmed), &raw)
	if err != nil {
		return signer, false, err
	}
	if !ok {
		return signer, false, nil
	}
	if len(raw) != 20 {
		return signer, false, fmt.Errorf("swap: price signer length invalid")
	}
	copy(signer[:], raw)
	return signer, true, nil
}

// SwapPutPriceProof stores the last accepted price proof for the provided base token.
func (m *Manager) SwapPutPriceProof(base string, record *swap.PriceProofRecord) error {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		return fmt.Errorf("swap: price proof base required")
	}
	stored := struct {
		Rate      string
		Timestamp int64
	}{}
	if record != nil {
		if record.Rate != nil {
			stored.Rate = strings.TrimSpace(record.Rate.FloatString(18))
		}
		if !record.Timestamp.IsZero() {
			stored.Timestamp = record.Timestamp.UTC().Unix()
		}
	}
	return m.KVPut(swapPriceProofKey(trimmed), stored)
}

// SwapLastPriceProof returns the stored proof record for the supplied base token.
func (m *Manager) SwapLastPriceProof(base string) (*swap.PriceProofRecord, bool, error) {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		return nil, false, nil
	}
	var stored struct {
		Rate      string
		Timestamp int64
	}
	ok, err := m.KVGet(swapPriceProofKey(trimmed), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	record := &swap.PriceProofRecord{}
	trimmedRate := strings.TrimSpace(stored.Rate)
	if trimmedRate != "" {
		rat, ok := new(big.Rat).SetString(trimmedRate)
		if !ok {
			return nil, false, fmt.Errorf("swap: invalid stored price proof rate")
		}
		record.Rate = rat
	}
	if stored.Timestamp != 0 {
		record.Timestamp = time.Unix(stored.Timestamp, 0).UTC()
	}
	return record, true, nil
}

// PotsoGetHeartbeat loads the most recent heartbeat state for the supplied participant.
func (m *Manager) PotsoGetHeartbeat(addr [20]byte) (*potso.HeartbeatState, bool, error) {
	var state potso.HeartbeatState
	ok, err := m.KVGet(potsoHeartbeatKey(addr[:]), &state)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return &potso.HeartbeatState{}, false, nil
	}
	return &state, true, nil
}

// PotsoPutHeartbeat persists the supplied heartbeat state for the participant.
func (m *Manager) PotsoPutHeartbeat(addr [20]byte, state *potso.HeartbeatState) error {
	if state == nil {
		return fmt.Errorf("potso: heartbeat state must not be nil")
	}
	return m.KVPut(potsoHeartbeatKey(addr[:]), state)
}

// PotsoGetMeter loads the meter for the provided day and participant.
func (m *Manager) PotsoGetMeter(addr [20]byte, day string) (*potso.Meter, bool, error) {
	var meter potso.Meter
	key := potsoMeterKey(day, addr[:])
	ok, err := m.KVGet(key, &meter)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return &potso.Meter{Day: potso.NormaliseDay(day)}, false, nil
	}
	return &meter, true, nil
}

// PotsoPutMeter stores the supplied meter for the participant and ensures the day index
// contains the address for leaderboard queries.
func (m *Manager) PotsoPutMeter(addr [20]byte, meter *potso.Meter) error {
	if meter == nil {
		return fmt.Errorf("potso: meter must not be nil")
	}
	meter.Day = potso.NormaliseDay(meter.Day)
	meter.RecomputeScore()
	key := potsoMeterKey(meter.Day, addr[:])
	if err := m.KVPut(key, meter); err != nil {
		return err
	}
	return m.PotsoAddParticipant(meter.Day, addr)
}

// PotsoMetricsSetMeter stores the raw engagement meters for an epoch.
func (m *Manager) PotsoMetricsSetMeter(epoch uint64, addr [20]byte, meter *potso.EngagementMeter) error {
	if meter == nil {
		return fmt.Errorf("potso: engagement meter must not be nil")
	}
	key := potsoMetricsMeterKey(epoch, addr[:])
	if err := m.KVPut(key, meter); err != nil {
		return err
	}
	indexKey := potsoMetricsIndexKey(epoch)
	var existing [][]byte
	if err := m.KVGetList(indexKey, &existing); err != nil {
		return err
	}
	for _, entry := range existing {
		if bytes.Equal(entry, addr[:]) {
			return nil
		}
	}
	existing = append(existing, append([]byte(nil), addr[:]...))
	return m.KVPut(indexKey, existing)
}

// PotsoMetricsGetMeter retrieves the stored meter for the given epoch and address.
func (m *Manager) PotsoMetricsGetMeter(epoch uint64, addr [20]byte) (*potso.EngagementMeter, bool, error) {
	var meter potso.EngagementMeter
	ok, err := m.KVGet(potsoMetricsMeterKey(epoch, addr[:]), &meter)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return &potso.EngagementMeter{}, false, nil
	}
	return &meter, true, nil
}

// PotsoMetricsListParticipants lists all addresses tracked for the epoch.
func (m *Manager) PotsoMetricsListParticipants(epoch uint64) ([][20]byte, error) {
	var raw [][]byte
	if err := m.KVGetList(potsoMetricsIndexKey(epoch), &raw); err != nil {
		return nil, err
	}
	result := make([][20]byte, 0, len(raw))
	for _, entry := range raw {
		if len(entry) != 20 {
			continue
		}
		var addr [20]byte
		copy(addr[:], entry)
		result = append(result, addr)
	}
	return result, nil
}

// PotsoMetricsSetSnapshot stores the computed leaderboard snapshot for an epoch.
func (m *Manager) PotsoMetricsSetSnapshot(epoch uint64, snapshot *potso.StoredWeightSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("potso: weight snapshot must not be nil")
	}
	return m.KVPut(potsoMetricsSnapshotKey(epoch), snapshot)
}

// PotsoMetricsGetSnapshot returns the stored leaderboard snapshot for the epoch.
func (m *Manager) PotsoMetricsGetSnapshot(epoch uint64) (*potso.StoredWeightSnapshot, bool, error) {
	var snapshot potso.StoredWeightSnapshot
	ok, err := m.KVGet(potsoMetricsSnapshotKey(epoch), &snapshot)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &snapshot, true, nil
}

// PotsoAddParticipant ensures the leaderboard index for the given day tracks the participant.
func (m *Manager) PotsoAddParticipant(day string, addr [20]byte) error {
	return m.KVAppend(potsoDayIndexKey(day), addr[:])
}

// PotsoListParticipants returns all addresses with recorded activity for the given day.
func (m *Manager) PotsoListParticipants(day string) ([][20]byte, error) {
	var raw [][]byte
	if err := m.KVGetList(potsoDayIndexKey(day), &raw); err != nil {
		return nil, err
	}
	result := make([][20]byte, 0, len(raw))
	for _, entry := range raw {
		if len(entry) != 20 {
			continue
		}
		var addr [20]byte
		copy(addr[:], entry)
		result = append(result, addr)
	}
	return result, nil
}

// PotsoStakeBondedTotal returns the currently bonded stake total for the owner.
func (m *Manager) PotsoStakeBondedTotal(owner [20]byte) (*big.Int, error) {
	return m.loadBigInt(potsoStakeTotalKey(owner[:]))
}

// PotsoStakeSetBondedTotal updates the bonded stake total tracked for the owner.
func (m *Manager) PotsoStakeSetBondedTotal(owner [20]byte, amount *big.Int) error {
	if err := m.writeBigInt(potsoStakeTotalKey(owner[:]), amount); err != nil {
		return err
	}
	if amount != nil && amount.Sign() > 0 {
		return m.appendStakeOwner(owner)
	}
	return m.removeStakeOwner(owner)
}

// PotsoStakeOwners lists all accounts with a positive bonded balance.
func (m *Manager) PotsoStakeOwners() ([][20]byte, error) {
	var raw [][]byte
	if err := m.KVGetList(potsoStakeOwnerIndexKey, &raw); err != nil {
		return nil, err
	}
	owners := make([][20]byte, 0, len(raw))
	for _, entry := range raw {
		if len(entry) != 20 {
			continue
		}
		var addr [20]byte
		copy(addr[:], entry[:20])
		owners = append(owners, addr)
	}
	return owners, nil
}

// PotsoStakeAllocateNonce reserves and returns the next lock nonce for the owner.
func (m *Manager) PotsoStakeAllocateNonce(owner [20]byte) (uint64, error) {
	key := potsoStakeNonceKey(owner[:])
	var next uint64
	ok, err := m.KVGet(key, &next)
	if err != nil {
		return 0, err
	}
	if !ok || next == 0 {
		next = 1
	}
	if err := m.KVPut(key, next+1); err != nil {
		return 0, err
	}
	return next, nil
}

// PotsoStakeLatestAuthNonce returns the latest consumed staking nonce for the owner.
func (m *Manager) PotsoStakeLatestAuthNonce(owner [20]byte) (uint64, error) {
	key := potsoStakeAuthNonceKey(owner[:])
	var nonce uint64
	ok, err := m.KVGet(key, &nonce)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return nonce, nil
}

// PotsoStakeSetAuthNonce records the latest consumed staking nonce for the owner.
func (m *Manager) PotsoStakeSetAuthNonce(owner [20]byte, nonce uint64) error {
	if nonce == 0 {
		return fmt.Errorf("potso: auth nonce must be greater than zero")
	}
	return m.KVPut(potsoStakeAuthNonceKey(owner[:]), nonce)
}

// PotsoStakeLockNonces lists all lock nonces tracked for the owner in creation order.
func (m *Manager) PotsoStakeLockNonces(owner [20]byte) ([]uint64, error) {
	key := potsoStakeLockIndexKey(owner[:])
	var nonces []uint64
	ok, err := m.KVGet(key, &nonces)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []uint64{}, nil
	}
	return nonces, nil
}

// PotsoStakePutLockNonces persists the supplied nonce ordering for the owner.
func (m *Manager) PotsoStakePutLockNonces(owner [20]byte, nonces []uint64) error {
	key := potsoStakeLockIndexKey(owner[:])
	if len(nonces) == 0 {
		return m.trie.Update(key, nil)
	}
	return m.KVPut(key, nonces)
}

// PotsoStakeGetLock retrieves a specific stake lock by owner and nonce.
func (m *Manager) PotsoStakeGetLock(owner [20]byte, nonce uint64) (*potso.StakeLock, bool, error) {
	var lock potso.StakeLock
	ok, err := m.KVGet(potsoStakeLockKey(owner[:], nonce), &lock)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &lock, true, nil
}

// PotsoStakePutLock stores the supplied lock data.
func (m *Manager) PotsoStakePutLock(owner [20]byte, nonce uint64, lock *potso.StakeLock) error {
	if lock == nil {
		return fmt.Errorf("potso: stake lock must not be nil")
	}
	return m.KVPut(potsoStakeLockKey(owner[:], nonce), lock)
}

// PotsoStakeDeleteLock removes the referenced lock from storage.
func (m *Manager) PotsoStakeDeleteLock(owner [20]byte, nonce uint64) error {
	return m.trie.Update(potsoStakeLockKey(owner[:], nonce), nil)
}

// PotsoStakeQueueEntries returns the withdrawal queue entries for the provided day.
func (m *Manager) PotsoStakeQueueEntries(day string) ([]potso.WithdrawalRef, error) {
	key := potsoStakeQueueKey(day)
	var entries []potso.WithdrawalRef
	ok, err := m.KVGet(key, &entries)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []potso.WithdrawalRef{}, nil
	}
	return entries, nil
}

// PotsoStakePutQueueEntries overwrites the queue bucket for the provided day.
func (m *Manager) PotsoStakePutQueueEntries(day string, entries []potso.WithdrawalRef) error {
	key := potsoStakeQueueKey(day)
	if len(entries) == 0 {
		return m.trie.Update(key, nil)
	}
	return m.KVPut(key, entries)
}

// PotsoStakeQueueAppend adds the supplied entry to the day bucket if not already present.
func (m *Manager) PotsoStakeQueueAppend(day string, entry potso.WithdrawalRef) error {
	key := potsoStakeQueueKey(day)
	var entries []potso.WithdrawalRef
	ok, err := m.KVGet(key, &entries)
	if err != nil {
		return err
	}
	if !ok {
		entries = []potso.WithdrawalRef{}
	}
	found := false
	for _, existing := range entries {
		if existing.Nonce == entry.Nonce && bytes.Equal(existing.Owner[:], entry.Owner[:]) {
			found = true
			break
		}
	}
	if !found {
		clone := potso.WithdrawalRef{Owner: entry.Owner, Nonce: entry.Nonce}
		if entry.Amount != nil {
			clone.Amount = new(big.Int).Set(entry.Amount)
		} else {
			clone.Amount = big.NewInt(0)
		}
		entries = append(entries, clone)
	}
	return m.KVPut(key, entries)
}

// PotsoStakeQueueRemove removes the matching entry from the withdrawal queue bucket.
func (m *Manager) PotsoStakeQueueRemove(day string, owner [20]byte, nonce uint64) error {
	key := potsoStakeQueueKey(day)
	var entries []potso.WithdrawalRef
	ok, err := m.KVGet(key, &entries)
	if err != nil {
		return err
	}
	if !ok || len(entries) == 0 {
		return nil
	}
	filtered := make([]potso.WithdrawalRef, 0, len(entries))
	for _, entry := range entries {
		if entry.Nonce == nonce && bytes.Equal(entry.Owner[:], owner[:]) {
			continue
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) == len(entries) {
		return nil
	}
	return m.PotsoStakePutQueueEntries(day, filtered)
}

// PotsoStakeVaultAddress returns the deterministic module vault used for staking locks.
func (m *Manager) PotsoStakeVaultAddress() [20]byte {
	return potsoStakeModuleAddress()
}

func (m *Manager) appendStakeOwner(owner [20]byte) error {
	return m.KVAppend(potsoStakeOwnerIndexKey, owner[:])
}

func (m *Manager) removeStakeOwner(owner [20]byte) error {
	var raw [][]byte
	if err := m.KVGetList(potsoStakeOwnerIndexKey, &raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	filtered := make([][]byte, 0, len(raw))
	for _, entry := range raw {
		if len(entry) == len(owner) && bytes.Equal(entry, owner[:]) {
			continue
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) == 0 {
		return m.trie.Update(kvKey(potsoStakeOwnerIndexKey), nil)
	}
	return m.KVPut(potsoStakeOwnerIndexKey, filtered)
}

// PotsoRewardsLastProcessedEpoch returns the last epoch marked as processed.
func (m *Manager) PotsoRewardsLastProcessedEpoch() (uint64, bool, error) {
	var value uint64
	ok, err := m.KVGet(potsoRewardLastProcessed, &value)
	if err != nil {
		return 0, false, err
	}
	return value, ok, nil
}

// PotsoRewardsSetLastProcessedEpoch updates the marker indicating the last processed epoch.
func (m *Manager) PotsoRewardsSetLastProcessedEpoch(epoch uint64) error {
	return m.KVPut(potsoRewardLastProcessed, epoch)
}

// PotsoRewardsGetMeta retrieves the stored metadata for the provided epoch.
func (m *Manager) PotsoRewardsGetMeta(epoch uint64) (*potso.RewardEpochMeta, bool, error) {
	var meta potso.RewardEpochMeta
	ok, err := m.KVGet(potsoRewardMetaKey(epoch), &meta)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &meta, true, nil
}

// PotsoRewardsSetMeta persists the metadata for a processed epoch.
func (m *Manager) PotsoRewardsSetMeta(epoch uint64, meta *potso.RewardEpochMeta) error {
	if meta == nil {
		return fmt.Errorf("potso: reward meta must not be nil")
	}
	return m.KVPut(potsoRewardMetaKey(epoch), meta)
}

// PotsoRewardsSetPayout stores the payout amount for a participant within an epoch.
func (m *Manager) PotsoRewardsSetPayout(epoch uint64, addr [20]byte, amount *big.Int) error {
	if amount == nil {
		amount = big.NewInt(0)
	}
	return m.KVPut(potsoRewardPayoutKey(epoch, addr[:]), amount)
}

// PotsoRewardsGetPayout loads the stored payout for the participant within the epoch.
func (m *Manager) PotsoRewardsGetPayout(epoch uint64, addr [20]byte) (*big.Int, bool, error) {
	value := new(big.Int)
	ok, err := m.KVGet(potsoRewardPayoutKey(epoch, addr[:]), value)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return value, true, nil
}

// PotsoRewardsSetWinners stores the ordered list of winners for the epoch.
func (m *Manager) PotsoRewardsSetWinners(epoch uint64, winners [][20]byte) error {
	encoded := make([][]byte, len(winners))
	for i := range winners {
		encoded[i] = append([]byte(nil), winners[i][:]...)
	}
	return m.KVPut(potsoRewardWinnersKey(epoch), encoded)
}

// PotsoRewardsListWinners returns the ordered winner list for the epoch.
func (m *Manager) PotsoRewardsListWinners(epoch uint64) ([][20]byte, error) {
	var raw [][]byte
	if err := m.KVGetList(potsoRewardWinnersKey(epoch), &raw); err != nil {
		return nil, err
	}
	winners := make([][20]byte, 0, len(raw))
	for _, entry := range raw {
		if len(entry) != 20 {
			continue
		}
		var addr [20]byte
		copy(addr[:], entry[:20])
		winners = append(winners, addr)
	}
	return winners, nil
}

// PotsoRewardsSetClaim records or updates a claim entry for the given epoch winner.
func (m *Manager) PotsoRewardsSetClaim(epoch uint64, addr [20]byte, claim *potso.RewardClaim) error {
	if claim == nil {
		return fmt.Errorf("potso: reward claim must not be nil")
	}
	stored := &potso.RewardClaim{
		Claimed:   claim.Claimed,
		ClaimedAt: claim.ClaimedAt,
		Mode:      claim.Mode.Normalise(),
	}
	if claim.Amount != nil {
		stored.Amount = new(big.Int).Set(claim.Amount)
	} else {
		stored.Amount = big.NewInt(0)
	}
	return m.KVPut(potsoRewardClaimKey(epoch, addr[:]), stored)
}

// PotsoRewardsGetClaim retrieves the claim entry for the given epoch winner.
func (m *Manager) PotsoRewardsGetClaim(epoch uint64, addr [20]byte) (*potso.RewardClaim, bool, error) {
	var claim potso.RewardClaim
	ok, err := m.KVGet(potsoRewardClaimKey(epoch, addr[:]), &claim)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return claim.Clone(), true, nil
}

// PotsoRewardsAppendHistory appends a settled payout entry to the participant history ledger.
func (m *Manager) PotsoRewardsAppendHistory(addr [20]byte, entry potso.RewardHistoryEntry) error {
	key := potsoRewardHistoryKey(addr[:])
	var history []potso.RewardHistoryEntry
	ok, err := m.KVGet(key, &history)
	if err != nil {
		return err
	}
	if !ok {
		history = []potso.RewardHistoryEntry{}
	}
	clone := entry.Clone()
	clone.Mode = clone.Mode.Normalise()
	history = append(history, clone)
	return m.KVPut(key, history)
}

// PotsoRewardsHistory returns the stored payout history for the participant.
func (m *Manager) PotsoRewardsHistory(addr [20]byte) ([]potso.RewardHistoryEntry, error) {
	key := potsoRewardHistoryKey(addr[:])
	var history []potso.RewardHistoryEntry
	ok, err := m.KVGet(key, &history)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []potso.RewardHistoryEntry{}, nil
	}
	cloned := make([]potso.RewardHistoryEntry, len(history))
	for i := range history {
		cloned[i] = history[i].Clone()
	}
	return cloned, nil
}

// PotsoRewardsBuildCSV constructs a CSV export for the epoch payout ledger.
func (m *Manager) PotsoRewardsBuildCSV(epoch uint64) ([]byte, *big.Int, int, error) {
	winners, err := m.PotsoRewardsListWinners(epoch)
	if err != nil {
		return nil, nil, 0, err
	}
	buf := &bytes.Buffer{}
	writer := csv.NewWriter(buf)
	if err := writer.Write([]string{"address", "amount", "claimed", "claimedAt", "mode"}); err != nil {
		return nil, nil, 0, err
	}
	total := big.NewInt(0)
	for _, addr := range winners {
		payout, _, err := m.PotsoRewardsGetPayout(epoch, addr)
		if err != nil {
			return nil, nil, 0, err
		}
		amount := big.NewInt(0)
		if payout != nil {
			amount = new(big.Int).Set(payout)
		}
		total.Add(total, amount)

		claim, _, err := m.PotsoRewardsGetClaim(epoch, addr)
		if err != nil {
			return nil, nil, 0, err
		}
		claimed := false
		claimedAt := uint64(0)
		mode := potso.RewardPayoutModeAuto
		if claim != nil {
			claimed = claim.Claimed
			claimedAt = claim.ClaimedAt
			if claim.Mode.Valid() {
				mode = claim.Mode.Normalise()
			}
		}
		record := []string{
			crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String(),
			amount.String(),
			strconv.FormatBool(claimed),
			strconv.FormatUint(claimedAt, 10),
			string(mode),
		}
		if err := writer.Write(record); err != nil {
			return nil, nil, 0, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, nil, 0, err
	}
	return buf.Bytes(), total, len(winners), nil
}

func escrowStorageKey(id [32]byte) []byte {
	buf := make([]byte, len(escrowRecordPrefix)+len(id))
	copy(buf, escrowRecordPrefix)
	copy(buf[len(escrowRecordPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func escrowRealmKey(id string) []byte {
	trimmed := strings.TrimSpace(id)
	buf := make([]byte, len(escrowRealmPrefix)+len(trimmed))
	copy(buf, escrowRealmPrefix)
	copy(buf[len(escrowRealmPrefix):], trimmed)
	return buf
}

func escrowFrozenPolicyKey(id [32]byte) []byte {
	buf := make([]byte, len(escrowFrozenPolicyPrefix)+len(id))
	copy(buf, escrowFrozenPolicyPrefix)
	copy(buf[len(escrowFrozenPolicyPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func escrowVaultKey(id [32]byte, token string) []byte {
	normalized := strings.ToUpper(strings.TrimSpace(token))
	buf := make([]byte, len(escrowVaultPrefix)+len(normalized)+1+len(id))
	copy(buf, escrowVaultPrefix)
	copy(buf[len(escrowVaultPrefix):], normalized)
	buf[len(escrowVaultPrefix)+len(normalized)] = ':'
	copy(buf[len(escrowVaultPrefix)+len(normalized)+1:], id[:])
	return ethcrypto.Keccak256(buf)
}

func tradeStorageKey(id [32]byte) []byte {
	buf := make([]byte, len(tradeRecordPrefix)+len(id))
	copy(buf, tradeRecordPrefix)
	copy(buf[len(tradeRecordPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func identityAliasKey(alias string) []byte {
	buf := make([]byte, len(identityAliasPrefix)+len(alias))
	copy(buf, identityAliasPrefix)
	copy(buf[len(identityAliasPrefix):], alias)
	return kvKey(buf)
}

func identityAliasIDKey(id [32]byte) []byte {
	buf := make([]byte, len(identityAliasIDPrefix)+len(id))
	copy(buf, identityAliasIDPrefix)
	copy(buf[len(identityAliasIDPrefix):], id[:])
	return kvKey(buf)
}

func identityReverseKey(addr []byte) []byte {
	buf := make([]byte, len(identityReversePrefix)+len(addr))
	copy(buf, identityReversePrefix)
	copy(buf[len(identityReversePrefix):], addr)
	return kvKey(buf)
}

type storedAliasRecord struct {
	Alias     string
	Primary   [20]byte
	Addresses [][]byte
	AvatarRef string
	CreatedAt *big.Int
	UpdatedAt *big.Int
}

func newStoredAliasRecord(record *identity.AliasRecord) *storedAliasRecord {
	if record == nil {
		return nil
	}
	normalized := record.Clone()
	normalized.Addresses = uniqueAliasAddresses(normalized.Primary, normalized.Addresses)
	stored := &storedAliasRecord{
		Alias:     normalized.Alias,
		Primary:   normalized.Primary,
		AvatarRef: normalized.AvatarRef,
		CreatedAt: big.NewInt(normalized.CreatedAt),
		UpdatedAt: big.NewInt(normalized.UpdatedAt),
	}
	if len(normalized.Addresses) > 0 {
		stored.Addresses = make([][]byte, len(normalized.Addresses))
		for i, addr := range normalized.Addresses {
			stored.Addresses[i] = append([]byte(nil), addr[:]...)
		}
	}
	return stored
}

func (s *storedAliasRecord) toAliasRecord() (*identity.AliasRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("identity: nil alias record")
	}
	record := &identity.AliasRecord{
		Alias:     s.Alias,
		Primary:   s.Primary,
		AvatarRef: s.AvatarRef,
		CreatedAt: 0,
		UpdatedAt: 0,
	}
	if s.CreatedAt != nil {
		record.CreatedAt = s.CreatedAt.Int64()
	}
	if s.UpdatedAt != nil {
		record.UpdatedAt = s.UpdatedAt.Int64()
	}
	if len(s.Addresses) > 0 {
		record.Addresses = make([][20]byte, len(s.Addresses))
		for i, raw := range s.Addresses {
			if len(raw) != 20 {
				return nil, fmt.Errorf("identity: invalid address length in alias record")
			}
			copy(record.Addresses[i][:], raw)
		}
	}
	record.Addresses = uniqueAliasAddresses(record.Primary, record.Addresses)
	return record, nil
}

func uniqueAliasAddresses(primary [20]byte, addrs [][20]byte) [][20]byte {
	seen := make(map[[20]byte]struct{}, len(addrs)+1)
	out := make([][20]byte, 0, len(addrs)+1)
	if primary != ([20]byte{}) {
		seen[primary] = struct{}{}
		out = append(out, primary)
	}
	for _, addr := range addrs {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func copyAliasAddresses(addrs [][20]byte) [][20]byte {
	if len(addrs) == 0 {
		return nil
	}
	out := make([][20]byte, len(addrs))
	copy(out, addrs)
	return out
}

func containsAliasAddress(addrs [][20]byte, target [20]byte) bool {
	for _, addr := range addrs {
		if addr == target {
			return true
		}
	}
	return false
}

func parseAliasAddress(addr []byte) ([20]byte, error) {
	var out [20]byte
	if len(addr) != 20 {
		return out, fmt.Errorf("%w: must be 20 bytes", identity.ErrInvalidAddress)
	}
	copy(out[:], addr)
	if out == ([20]byte{}) {
		return out, fmt.Errorf("%w: must not be zero", identity.ErrInvalidAddress)
	}
	return out, nil
}

func (m *Manager) identityPersistRecord(record *identity.AliasRecord, previousAlias string, previousAddresses [][20]byte) error {
	if record == nil {
		return fmt.Errorf("identity: nil record")
	}
	if record.Alias == "" {
		return fmt.Errorf("identity: alias must not be empty")
	}
	record.Addresses = uniqueAliasAddresses(record.Primary, record.Addresses)
	stored := newStoredAliasRecord(record)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	aliasKey := identityAliasKey(record.Alias)
	aliasID := identity.DeriveAliasID(record.Alias)
	if err := m.trie.Update(aliasKey, encoded); err != nil {
		return err
	}
	if err := m.trie.Update(identityAliasIDKey(aliasID), []byte(record.Alias)); err != nil {
		return err
	}

	prevSet := make(map[[20]byte]struct{}, len(previousAddresses))
	for _, addr := range previousAddresses {
		prevSet[addr] = struct{}{}
	}
	newSet := make(map[[20]byte]struct{}, len(record.Addresses))
	for _, addr := range record.Addresses {
		newSet[addr] = struct{}{}
		if err := m.trie.Update(identityReverseKey(addr[:]), []byte(record.Alias)); err != nil {
			return err
		}
	}
	for addr := range prevSet {
		if _, ok := newSet[addr]; ok {
			continue
		}
		if err := m.trie.Update(identityReverseKey(addr[:]), nil); err != nil {
			return err
		}
	}

	if previousAlias != "" && previousAlias != record.Alias {
		oldID := identity.DeriveAliasID(previousAlias)
		if err := m.trie.Update(identityAliasKey(previousAlias), nil); err != nil {
			return err
		}
		if err := m.trie.Update(identityAliasIDKey(oldID), nil); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) identityGetAlias(alias string) (*identity.AliasRecord, bool, error) {
	data, err := m.trie.Get(identityAliasKey(alias))
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	stored := new(storedAliasRecord)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false, err
	}
	record, err := stored.toAliasRecord()
	if err != nil {
		return nil, false, err
	}
	return record, true, nil
}

func tradeEscrowIndexKey(escrowID [32]byte) []byte {
	buf := make([]byte, len(tradeEscrowIndexPrefix)+len(escrowID))
	copy(buf, tradeEscrowIndexPrefix)
	copy(buf[len(tradeEscrowIndexPrefix):], escrowID[:])
	return ethcrypto.Keccak256(buf)
}

func escrowModuleAddress(token string) ([20]byte, error) {
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return [20]byte{}, err
	}
	seed := escrowModuleSeedPrefix + normalized
	hash := ethcrypto.Keccak256([]byte(seed))
	var addr [20]byte
	copy(addr[:], hash[len(hash)-20:])
	return addr, nil
}

func potsoStakeModuleAddress() [20]byte {
	hash := ethcrypto.Keccak256([]byte(potsoStakeModuleSeedPrefix))
	var addr [20]byte
	copy(addr[:], hash[len(hash)-20:])
	return addr
}

type storedArbitratorSet struct {
	Scheme    uint8
	Threshold uint32
	Members   [][20]byte
}

func newStoredArbitratorSet(set *escrow.ArbitratorSet) *storedArbitratorSet {
	if set == nil {
		return nil
	}
	members := make([][20]byte, len(set.Members))
	copy(members, set.Members)
	return &storedArbitratorSet{
		Scheme:    uint8(set.Scheme),
		Threshold: set.Threshold,
		Members:   members,
	}
}

func (s *storedArbitratorSet) toArbitratorSet() (*escrow.ArbitratorSet, error) {
	if s == nil {
		return nil, fmt.Errorf("escrow: nil arbitrator set")
	}
	set := &escrow.ArbitratorSet{
		Scheme:    escrow.ArbitrationScheme(s.Scheme),
		Threshold: s.Threshold,
	}
	if len(s.Members) > 0 {
		set.Members = make([][20]byte, len(s.Members))
		copy(set.Members, s.Members)
	}
	sanitized, err := escrow.SanitizeArbitratorSet(set)
	if err != nil {
		return nil, err
	}
	return sanitized, nil
}

type storedEscrowRealm struct {
	ID              string
	Version         uint64
	NextPolicyNonce uint64
	CreatedAt       *big.Int
	UpdatedAt       *big.Int
	Arbitrators     *storedArbitratorSet
}

func newStoredEscrowRealm(r *escrow.EscrowRealm) *storedEscrowRealm {
	if r == nil {
		return nil
	}
	created := big.NewInt(r.CreatedAt)
	updated := big.NewInt(r.UpdatedAt)
	return &storedEscrowRealm{
		ID:              strings.TrimSpace(r.ID),
		Version:         r.Version,
		NextPolicyNonce: r.NextPolicyNonce,
		CreatedAt:       created,
		UpdatedAt:       updated,
		Arbitrators:     newStoredArbitratorSet(r.Arbitrators),
	}
}

func (s *storedEscrowRealm) toEscrowRealm() (*escrow.EscrowRealm, error) {
	if s == nil {
		return nil, fmt.Errorf("escrow: nil realm record")
	}
	realm := &escrow.EscrowRealm{
		ID:              strings.TrimSpace(s.ID),
		Version:         s.Version,
		NextPolicyNonce: s.NextPolicyNonce,
	}
	if s.CreatedAt != nil {
		realm.CreatedAt = s.CreatedAt.Int64()
	}
	if s.UpdatedAt != nil {
		realm.UpdatedAt = s.UpdatedAt.Int64()
	}
	if s.Arbitrators == nil {
		return nil, fmt.Errorf("escrow: realm missing arbitrators")
	}
	set, err := s.Arbitrators.toArbitratorSet()
	if err != nil {
		return nil, err
	}
	realm.Arbitrators = set
	sanitized, err := escrow.SanitizeEscrowRealm(realm)
	if err != nil {
		return nil, err
	}
	return sanitized, nil
}

type storedFrozenArb struct {
	RealmID      string
	RealmVersion uint64
	PolicyNonce  uint64
	Scheme       uint8
	Threshold    uint32
	Members      [][20]byte
	FrozenAt     *big.Int
}

func newStoredFrozenArb(f *escrow.FrozenArb) *storedFrozenArb {
	if f == nil {
		return nil
	}
	members := make([][20]byte, len(f.Members))
	copy(members, f.Members)
	return &storedFrozenArb{
		RealmID:      strings.TrimSpace(f.RealmID),
		RealmVersion: f.RealmVersion,
		PolicyNonce:  f.PolicyNonce,
		Scheme:       uint8(f.Scheme),
		Threshold:    f.Threshold,
		Members:      members,
		FrozenAt:     big.NewInt(f.FrozenAt),
	}
}

func (s *storedFrozenArb) toFrozenArb() (*escrow.FrozenArb, error) {
	if s == nil {
		return nil, fmt.Errorf("escrow: nil frozen policy")
	}
	frozen := &escrow.FrozenArb{
		RealmID:      strings.TrimSpace(s.RealmID),
		RealmVersion: s.RealmVersion,
		PolicyNonce:  s.PolicyNonce,
		Scheme:       escrow.ArbitrationScheme(s.Scheme),
		Threshold:    s.Threshold,
	}
	if len(s.Members) > 0 {
		frozen.Members = make([][20]byte, len(s.Members))
		copy(frozen.Members, s.Members)
	}
	if s.FrozenAt != nil {
		frozen.FrozenAt = s.FrozenAt.Int64()
	}
	sanitized, err := escrow.SanitizeFrozenArb(frozen)
	if err != nil {
		return nil, err
	}
	return sanitized, nil
}

type storedEscrow struct {
	ID           [32]byte
	Payer        [20]byte
	Payee        [20]byte
	Mediator     [20]byte
	Token        string
	Amount       *big.Int
	FeeBps       uint32
	Deadline     *big.Int
	CreatedAt    *big.Int
	Nonce        *big.Int
	MetaHash     [32]byte
	Status       uint8
	RealmID      string
	DecisionHash [32]byte
}

// EscrowRealmPut stores the provided realm definition after sanitising it.
func (m *Manager) EscrowRealmPut(realm *escrow.EscrowRealm) error {
	if realm == nil {
		return fmt.Errorf("escrow: nil realm")
	}
	sanitized, err := escrow.SanitizeEscrowRealm(realm)
	if err != nil {
		return err
	}
	record := newStoredEscrowRealm(sanitized)
	return m.KVPut(escrowRealmKey(sanitized.ID), record)
}

// EscrowRealmGet retrieves a realm definition by identifier.
func (m *Manager) EscrowRealmGet(id string) (*escrow.EscrowRealm, bool, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, false, fmt.Errorf("escrow: realm id must not be empty")
	}
	var stored storedEscrowRealm
	ok, err := m.KVGet(escrowRealmKey(trimmed), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	realm, err := stored.toEscrowRealm()
	if err != nil {
		return nil, false, err
	}
	return realm, true, nil
}

// EscrowFrozenPolicyPut persists the frozen arbitrator policy for the escrow.
func (m *Manager) EscrowFrozenPolicyPut(id [32]byte, policy *escrow.FrozenArb) error {
	if policy == nil {
		return fmt.Errorf("escrow: nil frozen policy")
	}
	sanitized, err := escrow.SanitizeFrozenArb(policy)
	if err != nil {
		return err
	}
	record := newStoredFrozenArb(sanitized)
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(escrowFrozenPolicyKey(id), encoded)
}

// EscrowFrozenPolicyGet retrieves the frozen policy for an escrow if present.
func (m *Manager) EscrowFrozenPolicyGet(id [32]byte) (*escrow.FrozenArb, bool, error) {
	data, err := m.trie.Get(escrowFrozenPolicyKey(id))
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	var stored storedFrozenArb
	if err := rlp.DecodeBytes(data, &stored); err != nil {
		return nil, false, err
	}
	frozen, err := stored.toFrozenArb()
	if err != nil {
		return nil, false, err
	}
	return frozen, true, nil
}

func newStoredEscrow(e *escrow.Escrow) *storedEscrow {
	if e == nil {
		return nil
	}
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	deadline := big.NewInt(e.Deadline)
	created := big.NewInt(e.CreatedAt)
	nonce := new(big.Int).SetUint64(e.Nonce)
	return &storedEscrow{
		ID:           e.ID,
		Payer:        e.Payer,
		Payee:        e.Payee,
		Mediator:     e.Mediator,
		Token:        e.Token,
		Amount:       amount,
		FeeBps:       e.FeeBps,
		Deadline:     deadline,
		CreatedAt:    created,
		Nonce:        nonce,
		MetaHash:     e.MetaHash,
		Status:       uint8(e.Status),
		RealmID:      strings.TrimSpace(e.RealmID),
		DecisionHash: e.ResolutionHash,
	}
}

func (s *storedEscrow) toEscrow() (*escrow.Escrow, error) {
	if s == nil {
		return nil, fmt.Errorf("escrow: nil storage record")
	}
	out := &escrow.Escrow{
		ID:       s.ID,
		Payer:    s.Payer,
		Payee:    s.Payee,
		Mediator: s.Mediator,
		Token:    s.Token,
		Amount: func() *big.Int {
			if s.Amount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.Amount)
		}(),
		FeeBps:         s.FeeBps,
		MetaHash:       s.MetaHash,
		Status:         escrow.EscrowStatus(s.Status),
		RealmID:        strings.TrimSpace(s.RealmID),
		ResolutionHash: s.DecisionHash,
	}
	if s.Deadline != nil {
		out.Deadline = s.Deadline.Int64()
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.Int64()
	}
	if s.Nonce != nil {
		out.Nonce = s.Nonce.Uint64()
	}
	if !out.Status.Valid() {
		return nil, fmt.Errorf("escrow: invalid status in storage")
	}
	return out, nil
}

type storedTrade struct {
	ID          [32]byte
	OfferID     string
	Buyer       [20]byte
	Seller      [20]byte
	QuoteToken  string
	QuoteAmount *big.Int
	EscrowQuote [32]byte
	BaseToken   string
	BaseAmount  *big.Int
	EscrowBase  [32]byte
	Deadline    *big.Int
	CreatedAt   *big.Int
	FundedAt    *big.Int
	SlippageBps uint32
	Status      uint8
}

func newStoredTrade(t *escrow.Trade) *storedTrade {
	if t == nil {
		return nil
	}
	quote := big.NewInt(0)
	if t.QuoteAmount != nil {
		quote = new(big.Int).Set(t.QuoteAmount)
	}
	base := big.NewInt(0)
	if t.BaseAmount != nil {
		base = new(big.Int).Set(t.BaseAmount)
	}
	return &storedTrade{
		ID:          t.ID,
		OfferID:     t.OfferID,
		Buyer:       t.Buyer,
		Seller:      t.Seller,
		QuoteToken:  t.QuoteToken,
		QuoteAmount: quote,
		EscrowQuote: t.EscrowQuote,
		BaseToken:   t.BaseToken,
		BaseAmount:  base,
		EscrowBase:  t.EscrowBase,
		Deadline:    big.NewInt(t.Deadline),
		CreatedAt:   big.NewInt(t.CreatedAt),
		FundedAt: func() *big.Int {
			if t.FundedAt == 0 {
				return nil
			}
			return big.NewInt(t.FundedAt)
		}(),
		SlippageBps: t.SlippageBps,
		Status:      uint8(t.Status),
	}
}

func (s *storedTrade) toTrade() (*escrow.Trade, error) {
	if s == nil {
		return nil, fmt.Errorf("trade: nil storage record")
	}
	out := &escrow.Trade{
		ID:         s.ID,
		OfferID:    s.OfferID,
		Buyer:      s.Buyer,
		Seller:     s.Seller,
		QuoteToken: s.QuoteToken,
		QuoteAmount: func() *big.Int {
			if s.QuoteAmount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.QuoteAmount)
		}(),
		EscrowQuote: s.EscrowQuote,
		BaseToken:   s.BaseToken,
		BaseAmount: func() *big.Int {
			if s.BaseAmount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.BaseAmount)
		}(),
		EscrowBase:  s.EscrowBase,
		SlippageBps: s.SlippageBps,
		Status:      escrow.TradeStatus(s.Status),
	}
	if s.Deadline != nil {
		out.Deadline = s.Deadline.Int64()
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.Int64()
	}
	if s.FundedAt != nil {
		out.FundedAt = s.FundedAt.Int64()
	}
	if !out.Status.Valid() {
		return nil, fmt.Errorf("trade: invalid status in storage")
	}
	return out, nil
}

func (m *Manager) loadTokenList() ([]string, error) {
	data, err := m.trie.Get(tokenListKey)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	var list []string
	if err := rlp.DecodeBytes(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (m *Manager) writeTokenList(list []string) error {
	encoded, err := rlp.EncodeToBytes(list)
	if err != nil {
		return err
	}
	return m.trie.Update(tokenListKey, encoded)
}

func (m *Manager) loadTokenMetadata(symbol string) (*TokenMetadata, error) {
	key := tokenMetadataKey(symbol)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	meta := new(TokenMetadata)
	if err := rlp.DecodeBytes(data, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func (m *Manager) writeTokenMetadata(symbol string, meta *TokenMetadata) error {
	key := tokenMetadataKey(symbol)
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// RegisterToken stores the metadata for a native token and records it in the
// token index.
func (m *Manager) RegisterToken(symbol, name string, decimals uint8) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("token symbol must not be empty")
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("token %s: name must not be empty", normalized)
	}
	if existing, err := m.loadTokenMetadata(normalized); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("token %s already registered", normalized)
	}

	list, err := m.loadTokenList()
	if err != nil {
		return err
	}
	list = append(list, normalized)
	sort.Strings(list)
	if err := m.writeTokenList(list); err != nil {
		return err
	}

	meta := &TokenMetadata{
		Symbol:   normalized,
		Name:     name,
		Decimals: decimals,
	}
	return m.writeTokenMetadata(normalized, meta)
}

// SetTokenMintAuthority configures the mint authority for the given token.
func (m *Manager) SetTokenMintAuthority(symbol string, authority []byte) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}
	meta.MintAuthority = append([]byte(nil), authority...)
	return m.writeTokenMetadata(normalized, meta)
}

// SetTokenMintPaused stores the paused state for the given token.
func (m *Manager) SetTokenMintPaused(symbol string, paused bool) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}
	meta.MintPaused = paused
	return m.writeTokenMetadata(normalized, meta)
}

// Token retrieves metadata for a registered token.
func (m *Manager) Token(symbol string) (*TokenMetadata, error) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	return m.loadTokenMetadata(normalized)
}

// TokenList returns all registered token symbols in sorted order.
func (m *Manager) TokenList() ([]string, error) {
	return m.loadTokenList()
}

// SetBalance stores an account balance for the provided token.
func (m *Manager) SetBalance(addr []byte, symbol string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if amount == nil {
		amount = big.NewInt(0)
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("negative balance not allowed")
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("token symbol must not be empty")
	}
	if meta, err := m.loadTokenMetadata(normalized); err != nil {
		return err
	} else if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}

	key := balanceKey(addr, normalized)
	encoded, err := rlp.EncodeToBytes(amount)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// Balance retrieves a token balance for the provided account and token.
func (m *Manager) Balance(addr []byte, symbol string) (*big.Int, error) {
	key := balanceKey(addr, strings.ToUpper(strings.TrimSpace(symbol)))
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	amount := new(big.Int)
	if err := rlp.DecodeBytes(data, amount); err != nil {
		return nil, err
	}
	return amount, nil
}

// SetLoyaltyGlobalConfig stores the global configuration for the loyalty engine.
func (m *Manager) SetLoyaltyGlobalConfig(cfg *loyalty.GlobalConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	normalized := cfg.Clone().Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}
	state, err := m.LoyaltyDynamicState()
	if err != nil {
		return err
	}
	if state == nil {
		state = NewLoyaltyEngineStateFromDynamic(normalized.Dynamic)
	} else {
		state = state.Clone().ApplyDynamicConfig(normalized.Dynamic)
	}
	if err := m.SetLoyaltyDynamicState(state); err != nil {
		return err
	}
	encoded, err := rlp.EncodeToBytes(normalized)
	if err != nil {
		return err
	}
	return m.trie.Update(loyaltyGlobalKeyBytes, encoded)
}

// SetLoyaltyDynamicState stores the runtime state for the loyalty controller.
func (m *Manager) SetLoyaltyDynamicState(state *LoyaltyEngineState) error {
	if state == nil {
		return fmt.Errorf("nil loyalty dynamic state")
	}
	normalized := state.Clone().Normalize()
	encoded, err := rlp.EncodeToBytes(normalized)
	if err != nil {
		return err
	}
	return m.trie.Update(loyaltyDynamicStateKeyBytes, encoded)
}

// LoyaltyDynamicState retrieves the stored loyalty controller runtime state.
func (m *Manager) LoyaltyDynamicState() (*LoyaltyEngineState, error) {
	data, err := m.trie.Get(loyaltyDynamicStateKeyBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	state := new(LoyaltyEngineState)
	if err := rlp.DecodeBytes(data, state); err != nil {
		return nil, err
	}
	return state.Normalize(), nil
}

// LoyaltyGlobalConfig retrieves the stored global configuration, if any.
func (m *Manager) LoyaltyGlobalConfig() (*loyalty.GlobalConfig, error) {
	data, err := m.trie.Get(loyaltyGlobalKeyBytes)
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

// SetLoyaltyBaseDailyAccrued stores the accrued base rewards for the provided
// address and UTC day string (YYYY-MM-DD).
func (m *Manager) SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	return m.writeBigInt(LoyaltyBaseDailyMeterKey(addr, day), amount)
}

// LoyaltyBaseDailyAccrued returns the accrued base rewards for the supplied
// address and day.
func (m *Manager) LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	return m.loadBigInt(LoyaltyBaseDailyMeterKey(addr, day))
}

// SetLoyaltyBaseTotalAccrued stores the lifetime accrued base rewards for the
// provided address.
func (m *Manager) SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	return m.writeBigInt(LoyaltyBaseTotalMeterKey(addr), amount)
}

// LoyaltyBaseTotalAccrued returns the lifetime accrued base rewards for the
// supplied address.
func (m *Manager) LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	return m.loadBigInt(LoyaltyBaseTotalMeterKey(addr))
}

// SetLoyaltyProgramDailyAccrued stores the accrued program rewards for the
// provided address and UTC day (YYYY-MM-DD).
func (m *Manager) SetLoyaltyProgramDailyAccrued(id loyalty.ProgramID, addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	return m.writeBigInt(LoyaltyProgramDailyMeterKey(id, addr, day), amount)
}

// LoyaltyProgramDailyAccrued returns the accrued program rewards for the
// supplied address and day.
func (m *Manager) LoyaltyProgramDailyAccrued(id loyalty.ProgramID, addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	return m.loadBigInt(LoyaltyProgramDailyMeterKey(id, addr, day))
}

// SetLoyaltyProgramDailyTotalAccrued stores the accrued rewards for the provided program and day across all users.
func (m *Manager) SetLoyaltyProgramDailyTotalAccrued(id loyalty.ProgramID, day string, amount *big.Int) error {
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	return m.writeBigInt(LoyaltyProgramDailyTotalKey(id, day), amount)
}

// LoyaltyProgramDailyTotalAccrued returns the daily accrued rewards for the provided program across all users.
func (m *Manager) LoyaltyProgramDailyTotalAccrued(id loyalty.ProgramID, day string) (*big.Int, error) {
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	return m.loadBigInt(LoyaltyProgramDailyTotalKey(id, day))
}

// SetLoyaltyProgramEpochAccrued stores the accrued rewards for the provided program and epoch.
func (m *Manager) SetLoyaltyProgramEpochAccrued(id loyalty.ProgramID, epoch uint64, amount *big.Int) error {
	return m.writeBigInt(LoyaltyProgramEpochKey(id, epoch), amount)
}

// LoyaltyProgramEpochAccrued returns the accrued rewards for the provided program and epoch.
func (m *Manager) LoyaltyProgramEpochAccrued(id loyalty.ProgramID, epoch uint64) (*big.Int, error) {
	return m.loadBigInt(LoyaltyProgramEpochKey(id, epoch))
}

// SetLoyaltyProgramIssuanceAccrued stores the lifetime accrued rewards for the provided program and address.
func (m *Manager) SetLoyaltyProgramIssuanceAccrued(id loyalty.ProgramID, addr []byte, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	return m.writeBigInt(LoyaltyProgramIssuanceKey(id, addr), amount)
}

// LoyaltyProgramIssuanceAccrued returns the lifetime accrued rewards for the provided program and address.
func (m *Manager) LoyaltyProgramIssuanceAccrued(id loyalty.ProgramID, addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	return m.loadBigInt(LoyaltyProgramIssuanceKey(id, addr))
}

// SetRole associates an address with the specified role. Duplicate assignments
// are ignored while the stored list remains sorted for determinism.
func (m *Manager) SetRole(role string, addr []byte) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return fmt.Errorf("role must not be empty")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	key := roleKey(trimmed)
	data, err := m.trie.Get(key)
	if err != nil {
		return err
	}
	var members [][]byte
	if len(data) > 0 {
		if err := rlp.DecodeBytes(data, &members); err != nil {
			return err
		}
	}
	found := false
	for _, existing := range members {
		if string(existing) == string(addr) {
			found = true
			break
		}
	}
	if !found {
		members = append(members, append([]byte(nil), addr...))
		sort.Slice(members, func(i, j int) bool {
			return hex.EncodeToString(members[i]) < hex.EncodeToString(members[j])
		})
	}
	encoded, err := rlp.EncodeToBytes(members)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// RemoveRole disassociates an address from the specified role. Missing entries
// are ignored so the operation is idempotent for governance proposals that may
// attempt duplicate revocations.
func (m *Manager) RemoveRole(role string, addr []byte) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return fmt.Errorf("role must not be empty")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	key := roleKey(trimmed)
	data, err := m.trie.Get(key)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var members [][]byte
	if err := rlp.DecodeBytes(data, &members); err != nil {
		return err
	}
	filtered := make([][]byte, 0, len(members))
	removed := false
	for _, existing := range members {
		if bytes.Equal(existing, addr) {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if !removed {
		return nil
	}
	if len(filtered) == 0 {
		return m.trie.Update(key, nil)
	}
	encoded, err := rlp.EncodeToBytes(filtered)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// RoleMembers returns all addresses assigned to the provided role.
func (m *Manager) RoleMembers(role string) ([][]byte, error) {
	key := roleKey(strings.TrimSpace(role))
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return [][]byte{}, nil
	}
	var members [][]byte
	if err := rlp.DecodeBytes(data, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// HasRole reports whether the provided address is associated with the
// specified role. Errors while reading the underlying state result in a false
// return, matching the best-effort semantics required by the callers.
func (m *Manager) HasRole(role string, addr []byte) bool {
	if len(addr) == 0 {
		return false
	}
	key := roleKey(strings.TrimSpace(role))
	data, err := m.trie.Get(key)
	if err != nil || len(data) == 0 {
		return false
	}
	var members [][]byte
	if err := rlp.DecodeBytes(data, &members); err != nil {
		return false
	}
	for _, member := range members {
		if bytes.Equal(member, addr) {
			return true
		}
	}
	return false
}

// TokenExists reports whether the provided token symbol is registered.
func (m *Manager) TokenExists(symbol string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return false
	}
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil || meta == nil {
		return false
	}
	return true
}

// EscrowPut persists the provided escrow definition after validating and
// normalising its contents.
func (m *Manager) EscrowPut(e *escrow.Escrow) error {
	if e == nil {
		return fmt.Errorf("escrow: nil value")
	}
	sanitized, err := escrow.SanitizeEscrow(e)
	if err != nil {
		return err
	}
	record := newStoredEscrow(sanitized)
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(escrowStorageKey(sanitized.ID), encoded)
}

// EscrowGet retrieves an escrow definition by identifier. The returned boolean
// indicates whether the escrow exists in state.
func (m *Manager) EscrowGet(id [32]byte) (*escrow.Escrow, bool) {
	data, err := m.trie.Get(escrowStorageKey(id))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	stored := new(storedEscrow)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false
	}
	escrowValue, err := stored.toEscrow()
	if err != nil {
		return nil, false
	}
	trimmedRealm := strings.TrimSpace(escrowValue.RealmID)
	if trimmedRealm != "" {
		frozen, ok, err := m.EscrowFrozenPolicyGet(id)
		if err != nil || !ok {
			return nil, false
		}
		escrowValue.FrozenArb = frozen
	}
	sanitized, err := escrow.SanitizeEscrow(escrowValue)
	if err != nil {
		return nil, false
	}
	return sanitized, true
}

// EscrowVaultAddress returns the deterministic module address that holds funds
// for escrows denominated in the supplied token.
func (m *Manager) EscrowVaultAddress(token string) ([20]byte, error) {
	return escrowModuleAddress(token)
}

// EscrowCredit increases the tracked escrow balance for the supplied token.
// Attempts to operate on unknown escrows, unsupported tokens or negative
// amounts result in an error.
func (m *Manager) EscrowCredit(id [32]byte, token string, amt *big.Int) error {
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative credit")
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return err
	}
	exists, err := m.trie.Get(escrowStorageKey(id))
	if err != nil {
		return err
	}
	if len(exists) == 0 {
		return fmt.Errorf("escrow not found")
	}
	if amt.Sign() == 0 {
		return nil
	}
	key := escrowVaultKey(id, normalized)
	balance, err := m.loadBigInt(key)
	if err != nil {
		return err
	}
	updated := new(big.Int).Add(balance, amt)
	return m.writeBigInt(key, updated)
}

// EscrowDebit decreases the tracked escrow balance for the supplied token.
func (m *Manager) EscrowDebit(id [32]byte, token string, amt *big.Int) error {
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative debit")
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return err
	}
	key := escrowVaultKey(id, normalized)
	balance, err := m.loadBigInt(key)
	if err != nil {
		return err
	}
	if balance.Cmp(amt) < 0 {
		return fmt.Errorf("escrow: insufficient balance")
	}
	if amt.Sign() == 0 {
		return nil
	}
	updated := new(big.Int).Sub(balance, amt)
	return m.writeBigInt(key, updated)
}

// EscrowBalance returns the tracked vault balance for the specified escrow
// identifier and token symbol.
func (m *Manager) EscrowBalance(id [32]byte, token string) (*big.Int, error) {
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return nil, err
	}
	key := escrowVaultKey(id, normalized)
	balance, err := m.loadBigInt(key)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

// TradePut persists the provided trade definition after validation.
func (m *Manager) TradePut(t *escrow.Trade) error {
	if t == nil {
		return fmt.Errorf("trade: nil value")
	}
	sanitized, err := escrow.SanitizeTrade(t)
	if err != nil {
		return err
	}
	record := newStoredTrade(sanitized)
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(tradeStorageKey(sanitized.ID), encoded)
}

// TradeGet retrieves a stored trade by identifier.
func (m *Manager) TradeGet(id [32]byte) (*escrow.Trade, bool) {
	data, err := m.trie.Get(tradeStorageKey(id))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	stored := new(storedTrade)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false
	}
	trade, err := stored.toTrade()
	if err != nil {
		return nil, false
	}
	sanitized, err := escrow.SanitizeTrade(trade)
	if err != nil {
		return nil, false
	}
	return sanitized, true
}

// TradeSetStatus updates the status of an existing trade.
func (m *Manager) TradeSetStatus(id [32]byte, status escrow.TradeStatus) error {
	if !status.Valid() {
		return fmt.Errorf("trade: invalid status %d", status)
	}
	trade, ok := m.TradeGet(id)
	if !ok {
		return fmt.Errorf("trade: not found")
	}
	if trade.Status == status {
		return nil
	}
	trade.Status = status
	return m.TradePut(trade)
}

// TradeIndexEscrow associates an escrow with a trade for quick lookups.
func (m *Manager) TradeIndexEscrow(escrowID [32]byte, tradeID [32]byte) error {
	key := tradeEscrowIndexKey(escrowID)
	return m.trie.Update(key, append([]byte(nil), tradeID[:]...))
}

// TradeLookupByEscrow resolves the trade identifier owning the provided escrow.
func (m *Manager) TradeLookupByEscrow(escrowID [32]byte) ([32]byte, bool, error) {
	key := tradeEscrowIndexKey(escrowID)
	data, err := m.trie.Get(key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if len(data) != len([32]byte{}) {
		return [32]byte{}, false, nil
	}
	var id [32]byte
	copy(id[:], data)
	return id, true, nil
}

// TradeRemoveByEscrow removes the reverse index entry for the escrow.
func (m *Manager) TradeRemoveByEscrow(escrowID [32]byte) error {
	key := tradeEscrowIndexKey(escrowID)
	return m.trie.Update(key, nil)
}

// IsEscrowFunded reports whether the escrow currently holds funds.
func (m *Manager) IsEscrowFunded(id [32]byte) (bool, error) {
	esc, ok := m.EscrowGet(id)
	if !ok {
		return false, fmt.Errorf("escrow not found")
	}
	return esc.Status == escrow.EscrowFunded, nil
}

// IdentitySetAlias registers or updates the alias associated with the provided address.
func (m *Manager) IdentitySetAlias(addr []byte, alias string) error {
	if len(addr) != 20 {
		return fmt.Errorf("identity: address must be 20 bytes")
	}
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return err
	}
	var address [20]byte
	copy(address[:], addr)

	existingRecord, exists, err := m.identityGetAlias(normalized)
	if err != nil {
		return err
	}
	if exists && existingRecord != nil && existingRecord.Primary != address {
		return identity.ErrAliasTaken
	}

	reverseKey := identityReverseKey(addr)
	currentAliasBytes, err := m.trie.Get(reverseKey)
	if err != nil {
		return err
	}
	currentAlias := string(currentAliasBytes)

	baseRecord := &identity.AliasRecord{Alias: normalized}
	if exists && existingRecord != nil {
		baseRecord = existingRecord.Clone()
	}

	if currentAlias != "" && currentAlias != normalized {
		oldRecord, oldExists, err := m.identityGetAlias(currentAlias)
		if err != nil {
			return err
		}
		if oldExists && oldRecord != nil {
			if baseRecord.CreatedAt == 0 {
				baseRecord.CreatedAt = oldRecord.CreatedAt
			}
			if baseRecord.AvatarRef == "" {
				baseRecord.AvatarRef = oldRecord.AvatarRef
			}
			if len(baseRecord.Addresses) == 0 {
				baseRecord.Addresses = oldRecord.Addresses
			}
		}
		oldID := identity.DeriveAliasID(currentAlias)
		if err := m.trie.Update(identityAliasKey(currentAlias), nil); err != nil {
			return err
		}
		if err := m.trie.Update(identityAliasIDKey(oldID), nil); err != nil {
			return err
		}
	}

	baseRecord.Alias = normalized
	baseRecord.Primary = address
	baseRecord.Addresses = uniqueAliasAddresses(address, baseRecord.Addresses)
	now := time.Now().Unix()
	if baseRecord.CreatedAt == 0 {
		baseRecord.CreatedAt = now
	}
	baseRecord.UpdatedAt = now

	stored := newStoredAliasRecord(baseRecord)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	aliasID := identity.DeriveAliasID(normalized)
	if err := m.trie.Update(identityAliasKey(normalized), encoded); err != nil {
		return err
	}
	if err := m.trie.Update(identityAliasIDKey(aliasID), []byte(normalized)); err != nil {
		return err
	}
	if err := m.trie.Update(reverseKey, []byte(normalized)); err != nil {
		return err
	}
	return nil
}

// IdentityAliasByID resolves an alias identifier to its canonical alias string.
func (m *Manager) IdentityAliasByID(id [32]byte) (string, bool) {
	data, err := m.trie.Get(identityAliasIDKey(id))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// IdentityResolve resolves an alias to its owning address.
func (m *Manager) IdentityResolve(alias string) (*identity.AliasRecord, bool) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, false
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil || !ok {
		return nil, false
	}
	return record, true
}

func (m *Manager) IdentitySetAvatar(alias string, avatarRef string, now int64) (*identity.AliasRecord, error) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, err
	}
	normalizedRef, err := identity.NormalizeAvatarRef(avatarRef)
	if err != nil {
		return nil, err
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil {
		return nil, err
	}
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	record.AvatarRef = normalizedRef
	record.UpdatedAt = now
	stored := newStoredAliasRecord(record)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return nil, err
	}
	if err := m.trie.Update(identityAliasKey(normalized), encoded); err != nil {
		return nil, err
	}
	return record, nil
}

// IdentityAddAddress associates an additional address with an existing alias.
func (m *Manager) IdentityAddAddress(alias string, addr []byte, now int64) (*identity.AliasRecord, error) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, err
	}
	address, err := parseAliasAddress(addr)
	if err != nil {
		return nil, err
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil {
		return nil, err
	}
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if containsAliasAddress(record.Addresses, address) {
		return record, nil
	}
	existingAliasBytes, err := m.trie.Get(identityReverseKey(address[:]))
	if err != nil {
		return nil, err
	}
	if len(existingAliasBytes) > 0 {
		existingAlias := string(existingAliasBytes)
		if existingAlias != normalized {
			return nil, identity.ErrAddressLinked
		}
	}
	previousAddresses := copyAliasAddresses(record.Addresses)
	record.Addresses = append(record.Addresses, address)
	if now == 0 {
		now = time.Now().Unix()
	}
	record.UpdatedAt = now
	if err := m.identityPersistRecord(record, normalized, previousAddresses); err != nil {
		return nil, err
	}
	return record, nil
}

// IdentityRemoveAddress disassociates an address from an alias.
func (m *Manager) IdentityRemoveAddress(alias string, addr []byte, now int64) (*identity.AliasRecord, error) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, err
	}
	address, err := parseAliasAddress(addr)
	if err != nil {
		return nil, err
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil {
		return nil, err
	}
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if address == record.Primary {
		return nil, identity.ErrPrimaryAddressRequired
	}
	previousAddresses := copyAliasAddresses(record.Addresses)
	filtered := make([][20]byte, 0, len(record.Addresses))
	removed := false
	for _, existing := range record.Addresses {
		if existing == address {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if !removed {
		return nil, identity.ErrAddressNotLinked
	}
	record.Addresses = filtered
	if now == 0 {
		now = time.Now().Unix()
	}
	record.UpdatedAt = now
	if err := m.identityPersistRecord(record, normalized, previousAddresses); err != nil {
		return nil, err
	}
	return record, nil
}

// IdentitySetPrimary promotes the supplied address to the primary alias address.
func (m *Manager) IdentitySetPrimary(alias string, addr []byte, now int64) (*identity.AliasRecord, error) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, err
	}
	address, err := parseAliasAddress(addr)
	if err != nil {
		return nil, err
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil {
		return nil, err
	}
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	if record.Primary == address {
		return record, nil
	}
	if !containsAliasAddress(record.Addresses, address) {
		existingAliasBytes, err := m.trie.Get(identityReverseKey(address[:]))
		if err != nil {
			return nil, err
		}
		if len(existingAliasBytes) > 0 {
			existingAlias := string(existingAliasBytes)
			if existingAlias != normalized {
				return nil, identity.ErrAddressLinked
			}
		}
		record.Addresses = append(record.Addresses, address)
	}
	previousAddresses := copyAliasAddresses(record.Addresses)
	previousPrimary := record.Primary
	record.Primary = address
	if now == 0 {
		now = time.Now().Unix()
	}
	record.UpdatedAt = now
	if err := m.identityPersistRecord(record, normalized, previousAddresses); err != nil {
		// Restore previous primary in case of retry callers reusing the record reference.
		record.Primary = previousPrimary
		return nil, err
	}
	return record, nil
}

// IdentityRename renames an alias while preserving its metadata and addresses.
func (m *Manager) IdentityRename(alias string, newAlias string, now int64) (*identity.AliasRecord, error) {
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return nil, err
	}
	record, ok, err := m.identityGetAlias(normalized)
	if err != nil {
		return nil, err
	}
	if !ok || record == nil {
		return nil, identity.ErrAliasNotFound
	}
	target, err := identity.NormalizeAlias(newAlias)
	if err != nil {
		return nil, err
	}
	if target == normalized {
		return record, nil
	}
	if _, exists, err := m.identityGetAlias(target); err != nil {
		return nil, err
	} else if exists {
		return nil, identity.ErrAliasTaken
	}
	previousAddresses := copyAliasAddresses(record.Addresses)
	previousAlias := record.Alias
	record.Alias = target
	if now == 0 {
		now = time.Now().Unix()
	}
	record.UpdatedAt = now
	if err := m.identityPersistRecord(record, previousAlias, previousAddresses); err != nil {
		record.Alias = previousAlias
		return nil, err
	}
	return record, nil
}

// IdentityReverse resolves an address to its registered alias.
func (m *Manager) IdentityReverse(addr []byte) (string, bool) {
	if len(addr) != 20 {
		return "", false
	}
	data, err := m.trie.Get(identityReverseKey(addr))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// KVPut stores the provided value under the supplied key using RLP encoding.
// The key is automatically hashed with keccak256 to match the requirements of
// the underlying trie implementation.
func (m *Manager) KVPut(key []byte, value interface{}) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	return m.trie.Update(kvKey(key), encoded)
}

// KVDelete removes the value stored under the supplied key.
func (m *Manager) KVDelete(key []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	return m.trie.Update(kvKey(key), nil)
}

// KVGet retrieves the value stored under the supplied key and decodes it into
// the provided destination. The boolean return value indicates whether the key
// existed in state.
func (m *Manager) KVGet(key []byte, out interface{}) (bool, error) {
	if len(key) == 0 {
		return false, fmt.Errorf("kv: key must not be empty")
	}
	data, err := m.trie.Get(kvKey(key))
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// KVAppend appends the provided value to the RLP-encoded byte slice list stored
// under the supplied key. Duplicate values are ignored to keep the index
// deterministic.
func (m *Manager) KVAppend(key []byte, value []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	hashed := kvKey(key)
	data, err := m.trie.Get(hashed)
	if err != nil {
		return err
	}
	var list [][]byte
	if len(data) > 0 {
		if err := rlp.DecodeBytes(data, &list); err != nil {
			return err
		}
	}
	found := false
	for _, existing := range list {
		if bytes.Equal(existing, value) {
			found = true
			break
		}
	}
	if !found {
		list = append(list, append([]byte(nil), value...))
	}
	encoded, err := rlp.EncodeToBytes(list)
	if err != nil {
		return err
	}
	return m.trie.Update(hashed, encoded)
}

// KVGetList retrieves an RLP-encoded slice stored under the provided key and
// decodes it into the supplied destination slice pointer. When no value is
// present the destination is initialised with an empty slice to avoid nil
// surprises for callers.
func (m *Manager) KVGetList(key []byte, out interface{}) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	hashed := kvKey(key)
	data, err := m.trie.Get(hashed)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		val := reflect.ValueOf(out)
		if val.Kind() != reflect.Ptr || val.IsNil() {
			return fmt.Errorf("kv: destination must be a non-nil pointer")
		}
		elem := val.Elem()
		if elem.Kind() != reflect.Slice {
			return fmt.Errorf("kv: destination must point to a slice")
		}
		elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
		return nil
	}
	return rlp.DecodeBytes(data, out)
}

func creatorContentKey(id string) []byte {
	return []byte(fmt.Sprintf("%s%s", creatorContentPrefix, strings.TrimSpace(id)))
}

func creatorStakeKey(creator [20]byte, fan [20]byte) []byte {
	return []byte(fmt.Sprintf("%s%x/%x", creatorStakePrefix, creator, fan))
}

func creatorLedgerKey(creator [20]byte) []byte {
	return []byte(fmt.Sprintf("%s%x", creatorLedgerPrefix, creator))
}

func creatorRateLimitKey() []byte {
	return append([]byte(nil), creatorRateLimitPrefix...)
}

type storedCreatorContent struct {
	ID          string
	Creator     [20]byte
	URI         string
	Metadata    string
	Hash        string
	PublishedAt int64
	TotalTips   *big.Int
	TotalStake  *big.Int
}

func newStoredCreatorContent(content *creator.Content) *storedCreatorContent {
	if content == nil {
		return nil
	}
	stored := &storedCreatorContent{
		ID:          strings.TrimSpace(content.ID),
		Creator:     content.Creator,
		URI:         strings.TrimSpace(content.URI),
		Metadata:    strings.TrimSpace(content.Metadata),
		Hash:        strings.TrimSpace(content.Hash),
		PublishedAt: content.PublishedAt,
		TotalTips:   big.NewInt(0),
		TotalStake:  big.NewInt(0),
	}
	if content.TotalTips != nil {
		stored.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		stored.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	return stored
}

func (s *storedCreatorContent) toContent() *creator.Content {
	if s == nil {
		return nil
	}
	result := &creator.Content{
		ID:          strings.TrimSpace(s.ID),
		Creator:     s.Creator,
		URI:         strings.TrimSpace(s.URI),
		Metadata:    strings.TrimSpace(s.Metadata),
		Hash:        strings.TrimSpace(s.Hash),
		PublishedAt: s.PublishedAt,
		TotalTips:   big.NewInt(0),
		TotalStake:  big.NewInt(0),
	}
	if s.TotalTips != nil {
		result.TotalTips = new(big.Int).Set(s.TotalTips)
	}
	if s.TotalStake != nil {
		result.TotalStake = new(big.Int).Set(s.TotalStake)
	}
	return result
}

type storedCreatorStake struct {
	Creator     [20]byte
	Fan         [20]byte
	Amount      *big.Int
	Shares      *big.Int
	StakedAt    int64
	LastAccrual int64
}

func newStoredCreatorStake(stake *creator.Stake) *storedCreatorStake {
	if stake == nil {
		return nil
	}
	stored := &storedCreatorStake{
		Creator:     stake.Creator,
		Fan:         stake.Fan,
		Amount:      big.NewInt(0),
		Shares:      big.NewInt(0),
		StakedAt:    stake.StakedAt,
		LastAccrual: stake.LastAccrual,
	}
	if stake.Amount != nil {
		stored.Amount = new(big.Int).Set(stake.Amount)
	}
	if stake.Shares != nil {
		stored.Shares = new(big.Int).Set(stake.Shares)
	}
	return stored
}

func (s *storedCreatorStake) toStake() *creator.Stake {
	if s == nil {
		return nil
	}
	stake := &creator.Stake{
		Creator:     s.Creator,
		Fan:         s.Fan,
		Amount:      big.NewInt(0),
		Shares:      big.NewInt(0),
		StakedAt:    s.StakedAt,
		LastAccrual: s.LastAccrual,
	}
	if s.Amount != nil {
		stake.Amount = new(big.Int).Set(s.Amount)
	}
	if s.Shares != nil {
		stake.Shares = new(big.Int).Set(s.Shares)
	}
	return stake
}

type storedCreatorLedger struct {
	Creator             [20]byte
	TotalTips           *big.Int
	TotalStakingYield   *big.Int
	PendingDistribution *big.Int
	LastPayout          int64
	TotalAssets         *big.Int
	TotalShares         *big.Int
	IndexRay            *big.Int
}

func newStoredCreatorLedger(ledger *creator.PayoutLedger) *storedCreatorLedger {
	if ledger == nil {
		return nil
	}
	stored := &storedCreatorLedger{
		Creator:             ledger.Creator,
		TotalTips:           big.NewInt(0),
		TotalStakingYield:   big.NewInt(0),
		PendingDistribution: big.NewInt(0),
		LastPayout:          ledger.LastPayout,
		TotalAssets:         big.NewInt(0),
		TotalShares:         big.NewInt(0),
		IndexRay:            big.NewInt(0),
	}
	if ledger.TotalTips != nil {
		stored.TotalTips = new(big.Int).Set(ledger.TotalTips)
	}
	if ledger.TotalStakingYield != nil {
		stored.TotalStakingYield = new(big.Int).Set(ledger.TotalStakingYield)
	}
	if ledger.PendingDistribution != nil {
		stored.PendingDistribution = new(big.Int).Set(ledger.PendingDistribution)
	}
	if ledger.TotalAssets != nil {
		stored.TotalAssets = new(big.Int).Set(ledger.TotalAssets)
	}
	if ledger.TotalShares != nil {
		stored.TotalShares = new(big.Int).Set(ledger.TotalShares)
	}
	if ledger.IndexRay != nil {
		stored.IndexRay = new(big.Int).Set(ledger.IndexRay)
	}
	return stored
}

func (s *storedCreatorLedger) toLedger() *creator.PayoutLedger {
	if s == nil {
		return nil
	}
	ledger := &creator.PayoutLedger{
		Creator:             s.Creator,
		TotalTips:           big.NewInt(0),
		TotalStakingYield:   big.NewInt(0),
		PendingDistribution: big.NewInt(0),
		LastPayout:          s.LastPayout,
		TotalAssets:         big.NewInt(0),
		TotalShares:         big.NewInt(0),
		IndexRay:            big.NewInt(0),
	}
	if s.TotalTips != nil {
		ledger.TotalTips = new(big.Int).Set(s.TotalTips)
	}
	if s.TotalStakingYield != nil {
		ledger.TotalStakingYield = new(big.Int).Set(s.TotalStakingYield)
	}
	if s.PendingDistribution != nil {
		ledger.PendingDistribution = new(big.Int).Set(s.PendingDistribution)
	}
	if s.TotalAssets != nil {
		ledger.TotalAssets = new(big.Int).Set(s.TotalAssets)
	}
	if s.TotalShares != nil {
		ledger.TotalShares = new(big.Int).Set(s.TotalShares)
	}
	if s.IndexRay != nil {
		ledger.IndexRay = new(big.Int).Set(s.IndexRay)
	}
	return ledger
}

type storedStakeRateLimit struct {
	Fan         [20]byte
	WindowStart int64
	Amount      *big.Int
}

type storedTipRateLimit struct {
	Creator    [20]byte
	Timestamps []int64
}

type storedCreatorRateLimits struct {
	Stake []*storedStakeRateLimit
	Tip   []*storedTipRateLimit
}

func newStoredCreatorRateLimits(snapshot *creator.RateLimitSnapshot) *storedCreatorRateLimits {
	stored := &storedCreatorRateLimits{}
	if snapshot == nil {
		return stored
	}
	for fan, window := range snapshot.StakeWindows {
		if window == nil {
			continue
		}
		copyWindow := &storedStakeRateLimit{
			Fan:         fan,
			WindowStart: window.WindowStart,
		}
		if window.Amount != nil {
			copyWindow.Amount = new(big.Int).Set(window.Amount)
		} else {
			copyWindow.Amount = big.NewInt(0)
		}
		stored.Stake = append(stored.Stake, copyWindow)
	}
	for creatorAddr, window := range snapshot.TipWindows {
		if window == nil {
			continue
		}
		copyWindow := &storedTipRateLimit{
			Creator:    creatorAddr,
			Timestamps: append([]int64(nil), window.Timestamps...),
		}
		stored.Tip = append(stored.Tip, copyWindow)
	}
	sort.Slice(stored.Stake, func(i, j int) bool {
		return bytes.Compare(stored.Stake[i].Fan[:], stored.Stake[j].Fan[:]) < 0
	})
	sort.Slice(stored.Tip, func(i, j int) bool {
		return bytes.Compare(stored.Tip[i].Creator[:], stored.Tip[j].Creator[:]) < 0
	})
	return stored
}

func (s *storedCreatorRateLimits) toSnapshot() *creator.RateLimitSnapshot {
	if s == nil {
		return &creator.RateLimitSnapshot{
			StakeWindows: make(map[[20]byte]*creator.StakeRateLimitWindow),
			TipWindows:   make(map[[20]byte]*creator.TipRateLimitWindow),
		}
	}
	snapshot := &creator.RateLimitSnapshot{
		StakeWindows: make(map[[20]byte]*creator.StakeRateLimitWindow, len(s.Stake)),
		TipWindows:   make(map[[20]byte]*creator.TipRateLimitWindow, len(s.Tip)),
	}
	for _, window := range s.Stake {
		if window == nil {
			continue
		}
		amount := big.NewInt(0)
		if window.Amount != nil {
			amount = new(big.Int).Set(window.Amount)
		}
		snapshot.StakeWindows[window.Fan] = &creator.StakeRateLimitWindow{
			WindowStart: window.WindowStart,
			Amount:      amount,
		}
	}
	for _, window := range s.Tip {
		if window == nil {
			continue
		}
		snapshot.TipWindows[window.Creator] = &creator.TipRateLimitWindow{
			Timestamps: append([]int64(nil), window.Timestamps...),
		}
	}
	return snapshot
}

// CreatorContentPut persists a creator content record.
func (m *Manager) CreatorContentPut(content *creator.Content) error {
	if content == nil {
		return fmt.Errorf("creator: nil content")
	}
	stored := newStoredCreatorContent(content)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(creatorContentKey(stored.ID), encoded)
}

// CreatorContentGet loads a content record by identifier.
func (m *Manager) CreatorContentGet(id string) (*creator.Content, bool, error) {
	key := creatorContentKey(id)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	stored := new(storedCreatorContent)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false, err
	}
	return stored.toContent(), true, nil
}

// CreatorStakePut stores or updates a fan stake behind a creator.
func (m *Manager) CreatorStakePut(stake *creator.Stake) error {
	if stake == nil {
		return fmt.Errorf("creator: nil stake")
	}
	stored := newStoredCreatorStake(stake)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(creatorStakeKey(stake.Creator, stake.Fan), encoded)
}

// CreatorStakeGet retrieves a stake record for the given creator and fan pair.
func (m *Manager) CreatorStakeGet(creatorAddr [20]byte, fan [20]byte) (*creator.Stake, bool, error) {
	key := creatorStakeKey(creatorAddr, fan)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	stored := new(storedCreatorStake)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false, err
	}
	return stored.toStake(), true, nil
}

// CreatorStakeDelete removes a stake record for the provided creator/fan pair.
func (m *Manager) CreatorStakeDelete(creatorAddr [20]byte, fan [20]byte) error {
	return m.trie.Update(creatorStakeKey(creatorAddr, fan), nil)
}

// CreatorPayoutLedgerPut stores the payout ledger for a creator.
func (m *Manager) CreatorPayoutLedgerPut(ledger *creator.PayoutLedger) error {
	if ledger == nil {
		return fmt.Errorf("creator: nil ledger")
	}
	stored := newStoredCreatorLedger(ledger)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(creatorLedgerKey(ledger.Creator), encoded)
}

// CreatorPayoutLedgerGet fetches the payout ledger for a creator if present.
func (m *Manager) CreatorPayoutLedgerGet(creatorAddr [20]byte) (*creator.PayoutLedger, bool, error) {
	key := creatorLedgerKey(creatorAddr)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	stored := new(storedCreatorLedger)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false, err
	}
	return stored.toLedger(), true, nil
}

// CreatorRateLimitPut persists the in-flight rate limit snapshot for the
// creator engine.
func (m *Manager) CreatorRateLimitPut(snapshot *creator.RateLimitSnapshot) error {
	if snapshot == nil || (len(snapshot.StakeWindows) == 0 && len(snapshot.TipWindows) == 0) {
		return m.trie.Update(creatorRateLimitKey(), nil)
	}
	stored := newStoredCreatorRateLimits(snapshot)
	encoded, err := rlp.EncodeToBytes(stored)
	if err != nil {
		return err
	}
	return m.trie.Update(creatorRateLimitKey(), encoded)
}

// CreatorRateLimitGet retrieves the rate limit snapshot for the creator
// engine, if one has been persisted.
func (m *Manager) CreatorRateLimitGet() (*creator.RateLimitSnapshot, bool, error) {
	key := creatorRateLimitKey()
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	stored := new(storedCreatorRateLimits)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false, err
	}
	return stored.toSnapshot(), true, nil
}
