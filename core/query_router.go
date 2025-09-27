package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	swap "nhbchain/native/swap"

	"github.com/ethereum/go-ethereum/common"
)

func (sp *StateProcessor) QueryState(namespace, key string) (*QueryResult, error) {
	if sp == nil {
		return nil, fmt.Errorf("state processor unavailable")
	}
	ns := strings.TrimSpace(strings.ToLower(namespace))
	path := strings.TrimSpace(key)
	switch ns {
	case "lending":
		return sp.queryLendingState(path)
	case "swap":
		return sp.querySwapState(path)
	case "gov", "governance":
		return sp.queryGovernanceState(path)
	default:
		return nil, ErrQueryNotSupported
	}
}

func (sp *StateProcessor) QueryPrefix(namespace, prefix string) ([]QueryRecord, error) {
	if sp == nil {
		return nil, fmt.Errorf("state processor unavailable")
	}
	ns := strings.TrimSpace(strings.ToLower(namespace))
	scope := strings.TrimSpace(prefix)
	switch ns {
	case "lending":
		return sp.queryLendingPrefix(scope)
	case "gov", "governance":
		return sp.queryGovernancePrefix(scope)
	default:
		return nil, ErrQueryNotSupported
	}
}

func (sp *StateProcessor) queryLendingState(path string) (*QueryResult, error) {
	manager := nhbstate.NewManager(sp.Trie)
	switch {
	case path == "markets":
		markets, err := manager.LendingListMarkets()
		if err != nil {
			return nil, err
		}
		if markets == nil {
			markets = []*lending.Market{}
		}
		payload, err := json.Marshal(markets)
		if err != nil {
			return nil, err
		}
		return &QueryResult{Value: payload}, nil
	case strings.HasPrefix(path, "positions/"):
		addrText := strings.TrimSpace(strings.TrimPrefix(path, "positions/"))
		if addrText == "" {
			return nil, fmt.Errorf("lending: address required")
		}
		rawAddr, err := decodeQueryAddress(addrText)
		if err != nil {
			return nil, err
		}
		var accountAddr [common.AddressLength]byte
		copy(accountAddr[:], rawAddr)
		poolIDs, err := manager.LendingListPoolIDs()
		if err != nil {
			return nil, err
		}
		type position struct {
			PoolID  string               `json:"poolId"`
			Account *lending.UserAccount `json:"account"`
		}
		positions := make([]position, 0, len(poolIDs))
		for _, poolID := range poolIDs {
			stored, ok, err := manager.LendingGetUserAccount(poolID, accountAddr)
			if err != nil {
				return nil, err
			}
			if ok && stored != nil {
				positions = append(positions, position{PoolID: poolID, Account: stored})
			}
		}
		payload, err := json.Marshal(positions)
		if err != nil {
			return nil, err
		}
		return &QueryResult{Value: payload}, nil
	default:
		return nil, ErrQueryNotSupported
	}
}

func (sp *StateProcessor) queryLendingPrefix(scope string) ([]QueryRecord, error) {
	if scope != "" && scope != "markets" {
		return nil, ErrQueryNotSupported
	}
	manager := nhbstate.NewManager(sp.Trie)
	markets, err := manager.LendingListMarkets()
	if err != nil {
		return nil, err
	}
	if markets == nil {
		markets = []*lending.Market{}
	}
	records := make([]QueryRecord, 0, len(markets))
	for _, market := range markets {
		if market == nil {
			continue
		}
		payload, err := json.Marshal(market)
		if err != nil {
			return nil, err
		}
		records = append(records, QueryRecord{Key: market.PoolID, Value: payload})
	}
	return records, nil
}

func (sp *StateProcessor) querySwapState(path string) (*QueryResult, error) {
	manager := nhbstate.NewManager(sp.Trie)
	switch {
	case strings.HasPrefix(path, "vouchers/"):
		identifier := strings.TrimSpace(strings.TrimPrefix(path, "vouchers/"))
		if identifier == "" {
			return nil, fmt.Errorf("swap: voucher id required")
		}
		ledger := swap.NewLedger(manager)
		record, ok, err := ledger.Get(identifier)
		if err != nil {
			return nil, err
		}
		if !ok || record == nil {
			return &QueryResult{}, nil
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		return &QueryResult{Value: payload}, nil
	default:
		return nil, ErrQueryNotSupported
	}
}

func (sp *StateProcessor) queryGovernanceState(path string) (*QueryResult, error) {
	manager := nhbstate.NewManager(sp.Trie)
	switch {
	case strings.HasPrefix(path, "proposals/"):
		idText := strings.TrimSpace(strings.TrimPrefix(path, "proposals/"))
		if idText == "" {
			return nil, fmt.Errorf("gov: proposal id required")
		}
		proposalID, err := strconv.ParseUint(idText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("gov: invalid proposal id: %w", err)
		}
		proposal, ok, err := manager.GovernanceGetProposal(proposalID)
		if err != nil {
			return nil, err
		}
		if !ok || proposal == nil {
			return &QueryResult{}, nil
		}
		payload, err := json.Marshal(proposal)
		if err != nil {
			return nil, err
		}
		return &QueryResult{Value: payload}, nil
	case path == "params":
		return nil, ErrQueryNotSupported
	default:
		return nil, ErrQueryNotSupported
	}
}

func (sp *StateProcessor) queryGovernancePrefix(scope string) ([]QueryRecord, error) {
	if scope != "params" {
		return nil, ErrQueryNotSupported
	}
	manager := nhbstate.NewManager(sp.Trie)
	keys := []string{governance.ParamKeyMinimumValidatorStake}
	records := make([]QueryRecord, 0, len(keys))
	for _, name := range keys {
		value, ok, err := manager.ParamStoreGet(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		records = append(records, QueryRecord{Key: name, Value: append([]byte(nil), value...)})
	}
	return records, nil
}

func decodeQueryAddress(input string) ([]byte, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("address required")
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		decoded, err := hex.DecodeString(trimmed[2:])
		if err != nil {
			return nil, fmt.Errorf("invalid hex address: %w", err)
		}
		if len(decoded) != common.AddressLength {
			return nil, fmt.Errorf("address must be %d bytes", common.AddressLength)
		}
		return decoded, nil
	}
	addr, err := crypto.DecodeAddress(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", trimmed, err)
	}
	return append([]byte(nil), addr.Bytes()...), nil
}
