package potso

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
)

const (
	// WeightBpsDenominator defines the scaling factor for basis point math.
	WeightBpsDenominator uint64 = 10000
	// engagementBetaScale is the fixed point denominator used when applying
	// exponential decay to engagement meters.
	engagementBetaScale uint64 = 1_000_000_000
)

// TieBreakMode describes how deterministic ordering is applied when multiple
// participants share the same composite weight.
type TieBreakMode string

const (
	// TieBreakAddrHash sorts ties by the SHA-256 digest of the address in
	// ascending order. This provides a stable yet hard-to-game ordering.
	TieBreakAddrHash TieBreakMode = "addrHash"
	// TieBreakAddrLex sorts ties lexicographically by raw address bytes.
	TieBreakAddrLex TieBreakMode = "addrLex"
)

// WeightParams controls the composite engagement weighting pipeline.
type WeightParams struct {
	AlphaStakeBps          uint64
	TxWeightBps            uint64
	EscrowWeightBps        uint64
	UptimeWeightBps        uint64
	MaxEngagementPerEpoch  uint64
	MinStakeToWinWei       *big.Int
	MinStakeToEarnWei      *big.Int
	MinEngagementToWin     uint64
	DecayHalfLifeEpochs    uint64
	TopKWinners            uint64
	TieBreak               TieBreakMode
	QuadraticTxDampenAfter uint64
	QuadraticTxDampenPower uint64
}

// DefaultWeightParams returns a conservative baseline configuration.
func DefaultWeightParams() WeightParams {
	return WeightParams{
		AlphaStakeBps:          7000,
		TxWeightBps:            6000,
		EscrowWeightBps:        3000,
		UptimeWeightBps:        1000,
		MaxEngagementPerEpoch:  1000,
		MinStakeToWinWei:       big.NewInt(0),
		MinStakeToEarnWei:      big.NewInt(0),
		MinEngagementToWin:     0,
		DecayHalfLifeEpochs:    7,
		TopKWinners:            5000,
		TieBreak:               TieBreakAddrHash,
		QuadraticTxDampenAfter: 0,
		QuadraticTxDampenPower: 2,
	}
}

// Validate ensures the configuration is internally consistent.
func (p WeightParams) Validate() error {
	if p.AlphaStakeBps > WeightBpsDenominator {
		return fmt.Errorf("alpha stake weight must be <= %d", WeightBpsDenominator)
	}
	if p.TxWeightBps > WeightBpsDenominator || p.EscrowWeightBps > WeightBpsDenominator || p.UptimeWeightBps > WeightBpsDenominator {
		return fmt.Errorf("component weights must be <= %d", WeightBpsDenominator)
	}
	if p.MinStakeToWinWei != nil && p.MinStakeToWinWei.Sign() < 0 {
		return errors.New("min stake to win cannot be negative")
	}
	if p.MinStakeToEarnWei != nil && p.MinStakeToEarnWei.Sign() < 0 {
		return errors.New("min stake to earn cannot be negative")
	}
	switch p.TieBreak {
	case TieBreakAddrHash, TieBreakAddrLex, "":
	default:
		return fmt.Errorf("unsupported tie break mode %q", p.TieBreak)
	}
	return nil
}

// EngagementMeter captures raw counters accumulated over the epoch.
type EngagementMeter struct {
	TxCount       uint64
	EscrowCount   uint64
	UptimeDevices uint64
}

// WeightInput bundles the raw state required to compute composite weights for
// an address.
type WeightInput struct {
	Address            [20]byte
	Stake              *big.Int
	PreviousEngagement uint64
	Meter              EngagementMeter
}

// WeightEntry captures the derived weighting components for a participant.
type WeightEntry struct {
	Address            [20]byte
	Stake              *big.Int
	Engagement         uint64
	StakeShare         *big.Rat
	EngagementShare    *big.Rat
	Weight             *big.Rat
	StakeShareBps      uint64
	EngagementShareBps uint64
	WeightBps          uint64
	tieKey             []byte
}

