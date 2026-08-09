package rpc

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
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

// registerSwapPriceSigner writes the trusted signer address for a price-proof
// provider directly into node state. This mirrors how other tests in this
// suite (e.g. TestHandleSwapVoucherReverse) seed state via node.WithState --
// safe for a single-node test harness, but NOT how production would do this
// (see the round-2 fix report: there is currently no consensus-safe,
// governance-controlled way to register a signer in production without
// risking the same non-replayable direct-state-write bug class tracked
// elsewhere in this codebase).
func registerSwapPriceSigner(t *testing.T, node *core.Node, provider string, key *crypto.PrivateKey) {
	t.Helper()
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.SwapSetPriceSigner(provider, addr)
	}); err != nil {
		t.Fatalf("register price signer: %v", err)
	}
}

// signedSwapPriceProof builds a ZNHB/USD price proof signed by key, matching
// the domain/pair/provider a real registered oracle signer would produce.
// TxTypeSwapVoucherMint's deterministic execution path (fix #3) requires
// every submission to carry one of these -- unlike the retired direct-write
// RPC path, an unsigned or missing price proof is always rejected regardless
// of the general swap.risk.PriceProofSignatureRequired toggle.
func signedSwapPriceProof(t *testing.T, key *crypto.PrivateKey, provider, rate string, ts time.Time) *swap.PriceProof {
	t.Helper()
	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", rate, ts.UTC().Unix(), nil)
	if err != nil {
		t.Fatalf("build price proof: %v", err)
	}
	hash, err := proof.Hash()
	if err != nil {
		t.Fatalf("hash price proof: %v", err)
	}
	sig, err := ethcrypto.Sign(hash, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign price proof: %v", err)
	}
	proof.Signature = sig
	return proof
}

// priceProofParam renders a *swap.PriceProof into the JSON shape
// swap_submitVoucher's "priceProof" payload field expects.
func priceProofParam(proof *swap.PriceProof) map[string]interface{} {
	rate := ""
	if proof.Rate != nil {
		rate = proof.Rate.FloatString(18)
	}
	return map[string]interface{}{
		"domain":    proof.Domain,
		"provider":  proof.Provider,
		"pair":      proof.Base + "/" + proof.Quote,
		"rate":      rate,
		"timestamp": proof.Timestamp.UTC().Unix(),
		"signature": "0x" + hex.EncodeToString(proof.Signature),
	}
}

