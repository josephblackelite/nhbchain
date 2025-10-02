package core

import (
	"bytes"
	"math/big"
	"sort"
	"strconv"

	"nhbchain/core/epoch"
	"nhbchain/core/rewards"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func (sp *StateProcessor) accrueEpochRewards(height uint64) error {
	if !sp.rewardConfig.IsEnabled() {
		return nil
	}
	length := sp.epochConfig.Length
	if length == 0 || height == 0 {
		return nil
	}
	epochNumber := ((height - 1) / length) + 1
	emission := sp.rewardConfig.EmissionForEpoch(epochNumber)
	if emission == nil || emission.Sign() == 0 {
		if sp.rewardAccrual != nil && sp.rewardAccrual.Epoch != epochNumber {
			sp.rewardAccrual = nil
		}
		return nil
	}
	validatorsPlan, stakersPlan, engagementPlan := sp.rewardConfig.SplitEmission(emission)
	if sp.rewardAccrual == nil || sp.rewardAccrual.Epoch != epochNumber {
		sp.rewardAccrual = rewards.NewAccumulator(epochNumber, length, validatorsPlan, stakersPlan, engagementPlan)
	}
	if sp.rewardAccrual == nil {
		return nil
	}
	blockIndex := ((height - 1) % length) + 1
	if sp.rewardAccrual.BlocksProcessed >= blockIndex {
		return nil
	}
	sp.rewardAccrual.AccrueBlock()
	return nil
}

type accountReward struct {
	addr       []byte
	total      *big.Int
	validators *big.Int
	stakers    *big.Int
	engagement *big.Int
}

type remainderEntry struct {
	addr      []byte
	remainder *big.Int
}

func (sp *StateProcessor) settleEpochRewards(snapshot epoch.Snapshot) error {
	if snapshot.Epoch == 0 {
		return nil
	}
	if _, exists := sp.RewardEpochSettlement(snapshot.Epoch); exists {
		sp.rewardAccrual = nil
		return nil
	}

	emission := sp.rewardConfig.EmissionForEpoch(snapshot.Epoch)
	validatorsPlan, stakersPlan, engagementPlan := sp.rewardConfig.SplitEmission(emission)
	blockCount := sp.epochConfig.Length
	if sp.rewardAccrual != nil && sp.rewardAccrual.Epoch == snapshot.Epoch {
		validatorsPlan = copyBigInt(sp.rewardAccrual.ValidatorsPlanned)
		stakersPlan = copyBigInt(sp.rewardAccrual.StakersPlanned)
		engagementPlan = copyBigInt(sp.rewardAccrual.EngagementPlanned)
		if sp.rewardAccrual.BlocksProcessed > 0 {
			blockCount = sp.rewardAccrual.BlocksProcessed
		}
	}

	rewardMap := make(map[string]*accountReward)
	validatorPaid := distributeValidatorRewards(validatorsPlan, snapshot.Selected, rewardMap)
	stakerPaid := distributeStakerRewards(stakersPlan, snapshot.Weights, rewardMap)
	engagementPaid := distributeEngagementRewards(engagementPlan, snapshot.Weights, rewardMap)

	paidTotal := big.NewInt(0)
	paidTotal.Add(paidTotal, validatorPaid)
	paidTotal.Add(paidTotal, stakerPaid)
	paidTotal.Add(paidTotal, engagementPaid)

	plannedTotal := big.NewInt(0)
	plannedTotal.Add(plannedTotal, validatorsPlan)
	plannedTotal.Add(plannedTotal, stakersPlan)
	plannedTotal.Add(plannedTotal, engagementPlan)

	payouts, err := sp.applyAccountRewards(snapshot.Epoch, rewardMap)
	if err != nil {
		return err
	}

	settlement := rewards.EpochSettlement{
		Epoch:             snapshot.Epoch,
		Height:            snapshot.Height,
		ClosedAt:          snapshot.FinalizedAt,
		Blocks:            blockCount,
		PlannedTotal:      plannedTotal,
		PaidTotal:         paidTotal,
		ValidatorsPlanned: validatorsPlan,
		ValidatorsPaid:    validatorPaid,
		StakersPlanned:    stakersPlan,
		StakersPaid:       stakerPaid,
		EngagementPlanned: engagementPlan,
		EngagementPaid:    engagementPaid,
		Payouts:           payouts,
	}

	sp.rewardHistory = append(sp.rewardHistory, settlement)
	sp.pruneRewardHistory()
	if err := sp.persistRewardHistory(); err != nil {
		return err
	}

	attrs := map[string]string{
		"epoch":              strconv.FormatUint(snapshot.Epoch, 10),
		"height":             strconv.FormatUint(snapshot.Height, 10),
		"closed_at":          strconv.FormatInt(snapshot.FinalizedAt, 10),
		"blocks":             strconv.FormatUint(blockCount, 10),
		"planned_total":      plannedTotal.String(),
		"paid_total":         paidTotal.String(),
		"validators_planned": validatorsPlan.String(),
		"validators_paid":    validatorPaid.String(),
		"stakers_planned":    stakersPlan.String(),
		"stakers_paid":       stakerPaid.String(),
		"engagement_planned": engagementPlan.String(),
		"engagement_paid":    engagementPaid.String(),
	}
	sp.AppendEvent(&types.Event{Type: "rewards.epoch_closed", Attributes: attrs})
	sp.rewardAccrual = nil
	return nil
}

func (sp *StateProcessor) applyAccountRewards(epochNumber uint64, rewardMap map[string]*accountReward) ([]rewards.Payout, error) {
	if len(rewardMap) == 0 {
		return []rewards.Payout{}, nil
	}
	keys := make([]string, 0, len(rewardMap))
	for key := range rewardMap {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0
	})
	payouts := make([]rewards.Payout, 0, len(keys))
	for _, key := range keys {
		reward := rewardMap[key]
		if reward == nil || reward.total.Sign() == 0 {
			continue
		}
		account, err := sp.getAccount(reward.addr)
		if err != nil {
			return nil, err
		}
		account.BalanceZNHB.Add(account.BalanceZNHB, reward.total)
		if err := sp.setAccount(reward.addr, account); err != nil {
			return nil, err
		}
		payout := rewards.Payout{
			Account:    append([]byte(nil), reward.addr...),
			Total:      new(big.Int).Set(reward.total),
			Validators: new(big.Int).Set(reward.validators),
			Stakers:    new(big.Int).Set(reward.stakers),
			Engagement: new(big.Int).Set(reward.engagement),
		}
		payouts = append(payouts, payout)

		bech := crypto.MustNewAddress(crypto.NHBPrefix, reward.addr)
		attrs := map[string]string{
			"epoch":   strconv.FormatUint(epochNumber, 10),
			"account": bech.String(),
			"amount":  payout.Total.String(),
		}
		if reward.validators.Sign() > 0 {
			attrs["validators"] = reward.validators.String()
		}
		if reward.stakers.Sign() > 0 {
			attrs["stakers"] = reward.stakers.String()
		}
		if reward.engagement.Sign() > 0 {
			attrs["engagement"] = reward.engagement.String()
		}
		sp.AppendEvent(&types.Event{Type: "rewards.paid", Attributes: attrs})
	}
	return payouts, nil
}

