package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbcrypto "nhbchain/crypto"
)

type mockNodeClient struct {
	mu           sync.Mutex
	createResp   *EscrowCreateResponse
	createErr    error
	getResp      *EscrowState
	getErr       error
	realmResp    *EscrowRealm
	realmErr     error
	createCalls  int
	releaseCalls int
	refundCalls  int
	disputeCalls int
	resolveCalls int
	resolveArgs  []struct {
		escrowID string
		caller   string
		outcome  string
	}
	releaseErr error
	refundErr  error
	disputeErr error
	resolveErr error

	realmCalls int

	p2pCreateResp  *P2PAcceptResponse
	p2pCreateErr   error
	p2pCreateCalls int
	lastP2PRequest P2PAcceptRequest

	p2pTradeResp *P2PTradeState
	p2pTradeErr  error

	events       []NodeEvent
	eventsCalled int
}

func (m *mockNodeClient) EscrowCreate(ctx context.Context, req EscrowCreateRequest) (*EscrowCreateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createResp != nil {
		// Return a copy to avoid mutation.
		resp := *m.createResp
		return &resp, nil
	}
	return nil, nil
}

func (m *mockNodeClient) EscrowGet(ctx context.Context, id string) (*EscrowState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.getResp != nil {
		resp := *m.getResp
		return &resp, nil
	}
	return nil, nil
}

func (m *mockNodeClient) EscrowGetRealm(ctx context.Context, id string) (*EscrowRealm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.realmCalls++
	if m.realmErr != nil {
		return nil, m.realmErr
	}
	if m.realmResp != nil {
		resp := *m.realmResp
		if m.realmResp.Metadata != nil {
			meta := *m.realmResp.Metadata
			resp.Metadata = &meta
		}
		if m.realmResp.Arbitrators != nil {
			policy := *m.realmResp.Arbitrators
			resp.Arbitrators = &policy
		}
		return &resp, nil
	}
	return nil, nil
}

func (m *mockNodeClient) EscrowRelease(ctx context.Context, escrowID, caller string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls++
	if m.releaseErr != nil {
		return m.releaseErr
	}
	return nil
}

func (m *mockNodeClient) EscrowRefund(ctx context.Context, escrowID, caller string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refundCalls++
	if m.refundErr != nil {
		return m.refundErr
	}
	return nil
}

func (m *mockNodeClient) EscrowDispute(ctx context.Context, escrowID, caller string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disputeCalls++
	if m.disputeErr != nil {
		return m.disputeErr
	}
	return nil
}

func (m *mockNodeClient) EscrowResolve(ctx context.Context, escrowID, caller, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolveCalls++
	m.resolveArgs = append(m.resolveArgs, struct {
		escrowID string
		caller   string
		outcome  string
	}{escrowID: escrowID, caller: caller, outcome: outcome})
	if m.resolveErr != nil {
		return m.resolveErr
	}
	return nil
}

func (m *mockNodeClient) P2PCreateTrade(ctx context.Context, req P2PAcceptRequest) (*P2PAcceptResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.p2pCreateCalls++
	m.lastP2PRequest = req
	if m.p2pCreateErr != nil {
		return nil, m.p2pCreateErr
	}
	if m.p2pCreateResp != nil {
		resp := *m.p2pCreateResp
		return &resp, nil
	}
	return nil, nil
}

func (m *mockNodeClient) P2PGetTrade(ctx context.Context, tradeID string) (*P2PTradeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p2pTradeErr != nil {
		return nil, m.p2pTradeErr
	}
	if m.p2pTradeResp != nil {
		resp := *m.p2pTradeResp
		return &resp, nil
	}
	return nil, nil
}

func (m *mockNodeClient) FetchEvents(ctx context.Context, afterSeq int64, limit int) ([]NodeEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventsCalled++
	return append([]NodeEvent(nil), m.events...), nil
}

