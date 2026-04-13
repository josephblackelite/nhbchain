package swap

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	data     map[string]interface{}
	lists    map[string][][]byte
	supplies map[string]*big.Int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		data:     make(map[string]interface{}),
		lists:    make(map[string][][]byte),
		supplies: make(map[string]*big.Int),
	}
}

func (m *memoryStore) KVGet(key []byte, out interface{}) (bool, error) {
	value, ok := m.data[string(key)]
	if !ok {
		return false, nil
	}
	switch dst := out.(type) {
	case *amountRecord:
		if src, ok := value.(amountRecord); ok {
			*dst = src
			return true, nil
		}
	case *velocityRecord:
		if src, ok := value.(velocityRecord); ok {
			*dst = src
			return true, nil
		}
	case *riskIndexRecord:
		if src, ok := value.(riskIndexRecord); ok {
			*dst = src
			return true, nil
		}
	default:
		return false, nil
	}
	return false, nil
}

func (m *memoryStore) KVPut(key []byte, value interface{}) error {
	m.data[string(key)] = value
	return nil
}

func (m *memoryStore) KVDelete(key []byte) error {
	delete(m.data, string(key))
	return nil
}

func (m *memoryStore) KVAppend(key []byte, value []byte) error {
	skey := string(key)
	list := m.lists[skey]
	for _, existing := range list {
		if bytes.Equal(existing, value) {
			return nil
		}
	}
	cloned := append([]byte(nil), value...)
	m.lists[skey] = append(list, cloned)
	return nil
}

