package creator_test

import (
	"math/big"

	"nhbchain/core/types"
	"nhbchain/native/creator"
)

type testState struct {
	contents map[string]*creator.Content
	stakes   map[string]*creator.Stake
	ledgers  map[string]*creator.PayoutLedger
	accounts map[string]*types.Account
}

func newTestState() *testState {
	return &testState{
		contents: make(map[string]*creator.Content),
		stakes:   make(map[string]*creator.Stake),
		ledgers:  make(map[string]*creator.PayoutLedger),
		accounts: make(map[string]*types.Account),
	}
}

func (s *testState) CreatorContentGet(id string) (*creator.Content, bool, error) {
	if content, ok := s.contents[id]; ok {
		clone := *content
		clone.Hash = content.Hash
		if content.TotalTips != nil {
			clone.TotalTips = new(big.Int).Set(content.TotalTips)
		}
		if content.TotalStake != nil {
			clone.TotalStake = new(big.Int).Set(content.TotalStake)
		}
		return &clone, true, nil
	}
	return nil, false, nil
}

func (s *testState) CreatorContentPut(content *creator.Content) error {
	if content == nil {
		return nil
	}
	clone := *content
	clone.Hash = content.Hash
	if content.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	s.contents[content.ID] = &clone
	return nil
}

func stakeKey(creatorAddr [20]byte, fan [20]byte) string {
	key := make([]byte, 0, 40)
	key = append(key, creatorAddr[:]...)
	key = append(key, fan[:]...)
	return string(key)
}

func (s *testState) CreatorStakeGet(creatorAddr [20]byte, fan [20]byte) (*creator.Stake, bool, error) {
	if stake, ok := s.stakes[stakeKey(creatorAddr, fan)]; ok {
		clone := *stake
		if stake.Amount != nil {
			clone.Amount = new(big.Int).Set(stake.Amount)
		}
		if stake.Shares != nil {
			clone.Shares = new(big.Int).Set(stake.Shares)
		}
		return &clone, true, nil
	}
	return nil, false, nil
}

func (s *testState) CreatorStakePut(stake *creator.Stake) error {
	if stake == nil {
		return nil
	}
	clone := *stake
	if stake.Amount != nil {
		clone.Amount = new(big.Int).Set(stake.Amount)
	}
	if stake.Shares != nil {
		clone.Shares = new(big.Int).Set(stake.Shares)
	}
	s.stakes[stakeKey(stake.Creator, stake.Fan)] = &clone
	return nil
}

func (s *testState) CreatorStakeDelete(creatorAddr [20]byte, fan [20]byte) error {
	delete(s.stakes, stakeKey(creatorAddr, fan))
	return nil
}

func (s *testState) CreatorPayoutLedgerGet(creatorAddr [20]byte) (*creator.PayoutLedger, bool, error) {
	if ledger, ok := s.ledgers[string(creatorAddr[:])]; ok {
		return ledger.Clone(), true, nil
	}
	return nil, false, nil
}

func (s *testState) CreatorPayoutLedgerPut(ledger *creator.PayoutLedger) error {
	if ledger == nil {
		return nil
	}
	s.ledgers[string(ledger.Creator[:])] = ledger.Clone()
	return nil
}

func (s *testState) GetAccount(addr []byte) (*types.Account, error) {
	if acc, ok := s.accounts[string(addr)]; ok {
		return cloneAccount(acc), nil
	}
	return nil, nil
}

func (s *testState) PutAccount(addr []byte, account *types.Account) error {
	if account == nil {
		delete(s.accounts, string(addr))
		return nil
	}
	s.accounts[string(addr)] = cloneAccount(account)
	return nil
}

func (s *testState) setAccount(addr [20]byte, amount int64) {
	s.accounts[string(addr[:])] = &types.Account{
		BalanceNHB:  big.NewInt(amount),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
}

func (s *testState) setAccountBig(addr [20]byte, amount *big.Int) {
	s.accounts[string(addr[:])] = &types.Account{
		BalanceNHB:  new(big.Int).Set(amount),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
}

func (s *testState) account(addr [20]byte) *types.Account {
	if acc, ok := s.accounts[string(addr[:])]; ok {
		return cloneAccount(acc)
	}
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return nil
	}
	clone := *acc
	if acc.BalanceNHB != nil {
		clone.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	}
	if acc.BalanceZNHB != nil {
		clone.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	}
	if acc.Stake != nil {
		clone.Stake = new(big.Int).Set(acc.Stake)
	}
	return &clone
}

func addr(last byte) [20]byte {
	var out [20]byte
	out[19] = last
	return out
}
