package rpc

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func configureSwapToken(t *testing.T, node *core.Node, minterAddr [20]byte) {
	t.Helper()
	if err := node.WithState(func(m *nhbstate.Manager) error {
		meta, err := m.Token("ZNHB")
		if err != nil {
			return err
		}
		if meta == nil {
			if err := m.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
				return err
			}
		}
		if err := m.SetTokenMintAuthority("ZNHB", minterAddr[:]); err != nil {
			return err
		}
		return m.SetTokenMintPaused("ZNHB", false)
	}); err != nil {
		t.Fatalf("configure swap token: %v", err)
	}
}

func (env *testEnv) setManualRate(t *testing.T, rate string, ts time.Time) {
	t.Helper()
	if err := env.node.SetSwapManualQuote("USD", "ZNHB", rate, ts); err != nil {
		t.Fatalf("set manual rate: %v", err)
	}
}

func buildSwapVoucher(t *testing.T, chainID uint64, recipient [20]byte, rate string, orderID string) swap.VoucherV1 {
	t.Helper()
	rat, ok := new(big.Rat).SetString(rate)
	if !ok {
		t.Fatalf("invalid rate %s", rate)
	}
	amount, err := swap.ComputeMintAmount("100.00", rat, 18)
	if err != nil {
		t.Fatalf("compute amount: %v", err)
	}
	return swap.VoucherV1{
		Domain:     swap.VoucherDomainV1,
		ChainID:    chainID,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     amount,
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       rate,
		OrderID:    orderID,
		Nonce:      []byte("nonce-1"),
		Expiry:     time.Now().Add(time.Hour).Unix(),
	}
}

func signSwapVoucher(t *testing.T, key *crypto.PrivateKey, voucher swap.VoucherV1) []byte {
	t.Helper()
	hash := voucher.Hash()
	sig, err := ethcrypto.Sign(hash, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign voucher: %v", err)
	}
	return sig
}