func (m *memoryStore) KVGetList(key []byte, out interface{}) error {
	switch dst := out.(type) {
	case *[][]byte:
		list := m.lists[string(key)]
		copied := make([][]byte, len(list))
		for i, entry := range list {
			copied[i] = append([]byte(nil), entry...)
		}
		*dst = copied
	default:
		// unsupported type for test helpers
	}
	return nil
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

func TestRiskParametersParse(t *testing.T) {
	cfg := RiskConfig{
		PerAddressDailyCapWei:   "10000e18",
		PerAddressMonthlyCapWei: "300000e18",
		PerTxMinWei:             "1e18",
		PerTxMaxWei:             "50000e18",
		VelocityWindowSeconds:   600,
		VelocityMaxMints:        5,
	}
	params, err := cfg.Parameters()
	if err != nil {
		t.Fatalf("parse parameters: %v", err)
	}
	wantDaily, _ := new(big.Int).SetString("10000", 10)
	wantDaily.Mul(wantDaily, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	if params.PerAddressDailyCapWei.Cmp(wantDaily) != 0 {
		t.Fatalf("unexpected daily cap: %s", params.PerAddressDailyCapWei.String())
	}
	if params.VelocityWindowSeconds != 600 || params.VelocityMaxMints != 5 {
		t.Fatalf("unexpected velocity params: %+v", params)
	}
}

func TestRiskEnginePerTxLimits(t *testing.T) {
	engine := NewRiskEngine(newMemoryStore())
	params := RiskParameters{
		PerTxMinWei: big.NewInt(10),
		PerTxMaxWei: big.NewInt(100),
	}
	violation, err := engine.CheckLimits([20]byte{}, big.NewInt(5), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodePerTxMin {
		t.Fatalf("expected per tx min violation")
	}
	violation, err = engine.CheckLimits([20]byte{}, big.NewInt(101), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodePerTxMax {
		t.Fatalf("expected per tx max violation")
	}
}

func TestRiskEngineDailyMonthlyLimits(t *testing.T) {
	store := newMemoryStore()
	engine := NewRiskEngine(store)
	now := time.Unix(1_000_000, 0)
	engine.SetClock(func() time.Time { return now })
	addr := [20]byte{1}
	params := RiskParameters{
		PerAddressDailyCapWei:   big.NewInt(100),
		PerAddressMonthlyCapWei: big.NewInt(150),
	}
	if err := engine.RecordMint(addr, big.NewInt(90), 0); err != nil {
		t.Fatalf("record mint: %v", err)
	}
	violation, err := engine.CheckLimits(addr, big.NewInt(11), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodeDailyCap {
		t.Fatalf("expected daily cap violation")
	}
	engine.SetClock(func() time.Time { return now.Add(24 * time.Hour) })
	if err := engine.RecordMint(addr, big.NewInt(60), 0); err != nil {
		t.Fatalf("record second mint: %v", err)
	}
	engine.SetClock(func() time.Time { return now.Add(36 * time.Hour) })
	violation, err = engine.CheckLimits(addr, big.NewInt(10), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodeMonthlyCap {
		t.Fatalf("expected monthly cap violation")
	}
}

func TestRiskEngineVelocityLimit(t *testing.T) {
	store := newMemoryStore()
	engine := NewRiskEngine(store)
	addr := [20]byte{2}
	params := RiskParameters{VelocityWindowSeconds: 600, VelocityMaxMints: 2}
	base := time.Unix(2_000_000, 0)
	engine.SetClock(func() time.Time { return base.Add(-time.Minute * 15) })
	if err := engine.RecordMint(addr, big.NewInt(1), params.VelocityWindowSeconds); err != nil {
		t.Fatalf("record mint outside window: %v", err)
	}
	engine.SetClock(func() time.Time { return base.Add(-time.Minute * 5) })
	if err := engine.RecordMint(addr, big.NewInt(1), params.VelocityWindowSeconds); err != nil {
		t.Fatalf("record mint inside window: %v", err)
	}
	engine.SetClock(func() time.Time { return base.Add(-time.Minute * 2) })
	if err := engine.RecordMint(addr, big.NewInt(1), params.VelocityWindowSeconds); err != nil {
		t.Fatalf("record mint inside window: %v", err)
	}
	var record velocityRecord
	ok, err := store.KVGet(riskVelocityKey(addr), &record)
	if err != nil {
		t.Fatalf("load velocity record: %v", err)
	}
	if !ok {
		t.Fatalf("expected velocity record persisted")
	}
	if record.WindowSeconds != params.VelocityWindowSeconds {
		t.Fatalf("expected stored window %d, got %d", params.VelocityWindowSeconds, record.WindowSeconds)
	}
	if len(record.Samples) != 2 {
		t.Fatalf("expected two samples within window, got %d", len(record.Samples))
	}
	engine.SetClock(func() time.Time { return base })
	violation, err := engine.CheckLimits(addr, big.NewInt(1), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodeVelocity {
		t.Fatalf("expected velocity violation")
	}
	engine.SetClock(func() time.Time { return base.Add(time.Minute * 15) })
	violation, err = engine.CheckLimits(addr, big.NewInt(1), params)
	if err != nil {
		t.Fatalf("check limits after window: %v", err)
	}
	if violation != nil {
		t.Fatalf("expected velocity limit to clear after window, got %v", violation)
	}
}

func TestRiskEngineUsage(t *testing.T) {
	store := newMemoryStore()
	engine := NewRiskEngine(store)
	addr := [20]byte{3}
	now := time.Unix(3_000_000, 0)
	engine.SetClock(func() time.Time { return now })
	if err := engine.RecordMint(addr, big.NewInt(50), 0); err != nil {
		t.Fatalf("record mint: %v", err)
	}
	usage, err := engine.Usage(addr)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.DayTotalWei.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected day total 50, got %s", usage.DayTotalWei.String())
	}
	if usage.MonthTotalWei.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected month total 50, got %s", usage.MonthTotalWei.String())
	}
	if len(usage.VelocityTimestamps) != 1 {
		t.Fatalf("expected velocity sample recorded")
	}
}

func TestRiskEnginePrunesStaleBuckets(t *testing.T) {
	store := newMemoryStore()
	engine := NewRiskEngine(store)
	addr := [20]byte{4}
	first := time.Date(2024, time.January, 31, 12, 0, 0, 0, time.UTC)
	engine.SetClock(func() time.Time { return first })
	if err := engine.RecordMint(addr, big.NewInt(20), 0); err != nil {
		t.Fatalf("record initial mint: %v", err)
	}
	dayKey := string(riskDailyKey(first, addr))
	monthKey := string(riskMonthlyKey(first, addr))
	if _, ok := store.data[dayKey]; !ok {
		t.Fatalf("expected day bucket recorded")
	}
	second := time.Date(2024, time.February, 2, 0, 0, 0, 0, time.UTC)
	engine.SetClock(func() time.Time { return second })
	if err := engine.RecordMint(addr, big.NewInt(5), 0); err != nil {
		t.Fatalf("record second mint: %v", err)
	}
	if _, ok := store.data[dayKey]; ok {
		t.Fatalf("expected previous day bucket pruned")
	}
	if _, ok := store.data[monthKey]; ok {
		t.Fatalf("expected previous month bucket pruned")
	}
	currentDayKey := string(riskDailyKey(second, addr))
	if _, ok := store.data[currentDayKey]; !ok {
		t.Fatalf("expected new day bucket persisted")
	}
}
