package core

import (
	"fmt"
	"math/big"
	"time"

	"nhbchain/core/epoch"
	"nhbchain/core/events"
	"nhbchain/core/types"

	"github.com/ethereum/go-ethereum/rlp"
)

const (
	validatorReadinessMinGrace          = 15 * time.Minute
	validatorReadinessHeartbeatMultiple = 5
)

type epochWeightRecord struct {
	Address    []byte
	Stake      *big.Int
	Engagement uint64
	Composite  *big.Int
}

type epochSnapshotRecord struct {
	Epoch       uint64
	Height      uint64
	FinalizedAt uint64
	TotalWeight *big.Int
	Weights     []epochWeightRecord
	Selected    [][]byte
}

func (sp *StateProcessor) loadEpochHistory() error {
	data, err := sp.Trie.Get(epochHistoryKey)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		sp.epochHistory = make([]epoch.Snapshot, 0)
		return nil
	}
	var records []epochSnapshotRecord
	if err := rlp.DecodeBytes(data, &records); err != nil {
		return err
	}
	history := make([]epoch.Snapshot, len(records))
	for i := range records {
		rec := records[i]
		total := copyBigInt(rec.TotalWeight)
		weights := make([]epoch.Weight, len(rec.Weights))
		for j := range rec.Weights {
			weights[j] = epoch.Weight{
				Address:    append([]byte(nil), rec.Weights[j].Address...),
				Stake:      copyBigInt(rec.Weights[j].Stake),
				Engagement: rec.Weights[j].Engagement,
				Composite:  copyBigInt(rec.Weights[j].Composite),
			}
		}
		selected := make([][]byte, len(rec.Selected))
		for j := range rec.Selected {
			selected[j] = append([]byte(nil), rec.Selected[j]...)
		}
		history[i] = epoch.Snapshot{
			Epoch:       rec.Epoch,
			Height:      rec.Height,
			FinalizedAt: int64(rec.FinalizedAt),
			TotalWeight: total,
			Weights:     weights,
			Selected:    selected,
		}
	}
	sp.epochHistory = history
	return nil
}

func (sp *StateProcessor) persistEpochHistory() error {
	records := make([]epochSnapshotRecord, len(sp.epochHistory))
	for i := range sp.epochHistory {
		snapshot := sp.epochHistory[i]
		weights := make([]epochWeightRecord, len(snapshot.Weights))
		for j := range snapshot.Weights {
			weights[j] = epochWeightRecord{
				Address:    append([]byte(nil), snapshot.Weights[j].Address...),
				Stake:      copyBigInt(snapshot.Weights[j].Stake),
				Engagement: snapshot.Weights[j].Engagement,
				Composite:  copyBigInt(snapshot.Weights[j].Composite),
			}
		}
		selected := make([][]byte, len(snapshot.Selected))
		for j := range snapshot.Selected {
			selected[j] = append([]byte(nil), snapshot.Selected[j]...)
		}
		records[i] = epochSnapshotRecord{
			Epoch:       snapshot.Epoch,
			Height:      snapshot.Height,
			FinalizedAt: uint64(snapshot.FinalizedAt),
			TotalWeight: copyBigInt(snapshot.TotalWeight),
			Weights:     weights,
			Selected:    selected,
		}
	}
	encoded, err := rlp.EncodeToBytes(records)
	if err != nil {
		return err
	}
	return sp.Trie.Update(epochHistoryKey, encoded)
}

func (sp *StateProcessor) pruneEpochHistory() {
	limit := sp.epochConfig.SnapshotHistory
	if limit == 0 {
		return
	}
	if len(sp.epochHistory) <= int(limit) {
		return
	}
	trim := len(sp.epochHistory) - int(limit)
	if trim <= 0 {
		return
	}
	sp.epochHistory = append([]epoch.Snapshot(nil), sp.epochHistory[trim:]...)
}