func newTestServer(t *testing.T, node NodeClient, merchants map[string]MerchantConfig) (*Server, *SQLiteStore, *WebhookQueue) {
	t.Helper()
	store, err := NewSQLiteStore("file:testdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	auth := NewAuthenticator([]APIKeyConfig{{Key: "test", Secret: "secret"}}, time.Minute, 2*time.Minute, 4, func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	})
	queue := NewWebhookQueue()
	server := NewServer(auth, node, store, queue, NewPayIntentBuilder(), merchants)
	return server, store, queue
}

func signHeaders(secret, method, path string, body []byte, ts time.Time, nonce string) (timestamp, nonceOut, signature string) {
	timestamp = fmt.Sprintf("%d", ts.Unix())
	if nonce == "" {
		nonce = fmt.Sprintf("nonce-%d", ts.UnixNano())
	}
	signature = computeSignature(secret, timestamp, nonce, method, path, body)
	return timestamp, nonce, signature
}

func newWallet(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey).Bytes()
	bech := nhbcrypto.MustNewAddress(nhbcrypto.NHBPrefix, addr).String()
	return priv, bech
}

func signWalletRequest(t *testing.T, priv *ecdsa.PrivateKey, method, path string, body []byte, timestamp, nonce, resource string) string {
	t.Helper()
	payload := strings.Join([]string{strings.ToUpper(method), path, string(body), timestamp, nonce, strings.ToLower(strings.TrimSpace(resource))}, "|")
	hash := ethcrypto.Keccak256([]byte(payload))
	digest := accounts.TextHash(hash)
	sig, err := ethcrypto.Sign(digest, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return hex.EncodeToString(sig)
}

func TestAuthenticateRejectsInvalidSignature(t *testing.T) {
	node := &mockNodeClient{}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	body := []byte(`{"payer":"a","payee":"b","token":"NHB","amount":"1","feeBps":0,"deadline":1700000500}`)
	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, "1700000000")
	req.Header.Set(headerNonce, "nonce-invalid")
	req.Header.Set(headerSignature, "deadbeef")
	req.Header.Set(headerIdempotencyKey, "abc")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized got %d", rec.Code)
	}
	if node.createCalls != 0 {
		t.Fatalf("expected no create calls, got %d", node.createCalls)
	}
}

func TestIdempotentCreateCachesResponse(t *testing.T) {
	node := &mockNodeClient{createResp: &EscrowCreateResponse{ID: "0xabc"}}
	server, store, queue := newTestServer(t, node, nil)
	defer store.Close()

	payload := EscrowCreateRequest{
		Payer:    "payer",
		Payee:    "payee",
		Token:    "NHB",
		Amount:   "10",
		FeeBps:   0,
		Deadline: 1700000500,
		Nonce:    1,
	}
	body, _ := json.Marshal(payload)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts, "nonce-create-1")

	req1 := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req1.Header.Set(headerAPIKey, "test")
	req1.Header.Set(headerTimestamp, timestamp)
	req1.Header.Set(headerNonce, nonce)
	req1.Header.Set(headerSignature, sig)
	req1.Header.Set(headerIdempotencyKey, "idem123")

	rec1 := httptest.NewRecorder()
	server.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201 created got %d", rec1.Code)
	}
	if node.createCalls != 1 {
		t.Fatalf("expected one create call, got %d", node.createCalls)
	}
	if len(queue.Events()) != 1 {
		t.Fatalf("expected webhook event to be enqueued")
	}

	timestamp2, nonce2, sig2 := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts.Add(time.Second), "nonce-create-2")
	req2 := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req2.Header.Set(headerAPIKey, "test")
	req2.Header.Set(headerTimestamp, timestamp2)
	req2.Header.Set(headerNonce, nonce2)
	req2.Header.Set(headerSignature, sig2)
	req2.Header.Set(headerIdempotencyKey, "idem123")

	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected cached status 201 got %d", rec2.Code)
	}
	if node.createCalls != 1 {
		t.Fatalf("expected node not to be called again, got %d calls", node.createCalls)
	}
	if !bytes.Equal(rec1.Body.Bytes(), rec2.Body.Bytes()) {
		t.Fatalf("expected identical responses for idempotent requests")
	}
}

