package swap

import (
	"math/big"
	"testing"
	"time"
)

type memoryStore struct {
	data map[string]interface{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: make(map[string]interface{})}
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
	default:
		return false, nil
	}
	return false, nil
}

func (m *memoryStore) KVPut(key []byte, value interface{}) error {
	m.data[string(key)] = value
	return nil
}

func (m *memoryStore) KVAppend(key []byte, value []byte) error {
	return nil
}

func (m *memoryStore) KVGetList(key []byte, out interface{}) error {
	return nil
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
	if err := engine.RecordMint(addr, big.NewInt(90)); err != nil {
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
	if err := engine.RecordMint(addr, big.NewInt(60)); err != nil {
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
	engine.SetClock(func() time.Time { return base.Add(-time.Minute * 5) })
	if err := engine.RecordMint(addr, big.NewInt(1)); err != nil {
		t.Fatalf("record mint: %v", err)
	}
	engine.SetClock(func() time.Time { return base.Add(-time.Minute * 2) })
	if err := engine.RecordMint(addr, big.NewInt(1)); err != nil {
		t.Fatalf("record mint: %v", err)
	}
	engine.SetClock(func() time.Time { return base })
	violation, err := engine.CheckLimits(addr, big.NewInt(1), params)
	if err != nil {
		t.Fatalf("check limits: %v", err)
	}
	if violation == nil || violation.Code != RiskCodeVelocity {
		t.Fatalf("expected velocity violation")
	}
}

func TestRiskEngineUsage(t *testing.T) {
	store := newMemoryStore()
	engine := NewRiskEngine(store)
	addr := [20]byte{3}
	now := time.Unix(3_000_000, 0)
	engine.SetClock(func() time.Time { return now })
	if err := engine.RecordMint(addr, big.NewInt(50)); err != nil {
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