func distributeValidatorRewards(total *big.Int, selected [][]byte, rewardMap map[string]*accountReward) *big.Int {
	if total == nil || total.Sign() == 0 {
		return big.NewInt(0)
	}
	unique := make(map[string][]byte)
	for _, addr := range selected {
		if len(addr) == 0 {
			continue
		}
		key := string(addr)
		if _, exists := unique[key]; !exists {
			unique[key] = append([]byte(nil), addr...)
		}
	}
	if len(unique) == 0 {
		return big.NewInt(0)
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0
	})
	count := len(keys)
	base := new(big.Int).Quo(new(big.Int).Set(total), big.NewInt(int64(count)))
	remainder := new(big.Int).Mod(new(big.Int).Set(total), big.NewInt(int64(count)))
	distributed := big.NewInt(0)
	one := big.NewInt(1)
	for _, key := range keys {
		amount := new(big.Int).Set(base)
		if remainder.Sign() > 0 {
			amount.Add(amount, one)
			remainder.Sub(remainder, one)
		}
		if amount.Sign() == 0 {
			continue
		}
		addValidatorReward(rewardMap, unique[key], amount)
		distributed.Add(distributed, amount)
	}
	return distributed
}

func distributeStakerRewards(total *big.Int, weights []epoch.Weight, rewardMap map[string]*accountReward) *big.Int {
	return distributeProRata(total, weights, func(w epoch.Weight) *big.Int {
		if w.Stake == nil {
			return big.NewInt(0)
		}
		return w.Stake
	}, func(addr []byte, amount *big.Int) {
		addStakerReward(rewardMap, addr, amount)
	})
}

