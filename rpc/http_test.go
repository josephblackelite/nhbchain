package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/observability"
	"nhbchain/storage"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func injectClientIP(t *testing.T, srv *Server, req *http.Request) *http.Request {
	t.Helper()
	ip, err := srv.resolveClientIP(req)
	if err != nil {
		t.Fatalf("resolve client ip: %v", err)
	}
	ctx := context.WithValue(req.Context(), clientIPContextKey, ip)
	return req.WithContext(ctx)
}

func TestResolveClientIPRejectsUntrustedForwardedFor(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{ProxyHeaders: ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle}})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if _, err := server.resolveClientIP(req); err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("expected untrusted proxy error, got %v", err)
	}
}

func TestServerServeRejectsPlaintextWithoutAllowInsecure(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := server.Serve(listener); err == nil || !strings.Contains(err.Error(), "TLS is required") {
		t.Fatalf("expected TLS requirement error, got %v", err)
	}
}

func TestServerServeAllowsPlaintextOnLoopbackWhenExplicit(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{AllowInsecure: true})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		server.serverMu.Lock()
		ready := server.httpServer != nil
		server.serverMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if err := <-serveErr; err != nil && err != http.ErrServerClosed && !strings.Contains(err.Error(), "use of closed") {
		t.Fatalf("serve returned unexpected error: %v", err)
	}
}

func TestServerServeRejectsPlaintextOnNonLoopback(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{AllowInsecure: true})
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	listener := &addrOverrideListener{Listener: base, addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: base.Addr().(*net.TCPAddr).Port}}
	if err := server.Serve(listener); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback restriction error, got %v", err)
	}
}

func TestServerServeRejectsPlaintextOnUnspecifiedWithoutOverride(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{AllowInsecure: true})
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := server.Serve(listener); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback restriction error, got %v", err)
	}
}

func TestServerServeAllowsPlaintextOnUnspecifiedWithOverride(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{AllowInsecure: true, AllowInsecureUnspecified: true})
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		server.serverMu.Lock()
		ready := server.httpServer != nil
		server.serverMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if err := <-serveErr; err != nil && err != http.ErrServerClosed && !strings.Contains(err.Error(), "use of closed") {
		t.Fatalf("serve returned unexpected error: %v", err)
	}
}

type addrOverrideListener struct {
	net.Listener
	addr net.Addr
}

func (l *addrOverrideListener) Addr() net.Addr {
	return l.addr
}

func TestClientSourceHonorsForwardedForFromTrustedProxy(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustedProxies: []string{"10.0.0.1"},
		ProxyHeaders:   ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle},
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req = injectClientIP(t, server, req)
	if source := server.clientSource(req); source != "198.51.100.7" {
		t.Fatalf("expected forwarded client, got %q", source)
	}
}

func TestClientSourceHonorsForwardedForWhenTrustFlagEnabled(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustProxyHeaders: true,
		ProxyHeaders:      ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle},
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.10:7000"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	req = injectClientIP(t, server, req)
	if source := server.clientSource(req); source != "198.51.100.8" {
		t.Fatalf("expected forwarded client, got %q", source)
	}
}

func TestRateLimitTrustedProxyHonorsForwardedFor(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustedProxies: []string{"10.0.0.1"},
		ProxyHeaders:   ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle},
	})
	now := time.Now()
	remoteAddr := "10.0.0.1:5000"

	forwarded := "198.51.100.1"
	limit := server.rateLimitMax[limiterScopeIP]
	for i := 0; i < limit; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", forwarded)
		req = injectClientIP(t, server, req)
		if !server.allowSource(server.clientSource(req), "", "", now) {
			t.Fatalf("trusted proxy request %d should be allowed", i)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-For", forwarded)
	req = injectClientIP(t, server, req)
	if server.allowSource(server.clientSource(req), "", "", now) {
		t.Fatalf("expected rate limit when exceeding window for same client")
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-For", "198.51.100.2")
	req = injectClientIP(t, server, req)
	if !server.allowSource(server.clientSource(req), "", "", now) {
		t.Fatalf("distinct client behind trusted proxy should be allowed")
	}
}

func TestClientSourceCanonicalizesForwardedFor(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustedProxies: []string{"10.0.0.1"},
		ProxyHeaders:   ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle},
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8000"
	req.Header.Set("X-Forwarded-For", " 198.51.100.9:443 ")
	req = injectClientIP(t, server, req)
	if source := server.clientSource(req); source != "198.51.100.9" {
		t.Fatalf("expected canonical forwarded client, got %q", source)
	}
}