// mineSwapBlock drains the mempool into a new block and commits it,
// exercising the same AddTransaction -> mempool -> CreateBlock ->
// CommitBlock path a real validator round takes -- the "genuine end-to-end
// coverage" round 1 was missing, since minted balance/ledger/events for
// TxTypeSwapVoucherMint now only land after consensus, not synchronously
// from the RPC call.
func mineSwapBlock(t *testing.T, node *core.Node) {
	t.Helper()
	pending := node.GetMempool()
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
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

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	rogueKey, _ := crypto.GeneratePrivateKey()
	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-123")
	sig := signSwapVoucher(t, rogueKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
		"priceProof":   priceProofParam(proof),
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

// TestSwapSubmitVoucherSuccessAndReplay is the primary genuine end-to-end
// test for the fix: it drives the real Node.SwapSubmitVoucher -> AddTransaction
// -> mempool -> CreateBlock -> CommitBlock path (not just a StateProcessor-only
// unit test) and only checks minted balance/ledger/events AFTER the block
// commits, matching how minting actually happens now. It then verifies the
// network-wide-agreed duplicate guard (ledger.Exists) rejects a replay of the
// same providerTxID once the first mint is durably committed to state.
func TestSwapSubmitVoucherSuccessAndReplay(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	chainID := env.node.Chain().ChainID()
	voucher := buildSwapVoucher(t, chainID, recipient, "0.10", "ORDER-123")
	sig := signSwapVoucher(t, minterKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-123",
		"priceProof":   priceProofParam(proof),
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
	if response.Minted {
		t.Fatalf("expected minted false -- submission now only enqueues, consensus mints")
	}
	if response.TxHash == "" {
		t.Fatalf("expected tx hash")
	}
	if got := env.node.MempoolSize(); got != 1 {
		t.Fatalf("expected 1 pending transaction in mempool before commit, got %d", got)
	}

	// Balance must not move until the transaction is actually included in a
	// committed block -- this is the whole point of the fix (real,
	// network-wide-agreed execution instead of a direct RPC-time write).
	preCommit, err := env.node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if preCommit.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected zero balance before commit, got %s", preCommit.BalanceZNHB.String())
	}

	mineSwapBlock(t, env.node)

	if got := env.node.MempoolSize(); got != 0 {
		t.Fatalf("expected mempool empty after commit, got %d", got)
	}

	account, err := env.node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB.Cmp(voucher.Amount) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", account.BalanceZNHB.String(), voucher.Amount.String())
	}

	evts := env.node.Events()
	var mintedEvt, proofEvt, supplyEvt *types.Event
	for i := range evts {
		switch evts[i].Type {
		case events.TypeSwapMinted:
			mintedEvt = &evts[i]
		case events.TypeSwapMintProof:
			proofEvt = &evts[i]
		case events.TypeTokenSupply:
			supplyEvt = &evts[i]
		}
	}
	if mintedEvt == nil {
		t.Fatalf("expected swap.minted event")
	}
	if proofEvt == nil {
		t.Fatalf("expected swap.mint.proof event")
	}
	if supplyEvt == nil {
		t.Fatalf("expected token.supply event")
	}
	if mintedEvt.Attributes["orderId"] != voucher.OrderID {
		t.Fatalf("unexpected orderId %s", mintedEvt.Attributes["orderId"])
	}
	if mintedEvt.Attributes["amount"] != voucher.Amount.String() {
		t.Fatalf("unexpected amount %s", mintedEvt.Attributes["amount"])
	}
	if supplyEvt.Attributes["token"] != strings.ToUpper(voucher.Token) {
		t.Fatalf("unexpected supply token %s", supplyEvt.Attributes["token"])
	}
	if supplyEvt.Attributes["delta"] != voucher.Amount.String() {
		t.Fatalf("unexpected supply delta %s", supplyEvt.Attributes["delta"])
	}
	if proofEvt.Attributes["priceProofId"] == "" {
		t.Fatalf("expected priceProofId in proof event")
	}

	// Replay after commit: the providerTxID is now durably recorded in the
	// committed ledger (a real, network-wide-agreed check once state is
	// synced), not just this node's in-memory mempool, so the resubmission
	// must be rejected deterministically by applySwapVoucherMintTransaction.
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

func TestSwapSubmitVoucherUnsignedProofRejected(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	cfg := swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk: swap.RiskConfig{
			// This toggle is deliberately irrelevant to the assertion below:
			// TxTypeSwapVoucherMint requires a signed price proof
			// unconditionally now (fix #3), regardless of
			// PriceProofSignatureRequired's value.
			PriceProofSignatureRequired: true,
		},
	}
	env.node.SetSwapConfig(cfg)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-SIG")
	sig := signSwapVoucher(t, minterKey, voucher)

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-SIG",
	}
	req := &RPCRequest{ID: 7, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
	if !strings.Contains(rpcErr.Message, core.ErrSwapPriceProofRequired.Error()) {
		t.Fatalf("expected price proof required message, got %q", rpcErr.Message)
	}
}

// TestSwapSubmitVoucherPriceProofStale covers fix #3's freshness guard: the
// deterministic path no longer calls a live oracle at all, so staleness is
// now judged purely from the price proof's own embedded timestamp against
// swap.MaxQuoteAgeSeconds -- this replaces the old
// TestSwapSubmitVoucherStaleOracle, whose premise (a stale *live oracle*
// quote blocking the mint) no longer applies once oracle.GetRate() was
// removed from the execution path.
func TestSwapSubmitVoucherPriceProofStale(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-STALE")
	sig := signSwapVoucher(t, minterKey, voucher)
	// swap.MaxQuoteAgeSeconds is 120 in this env's config; a proof signed an
	// hour ago must be rejected as stale.
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now().Add(-time.Hour))

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-STALE",
		"priceProof":   priceProofParam(proof),
	}
	req := &RPCRequest{ID: 7, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params due to stale price proof, got %+v", rpcErr)
	}
	if !strings.Contains(rpcErr.Message, core.ErrSwapPriceProofStale.Error()) {
		t.Fatalf("expected stale price proof message, got %q", rpcErr.Message)
	}
}

