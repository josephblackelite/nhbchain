package perf

import (
	"fmt"
	"math/big"
	"testing"

	nativeswap "nhbchain/native/swap"

	"github.com/ethereum/go-ethereum/rlp"
)

type benchStore struct {
	kv    map[string][]byte
	lists map[string][][]byte
}

func newBenchStore() *benchStore {
	return &benchStore{kv: make(map[string][]byte), lists: make(map[string][][]byte)}
}

func (b *benchStore) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	b.kv[string(key)] = encoded
	return nil
}

func (b *benchStore) KVDelete(key []byte) error {
	delete(b.kv, string(key))
	return nil
}

func (b *benchStore) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := b.kv[string(key)]
	if !ok {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(encoded, out); err != nil {
		return false, err
	}
	return true, nil
}

func (b *benchStore) KVAppend(key []byte, value []byte) error {
	b.lists[string(key)] = append(b.lists[string(key)], append([]byte(nil), value...))
	return nil
}

func (b *benchStore) KVGetList(key []byte, out interface{}) error {
	encoded, err := rlp.EncodeToBytes(b.lists[string(key)])
	if err != nil {
		return err
	}
	return rlp.DecodeBytes(encoded, out)
}

func BenchmarkLedgerPut(b *testing.B) {
	store := newBenchStore()
	ledger := nativeswap.NewLedger(store)
	amount := big.NewInt(1_000_000_000_000_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := &nativeswap.VoucherRecord{
			Provider:      "bench",
			ProviderTxID:  fmt.Sprintf("bench-%d", i),
			MintAmountWei: amount,
		}
		if err := ledger.Put(record); err != nil {
			b.Fatalf("ledger put: %v", err)
		}
	}
}
