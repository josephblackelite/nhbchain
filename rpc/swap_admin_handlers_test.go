package rpc

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"
)

type limitsResponse struct {
	Address           string            `json:"address"`
	Day               map[string]string `json:"day"`
	Month             map[string]string `json:"month"`
	DayRemainingWei   string            `json:"dayRemainingWei"`
	MonthRemainingWei string            `json:"monthRemainingWei"`
	Velocity          struct {
		WindowSeconds uint64 `json:"windowSeconds"`
		MaxMints      uint64 `json:"maxMints"`
		Observed      int    `json:"observed"`
		Remaining     int64  `json:"remaining"`
	} `json:"velocity"`
}

func TestHandleSwapLimitsSuccess(t *testing.T) {
	env := newTestEnv(t)
	cfg := swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk: swap.RiskConfig{
			PerAddressDailyCapWei:   "1000",
			PerAddressMonthlyCapWei: "5000",
			PerTxMinWei:             "1",
			PerTxMaxWei:             "2000",
			VelocityWindowSeconds:   600,
			VelocityMaxMints:        5,
			SanctionsCheckEnabled:   true,
		},
	}
	env.node.SetSwapConfig(cfg)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().String()

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, addr)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapLimits(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp limitsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Address != addr {
		t.Fatalf("expected address %s, got %s", addr, resp.Address)
	}
	if resp.Day["mintedWei"] != "0" {
		t.Fatalf("expected day minted 0, got %s", resp.Day["mintedWei"])
	}
	if resp.Month["mintedWei"] != "0" {
		t.Fatalf("expected month minted 0, got %s", resp.Month["mintedWei"])
	}
	if resp.DayRemainingWei != "1000" {
		t.Fatalf("expected day remaining 1000, got %s", resp.DayRemainingWei)
	}
	if resp.MonthRemainingWei != "5000" {
		t.Fatalf("expected month remaining 5000, got %s", resp.MonthRemainingWei)
	}
	if resp.Velocity.MaxMints != 5 || resp.Velocity.Remaining != 5 {
		t.Fatalf("unexpected velocity payload: %+v", resp.Velocity)
	}
}

func TestHandleSwapProviderStatus(t *testing.T) {
	env := newTestEnv(t)
	cfg := swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Providers:          swap.ProviderConfig{Allow: []string{"nowpayments"}},
	}
	env.node.SetSwapConfig(cfg)

	req := &RPCRequest{ID: 2}
	recorder := httptest.NewRecorder()
	env.server.handleSwapProviderStatus(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var payload struct {
		Allow                 []string                `json:"allow"`
		LastOracleHealthCheck int64                   `json:"lastOracleHealthCheck"`
		OracleFeeds           []swap.OracleFeedStatus `json:"oracleFeeds"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Allow) != 1 || payload.Allow[0] != "nowpayments" {
		t.Fatalf("unexpected allow list: %+v", payload.Allow)
	}
	if payload.LastOracleHealthCheck != 0 {
		t.Fatalf("expected zero oracle health, got %d", payload.LastOracleHealthCheck)
	}
	if len(payload.OracleFeeds) != 0 {
		t.Fatalf("expected no oracle feeds, got %+v", payload.OracleFeeds)
	}
}

func TestHandleSwapBurnList(t *testing.T) {
	env := newTestEnv(t)
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewBurnLedger(m)
		ledger.SetClock(func() time.Time { return time.Unix(2300000000, 0) })
		if err := ledger.Put(&swap.BurnReceipt{ReceiptID: "burn-a", Token: "ZNHB", AmountWei: big.NewInt(10)}); err != nil {
			return err
		}
		ledger.SetClock(func() time.Time { return time.Unix(2300000600, 0) })
		if err := ledger.Put(&swap.BurnReceipt{ReceiptID: "burn-b", Token: "ZNHB", AmountWei: big.NewInt(20)}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed burns: %v", err)
	}
	params := []json.RawMessage{marshalParam(t, int64(0)), marshalParam(t, int64(0)), marshalParam(t, ""), marshalParam(t, int64(10))}
	req := &RPCRequest{ID: 3, Params: params}
	recorder := httptest.NewRecorder()
	env.server.handleSwapBurnList(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var payload struct {
		Receipts   []map[string]interface{} `json:"receipts"`
		NextCursor string                   `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Receipts) != 2 {
		t.Fatalf("expected 2 receipts, got %d", len(payload.Receipts))
	}
	if payload.Receipts[0]["receiptId"] != "burn-a" {
		t.Fatalf("unexpected first receipt: %+v", payload.Receipts[0])
	}
}

func TestHandleSwapVoucherReverse(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	sinkKey, _ := crypto.GeneratePrivateKey()
	var sinkAddr [20]byte
	copy(sinkAddr[:], sinkKey.PubKey().Address().Bytes())
	env.node.SetSwapRefundSink(sinkAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	amount := big.NewInt(250)
	providerTxID := "order-1"

	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		record := &swap.VoucherRecord{
			Provider:      "nowpayments",
			ProviderTxID:  providerTxID,
			Token:         "ZNHB",
			MintAmountWei: amount,
			Recipient:     recipient,
			Status:        swap.VoucherStatusMinted,
		}
		if err := ledger.Put(record); err != nil {
			return err
		}
		return m.SetBalance(recipient[:], "ZNHB", new(big.Int).Set(amount))
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	req := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, providerTxID)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapVoucherReverse(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var okResp struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &okResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !okResp.OK {
		t.Fatalf("expected ok response")
	}

	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		record, _, err := ledger.Get(providerTxID)
		if err != nil {
			return err
		}
		if record.Status != swap.VoucherStatusReversed {
			t.Fatalf("expected reversed status, got %s", record.Status)
		}
		balance, err := m.Balance(recipient[:], "ZNHB")
		if err != nil {
			return err
		}
		if balance.Cmp(big.NewInt(0)) != 0 {
			t.Fatalf("expected recipient balance 0, got %s", balance.String())
		}
		sinkBalance, err := m.Balance(sinkAddr[:], "ZNHB")
		if err != nil {
			return err
		}
		if sinkBalance.Cmp(amount) != 0 {
			t.Fatalf("expected sink balance %s, got %s", amount.String(), sinkBalance.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("verify state: %v", err)
	}

	recorder2 := httptest.NewRecorder()
	env.server.handleSwapVoucherReverse(recorder2, env.newRequest(), req)
	raw2, rpcErr2 := decodeRPCResponse(t, recorder2)
	if rpcErr2 != nil {
		t.Fatalf("unexpected rpc error on second call: %+v", rpcErr2)
	}
	if err := json.Unmarshal(raw2, &okResp); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if !okResp.OK {
		t.Fatalf("expected ok response on idempotent call")
	}
}