// TestSwapSubmitVoucherSlippageExceeded now supplies a genuinely valid,
// signed price proof whose rate disagrees with the voucher's own signed
// Amount by more than swap.SlippageBps -- exercising the real deterministic
// slippage gate (computed from the price proof's rate, not a live oracle
// call) instead of accidentally passing because no price proof was supplied
// at all.
func TestSwapSubmitVoucherSlippageExceeded(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-SLIP")
	// Voucher.Amount now claims double the ZNHB the price proof's rate
	// would justify for the same FiatAmount -- well beyond the default 50bps
	// slippage allowance.
	voucher.Amount = new(big.Int).Mul(voucher.Amount, big.NewInt(2))
	sig := signSwapVoucher(t, minterKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-SLIP",
		"priceProof":   priceProofParam(proof),
	}
	req := &RPCRequest{ID: 8, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params due to slippage, got %+v", rpcErr)
	}
	if !strings.Contains(rpcErr.Message, core.ErrSwapSlippageExceeded.Error()) {
		t.Fatalf("expected slippage exceeded message, got %q", rpcErr.Message)
	}
}

func TestSwapSubmitVoucherDuplicateProvider(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-A")
	sig := signSwapVoucher(t, minterKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-1",
		"priceProof":   priceProofParam(proof),
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

	// Second voucher with different order but same providerTxId, still
	// resident in this node's own mempool (the first hasn't been mined yet)
	// -- rejected by the local same-mempool dedup check.
	voucherB := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-B")
	sigB := signSwapVoucher(t, minterKey, voucherB)
	proofB := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())
	payloadB := map[string]interface{}{
		"voucher":      voucherB,
		"sig":          "0x" + hex.EncodeToString(sigB),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-1",
		"priceProof":   priceProofParam(proofB),
	}
	reqB := &RPCRequest{ID: 10, Params: []json.RawMessage{marshalParam(t, payloadB)}}
	recorderB := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorderB, env.newRequest(), reqB)
	_, rpcErr = decodeRPCResponse(t, recorderB)
	if rpcErr == nil || rpcErr.Code != codeDuplicateTx {
		t.Fatalf("expected duplicate provider error, got %+v", rpcErr)
	}
}