// WeightSnapshot summarises the composite results for an epoch. Entries are
// sorted in descending weight order and truncated according to TopKWinners.
type WeightSnapshot struct {
	Epoch           uint64
	TotalStake      *big.Int
	TotalEngagement uint64
	Entries         []WeightEntry
}

// StoredWeightEntry provides a serialisable representation suitable for state
// persistence and RPC responses.
type StoredWeightEntry struct {
	Address            [20]byte
	Stake              *big.Int
	Engagement         uint64
	StakeShareBps      uint64
	EngagementShareBps uint64
	WeightBps          uint64
}

// StoredWeightSnapshot mirrors WeightSnapshot but omits transient rationals so
// it can be encoded into the state trie.
type StoredWeightSnapshot struct {
	Epoch           uint64
	TotalStake      *big.Int
	TotalEngagement uint64
	Entries         []StoredWeightEntry
}

// ToStored converts an in-memory snapshot into its serialisable form.
func (s *WeightSnapshot) ToStored() *StoredWeightSnapshot {
	if s == nil {
		return nil
	}
	stored := &StoredWeightSnapshot{
		Epoch:           s.Epoch,
		TotalStake:      copyBigInt(s.TotalStake),
		TotalEngagement: s.TotalEngagement,
		Entries:         make([]StoredWeightEntry, len(s.Entries)),
	}
	for i := range s.Entries {
		stored.Entries[i] = StoredWeightEntry{
			Address:            s.Entries[i].Address,
			Stake:              copyBigInt(s.Entries[i].Stake),
			Engagement:         s.Entries[i].Engagement,
			StakeShareBps:      s.Entries[i].StakeShareBps,
			EngagementShareBps: s.Entries[i].EngagementShareBps,
			WeightBps:          s.Entries[i].WeightBps,
		}
	}
	return stored
}

// FromStored reconstructs the runtime snapshot from persisted data. Shares and
// weights are recomputed to ensure exactness.
func (s *StoredWeightSnapshot) FromStored(params WeightParams) *WeightSnapshot {
	if s == nil {
		return nil
	}
	snapshot := &WeightSnapshot{
		Epoch:           s.Epoch,
		TotalStake:      copyBigInt(s.TotalStake),
		TotalEngagement: s.TotalEngagement,
		Entries:         make([]WeightEntry, len(s.Entries)),
	}
	alpha := new(big.Rat).SetFrac(big.NewInt(int64(params.AlphaStakeBps)), big.NewInt(int64(WeightBpsDenominator)))
	invAlpha := new(big.Rat).Sub(big.NewRat(1, 1), alpha)
	stakeTotal := copyBigInt(s.TotalStake)
	engagementTotal := new(big.Int).SetUint64(s.TotalEngagement)
	for i := range s.Entries {
		entry := s.Entries[i]
		stakeShare := new(big.Rat)
		engagementShare := new(big.Rat)
		if stakeTotal.Sign() > 0 && entry.Stake.Sign() > 0 {
			stakeShare.SetFrac(entry.Stake, stakeTotal)
		}
		if engagementTotal.Sign() > 0 && entry.Engagement > 0 {
			engagementShare.SetFrac(new(big.Int).SetUint64(entry.Engagement), engagementTotal)
		}
		weight := new(big.Rat)
		if stakeShare.Sign() > 0 {
			tmp := new(big.Rat).Mul(stakeShare, alpha)
			weight.Add(weight, tmp)
		}
		if engagementShare.Sign() > 0 {
			tmp := new(big.Rat).Mul(engagementShare, invAlpha)
			weight.Add(weight, tmp)
		}
		snapshot.Entries[i] = WeightEntry{
			Address:            entry.Address,
			Stake:              copyBigInt(entry.Stake),
			Engagement:         entry.Engagement,
			StakeShare:         stakeShare,
			EngagementShare:    engagementShare,
			Weight:             weight,
			StakeShareBps:      entry.StakeShareBps,
			EngagementShareBps: entry.EngagementShareBps,
			WeightBps:          entry.WeightBps,
		}
	}
	return snapshot
}

