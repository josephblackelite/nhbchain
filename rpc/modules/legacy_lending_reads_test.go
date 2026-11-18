package modules

// The three public lending reads as they were when every call rebuilt the pool
// by walking the blocks (see legacy_lending_replay_test.go) and wrote every
// account it found into the state view, inside the state lock. Kept verbatim
// (comments aside) so the tests can check that the reads now answer exactly what
// they answered then.

import (
	"fmt"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/lending"
)

func (m *LendingModule) legacyRebuildPoolStateInto(manager *nhbstate.Manager, poolID string) error {
	if m == nil || m.node == nil {
		return fmt.Errorf("lending module unavailable")
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	market, users, err := m.legacyReplayCommittedPoolState(id)
	if err != nil {
		return err
	}
	if market == nil {
		return nil
	}
	if err := manager.LendingPutMarket(id, market); err != nil {
		return err
	}
	for _, user := range users {
		if err := manager.LendingPutUserAccount(id, user); err != nil {
			return err
		}
	}
	return nil
}

func (m *LendingModule) legacyGetMarket(poolID string) (*lending.Market, lending.RiskParameters, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, lending.RiskParameters{}, m.moduleUnavailable()
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	params := m.node.LendingRiskParameters()
	var market *lending.Market
	err := m.node.WithStateView(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetMarket(id)
		if err != nil {
			return err
		}
		if ok {
			market = stored
		}
		if marketNeedsRebuild(market) {
			if err := m.legacyRebuildPoolStateInto(manager, id); err != nil {
				return err
			}
			stored, ok, err := manager.LendingGetMarket(id)
			if err != nil {
				return err
			}
			if ok {
				market = stored
			}
		}
		if err := m.reconcileLegacyPoolStateInto(manager, id); err != nil {
			return err
		}
		return m.projectMarketAccrual(manager, id, market)
	})
	if err != nil {
		return nil, params, m.wrapError(err)
	}
	if market == nil && id == defaultLendingPoolID {
		market = m.defaultMarket(id)
	}
	m.applyComputedAPY(market)
	return market, params, nil
}

func (m *LendingModule) legacyGetPools() ([]*lending.Market, lending.RiskParameters, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, lending.RiskParameters{}, m.moduleUnavailable()
	}
	params := m.node.LendingRiskParameters()
	var markets []*lending.Market
	err := m.node.WithStateView(func(manager *nhbstate.Manager) error {
		list, err := manager.LendingListMarkets()
		if err != nil {
			return err
		}
		markets = list
		if len(markets) == 0 || allMarketsNeedRebuild(markets) {
			if err := m.legacyRebuildPoolStateInto(manager, defaultLendingPoolID); err != nil {
				return err
			}
			list, err := manager.LendingListMarkets()
			if err != nil {
				return err
			}
			markets = list
		}
		if err := m.reconcileLegacyPoolStateInto(manager, defaultLendingPoolID); err != nil {
			return err
		}
		for _, market := range markets {
			if market == nil {
				continue
			}
			if err := m.projectMarketAccrual(manager, market.PoolID, market); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, params, m.wrapError(err)
	}
	if len(markets) == 0 {
		markets = []*lending.Market{m.defaultMarket(defaultLendingPoolID)}
	}
	for _, market := range markets {
		m.applyComputedAPY(market)
	}
	return markets, params, nil
}

func (m *LendingModule) legacyGetUserAccount(poolID string, addr [20]byte) (*lending.UserAccount, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, m.moduleUnavailable()
	}
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	var account *lending.UserAccount
	err := m.node.WithStateView(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.LendingGetUserAccount(id, addr)
		if err != nil {
			return err
		}
		if ok {
			account = stored
			return nil
		}
		migrated, migratedErr := m.reconcileLegacyUserAccount(manager, id, addr)
		if migratedErr != nil {
			return migratedErr
		}
		account = migrated
		if account != nil {
			return nil
		}
		if err := m.legacyRebuildPoolStateInto(manager, id); err != nil {
			return err
		}
		stored, ok, err = manager.LendingGetUserAccount(id, addr)
		if err != nil {
			return err
		}
		if ok {
			account = stored
		}
		return nil
	})
	if err != nil {
		return nil, m.wrapError(err)
	}
	return account, nil
}