func (sp *StateProcessor) ProcessBlockLifecycle(height uint64, timestamp int64) error {
	if err := sp.pruneQuotaCounters(time.Unix(timestamp, 0).UTC()); err != nil {
		return err
	}
	// Subscription billing runs on a calendar-day cadence
	// (Plan.IntervalSeconds is typically monthly), which has no natural
	// relationship to epochConfig.Length -- unlike settleEpochRewards and
	// settleBuybackEpoch below, this must not wait for an epoch boundary.
	// settleSubscriptionCharges is internally day-gated against its own
	// persisted watermark, so calling it unconditionally on every block is
	// a cheap no-op on every block that isn't a day rollover.
	if err := sp.settleSubscriptionCharges(timestamp); err != nil {
		return err
	}
	if err := sp.maybeProcessPotsoRewards(height, timestamp); err != nil {
		return err
	}
	if err := sp.accrueEpochRewards(height); err != nil {
		return err
	}
	// Called here, as part of normal block state-transition, rather than at
	// node startup: EnsureZNHBPoolsBootstrapped's writes must be folded into
	// a real committed block (like every other write in this function) so
	// they can't be silently discarded by the startup/peer-block state-root
	// drift-reset in core/node.go -- see NewNode's comment on why it
	// deliberately does not call this itself. Idempotent, so this is a
	// cheap no-op on every block after the first successful bootstrap.
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		return fmt.Errorf("bootstrap ZNHB sale/reward pools: %w", err)
	}
	// One-time repair for the pre-fix reward-payout accounting gap (see
	// ReconcileZNHBSupplyDriftOnce's doc comment) -- must run before the
	// invariant check below, and is a no-op forever after its guard flag
	// is set, so it can never mask a genuine future violation.
	if err := sp.ReconcileZNHBSupplyDriftOnce(); err != nil {
		return fmt.Errorf("reconcile ZNHB supply drift: %w", err)
	}
	// One-time backfill for the pre-2026-08-13 delegator-reward-attribution
	// gap (see BackfillStakeDelegationIndexOnce's doc comment). Moves no
	// funds and doesn't affect the supply invariant either way, so ordering
	// relative to the two calls above/below doesn't matter -- placed here
	// only to stay grouped with the other one-time state-repair migrations.
	if err := sp.BackfillStakeDelegationIndexOnce(); err != nil {
		return fmt.Errorf("backfill stake delegation index: %w", err)
	}
	// One-time seed for the missing genesis NHB supply (see
	// SeedGenesisNHBSupplyOnce's doc comment) -- must run before any
	// TxTypeRedeemNHB burn is processed in this same block, since that path
	// now decrements this counter and would underflow without it. No
	// ordering dependency on the ZNHB calls above.
	//
	// ProcessBlockLifecycle itself only runs AFTER the block's own
	// transactions are applied (see core/node.go's three ApplyTransaction
	// loops), so relying solely on this call would leave exactly the
	// underflow window described above. core/node.go now also calls
	// SeedGenesisNHBSupplyOnce directly, before each of those tx loops --
	// this call stays here too, idempotent and effectively free once seeded,
	// as a backstop for any other lifecycle call site.
	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		return fmt.Errorf("seed genesis NHB supply: %w", err)
	}
	// Item 5's grandfathering migration -- see BackfillValidatorRegistrationOnce's
	// doc comment. Must run on every block (idempotent, no-op after its
	// guard flag is set) so the currently-active validator(s) are
	// registered with zero operator action the instant this deploys.
	if err := sp.BackfillValidatorRegistrationOnce(); err != nil {
		return fmt.Errorf("backfill validator registration: %w", err)
	}
	// One-time cleanup of the specific stale PendingUnbonds entries left on
	// the admin wallet by the 2026-08-26 incident (see
	// ClearAdminStalePendingUnbondsOnce's doc comment) -- must run before
	// the invariant check below, since CheckZNHBSupplyInvariant now counts
	// PendingUnbonds, and is a no-op forever after its guard flag is set.
	if err := sp.ClearAdminStalePendingUnbondsOnce(); err != nil {
		return fmt.Errorf("clear admin stale pending unbonds: %w", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		return err
	}
	if sp.epochConfig.Length == 0 {
		return nil
	}
	if height == 0 {
		return nil
	}
	if height%sp.epochConfig.Length != 0 {
		return nil
	}
	if err := sp.tickLoyaltySmoothing(); err != nil {
		return err
	}
	return sp.finalizeEpoch(height, timestamp)
}

