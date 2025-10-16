package core

import (
	"log/slog"
	"math/big"

	"nhbchain/core/rewards"
	"nhbchain/observability"

	"github.com/ethereum/go-ethereum/rlp"
)

type rewardPayoutRecord struct {
	Address    []byte
	Total      *big.Int
	Validators *big.Int
	Stakers    *big.Int
	Engagement *big.Int
}

type rewardEpochRecord struct {
	Epoch    uint64
	Height   uint64
	ClosedAt uint64
	Blocks   uint64

	PlannedTotal      *big.Int
	PaidTotal         *big.Int
	ValidatorsPlanned *big.Int
	ValidatorsPaid    *big.Int
	StakersPlanned    *big.Int
	StakersPaid       *big.Int
	EngagementPlanned *big.Int
	EngagementPaid    *big.Int

	Payouts []rewardPayoutRecord
}

type stakeRewardRecord struct {
	Index      *big.Int
	LastUpdate uint64
	APRBps     uint64
}

func (sp *StateProcessor) loadStakeRewardState() error {
	if sp == nil || sp.Trie == nil {
		return nil
	}
	data, err := sp.Trie.Get(stakeRewardStateKey)
	if err != nil {
		return err
	}
	if sp.stakeRewardEngine == nil {
		sp.stakeRewardEngine = rewards.NewEngine()
	}
	if len(data) == 0 {
		return nil
	}
	var record stakeRewardRecord
	if err := rlp.DecodeBytes(data, &record); err != nil {
		return err
	}
	if record.Index != nil {
		sp.stakeRewardEngine.SetIndex(record.Index)
	}
	if record.LastUpdate != 0 {
		sp.stakeRewardEngine.SetLastUpdateTs(record.LastUpdate)
	}
	sp.stakeRewardAPR = record.APRBps
	return nil
}

func (sp *StateProcessor) persistStakeRewardState() error {
	if sp == nil || sp.Trie == nil {
		return nil
	}
	if sp.stakeRewardEngine == nil {
		sp.stakeRewardEngine = rewards.NewEngine()
	}
	record := stakeRewardRecord{
		Index:      sp.stakeRewardEngine.Index(),
		LastUpdate: sp.stakeRewardEngine.LastUpdateTs(),
		APRBps:     sp.stakeRewardAPR,
	}
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return sp.Trie.Update(stakeRewardStateKey, encoded)
}

func (sp *StateProcessor) persistStakeRewardStateInstrumented() error {
	persist := sp.persistStakeRewardState
	if sp.stakeRewardPersistOverride != nil {
		persist = sp.stakeRewardPersistOverride
	}
	if err := persist(); err != nil {
		observability.Staking().RecordIndexPersistFailure()
		slog.Error("staking: persist reward state failed", slog.Any("error", err))
		return err
	}
	return nil
}

func (sp *StateProcessor) loadRewardHistory() error {
	data, err := sp.Trie.Get(rewardHistoryKey)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		sp.rewardHistory = make([]rewards.EpochSettlement, 0)
		return nil
	}
	var records []rewardEpochRecord
	if err := rlp.DecodeBytes(data, &records); err != nil {
		return err
	}
	history := make([]rewards.EpochSettlement, len(records))
	for i := range records {
		rec := records[i]
		payouts := make([]rewards.Payout, len(rec.Payouts))
		for j := range rec.Payouts {
			payout := rec.Payouts[j]
			payouts[j] = rewards.Payout{
				Account:    append([]byte(nil), payout.Address...),
				Total:      copyBigInt(payout.Total),
				Validators: copyBigInt(payout.Validators),
				Stakers:    copyBigInt(payout.Stakers),
				Engagement: copyBigInt(payout.Engagement),
			}
		}
		history[i] = rewards.EpochSettlement{
			Epoch:             rec.Epoch,
			Height:            rec.Height,
			ClosedAt:          int64(rec.ClosedAt),
			Blocks:            rec.Blocks,
			PlannedTotal:      copyBigInt(rec.PlannedTotal),
			PaidTotal:         copyBigInt(rec.PaidTotal),
			ValidatorsPlanned: copyBigInt(rec.ValidatorsPlanned),
			ValidatorsPaid:    copyBigInt(rec.ValidatorsPaid),
			StakersPlanned:    copyBigInt(rec.StakersPlanned),
			StakersPaid:       copyBigInt(rec.StakersPaid),
			EngagementPlanned: copyBigInt(rec.EngagementPlanned),
			EngagementPaid:    copyBigInt(rec.EngagementPaid),
			Payouts:           payouts,
		}
	}
	sp.rewardHistory = history
	return nil
}