// ComputeWeightSnapshot processes the supplied participants and produces a
// deterministic leaderboard respecting the configured filters and caps.
func ComputeWeightSnapshot(epoch uint64, inputs []WeightInput, params WeightParams) (*WeightSnapshot, error) {
	if err := params.Validate(); err != nil {
		return nil, err
	}
	beta := computeBetaScaled(params.DecayHalfLifeEpochs)
	alpha := new(big.Rat).SetFrac(big.NewInt(int64(params.AlphaStakeBps)), big.NewInt(int64(WeightBpsDenominator)))
	one := big.NewRat(1, 1)
	invAlpha := new(big.Rat).Sub(one, alpha)

	snapshot := &WeightSnapshot{
		Epoch:           epoch,
		TotalStake:      big.NewInt(0),
		TotalEngagement: 0,
		Entries:         make([]WeightEntry, 0, len(inputs)),
	}

	minStake := params.MinStakeToWinWei
	if minStake == nil {
		minStake = big.NewInt(0)
	}
	minEarn := params.MinStakeToEarnWei
	if minEarn == nil {
		minEarn = big.NewInt(0)
	}

	// First pass – compute decayed engagement and filter participants.
	filtered := make([]WeightEntry, 0, len(inputs))
	for _, input := range inputs {
		stake := copyBigInt(input.Stake)
		if stake == nil {
			stake = big.NewInt(0)
		}
		eligibleForEarnings := stake.Cmp(minEarn) >= 0
		rawComposite := uint64(0)
		if eligibleForEarnings {
			rawComposite = computeComposite(input.Meter, params)
		}
		decayed := applyEMA(input.PreviousEngagement, rawComposite, beta)
		if !eligibleForEarnings {
			decayed = 0
		}
		if params.MaxEngagementPerEpoch > 0 && decayed > params.MaxEngagementPerEpoch {
			decayed = params.MaxEngagementPerEpoch
		}
		if stake.Cmp(minStake) < 0 {
			continue
		}
		if decayed < params.MinEngagementToWin {
			continue
		}
		if stake.Sign() == 0 && decayed == 0 {
			continue
		}
		entry := WeightEntry{
			Address:    input.Address,
			Stake:      stake,
			Engagement: decayed,
		}
		filtered = append(filtered, entry)
		snapshot.TotalStake.Add(snapshot.TotalStake, stake)
		snapshot.TotalEngagement += decayed
	}

	if len(filtered) == 0 {
		snapshot.Entries = []WeightEntry{}
		snapshot.TotalStake = big.NewInt(0)
		snapshot.TotalEngagement = 0
		return snapshot, nil
	}

	stakeTotal := copyBigInt(snapshot.TotalStake)
	engagementTotal := new(big.Int).SetUint64(snapshot.TotalEngagement)

	// Second pass – compute shares and composite weights.
	for i := range filtered {
		stakeShare := new(big.Rat)
		if stakeTotal.Sign() > 0 && filtered[i].Stake.Sign() > 0 {
			stakeShare.SetFrac(filtered[i].Stake, stakeTotal)
		}
		engagementShare := new(big.Rat)
		if engagementTotal.Sign() > 0 && filtered[i].Engagement > 0 {
			engagementShare.SetFrac(new(big.Int).SetUint64(filtered[i].Engagement), engagementTotal)
		}
		weight := new(big.Rat)
		if stakeShare.Sign() > 0 {
			tmp := new(big.Rat).Mul(stakeShare, alpha)
			weight.Add(weight, tmp)
		}
		if engagementShare.Sign() > 0 {
			tmp := new(big.Rat).Mul(engagementShare, invAlpha)
			weight.Add(weight, tmp)
		}
		filtered[i].StakeShare = stakeShare
		filtered[i].EngagementShare = engagementShare
		filtered[i].Weight = weight
		filtered[i].StakeShareBps = ratToBps(stakeShare)
		filtered[i].EngagementShareBps = ratToBps(engagementShare)
		filtered[i].WeightBps = ratToBps(weight)
		filtered[i].tieKey = tieBreakKey(filtered[i].Address, params.TieBreak)
	}

	sort.Slice(filtered, func(i, j int) bool {
		cmp := filtered[i].Weight.Cmp(filtered[j].Weight)
		if cmp == 0 {
			return bytes.Compare(filtered[i].tieKey, filtered[j].tieKey) < 0
		}
		return cmp > 0
	})

	if params.TopKWinners > 0 && uint64(len(filtered)) > params.TopKWinners {
		filtered = filtered[:params.TopKWinners]
	}

	// Remove tie keys from exported entries.
	for i := range filtered {
		filtered[i].tieKey = nil
	}
	snapshot.Entries = filtered
	return snapshot, nil
}

