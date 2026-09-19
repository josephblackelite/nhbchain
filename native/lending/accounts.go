package lending

import (
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// accountSet loads each address once per state transition, so roles that
// resolve to the same address (a borrower and the developer fee recipient, the
// module account and a withdrawal recipient) share one account object and
// every delta lands on it. State adapters return a fresh copy per load; with
// one copy per role the last persist would overwrite the deltas of the others
// and create or destroy value. persist writes each address once, in the order
// it was first loaded.
type accountSet struct {
	engine *Engine
	byAddr map[string]*types.Account
	order  []crypto.Address
}

func (e *Engine) newAccountSet() *accountSet {
	return &accountSet{engine: e, byAddr: make(map[string]*types.Account)}
}

func (s *accountSet) load(addr crypto.Address) (*types.Account, error) {
	key := string(addr.Bytes())
	if acc, ok := s.byAddr[key]; ok {
		return acc, nil
	}
	acc, err := s.engine.loadAccount(addr)
	if err != nil {
		return nil, err
	}
	s.byAddr[key] = acc
	s.order = append(s.order, addr)
	return acc, nil
}

func (s *accountSet) persist() error {
	for _, addr := range s.order {
		if err := s.engine.persistAccount(addr, s.byAddr[string(addr.Bytes())]); err != nil {
			return err
		}
	}
	return nil
}
