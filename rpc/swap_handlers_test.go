package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http/httptest"
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

func buildSwapVoucher(t *testing.T, chainID uint64, recipient [20]byte) swap.VoucherV1 {
	t.Helper()
	return swap.VoucherV1{
		Domain:     swap.VoucherDomainV1,
		ChainID:    chainID,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     big.NewInt(1_000_000_000_000_000_000),
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       "0.10",
		OrderID:    "ORDER-123",
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

	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient)
	voucher.Domain = "BAD_DOMAIN"
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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

	voucher := buildSwapVoucher(t, env.node.Chain().ChainID()+1, recipient)
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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

	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient)
	voucher.Expiry = time.Now().Add(-time.Minute).Unix()
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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

	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient)
	voucher.Token = "NHB"
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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

	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient)
	sig := signSwapVoucher(t, rogueKey, voucher)

	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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

	chainID := env.node.Chain().ChainID()
	voucher := buildSwapVoucher(t, chainID, recipient)
	sig := signSwapVoucher(t, minterKey, voucher)
	payload := map[string]interface{}{
		"voucher": voucher,
		"sig":     "0x" + hex.EncodeToString(sig),
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