func (sp *StateProcessor) finalizeEpoch(height uint64, timestamp int64) error {
	now := time.Unix(timestamp, 0).UTC()
	weights, totalWeight, err := sp.computeEpochWeights(now)
	if err != nil {
		return err
	}
	epochNumber := height / sp.epochConfig.Length
	selected, err := sp.selectValidators(weights)
	if err != nil {
		return err
	}
	snapshot := epoch.Snapshot{
		Epoch:       epochNumber,
		Height:      height,
		FinalizedAt: timestamp,
		TotalWeight: totalWeight,
		Weights:     weights,
		Selected:    selected,
	}
	if err := sp.settleEpochRewards(snapshot); err != nil {
		return err
	}
	if err := sp.settleBuybackEpoch(snapshot); err != nil {
		return err
	}
	sp.epochHistory = append(sp.epochHistory, snapshot)
	sp.pruneEpochHistory()
	if err := sp.persistEpochHistory(); err != nil {
		return err
	}
	sp.emitEpochEvents(snapshot)
	return sp.applyValidatorSelection(snapshot, now)
}

func (sp *StateProcessor) computeEpochWeights(now time.Time) ([]epoch.Weight, *big.Int, error) {
	if sp.EligibleValidators == nil {
		return []epoch.Weight{}, big.NewInt(0), nil
	}
	weights := make([]epoch.Weight, 0, len(sp.EligibleValidators))
	total := big.NewInt(0)
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return nil, nil, err
	}
	for addrKey := range sp.EligibleValidators {
		addrBytes := []byte(addrKey)
		account, err := sp.getAccount(addrBytes)
		if err != nil {
			return nil, nil, err
		}
		if !account.ValidatorRegistered {
			continue
		}
		basis, err := sp.stakeRewardBasis(account, addrBytes)
		if err != nil {
			return nil, nil, err
		}
		if basis == nil || basis.Cmp(minStake) < 0 {
			continue
		}
		// See selfDelegated's doc comment: without this, an address that has
		// delegated its own stake AWAY to a different validator would have
		// basis read back as that delegated-away amount (stakeRewardBasis
		// falls back to LockedZNHB once DelegatedValidator points elsewhere),
		// which is not real own-stake and must not count toward epoch weight.
		if !selfDelegated(account, addrBytes) {
			continue
		}
		if !sp.validatorReadyForActivation(account, now) {
			continue
		}
		composite := epoch.ComputeCompositeWeight(sp.epochConfig, basis, account.EngagementScore)
		weight := epoch.Weight{
			Address:    append([]byte(nil), addrBytes...),
			Stake:      copyBigInt(basis),
			Engagement: account.EngagementScore,
			Composite:  composite,
		}
		weights = append(weights, weight)
		total.Add(total, composite)
	}
	epoch.SortWeights(weights)
	return weights, total, nil
}

func (sp *StateProcessor) selectValidators(weights []epoch.Weight) ([][]byte, error) {
	if !sp.epochConfig.RotationEnabled || sp.epochConfig.MaxValidators == 0 {
		selected := make([][]byte, len(weights))
		for i := range weights {
			selected[i] = append([]byte(nil), weights[i].Address...)
		}
		return selected, nil
	}
	count := int(sp.epochConfig.MaxValidators)
	if count <= 0 {
		return [][]byte{}, nil
	}
	selected := make([][]byte, 0, count)
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return nil, err
	}
	for _, w := range weights {
		if w.Stake == nil || w.Stake.Cmp(minStake) < 0 {
			continue
		}
		selected = append(selected, append([]byte(nil), w.Address...))
		if len(selected) == count {
			break
		}
	}
	return selected, nil
}