func TestClientSourceCapsForwardedForChain(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustedProxies: []string{"10.0.0.1"},
		ProxyHeaders:   ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle},
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8000"
	parts := make([]string, maxForwardedForAddrs+1)
	for i := range parts {
		parts[i] = " 198.51.100.9 "
	}
	req.Header.Set("X-Forwarded-For", strings.Join(parts, ","))
	if _, err := server.resolveClientIP(req); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected forwarded chain rejection, got %v", err)
	}
}

func TestRateLimiterNormalizesSources(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()

	if !server.allowSource(" 198.51.100.11 ", "", "", now) {
		t.Fatalf("expected first request to be allowed")
	}
	if !server.allowSource("198.51.100.11", "", "", now) {
		t.Fatalf("expected normalized source to use same limiter")
	}
	server.mu.Lock()
	limiterCount := len(server.rateLimiters)
	server.mu.Unlock()
	if limiterCount != 1 {
		t.Fatalf("expected a single limiter entry, got %d", limiterCount)
	}
}

func TestRateLimiterEvictsStaleEntries(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()
	staleTime := now.Add(-server.rateLimiterStaleAfter - time.Second)

	for i := 0; i < 3; i++ {
		source := fmt.Sprintf("198.51.100.%d", i)
		if !server.allowSource(source, "", "", staleTime) {
			t.Fatalf("expected stale source %d to be tracked", i)
		}
	}
	server.mu.Lock()
	if len(server.rateLimiters) != 3 {
		server.mu.Unlock()
		t.Fatalf("expected three limiter entries before eviction, got %d", len(server.rateLimiters))
	}
	server.mu.Unlock()

	if !server.allowSource("new-source", "", "", now) {
		t.Fatalf("expected request from new source to be allowed")
	}

	server.mu.Lock()
	if len(server.rateLimiters) != 1 {
		count := len(server.rateLimiters)
		server.mu.Unlock()
		t.Fatalf("expected stale limiters to be evicted, got %d entries", count)
	}
	if _, ok := server.rateLimiters[limiterKeyPrefixIP+"new-source"]; !ok {
		server.mu.Unlock()
		t.Fatalf("expected new source limiter to remain")
	}
	server.mu.Unlock()
}

func TestRateLimiterEvictsOldestWhenCapacityExceeded(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()

	for i := 0; i < rateLimiterMaxEntries; i++ {
		source := fmt.Sprintf("client-%d", i)
		if !server.allowSource(source, "", "", now) {
			t.Fatalf("expected initial requests to be allowed")
		}
	}

	if !server.allowSource("extra-client", "", "", now) {
		t.Fatalf("expected extra client to be allowed after eviction")
	}

	server.mu.Lock()
	if len(server.rateLimiters) != rateLimiterMaxEntries {
		count := len(server.rateLimiters)
		server.mu.Unlock()
		t.Fatalf("expected limiter map to cap at %d entries, got %d", rateLimiterMaxEntries, count)
	}
	if _, ok := server.rateLimiters[limiterKeyPrefixIP+"extra-client"]; !ok {
		server.mu.Unlock()
		t.Fatalf("expected extra client limiter to be stored")
	}
	evictedInitial := false
	for i := 0; i < rateLimiterMaxEntries; i++ {
		if _, ok := server.rateLimiters[limiterKeyPrefixIP+fmt.Sprintf("client-%d", i)]; !ok {
			evictedInitial = true
			break
		}
	}
	server.mu.Unlock()
	if !evictedInitial {
		t.Fatalf("expected at least one initial limiter to be evicted")
	}
}

func TestRateLimiterChurnEnforcesLimits(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()
	source := "198.51.100.200"

	limit := server.rateLimitMax[limiterScopeIP]
	for i := 0; i < limit; i++ {
		if !server.allowSource(source, "", "", now) {
			t.Fatalf("expected request %d to be allowed", i)
		}
	}

	for i := 0; i < rateLimiterMaxEntries-1; i++ {
		churnSource := fmt.Sprintf("churn-%d", i)
		if !server.allowSource(churnSource, "", "", now) {
			t.Fatalf("expected churn source %d to be allowed", i)
		}
	}

	if server.allowSource(source, "", "", now) {
		t.Fatalf("expected churned source to remain rate limited within same window")
	}
}

func TestRateLimiterUsesIdentityAndNonce(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()

	identity := "user@example.com"
	nonceKey := "chain-1:1"
	limit := server.rateLimitMax[limiterScopeIdentityChain]
	for i := 0; i < limit; i++ {
		if !server.allowSource("203.0.113.10", identity, nonceKey, now) {
			t.Fatalf("expected request %d for identity to be allowed", i)
		}
	}
	if server.allowSource("203.0.113.10", identity, nonceKey, now) {
		t.Fatalf("expected identity to be rate limited once window exhausted")
	}

	if server.allowSource("198.51.100.5", identity, "chain-1:2", now) {
		t.Fatalf("expected identity limiter to persist across nonce changes")
	}
	if server.allowSource("198.51.100.5", "other@example.com", nonceKey, now) {
		t.Fatalf("expected chain limiter to prevent reuse under a new identity")
	}
}

