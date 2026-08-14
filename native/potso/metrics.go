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

// maxDecayHalfLifeEpochs and maxQuadraticTxDampenPower bound computeBetaScaled
// and integerNthRootRounded's big.Int binary search: each iteration computes
// mid^exponent, whose bit-length (and therefore cost) scales with the
// exponent's magnitude, not just the fixed 64-iteration count. Both fields
// are read directly from config with no other ceiling, and both run on the
// consensus state-transition path once per epoch -- an unbounded value
// (operator typo or otherwise) turns what used to be an O(1) float64 call
// into a computation that can take minutes, stalling epoch settlement on
// every validator. These caps are generously above any realistic value
// (production uses DecayHalfLifeEpochs=7, QuadraticTxDampenPower=2) while
// staying well inside the benchmarked-safe range (100,000 half-life epochs
// costs ~2.6s; 1,000 dampen power is effectively instant).
const (
	maxDecayHalfLifeEpochs    = 100_000
	maxQuadraticTxDampenPower = 1_000
)

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
	if p.DecayHalfLifeEpochs > maxDecayHalfLifeEpochs {
		return fmt.Errorf("decay half life epochs must be <= %d", maxDecayHalfLifeEpochs)
	}
	if p.QuadraticTxDampenPower > maxQuadraticTxDampenPower {
		return fmt.Errorf("quadratic tx dampen power must be <= %d", maxQuadraticTxDampenPower)
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
		dampened := integerNthRootRounded(excess, params.QuadraticTxDampenPower)
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

// integerNthRootRounded computes round(excess^(1/power)) using exact integer
// arithmetic (big.Int binary search), matching the rounding behaviour of the
// previous math.Round(math.Pow(...)) formula without introducing any
// floating-point, and therefore architecture/compiler-independent
// non-determinism, into consensus-critical state.
//
// The search finds the floor integer root r via binary search over a fixed,
// data-independent iteration count (64, since excess is a uint64 and its
// floor nth root for any power >= 1 always fits in 64 bits), then applies a
// single exact comparison to decide round-half-up, matching math.Round's
// away-from-zero behaviour on positive inputs.
func integerNthRootRounded(excess uint64, power uint64) uint64 {
	if excess == 0 {
		return 0
	}
	n := new(big.Int).SetUint64(excess)
	p := new(big.Int).SetUint64(power)

	lo := big.NewInt(0)
	hi := new(big.Int).SetUint64(excess) // nthRoot(excess, power) <= excess for excess >= 1, power >= 1
	one := big.NewInt(1)
	for i := 0; i < 64; i++ {
		if lo.Cmp(hi) >= 0 {
			break
		}
		mid := new(big.Int).Add(lo, hi)
		mid.Add(mid, one)
		mid.Rsh(mid, 1)
		if new(big.Int).Exp(mid, p, nil).Cmp(n) <= 0 {
			lo.Set(mid)
		} else {
			hi.Set(mid)
			hi.Sub(hi, one)
		}
	}
	r := lo // floor: r^power <= excess < (r+1)^power

	// Round half away from zero: round up iff excess >= (r+0.5)^power, i.e.
	// excess*2^power >= (2r+1)^power.
	lhs := new(big.Int).Lsh(n, uint(power))
	rhs := new(big.Int).Lsh(r, 1)
	rhs.Add(rhs, one)
	rhs.Exp(rhs, p, nil)
	if lhs.Cmp(rhs) >= 0 {
		r.Add(r, one)
	}

	if !r.IsUint64() {
		return math.MaxUint64
	}
	return r.Uint64()
}

// computeBetaScaled derives the fixed-point EMA decay factor
// beta = round(engagementBetaScale / 2^(1/halfLife)) using exact integer
// arithmetic (big.Int binary search) instead of math.Pow/math.Round, so the
// result is bit-identical across CPU architectures, Go compiler versions, and
// build flags -- required since this value feeds directly into reward
// payouts that are hashed into the consensus state root.
//
// It first computes root = floor(scale * 2^(1/halfLife)) via binary search
// for the halfLife-th root of (2 * scale^halfLife) in fixed-point units of
// scale = 2^fixedPointBits, then derives beta = round(engagementBetaScale *
// scale / root). fixedPointBits=64 gives ~34 bits of safety margin beyond the
// ~30 bits engagementBetaScale (1e9) needs, and bounds the search range to a
// fixed size (scale, 2*scale] regardless of halfLife's magnitude, so the loop
// runs an exact, data-independent 64 iterations -- never a float-epsilon
// convergence check.
const fixedPointBits = 64

func computeBetaScaled(halfLife uint64) uint64 {
	if halfLife == 0 {
		return 0
	}
	one := big.NewInt(1)
	scale := new(big.Int).Lsh(one, fixedPointBits) // scale = 2^fixedPointBits
	halfLifeBig := new(big.Int).SetUint64(halfLife)

	target := new(big.Int).Exp(scale, halfLifeBig, nil)
	target.Mul(target, big.NewInt(2)) // target = 2 * scale^halfLife

	lo := new(big.Int).Set(scale)     // R=scale   -> R^halfLife = scale^halfLife   <= target
	hi := new(big.Int).Lsh(scale, 1)  // R=2*scale -> R^halfLife = (2*scale)^halfLife >= target for halfLife>=1
	for i := 0; i < fixedPointBits; i++ {
		if lo.Cmp(hi) >= 0 {
			break
		}
		mid := new(big.Int).Add(lo, hi)
		mid.Add(mid, one)
		mid.Rsh(mid, 1)
		if new(big.Int).Exp(mid, halfLifeBig, nil).Cmp(target) <= 0 {
			lo.Set(mid)
		} else {
			hi.Set(mid)
			hi.Sub(hi, one)
		}
	}
	root := lo // floor: root^halfLife <= 2*scale^halfLife < (root+1)^halfLife

	// beta = round(engagementBetaScale / (root/scale)) = round(engagementBetaScale*scale / root),
	// round-half-up, matching math.Round's away-from-zero behaviour on positives.
	numerator := new(big.Int).Mul(new(big.Int).SetUint64(engagementBetaScale), scale)
	remainder := new(big.Int)
	quotient, _ := new(big.Int).QuoRem(numerator, root, remainder)
	doubledRemainder := new(big.Int).Lsh(remainder, 1)
	if doubledRemainder.Cmp(root) >= 0 {
		quotient.Add(quotient, one)
	}

	if quotient.Cmp(new(big.Int).SetUint64(engagementBetaScale)) > 0 {
		return engagementBetaScale
	}
	return quotient.Uint64()
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