func distributeEngagementRewards(total *big.Int, weights []epoch.Weight, rewardMap map[string]*accountReward) *big.Int {
	return distributeProRata(total, weights, func(w epoch.Weight) *big.Int {
		if w.Engagement == 0 {
			return big.NewInt(0)
		}
		return new(big.Int).SetUint64(w.Engagement)
	}, func(addr []byte, amount *big.Int) {
		addEngagementReward(rewardMap, addr, amount)
	})
}

func distributeProRata(total *big.Int, weights []epoch.Weight, valueFn func(epoch.Weight) *big.Int, apply func([]byte, *big.Int)) *big.Int {
	if total == nil || total.Sign() == 0 {
		return big.NewInt(0)
	}
	entries := make([]struct {
		addr  []byte
		value *big.Int
	}, 0, len(weights))
	denominator := big.NewInt(0)
	for i := range weights {
		value := valueFn(weights[i])
		if value == nil || value.Sign() <= 0 {
			continue
		}
		addr := append([]byte(nil), weights[i].Address...)
		entries = append(entries, struct {
			addr  []byte
			value *big.Int
		}{addr: addr, value: new(big.Int).Set(value)})
		denominator.Add(denominator, value)
	}
	if denominator.Sign() == 0 || len(entries) == 0 {
		return big.NewInt(0)
	}
	distributed := big.NewInt(0)
	remainders := make([]remainderEntry, len(entries))
	for i := range entries {
		product := new(big.Int).Mul(total, entries[i].value)
		share := new(big.Int)
		remainder := new(big.Int)
		share.QuoRem(product, denominator, remainder)
		if share.Sign() > 0 {
			apply(entries[i].addr, share)
			distributed.Add(distributed, share)
		}
		remainders[i] = remainderEntry{addr: entries[i].addr, remainder: remainder}
	}
	leftover := new(big.Int).Sub(new(big.Int).Set(total), distributed)
	if leftover.Sign() > 0 {
		sort.Slice(remainders, func(i, j int) bool {
			cmp := remainders[i].remainder.Cmp(remainders[j].remainder)
			if cmp == 0 {
				return bytes.Compare(remainders[i].addr, remainders[j].addr) < 0
			}
			return cmp > 0
		})
		leftoverUnits := leftover.Uint64()
		one := big.NewInt(1)
		for _, entry := range remainders {
			if leftoverUnits == 0 {
				break
			}
			if entry.remainder.Sign() == 0 {
				continue
			}
			apply(entry.addr, new(big.Int).Set(one))
			distributed.Add(distributed, one)
			leftoverUnits--
		}
		if leftoverUnits > 0 {
			sort.Slice(remainders, func(i, j int) bool {
				return bytes.Compare(remainders[i].addr, remainders[j].addr) < 0
			})
			for _, entry := range remainders {
				if leftoverUnits == 0 {
					break
				}
				apply(entry.addr, new(big.Int).Set(one))
				distributed.Add(distributed, one)
				leftoverUnits--
			}
		}
	}
	return distributed
}

func ensureAccountReward(m map[string]*accountReward, addr []byte) *accountReward {
	key := string(addr)
	reward, ok := m[key]
	if !ok {
		reward = &accountReward{
			addr:       append([]byte(nil), addr...),
			total:      big.NewInt(0),
			validators: big.NewInt(0),
			stakers:    big.NewInt(0),
			engagement: big.NewInt(0),
		}
		m[key] = reward
	}
	return reward
}

func addValidatorReward(m map[string]*accountReward, addr []byte, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}
	reward := ensureAccountReward(m, addr)
	reward.validators.Add(reward.validators, amount)
	reward.total.Add(reward.total, amount)
}

func addStakerReward(m map[string]*accountReward, addr []byte, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}
	reward := ensureAccountReward(m, addr)
	reward.stakers.Add(reward.stakers, amount)
	reward.total.Add(reward.total, amount)
}

func addEngagementReward(m map[string]*accountReward, addr []byte, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}
	reward := ensureAccountReward(m, addr)
	reward.engagement.Add(reward.engagement, amount)
	reward.total.Add(reward.total, amount)
}
