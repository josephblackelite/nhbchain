package core

import (
	"math/big"

	"nhbchain/core/epoch"
	"nhbchain/core/events"

	"github.com/ethereum/go-ethereum/rlp"
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
	if err := sp.maybeProcessPotsoRewards(height, timestamp); err != nil {
		return err
	}
	if err := sp.accrueEpochRewards(height); err != nil {
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
	return sp.finalizeEpoch(height, timestamp)
}

func (sp *StateProcessor) finalizeEpoch(height uint64, timestamp int64) error {
	weights, totalWeight, err := sp.computeEpochWeights()
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
	sp.epochHistory = append(sp.epochHistory, snapshot)
	sp.pruneEpochHistory()
	if err := sp.persistEpochHistory(); err != nil {
		return err
	}
	sp.emitEpochEvents(snapshot)
	return sp.applyValidatorSelection(snapshot)
}

func (sp *StateProcessor) computeEpochWeights() ([]epoch.Weight, *big.Int, error) {
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
		if account.Stake == nil || account.Stake.Cmp(minStake) < 0 {
			continue
		}
		composite := epoch.ComputeCompositeWeight(sp.epochConfig, account.Stake, account.EngagementScore)
		weight := epoch.Weight{
			Address:    append([]byte(nil), addrBytes...),
			Stake:      copyBigInt(account.Stake),
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

func (sp *StateProcessor) applyValidatorSelection(snapshot epoch.Snapshot) error {
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
			if account.Stake == nil || account.Stake.Cmp(minStake) < 0 {
				continue
			}
			newSet[string(addr)] = copyBigInt(account.Stake)
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
		desired[k] = copyBigInt(v)
	}
	if !validatorMapsEqual(sp.ValidatorSet, desired) {
		sp.ValidatorSet = desired
		if err := sp.persistValidatorSet(); err != nil {
			return err
		}
	}
	return nil
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