func (sp *StateProcessor) persistRewardHistory() error {
	records := make([]rewardEpochRecord, len(sp.rewardHistory))
	for i := range sp.rewardHistory {
		settlement := sp.rewardHistory[i]
		payouts := make([]rewardPayoutRecord, len(settlement.Payouts))
		for j := range settlement.Payouts {
			payout := settlement.Payouts[j]
			payouts[j] = rewardPayoutRecord{
				Address:    append([]byte(nil), payout.Account...),
				Total:      copyBigInt(payout.Total),
				Validators: copyBigInt(payout.Validators),
				Stakers:    copyBigInt(payout.Stakers),
				Engagement: copyBigInt(payout.Engagement),
			}
		}
		records[i] = rewardEpochRecord{
			Epoch:             settlement.Epoch,
			Height:            settlement.Height,
			ClosedAt:          uint64(settlement.ClosedAt),
			Blocks:            settlement.Blocks,
			PlannedTotal:      copyBigInt(settlement.PlannedTotal),
			PaidTotal:         copyBigInt(settlement.PaidTotal),
			ValidatorsPlanned: copyBigInt(settlement.ValidatorsPlanned),
			ValidatorsPaid:    copyBigInt(settlement.ValidatorsPaid),
			StakersPlanned:    copyBigInt(settlement.StakersPlanned),
			StakersPaid:       copyBigInt(settlement.StakersPaid),
			EngagementPlanned: copyBigInt(settlement.EngagementPlanned),
			EngagementPaid:    copyBigInt(settlement.EngagementPaid),
			Payouts:           payouts,
		}
	}
	encoded, err := rlp.EncodeToBytes(records)
	if err != nil {
		return err
	}
	return sp.Trie.Update(rewardHistoryKey, encoded)
}

func (sp *StateProcessor) pruneRewardHistory() {
	limit := sp.rewardConfig.HistoryLength
	if limit == 0 {
		return
	}
	if len(sp.rewardHistory) <= int(limit) {
		return
	}
	trim := len(sp.rewardHistory) - int(limit)
	if trim <= 0 {
		return
	}
	sp.rewardHistory = append([]rewards.EpochSettlement(nil), sp.rewardHistory[trim:]...)
}

func (sp *StateProcessor) RewardHistory() []rewards.EpochSettlement {
	history := make([]rewards.EpochSettlement, len(sp.rewardHistory))
	for i := range sp.rewardHistory {
		history[i] = sp.rewardHistory[i].Clone()
	}
	return history
}

func (sp *StateProcessor) RewardEpochSettlement(epochNumber uint64) (*rewards.EpochSettlement, bool) {
	for i := range sp.rewardHistory {
		if sp.rewardHistory[i].Epoch == epochNumber {
			settlement := sp.rewardHistory[i].Clone()
			return &settlement, true
		}
	}
	return nil, false
}

func (sp *StateProcessor) LatestRewardEpochSettlement() (*rewards.EpochSettlement, bool) {
	if len(sp.rewardHistory) == 0 {
		return nil, false
	}
	settlement := sp.rewardHistory[len(sp.rewardHistory)-1].Clone()
	return &settlement, true
}