func computeComposite(m EngagementMeter, params WeightParams) uint64 {
	total := new(big.Int)
	txCount := m.TxCount
	if params.QuadraticTxDampenAfter > 0 && txCount > params.QuadraticTxDampenAfter && params.QuadraticTxDampenPower > 1 {
		excess := txCount - params.QuadraticTxDampenAfter
		dampened := uint64(math.Round(math.Pow(float64(excess), 1.0/float64(params.QuadraticTxDampenPower))))
		if dampened == 0 {
			dampened = 1
		}
		if params.QuadraticTxDampenAfter > math.MaxUint64-dampened {
			txCount = math.MaxUint64
		} else {
			txCount = params.QuadraticTxDampenAfter + dampened
		}
	}
	addWeighted(total, txCount, params.TxWeightBps)
	addWeighted(total, m.EscrowCount, params.EscrowWeightBps)
	addWeighted(total, m.UptimeDevices, params.UptimeWeightBps)
	if total.BitLen() > 64 {
		return math.MaxUint64
	}
	return total.Uint64()
}

func addWeighted(total *big.Int, count uint64, weight uint64) {
	if count == 0 || weight == 0 {
		return
	}
	tmp := new(big.Int).SetUint64(count)
	tmp.Mul(tmp, new(big.Int).SetUint64(weight))
	total.Add(total, tmp)
}

func computeBetaScaled(halfLife uint64) uint64 {
	if halfLife == 0 {
		return 0
	}
	exponent := -1.0 / float64(halfLife)
	value := math.Pow(2, exponent)
	scaled := uint64(math.Round(value * float64(engagementBetaScale)))
	if scaled > engagementBetaScale {
		return engagementBetaScale
	}
	return scaled
}

func applyEMA(previous, raw, beta uint64) uint64 {
	if beta >= engagementBetaScale {
		return previous
	}
	prevComponent := new(big.Int).SetUint64(previous)
	prevComponent.Mul(prevComponent, new(big.Int).SetUint64(beta))

	complement := engagementBetaScale - beta
	rawComponent := new(big.Int).SetUint64(raw)
	rawComponent.Mul(rawComponent, new(big.Int).SetUint64(complement))

	prevComponent.Add(prevComponent, rawComponent)
	prevComponent.Div(prevComponent, new(big.Int).SetUint64(engagementBetaScale))
	if prevComponent.BitLen() > 64 {
		return math.MaxUint64
	}
	return prevComponent.Uint64()
}

func ratToBps(value *big.Rat) uint64 {
	if value == nil || value.Sign() <= 0 {
		return 0
	}
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetUint64(WeightBpsDenominator))
	num := scaled.Num()
	den := scaled.Denom()
	if den.Sign() == 0 {
		return 0
	}
	result := new(big.Int).Div(num, den)
	if !result.IsUint64() {
		return math.MaxUint64
	}
	return result.Uint64()
}

func tieBreakKey(addr [20]byte, mode TieBreakMode) []byte {
	switch mode {
	case TieBreakAddrLex, "":
		return append([]byte(nil), addr[:]...)
	default:
		digest := sha256.Sum256(addr[:])
		return digest[:]
	}
}

func copyBigInt(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}