// TestSwapSubmitVoucherSanctioned exercises the deterministic,
// config-driven sanctions deny-list (swap.SanctionsConfig.DenyList) instead
// of the old Node-level SetSwapSanctionsChecker callback hook -- a Go
// closure cannot run inside consensus execution (StateProcessor has no
// access to Node-level callbacks), so the deny-list is now the only sanctions
// mechanism the deterministic TxTypeSwapVoucherMint path can honour.
func TestSwapSubmitVoucherSanctioned(t *testing.T) {
	env := newTestEnv(t)
	minterKey, _ := crypto.GeneratePrivateKey()
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	configureSwapToken(t, env.node, minterAddr)

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())
	recipientAddrStr := crypto.MustNewAddress(crypto.NHBPrefix, recipient[:]).String()

	cfg := swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
		Risk:               swap.RiskConfig{SanctionsCheckEnabled: true},
		Sanctions:          swap.SanctionsConfig{DenyList: []string{recipientAddrStr}},
	}
	env.node.SetSwapConfig(cfg)

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-SANCTION")
	sig := signSwapVoucher(t, minterKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())

	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "ORDER-SANCTION",
		"priceProof":   priceProofParam(proof),
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

	oracleKey, _ := crypto.GeneratePrivateKey()
	registerSwapPriceSigner(t, env.node, "nowpayments", oracleKey)

	recipientKey, _ := crypto.GeneratePrivateKey()
	var recipient [20]byte
	copy(recipient[:], recipientKey.PubKey().Address().Bytes())

	env.setManualRate(t, "0.10", time.Now())
	voucher := buildSwapVoucher(t, env.node.Chain().ChainID(), recipient, "0.10", "ORDER-EXPORT")
	sig := signSwapVoucher(t, minterKey, voucher)
	proof := signedSwapPriceProof(t, oracleKey, "nowpayments", "0.10", time.Now())
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          "0x" + hex.EncodeToString(sig),
		"provider":     "nowpayments",
		"providerTxId": "PROVIDER-EXP",
		"priceProof":   priceProofParam(proof),
	}
	req := &RPCRequest{ID: 11, Params: []json.RawMessage{marshalParam(t, payload)}}
	recorder := httptest.NewRecorder()
	env.server.handleSwapSubmitVoucher(recorder, env.newRequest(), req)
	if _, rpcErr := decodeRPCResponse(t, recorder); rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}

	// The ledger record (what export/list read) only exists once the
	// transaction is actually committed -- it is not written synchronously
	// by the RPC call anymore.
	mineSwapBlock(t, env.node)

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
	proofID, ok := listPayload.Vouchers[0]["priceProofId"].(string)
	if !ok || strings.TrimSpace(proofID) == "" {
		t.Fatalf("expected priceProofId in voucher payload: %+v", listPayload.Vouchers[0])
	}
}

func TestSwapHandlersRequireHMAC(t *testing.T) {
	env := newTestEnv(t)
	env.now = time.Unix(1700000000, 0).UTC()
	params := []json.RawMessage{marshalParam(t, env.now.Add(-time.Hour).Unix()), marshalParam(t, env.now.Add(time.Hour).Unix())}
	body := buildRPCRequestBody(t, 1, "swap_voucher_list", params)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	env.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d", rec.Code)
	}
	if _, rpcErr := decodeRPCResponse(t, rec); rpcErr == nil || rpcErr.Code != codeUnauthorized {
		t.Fatalf("expected unauthorized error, got %+v", rpcErr)
	}
}

func TestSwapHandlersAcceptSignedRequest(t *testing.T) {
	env := newTestEnv(t)
	env.now = time.Unix(1700000000, 0).UTC()
	params := []json.RawMessage{marshalParam(t, env.now.Add(-time.Hour).Unix()), marshalParam(t, env.now.Add(time.Hour).Unix())}
	body := buildRPCRequestBody(t, 1, "swap_voucher_list", params)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	env.signSwapRequest(t, req, body, "nonce-accept")

	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK status, got %d", rec.Code)
	}
	if _, rpcErr := decodeRPCResponse(t, rec); rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
}

func TestSwapHandlersRateLimitPerPartner(t *testing.T) {
	env := newTestEnv(t)
	env.now = time.Unix(1700000000, 0).UTC()
	env.server.swapPartnerLimits[env.swapKey] = 1
	env.server.swapRateCounters = make(map[string]*rateLimiter)

	params := []json.RawMessage{marshalParam(t, env.now.Add(-time.Hour).Unix()), marshalParam(t, env.now.Add(time.Hour).Unix())}
	body := buildRPCRequestBody(t, 1, "swap_voucher_list", params)

	firstReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	env.signSwapRequest(t, firstReq, body, "nonce-1")
	firstRec := httptest.NewRecorder()
	env.server.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", firstRec.Code)
	}

	env.now = env.now.Add(time.Second)

	secondReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	env.signSwapRequest(t, secondReq, body, "nonce-2")
	secondRec := httptest.NewRecorder()
	env.server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limit status, got %d", secondRec.Code)
	}
	if _, rpcErr := decodeRPCResponse(t, secondRec); rpcErr == nil || rpcErr.Code != codeRateLimited {
		t.Fatalf("expected rate limited error, got %+v", rpcErr)
	}
}

func buildRPCRequestBody(t *testing.T, id int, method string, params []json.RawMessage) []byte {
	t.Helper()
	req := RPCRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return body
}
