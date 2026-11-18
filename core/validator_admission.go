package core

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// PL-VAL-01: a validator that registers and holds the minimum stake enters the set that
// votes at the next epoch boundary. The quorum is more than two thirds of the total
// power, so when the incumbents sit close to a third each a newcomer of their own size
// makes all of them needed, and if it is switched off no block can be finished and
// nothing on the chain can remove it. The admission guard bounds what a validator that is
// new to the set can do to the quorum.
//
// The rule (ValidatorRules.ValidatorAdmissionGuardActivationHeight and
// ValidatorAdmissionProbationSeconds): a validator is ESTABLISHED once it has been in the
// set for the whole probation; the validators in the set when the guard first runs are
// established. At each epoch boundary a candidate that is not in the set and is not
// established (a NEWCOMER) is admitted only while the established validators hold
// strictly more than two thirds of the power of the established validators plus the
// newcomers, that is while established > 2 x newcomers. So the newcomers together can
// never hold a third of the set, and the established validators keep their quorum
// without them whatever the newcomers do. Newcomers are served in the order they
// registered, then by address; a deferred newcomer stays eligible and is considered again
// at the next boundary. Nothing is ever removed from the set by this rule, an
// established validator that drops out (a stale heartbeat) and returns is not a newcomer,
// and with no established validator in the set (a young chain) everyone is admitted.

var validatorAdmittedKey = ethcrypto.Keccak256([]byte("validator-admitted-at"))

// admittedRecord is when a validator was first admitted to the set; zero is "established
// before the guard started".
type admittedRecord struct {
	Addr []byte
	At   uint64
}

// admittedState is the persisted record. Its presence says the guard has started.
type admittedState struct {
	Records []admittedRecord
}

// validatorAdmissionActive reports whether the guard applies at the block being executed.
func (sp *StateProcessor) validatorAdmissionActive() bool {
	return sp != nil &&
		sp.validatorRules.ValidatorAdmissionProbationSeconds > 0 &&
		ruleActiveAt(sp.validatorRules.ValidatorAdmissionGuardActivationHeight, sp.blockHeight())
}

// loadAdmittedAt reads the admission records: address to the time first admitted (0 for
// established before the guard). started is false when the guard has never run.
func (sp *StateProcessor) loadAdmittedAt() (map[string]uint64, error) {
	records, _, err := sp.loadAdmittedState()
	return records, err
}

func (sp *StateProcessor) loadAdmittedState() (records map[string]uint64, started bool, err error) {
	data, err := sp.Trie.Get(validatorAdmittedKey)
	if err != nil {
		return nil, false, err
	}
	records = map[string]uint64{}
	if len(data) == 0 {
		return records, false, nil
	}
	var state admittedState
	if err := rlp.DecodeBytes(data, &state); err != nil {
		return nil, false, fmt.Errorf("validator admission: decode records: %w", err)
	}
	for _, rec := range state.Records {
		records[string(rec.Addr)] = rec.At
	}
	return records, true, nil
}

func (sp *StateProcessor) persistAdmittedAt(records map[string]uint64) error {
	keys := make([]string, 0, len(records))
	for k := range records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	state := admittedState{Records: make([]admittedRecord, 0, len(keys))}
	for _, k := range keys {
		state.Records = append(state.Records, admittedRecord{Addr: []byte(k), At: records[k]})
	}
	encoded, err := rlp.EncodeToBytes(&state)
	if err != nil {
		return err
	}
	return sp.Trie.Update(validatorAdmittedKey, encoded)
}

// admitValidators applies the admission rule to the validators selected for the next
// epoch (address to power) and returns the set that votes. It reads the set as it stands
// (the incumbents) and records new admissions in the state. It does nothing before the
// activation height.
func (sp *StateProcessor) admitValidators(candidates map[string]*big.Int, now time.Time) (map[string]*big.Int, error) {
	if !sp.validatorAdmissionActive() || len(candidates) == 0 {
		return candidates, nil
	}
	records, started, err := sp.loadAdmittedState()
	if err != nil {
		return nil, err
	}
	dirty := false
	if !started {
		// The guard starts now: everyone in the set is established.
		for addr := range sp.ValidatorSet {
			records[addr] = 0
		}
		dirty = true
	}
	nowUnix := uint64(now.Unix())
	probation := sp.validatorRules.ValidatorAdmissionProbationSeconds
	established := func(addr string) bool {
		at, ok := records[addr]
		return ok && nowUnix >= at && nowUnix-at >= probation
	}

	establishedPower := new(big.Int)
	newcomerPower := new(big.Int)
	type newcomer struct {
		addr         string
		power        *big.Int
		registeredAt uint64
	}
	var newcomers []newcomer
	isNewcomer := map[string]bool{}
	for addr, power := range candidates {
		if power == nil {
			continue
		}
		_, member := sp.ValidatorSet[addr]
		switch {
		case established(addr):
			establishedPower.Add(establishedPower, power)
		case member:
			newcomerPower.Add(newcomerPower, power)
		default:
			account, err := sp.getAccount([]byte(addr))
			if err != nil {
				return nil, err
			}
			newcomers = append(newcomers, newcomer{addr: addr, power: power, registeredAt: account.ValidatorRegisteredAt})
			isNewcomer[addr] = true
		}
	}
	sort.Slice(newcomers, func(i, j int) bool {
		if newcomers[i].registeredAt != newcomers[j].registeredAt {
			return newcomers[i].registeredAt < newcomers[j].registeredAt
		}
		return newcomers[i].addr < newcomers[j].addr
	})

	out := make(map[string]*big.Int, len(candidates))
	for addr, power := range candidates {
		if !isNewcomer[addr] {
			out[addr] = power
		}
	}
	for _, c := range newcomers {
		admit := establishedPower.Sign() == 0 // nothing established to protect
		if !admit {
			total := new(big.Int).Add(newcomerPower, c.power)
			total.Lsh(total, 1)
			admit = establishedPower.Cmp(total) > 0
		}
		if !admit {
			continue
		}
		out[c.addr] = c.power
		newcomerPower.Add(newcomerPower, c.power)
		if _, ok := records[c.addr]; !ok {
			records[c.addr] = nowUnix
			dirty = true
		}
	}
	if dirty {
		if err := sp.persistAdmittedAt(records); err != nil {
			return nil, err
		}
	}
	return out, nil
}