func TestCreateValidationMissingFields(t *testing.T) {
	node := &mockNodeClient{createResp: &EscrowCreateResponse{ID: "0xabc"}}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	body := []byte(`{"payee":"payee"}`)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts, "nonce-validation")

	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "validation")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 bad request got %d", rec.Code)
	}
	if node.createCalls != 0 {
		t.Fatalf("expected node not to be invoked on validation errors")
	}
}

func TestEscrowGetIncludesProviderMetadata(t *testing.T) {
	scope := "marketplace"
	realmType := "private"
	profile := "Acme Arbitrators"
	feeBps := uint32(150)
	recipient := "nhb1feeaddress"
	node := &mockNodeClient{
		getResp: &EscrowState{
			ID:                "0xabc",
			Payer:             "nhb1payer",
			Payee:             "nhb1payee",
			Status:            "init",
			RealmScope:        &scope,
			RealmType:         &realmType,
			RealmProfile:      &profile,
			RealmFeeBps:       &feeBps,
			RealmFeeRecipient: &recipient,
		},
	}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	req := httptest.NewRequest(http.MethodGet, "/escrow/0xabc", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", rec.Code)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := payload["realmScope"]; got != scope {
		t.Fatalf("expected realmScope %s got %v", scope, got)
	}
	if got := payload["realmType"]; got != realmType {
		t.Fatalf("expected realmType %s got %v", realmType, got)
	}
	if got := payload["realmProfile"]; got != profile {
		t.Fatalf("expected realmProfile %s got %v", profile, got)
	}
	if got := payload["realmFeeRecipient"]; got != recipient {
		t.Fatalf("expected fee recipient %s got %v", recipient, got)
	}
	if got := payload["realmFeeBps"]; got != float64(feeBps) {
		t.Fatalf("expected fee bps %d got %v", feeBps, got)
	}
}

func TestCreateRejectsRealmOutsideMerchantScope(t *testing.T) {
	node := &mockNodeClient{
		realmResp: &EscrowRealm{
			ID: "core",
			Metadata: &EscrowRealmMetadata{
				Scope: "platform",
				Type:  "public",
			},
		},
		createResp: &EscrowCreateResponse{ID: "0xescrow"},
	}
	merchants := map[string]MerchantConfig{
		"test": {
			Identity: "merchant-xyz",
			Realm: MerchantRealmConfig{
				Scope: "marketplace",
			},
		},
	}
	server, store, _ := newTestServer(t, node, merchants)
	defer store.Close()

	payload := EscrowCreateRequest{
		Payer:    "payer",
		Payee:    "payee",
		Token:    "NHB",
		Amount:   "10",
		FeeBps:   0,
		Deadline: 1700000500,
		Nonce:    1,
		Realm:    "core",
	}
	body, _ := json.Marshal(payload)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts, "nonce-merchant-scope")

	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "scope123")

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 got %d", rec.Code)
	}
	if node.createCalls != 0 {
		t.Fatalf("expected create not invoked, got %d", node.createCalls)
	}
	if node.realmCalls == 0 {
		t.Fatalf("expected realm metadata lookup")
	}
}

func TestEscrowReleaseWithValidSignature(t *testing.T) {
	priv, addr := newWallet(t)
	node := &mockNodeClient{
		getResp: &EscrowState{
			ID:     "0xdeadbeef",
			Payer:  "nhb1payer",
			Payee:  addr,
			Status: "funded",
		},
	}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	body := []byte(`{"escrowId":"0xdeadbeef"}`)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/release", body, ts, "nonce-release")
	walletSig := signWalletRequest(t, priv, http.MethodPost, "/escrow/release", body, timestamp, nonce, "0xdeadbeef")

	req := httptest.NewRequest(http.MethodPost, "/escrow/release", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "rel123")
	req.Header.Set(headerWalletAddress, addr)
	req.Header.Set(headerWalletSig, walletSig)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 accepted got %d", rec.Code)
	}
	if node.releaseCalls != 1 {
		t.Fatalf("expected release to be invoked once, got %d", node.releaseCalls)
	}
}