func TestRateLimiterIsolatesIdentitiesSharingIP(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		MaxTxPerIP:            10,
		MaxTxPerIdentity:      2,
		MaxTxPerChain:         5,
		MaxTxPerIdentityChain: 2,
	})
	now := time.Now()
	source := "198.51.100.42"
	chain := "chain-1:1"
	alice := "alice@example.com"
	bob := "bob@example.com"

	if !server.allowSource(source, alice, chain, now) {
		t.Fatalf("expected initial request for alice to be allowed")
	}
	if !server.allowSource(source, alice, chain, now) {
		t.Fatalf("expected second request for alice to be allowed")
	}
	if server.allowSource(source, alice, chain, now) {
		t.Fatalf("expected alice to be rate limited after exhausting identity quota")
	}

	if !server.allowSource(source, bob, chain, now) {
		t.Fatalf("expected bob to be allowed despite alice being rate limited")
	}
	if !server.allowSource(source, bob, chain, now) {
		t.Fatalf("expected bob to have independent quota behind same IP")
	}
	if server.allowSource(source, bob, chain, now) {
		t.Fatalf("expected bob to eventually hit identity quota independently")
	}
}

func TestRateLimiterRecordsMetrics(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()
	metrics := observability.RPC()
	counter := metrics.LimiterHits()
	initial := testutil.ToFloat64(counter.WithLabelValues(limiterScopeIP))
	source := "198.51.100.250"
	limit := server.rateLimitMax[limiterScopeIP]
	for i := 0; i < limit; i++ {
		if !server.allowSource(source, "", "", now) {
			t.Fatalf("expected request %d to be allowed", i)
		}
	}
	if server.allowSource(source, "", "", now) {
		t.Fatalf("expected limiter to reject request beyond window")
	}
	final := testutil.ToFloat64(counter.WithLabelValues(limiterScopeIP))
	if final-initial != 1 {
		t.Fatalf("expected limiter hits metric to increment by 1, got %.0f -> %.0f", initial, final)
	}
}

func TestServerRememberTxRejectsDuplicateWithinTTL(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	now := time.Now()

	if !server.rememberTx("tx-1", now) {
		t.Fatalf("expected first occurrence to be accepted")
	}
	if server.rememberTx("tx-1", now.Add(time.Second)) {
		t.Fatalf("expected duplicate within TTL to be rejected")
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.txSeen) != 1 {
		t.Fatalf("expected a single entry to remain, got %d", len(server.txSeen))
	}
	if len(server.txSeenQueue) != 1 {
		t.Fatalf("expected a single queue entry to remain, got %d", len(server.txSeenQueue))
	}
}

func TestServerRememberTxEvictsExpired(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	base := time.Now().Add(-2 * txSeenTTL)

	if !server.rememberTx("tx-old", base) {
		t.Fatalf("expected initial transaction to be accepted")
	}

	advanced := base.Add(txSeenTTL + time.Minute)
	if !server.rememberTx("tx-new", advanced) {
		t.Fatalf("expected transaction after TTL to be accepted")
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if _, exists := server.txSeen["tx-old"]; exists {
		t.Fatalf("expected expired transaction to be evicted")
	}
	if _, exists := server.txSeen["tx-new"]; !exists {
		t.Fatalf("expected new transaction to be recorded")
	}
	if len(server.txSeenQueue) != 1 {
		t.Fatalf("expected queue to contain only the fresh transaction, got %d entries", len(server.txSeenQueue))
	}
}

func TestServerRememberTxIncludesPaymasterInHash(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}

	paymasterA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster A: %v", err)
	}

	paymasterB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster B: %v", err)
	}

	newTx := func(paymaster []byte) *types.Transaction {
		to := make([]byte, 20)
		copy(to, []byte{0x02})
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    5,
			To:       to,
			GasLimit: 25_000,
			GasPrice: big.NewInt(2_000_000_000),
			Value:    big.NewInt(42),
		}
		if len(paymaster) > 0 {
			tx.Paymaster = append([]byte(nil), paymaster...)
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign tx: %v", err)
		}
		return tx
	}

	now := time.Now()

	txA := newTx(paymasterA.PubKey().Address().Bytes())
	hashABytes, err := txA.Hash()
	if err != nil {
		t.Fatalf("hash tx A: %v", err)
	}
	hashA := hex.EncodeToString(hashABytes)
	if !server.rememberTx(hashA, now) {
		t.Fatalf("expected first paymaster submission to be accepted")
	}

	txB := newTx(paymasterB.PubKey().Address().Bytes())
	hashBBytes, err := txB.Hash()
	if err != nil {
		t.Fatalf("hash tx B: %v", err)
	}
	hashB := hex.EncodeToString(hashBBytes)
	if !server.rememberTx(hashB, now) {
		t.Fatalf("expected different paymaster submission to be accepted")
	}

	txAResub := newTx(paymasterA.PubKey().Address().Bytes())
	hashAResubBytes, err := txAResub.Hash()
	if err != nil {
		t.Fatalf("hash tx A resub: %v", err)
	}
	hashAResub := hex.EncodeToString(hashAResubBytes)
	if server.rememberTx(hashAResub, now) {
		t.Fatalf("expected identical paymaster submission to be rejected")
	}
}