func TestSwapSubmitVoucherInvalidDomain(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-123")
	voucher.Domain = "BAD_DOMAIN"
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestSwapSubmitVoucherInvalidChain(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID()+1, recipient, "0.10", "ORDER-123")
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestSwapSubmitVoucherExpired(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-123")
	voucher.Expiry = time.Now().Add(-time.Minute).Unix()
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestSwapSubmitVoucherInvalidToken(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-123")
	voucher.Token = "NHB"
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestSwapSubmitVoucherInvalidSigner(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	rogueKey, _ := crypto.GeneratePrivateKey()
	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-123")
	sig := signSwapVoucher(t, rogueKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 5, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected unauthorized code, got %d", rpcErr.Code)
	}
}

func TestSwapSubmitVoucherSuccessAndReplay(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	chainID := env.node.Chain().ChainID()
	voucher := buildSwapVoucher(t, chainID, recipient, "0.10", "ORDER-123")
	sig := signSwapVoucher(t, minterKey, voucher)
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
	}
	req := &RPCRequest{ID: 6, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var response struct {
		TxHash string `json:"txHash"`
		Minted bool   `json:"minted"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Minted {
		t.Fatalf("expected minted true")
	}
	if response.TxHash == "" {
		t.Fatalf("expected tx hash")
	}

	account, err := env.node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB.Cmp(voucher.Amount) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", account.BalanceZNHB.String(), voucher.Amount.String())
	}

	evts := env.node.Events()
	if len(evts) != 1 {
		t.Fatalf("expected 1 event got %d", len(evts))
	}
	evt := evts[0]
	if evt.Type != events.TypeSwapMinted {
		t.Fatalf("unexpected event type %s", evt.Type)
	}
	if evt.Attributes["orderId"] != voucher.OrderID {
		t.Fatalf("unexpected orderId %s", evt.Attributes["orderId"])
	}
	if evt.Attributes["amount"] != voucher.Amount.String() {
		t.Fatalf("unexpected amount %s", evt.Attributes["amount"])
	}

	// Replay should be rejected
	replayRec := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(replayRec, env.newRequest(), req)
	_, replayErr := decodeRPCResponse(t, replayRec)
	if replayErr == nil {
		t.Fatalf("expected replay error")
	}
	if replayErr.Code != codeDuplicateTx {
		t.Fatalf("expected duplicate code, got %d", replayErr.Code)
	}
}

func TestSwapSubmitVoucherStaleOracle(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now().Add(-time.Hour))
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-STALE")
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-STALE",
	}
	req := &RPCRequest{ID: 7, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params due to stale oracle, got %+v", rpcErr)
	}
}

func TestSwapSubmitVoucherSlippageExceeded(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-SLIP")
	voucher.Amount = new(big.Int).Mul(voucher.Amount, big.NewInt(2))
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-SLIP",
	}
	req := &RPCRequest{ID: 8, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params due to slippage, got %+v", rpcErr)
	}
}

func TestSwapSubmitVoucherDuplicateProvider(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-A")
	sig := signSwapVoucher(t, minterKey, voucher)
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-1",
	}
	req := &RPCRequest{ID: 9, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	if len(result) == 0 {
		t.Fatalf("expected result")
	}

	// Second voucher with different order but same providerTxId should be rejected.
	voucherB := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-B")
	sigB := signSwapVoucher(t, minterKey, voucherB)
	payloadB := map[string]interface{}{
		"voucher":      voucherB,
		"sig":          "0x" + hex.EncodeToString(sigB),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-1",
	}
	reqB := &RPCRequest{ID: 10, Params: []json.RawMessage{marshalParam(t, payloadB)}}
	recorderB := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorderB, env.newRequest(), reqB)
	_, rpcErr = decodeRPCResponse(t, recorderB)
	if rpcErr == nil || rpcErr.Code != codeDuplicateTx {
		t.Fatalf("expected duplicate provider error, got %+v", rpcErr)
	}
}

func TestSwapSubmitVoucherSanctioned(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	cfg := swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk:               swap.RiskConfig{SanctionsCheckEnabled: true},
	}
	env.node.SetSwapConfig(cfg)
	env.node.SetSwapSanctionsChecker(func(addr [20]byte) bool {
		return addr != recipient
	})

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-SANCTION")
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-SANCTION",
	}
	req := &RPCRequest{ID: 11, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)

	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected sanction error")
	}
	if rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected unauthorized code, got %d", rpcErr.Code)
	}
}

func TestSwapVoucherExportAndList(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-EXPORT")
	sig := signSwapVoucher(t, minterKey, voucher)
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-EXP",
	}
	req := &RPCRequest{ID: 11, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	if _, rpcErr := decodeRPCResponse(t, recorder); rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}

	start := time.Now().Add(-time.Hour).Unix()
	end := time.Now().Add(time.Hour).Unix()

	// Export
	exportReq := &RPCRequest{ID: 12, Params: []json.RawMessage{marshalParam(t, start), marshalParam(t, end)}}
	exportRec := httptest.NewRecorder()
	env.server.handleSwapVoucherExport(exportRec, env.newRequest(), exportReq)
	exportResult, rpcErr := decodeRPCResponse(t, exportRec)
	if rpcErr != nil {
		t.Fatalf("export error: %+v", rpcErr)
	}
	var exportPayload struct {
		CSVBase64    string `json:"csvBase64"`
		Count        int    `json:"count"`
		TotalMintWei string `json:"totalMintWei"`
	}
	if err := json.Unmarshal(exportResult, &exportPayload); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exportPayload.Count != 1 {
		t.Fatalf("expected count 1, got %d", exportPayload.Count)
	}
	if exportPayload.TotalMintWei != voucher.Amount.String() {
		t.Fatalf("unexpected total %s", exportPayload.TotalMintWei)
	}
	data, err := base64.StdEncoding.DecodeString(exportPayload.CSVBase64)
	if err != nil {
		t.Fatalf("decode csv: %v", err)
	}
	if !strings.Contains(string(data), "PROVIDER-EXP") {
		t.Fatalf("csv missing provider: %s", data)
	}

	// List
	listReq := &RPCRequest{ID: 13, Params: []json.RawMessage{marshalParam(t, start), marshalParam(t, end)}}
	listRec := httptest.NewRecorder()
	env.server.handleSwapVoucherList(listRec, env.newRequest(), listReq)
	listResult, rpcErr := decodeRPCResponse(t, listRec)
	if rpcErr != nil {
		t.Fatalf("list error: %+v", rpcErr)
	}
	var listPayload struct {
		Vouchers   []map[string]interface{} `json:"vouchers"`
		NextCursor string                   `json:"nextCursor"`
	}
	if err := json.Unmarshal(listResult, &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Vouchers) != 1 {
		t.Fatalf("expected 1 voucher, got %d", len(listPayload.Vouchers))
	}
	if listPayload.Vouchers[0]["providerTxId"].(string) != "PROVIDER-EXP" {
		t.Fatalf("unexpected voucher record: %+v", listPayload.Vouchers[0])
	}
}