func (sp *StateProcessor) applyValidatorSelection(snapshot epoch.Snapshot, now time.Time) error {
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return err
	}
	if sp.epochConfig.RotationEnabled {
		newSet := make(map[string]*big.Int, len(snapshot.Selected))
		for _, addr := range snapshot.Selected {
			account, err := sp.getAccount(addr)
			if err != nil {
				return err
			}
			if !account.ValidatorRegistered {
				continue
			}
			basis, err := sp.stakeRewardBasis(account, addr)
			if err != nil {
				return err
			}
			if basis == nil || basis.Cmp(minStake) < 0 {
				continue
			}
			// See selfDelegated's doc comment / computeEpochWeights above --
			// same gap, independently reachable here since this branch
			// re-derives basis straight from live account state rather than
			// going through EligibleValidators.
			if !selfDelegated(account, addr) {
				continue
			}
			newSet[string(addr)] = copyBigInt(basis)
		}
		if len(newSet) == 0 {
			fallback, err := sp.fallbackValidatorSet(minStake)
			if err != nil {
				return err
			}
			if len(fallback) > 0 {
				newSet = fallback
			}
		}
		sp.ValidatorSet = newSet
		if err := sp.persistValidatorSet(); err != nil {
			return err
		}
		rotation := events.ValidatorsRotated{Epoch: snapshot.Epoch, Validators: snapshot.Selected}
		if payload := rotation.Event(); payload != nil {
			sp.AppendEvent(payload)
		}
		return nil
	}

	desired := make(map[string]*big.Int, len(sp.EligibleValidators))
	for k, v := range sp.EligibleValidators {
		if v == nil || v.Cmp(minStake) < 0 {
			continue
		}
		account, err := sp.getAccount([]byte(k))
		if err != nil {
			return err
		}
		// EligibleValidators is already gated+basis'd by setAccount, so this
		// is functionally covered by that alone -- kept here as
		// defense-in-depth against any path that could ever repopulate
		// EligibleValidators outside setAccount (legacy-decode/restore paths,
		// or the manual admin recovery primitives in core/node.go).
		if !account.ValidatorRegistered {
			continue
		}
		if !sp.validatorReadyForActivation(account, now) {
			continue
		}
		desired[k] = copyBigInt(v)
	}
	if len(desired) == 0 {
		fallback, err := sp.fallbackValidatorSet(minStake)
		if err != nil {
			return err
		}
		if len(fallback) > 0 {
			desired = fallback
		}
	}
	if !validatorMapsEqual(sp.ValidatorSet, desired) {
		sp.ValidatorSet = desired
		if err := sp.persistValidatorSet(); err != nil {
			return err
		}
	}
	return nil
}

func (sp *StateProcessor) ensureValidatorSetLiveness(now time.Time) error {
	if sp == nil || len(sp.ValidatorSet) > 0 {
		return nil
	}
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return err
	}
	fallback, err := sp.fallbackValidatorSet(minStake)
	if err != nil {
		return err
	}
	if len(fallback) == 0 {
		return nil
	}
	sp.ValidatorSet = fallback
	return sp.persistValidatorSet()
}

func (sp *StateProcessor) fallbackValidatorSet(minStake *big.Int) (map[string]*big.Int, error) {
	fallback := make(map[string]*big.Int)
	addrs := make([][]byte, 0, len(sp.ValidatorSet)+len(sp.EligibleValidators))
	seen := make(map[string]struct{})
	appendAddr := func(addr []byte) {
		key := string(addr)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		addrs = append(addrs, append([]byte(nil), addr...))
	}

	for addrKey := range sp.ValidatorSet {
		appendAddr([]byte(addrKey))
	}
	for addrKey := range sp.EligibleValidators {
		appendAddr([]byte(addrKey))
	}
	for i := len(sp.epochHistory) - 1; i >= 0; i-- {
		for _, addr := range sp.epochHistory[i].Selected {
			appendAddr(addr)
		}
	}

	for _, addr := range addrs {
		account, err := sp.getAccount(addr)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		// This fallback deliberately bypasses validatorReadyForActivation
		// (see that function's own registration gate) so it needs its own
		// explicit registration check -- without it, a pre-fix phantom
		// address sitting in historical epochHistory[i].Selected (one of
		// this function's three address sources) could be resurrected here
		// purely on stake + a heartbeat, with no registration at all.
		if !account.ValidatorRegistered {
			continue
		}
		basis, err := sp.stakeRewardBasis(account, addr)
		if err != nil {
			return nil, err
		}
		if basis == nil || basis.Cmp(minStake) < 0 {
			continue
		}
		// See selfDelegated's doc comment -- same gap, independently
		// reachable here: an address whose own stake has been fully
		// delegated away to a different validator must not be resurrected
		// into the fallback set on the strength of money it no longer has
		// backing its own candidacy.
		if !selfDelegated(account, addr) {
			continue
		}
		// This fallback intentionally skips the heartbeat-recency check
		// (validatorReadyForActivation) so a validator having a brief
		// liveness blip doesn't zero out the active set. But an address
		// that has NEVER once sent a heartbeat has never proven it runs
		// a node at all -- resurrecting it here would let a pure stake
		// balance (e.g. from an accidental self-stake with no validator
		// target) get swept into active consensus power the moment every
		// real validator's heartbeat happens to lapse at once, which is
		// exactly the failure mode that stalled quorum in production.
		if account.EngagementLastHeartbeat == 0 {
			continue
		}
		fallback[string(addr)] = copyBigInt(basis)
	}
	return fallback, nil
}