func TestHandleSendTransactionInvalidSignature(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       make([]byte, 20),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(10),
	}

	param, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()

	server.handleSendTransaction(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected rpc error in response")
	}
	if resp.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params error code, got %d", resp.Error.Code)
	}
}

func TestHandleSendTransactionInvalidChainID(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})

	tx := &types.Transaction{
		ChainID:  big.NewInt(12345),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       make([]byte, 20),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(10),
	}

	param, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()

	server.handleSendTransaction(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected rpc error in response")
	}
	if resp.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params error code, got %d", resp.Error.Code)
	}
}

func TestHandleSendTransactionAcceptsZNHBTransfer(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()

	if err := node.WithState(func(m *nhbstate.Manager) error {
		if _, err := m.Token("ZNHB"); err != nil {
			if regErr := m.RegisterToken("ZNHB", "ZapNHB", 18); regErr != nil && !strings.Contains(regErr.Error(), "already registered") {
				return regErr
			}
		}
		senderAccount := &types.Account{
			BalanceNHB:  big.NewInt(1_000_000),
			BalanceZNHB: big.NewInt(900),
			Stake:       big.NewInt(0),
		}
		if err := m.PutAccount(senderAddr, senderAccount); err != nil {
			return err
		}
		recipientAccount := &types.Account{
			BalanceNHB:  big.NewInt(0),
			BalanceZNHB: big.NewInt(0),
			Stake:       big.NewInt(0),
		}
		return m.PutAccount(recipientAddr, recipientAccount)
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}

	server := newTestServer(t, node, nil, ServerConfig{})

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(250),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	param, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()

	server.handleSendTransaction(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	msg, ok := resp.Result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", resp.Result)
	}
	if msg != "Transaction received by node." {
		t.Fatalf("unexpected result message: %s", msg)
	}
}

func TestHandleSendTransactionInvalidTransactionError(t *testing.T) {
	db := storage.NewMemDB()
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	server := newTestServer(t, node, nil, ServerConfig{})

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Nonce:    0,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Data:     []byte("invalid-mint-payload"),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}

	param, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{param}}
	recorder := httptest.NewRecorder()

	server.handleSendTransaction(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected rpc error in response")
	}
	if resp.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params error code, got %d", resp.Error.Code)
	}
}

func TestHandleGetBalanceRejectsMalformedAddress(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{})
	recorder := httptest.NewRecorder()
	req := &RPCRequest{ID: 7}
	req.Params = []json.RawMessage{json.RawMessage(`"not-a-valid-address"`)}

	server.handleGetBalance(recorder, nil, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request status, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error in response")
	}
	if resp.Error.Code != codeInvalidParams {
		t.Fatalf("expected code %d, got %d", codeInvalidParams, resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Fatalf("expected error message to be present")
	}
}

func TestWriteErrorScrubsInternalDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeError(recorder, http.StatusInternalServerError, 42, codeServerError, "failed to encode transaction", "sensitive detail")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error in response")
	}
	if resp.Error.Data != nil {
		t.Fatalf("expected error data to be scrubbed, got %#v", resp.Error.Data)
	}
	if strings.Contains(recorder.Body.String(), "sensitive detail") {
		t.Fatalf("response leaked sensitive detail: %s", recorder.Body.String())
	}
}

func TestWriteErrorRetainsClientDetailsForBadRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeError(recorder, http.StatusBadRequest, 7, codeInvalidParams, "invalid parameter", "client detail")

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error in response")
	}
	detail, ok := resp.Error.Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", resp.Error.Data)
	}
	if detail != "client detail" {
		t.Fatalf("unexpected error data: %s", detail)
	}
}
