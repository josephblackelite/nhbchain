package ledger

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nativeswap "nhbchain/native/swap"

	"github.com/ethereum/go-ethereum/rlp"
	"gopkg.in/yaml.v3"
)

type memoryStore struct {
	kv       map[string][]byte
	lists    map[string][][]byte
	supplies map[string]*big.Int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{kv: make(map[string][]byte), lists: make(map[string][][]byte), supplies: make(map[string]*big.Int)}
}

func (m *memoryStore) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.kv[string(key)] = encoded
	return nil
}

func (m *memoryStore) KVDelete(key []byte) error {
	delete(m.kv, string(key))
	return nil
}

func (m *memoryStore) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := m.kv[string(key)]
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

func (m *memoryStore) KVAppend(key []byte, value []byte) error {
	copied := append([]byte(nil), value...)
	m.lists[string(key)] = append(m.lists[string(key)], copied)
	return nil
}

func (m *memoryStore) KVGetList(key []byte, out interface{}) error {
	encoded, err := rlp.EncodeToBytes(m.lists[string(key)])
	if err != nil {
		return err
	}
	return rlp.DecodeBytes(encoded, out)
}

func (m *memoryStore) AdjustTokenSupply(symbol string, delta *big.Int) (*big.Int, error) {
	if m.supplies == nil {
		m.supplies = make(map[string]*big.Int)
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	current := new(big.Int)
	if existing, ok := m.supplies[normalized]; ok && existing != nil {
		current = new(big.Int).Set(existing)
	}
	if delta != nil {
		current = current.Add(current, delta)
	}
	if current.Sign() < 0 {
		return nil, fmt.Errorf("supply underflow for %s", normalized)
	}
	m.supplies[normalized] = new(big.Int).Set(current)
	return new(big.Int).Set(current), nil
}

type ledgerFixture struct {
	Records []struct {
		Provider     string `yaml:"provider"`
		ProviderTxID string `yaml:"provider_tx_id"`
		AmountWei    string `yaml:"amount_wei"`
		Status       string `yaml:"status"`
	} `yaml:"records"`
	Expectations struct {
		StatusCounts map[string]int `yaml:"status_counts"`
	} `yaml:"expectations"`
}

func loadLedgerFixture(t *testing.T) ledgerFixture {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	base := filepath.Join(filepath.Dir(filename), "..", "..", "ops", "audit", "ledger.yaml")
	raw, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read ledger fixture: %v", err)
	}
	var fixture ledgerFixture
	if err := yaml.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode ledger fixture: %v", err)
	}
	return fixture
}

func TestVoucherLedgerMatchesFixtureExpectations(t *testing.T) {
	fixture := loadLedgerFixture(t)
	store := newMemoryStore()
	ledger := nativeswap.NewLedger(store)
	ledger.SetClock(func() time.Time { return time.Unix(1700000000, 0) })

	reconciled := make([]string, 0)
	reversed := make([]string, 0)

	for _, rec := range fixture.Records {
		amount, ok := new(big.Int).SetString(rec.AmountWei, 10)
		if !ok {
			t.Fatalf("invalid amount %s", rec.AmountWei)
		}
		record := &nativeswap.VoucherRecord{
			Provider:      rec.Provider,
			ProviderTxID:  rec.ProviderTxID,
			MintAmountWei: amount,
			Status:        rec.Status,
		}
		if err := ledger.Put(record); err != nil {
			t.Fatalf("ledger put %s: %v", rec.ProviderTxID, err)
		}
		switch rec.Status {
		case nativeswap.VoucherStatusReconciled:
			reconciled = append(reconciled, rec.ProviderTxID)
		case nativeswap.VoucherStatusReversed:
			reversed = append(reversed, rec.ProviderTxID)
		}
	}

	if len(reconciled) > 0 {
		if err := ledger.MarkReconciled(reconciled); err != nil {
			t.Fatalf("mark reconciled: %v", err)
		}
	}
	for _, id := range reversed {
		if err := ledger.MarkReversed(id); err != nil {
			t.Fatalf("mark reversed %s: %v", id, err)
		}
	}

	page, _, err := ledger.List(0, 0, "", len(fixture.Records))
	if err != nil {
		t.Fatalf("list ledger: %v", err)
	}
	if len(page) != len(fixture.Records) {
		t.Fatalf("expected %d records, got %d", len(fixture.Records), len(page))
	}

	got := make(map[string]int)
	for _, rec := range page {
		got[rec.Status]++
	}

	for status, want := range fixture.Expectations.StatusCounts {
		if got[status] != want {
			t.Fatalf("status %s: expected %d, got %d", status, want, got[status])
		}
	}
}