func TestEscrowResolveAuthorisedSignature(t *testing.T) {
	priv, addr := newWallet(t)
	node := &mockNodeClient{
		getResp: &EscrowState{
			ID:     "0xabc",
			Payer:  addr,
			Payee:  "nhb1payee",
			Status: "disputed",
		},
	}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	body := []byte(`{"escrowId":"0xabc","outcome":"release"}`)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/resolve", body, ts, "nonce-resolve")
	walletSig := signWalletRequest(t, priv, http.MethodPost, "/escrow/resolve", body, timestamp, nonce, "0xabc")

	req := httptest.NewRequest(http.MethodPost, "/escrow/resolve", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "res123")
	req.Header.Set(headerWalletAddress, addr)
	req.Header.Set(headerWalletSig, walletSig)

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 accepted got %d", rec.Code)
	}
	if node.resolveCalls != 1 {
		t.Fatalf("expected resolve to be invoked once, got %d", node.resolveCalls)
	}
	if len(node.resolveArgs) != 1 || node.resolveArgs[0].outcome != "release" {
		t.Fatalf("unexpected resolve args: %+v", node.resolveArgs)
	}
}

func TestP2POfferLifecycle(t *testing.T) {
	sellerPriv, sellerAddr := newWallet(t)
	buyerPriv, buyerAddr := newWallet(t)
	node := &mockNodeClient{
		p2pCreateResp: &P2PAcceptResponse{
			TradeID:       "0xtrade",
			EscrowBaseID:  "0xbase",
			EscrowQuoteID: "0xquote",
			PayIntents: map[string]P2PPayIntentPayload{
				"buyer":  {To: "nhb1buyer", Token: "ZNHB", Amount: "10", Memo: ""},
				"seller": {To: "nhb1seller", Token: "NHB", Amount: "5", Memo: ""},
			},
		},
	}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()
	server.nowFn = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	offerBody := []byte(fmt.Sprintf(`{"seller":"%s","baseToken":"NHB","baseAmount":"5","quoteToken":"ZNHB","quoteAmount":"10"}`, sellerAddr))
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/p2p/offers", offerBody, ts, "nonce-offer")
	walletSig := signWalletRequest(t, sellerPriv, http.MethodPost, "/p2p/offers", offerBody, timestamp, nonce, "")

	offerReq := httptest.NewRequest(http.MethodPost, "/p2p/offers", bytes.NewReader(offerBody))
	offerReq.Header.Set(headerAPIKey, "test")
	offerReq.Header.Set(headerTimestamp, timestamp)
	offerReq.Header.Set(headerNonce, nonce)
	offerReq.Header.Set(headerSignature, sig)
	offerReq.Header.Set(headerIdempotencyKey, "offer1")
	offerReq.Header.Set(headerWalletAddress, sellerAddr)
	offerReq.Header.Set(headerWalletSig, walletSig)

	offerRec := httptest.NewRecorder()
	server.ServeHTTP(offerRec, offerReq)
	if offerRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 created, got %d", offerRec.Code)
	}
	var created P2POffer
	if err := json.Unmarshal(offerRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected offer id")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/p2p/offers", nil)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 list, got %d", listRec.Code)
	}

	acceptBody := []byte(fmt.Sprintf(`{"offerId":"%s","buyer":"%s","deadline":1700000600}`, created.ID, buyerAddr))
	ts2 := time.Unix(1700000010, 0).UTC()
	timestamp2, nonce2, sig2 := signHeaders("secret", http.MethodPost, "/p2p/accept", acceptBody, ts2, "nonce-accept")
	walletSig2 := signWalletRequest(t, buyerPriv, http.MethodPost, "/p2p/accept", acceptBody, timestamp2, nonce2, created.ID)

	acceptReq := httptest.NewRequest(http.MethodPost, "/p2p/accept", bytes.NewReader(acceptBody))
	acceptReq.Header.Set(headerAPIKey, "test")
	acceptReq.Header.Set(headerTimestamp, timestamp2)
	acceptReq.Header.Set(headerNonce, nonce2)
	acceptReq.Header.Set(headerSignature, sig2)
	acceptReq.Header.Set(headerIdempotencyKey, "accept1")
	acceptReq.Header.Set(headerWalletAddress, buyerAddr)
	acceptReq.Header.Set(headerWalletSig, walletSig2)

	acceptRec := httptest.NewRecorder()
	server.ServeHTTP(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 created for accept got %d", acceptRec.Code)
	}
	if node.p2pCreateCalls != 1 {
		t.Fatalf("expected p2p create invoked once, got %d", node.p2pCreateCalls)
	}

	tradeReq := httptest.NewRequest(http.MethodGet, "/p2p/trades/0xtrade", nil)
	tradeRec := httptest.NewRecorder()
	server.ServeHTTP(tradeRec, tradeReq)
	if tradeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 trade get got %d", tradeRec.Code)
	}
}

