package quotas

import (
	"fmt"

	nativecommon "nhbchain/native/common"
)

type counterRecord struct {
	ReqCount uint32
	NHBUsed  uint64
}

type StoreState interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVAppend(key []byte, value []byte) error
	KVGetList(key []byte, out interface{}) error
	KVDelete(key []byte) error
}

type Store struct {
	state StoreState
}

func NewStore(state StoreState) *Store {
	return &Store{state: state}
}

func (s *Store) withState() (StoreState, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("quota store not initialised")
	}
	return s.state, nil
}

func (s *Store) Load(module string, epoch uint64, addr []byte) (nativecommon.QuotaNow, bool, error) {
	state, err := s.withState()
	if err != nil {
		return nativecommon.QuotaNow{}, false, err
	}
	if len(addr) == 0 {
		return nativecommon.QuotaNow{}, false, fmt.Errorf("quota: address required")
	}
	key := counterKey(module, epoch, addr)
	var stored counterRecord
	ok, err := state.KVGet(key, &stored)
	if err != nil {
		return nativecommon.QuotaNow{}, false, fmt.Errorf("quota: load counters: %w", err)
	}
	if !ok {
		return nativecommon.QuotaNow{EpochID: epoch}, false, nil
	}
	now := nativecommon.QuotaNow{EpochID: epoch, ReqCount: stored.ReqCount, NHBUsed: stored.NHBUsed}
	return now, true, nil
}

func (s *Store) Save(module string, epoch uint64, addr []byte, counters nativecommon.QuotaNow) error {
	state, err := s.withState()
	if err != nil {
		return err
	}
	if len(addr) == 0 {
		return fmt.Errorf("quota: address required")
	}
	record := counterRecord{ReqCount: counters.ReqCount, NHBUsed: counters.NHBUsed}
	if err := state.KVPut(counterKey(module, epoch, addr), record); err != nil {
		return fmt.Errorf("quota: persist counters: %w", err)
	}
	if err := state.KVAppend(epochIndexKey(module, epoch), append([]byte(nil), addr...)); err != nil {
		return fmt.Errorf("quota: update epoch index: %w", err)
	}
	return nil
}

func (s *Store) PruneEpoch(module string, epoch uint64) error {
	state, err := s.withState()
	if err != nil {
		return err
	}
	indexKey := epochIndexKey(module, epoch)
	var addrs [][]byte
	if err := state.KVGetList(indexKey, &addrs); err != nil {
		return fmt.Errorf("quota: load epoch index: %w", err)
	}
	for _, addr := range addrs {
		if err := state.KVDelete(counterKey(module, epoch, addr)); err != nil {
			return fmt.Errorf("quota: prune counter: %w", err)
		}
	}
	if err := state.KVDelete(indexKey); err != nil {
		return fmt.Errorf("quota: prune index: %w", err)
	}
	return nil
}