func (sp *StateProcessor) validatorReadyForActivation(account *types.Account, now time.Time) bool {
	if sp == nil || account == nil {
		return false
	}
	// Item 4: the heartbeat-based liveness gate below must only ever be
	// consulted for accounts that are explicitly registered (item 1) -- an
	// unregistered account's heartbeats (ordinary POTSO/engagement signal
	// for everyone) must have zero effect on validator-related computation.
	// This is the single canonical liveness-check function (called from
	// computeEpochWeights and applyValidatorSelection's non-rotation
	// branch), so the gate lives here once rather than at each call site.
	if !account.ValidatorRegistered {
		return false
	}
	if account.EngagementLastHeartbeat == 0 {
		return false
	}
	last := time.Unix(int64(account.EngagementLastHeartbeat), 0).UTC()
	if last.After(now.Add(2 * time.Minute)) {
		return false
	}
	return now.Sub(last) <= sp.validatorReadinessGracePeriod()
}

func (sp *StateProcessor) validatorReadinessGracePeriod() time.Duration {
	if sp == nil {
		return validatorReadinessMinGrace
	}
	grace := time.Duration(validatorReadinessHeartbeatMultiple) * sp.engagementConfig.HeartbeatInterval
	if grace < validatorReadinessMinGrace {
		grace = validatorReadinessMinGrace
	}
	return grace
}

func (sp *StateProcessor) emitEpochEvents(snapshot epoch.Snapshot) {
	finalised := events.EpochFinalized{
		Epoch:         snapshot.Epoch,
		Height:        snapshot.Height,
		FinalizedAt:   snapshot.FinalizedAt,
		TotalWeight:   snapshot.TotalWeight,
		EligibleCount: len(snapshot.Weights),
	}
	if payload := finalised.Event(); payload != nil {
		sp.AppendEvent(payload)
	}
}

func validatorMapsEqual(a, b map[string]*big.Int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		other, ok := b[k]
		if !ok {
			return false
		}
		if v == nil && other == nil {
			continue
		}
		if v == nil || other == nil {
			return false
		}
		if v.Cmp(other) != 0 {
			return false
		}
	}
	return true
}

func (sp *StateProcessor) EpochHistory() []epoch.Snapshot {
	history := make([]epoch.Snapshot, len(sp.epochHistory))
	for i := range sp.epochHistory {
		history[i] = cloneEpochSnapshot(sp.epochHistory[i])
	}
	return history
}

func (sp *StateProcessor) EpochSnapshot(epochNumber uint64) (*epoch.Snapshot, bool) {
	for i := range sp.epochHistory {
		if sp.epochHistory[i].Epoch == epochNumber {
			snapshot := cloneEpochSnapshot(sp.epochHistory[i])
			return &snapshot, true
		}
	}
	return nil, false
}

func (sp *StateProcessor) LatestEpochSnapshot() (*epoch.Snapshot, bool) {
	if len(sp.epochHistory) == 0 {
		return nil, false
	}
	snapshot := cloneEpochSnapshot(sp.epochHistory[len(sp.epochHistory)-1])
	return &snapshot, true
}

func (sp *StateProcessor) LatestEpochSummary() (*epoch.Summary, bool) {
	latest, ok := sp.LatestEpochSnapshot()
	if !ok {
		return nil, false
	}
	summary := latest.Summary()
	return &summary, true
}

func cloneEpochSnapshot(snapshot epoch.Snapshot) epoch.Snapshot {
	total := copyBigInt(snapshot.TotalWeight)
	weights := make([]epoch.Weight, len(snapshot.Weights))
	for i := range snapshot.Weights {
		weights[i] = epoch.Weight{
			Address:    append([]byte(nil), snapshot.Weights[i].Address...),
			Stake:      copyBigInt(snapshot.Weights[i].Stake),
			Engagement: snapshot.Weights[i].Engagement,
			Composite:  copyBigInt(snapshot.Weights[i].Composite),
		}
	}
	selected := make([][]byte, len(snapshot.Selected))
	for i := range snapshot.Selected {
		selected[i] = append([]byte(nil), snapshot.Selected[i]...)
	}
	return epoch.Snapshot{
		Epoch:       snapshot.Epoch,
		Height:      snapshot.Height,
		FinalizedAt: snapshot.FinalizedAt,
		TotalWeight: total,
		Weights:     weights,
		Selected:    selected,
	}
}

func copyBigInt(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}