func TestEventWatcherProcessesEvents(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore("file:testwatcher?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()
	queue := NewWebhookQueue()
	now := time.Unix(1700001000, 0).UTC()
	trade := P2PTrade{
		ID:            "0xtrade",
		OfferID:       "OFF_1",
		Buyer:         "nhb1buyer",
		Seller:        "nhb1seller",
		BaseToken:     "NHB",
		BaseAmount:    "5",
		QuoteToken:    "ZNHB",
		QuoteAmount:   "10",
		EscrowBaseID:  "0xbase",
		EscrowQuoteID: "0xquote",
		Status:        "created",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.InsertTrade(ctx, trade); err != nil {
		t.Fatalf("insert trade: %v", err)
	}
	if err := store.LinkEscrowToTrade(ctx, "0xbase", trade.ID); err != nil {
		t.Fatalf("link base: %v", err)
	}
	if err := store.LinkEscrowToTrade(ctx, "0xquote", trade.ID); err != nil {
		t.Fatalf("link quote: %v", err)
	}
	node := &mockNodeClient{
		events: []NodeEvent{{
			Sequence:   1,
			Type:       "escrow.trade.funded",
			Attributes: map[string]string{"tradeId": "trade"},
			Timestamp:  now.Unix(),
		}},
	}
	watcher := NewEventWatcher(node, store, queue)
	watcher.nowFn = func() time.Time { return now }
	watcher.poll(ctx, 0)

	events := queue.Events()
	if len(events) != 1 {
		t.Fatalf("expected one webhook event, got %d", len(events))
	}
	if events[0].Type != "escrow.trade.funded" {
		t.Fatalf("unexpected event type %s", events[0].Type)
	}
	if events[0].TradeID != trade.ID {
		t.Fatalf("expected trade id %s got %s", trade.ID, events[0].TradeID)
	}
	storedTrade, err := store.GetTrade(ctx, trade.ID)
	if err != nil {
		t.Fatalf("get trade: %v", err)
	}
	if storedTrade.Status != "funded" {
		t.Fatalf("expected trade status funded, got %s", storedTrade.Status)
	}
}

func TestWebhookWorkerDelivers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := NewSQLiteStore("file:testwebhook?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()
	queue := NewWebhookQueue()
	payloadCh := make(chan []byte, 1)
	sigCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		payloadCh <- body
		sigCh <- r.Header.Get("X-Webhook-Signature")
	}))
	defer server.Close()
	now := time.Unix(1700002000, 0).UTC()
	subID, err := store.InsertWebhook(ctx, WebhookSubscription{
		APIKey:    "test",
		EventType: "escrow.trade.funded",
		URL:       server.URL,
		Secret:    "whsecret",
		RateLimit: 10,
		Active:    true,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("insert webhook: %v", err)
	}
	worker := NewWebhookWorker(store, queue)
	worker.nowFn = func() time.Time { return now }

	go worker.Run(ctx)

	queue.Enqueue(WebhookEvent{
		Sequence:   1,
		Type:       "escrow.trade.funded",
		TradeID:    "0xtrade",
		Attributes: map[string]string{"tradeId": "trade"},
		CreatedAt:  now,
	})

	select {
	case body := <-payloadCh:
		sig := <-sigCh
		expected := signPayload("whsecret", body)
		if sig != expected {
			t.Fatalf("unexpected signature got %s want %s", sig, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for webhook delivery")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		row := store.db.QueryRow("SELECT status FROM webhook_attempts WHERE webhook_id = ?", subID)
		var status string
		err := row.Scan(&status)
		if err == nil {
			if status != "success" {
				t.Fatalf("expected success status, got %s", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan attempt: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
