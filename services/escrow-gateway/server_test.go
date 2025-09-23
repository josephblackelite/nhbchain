package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type mockNodeClient struct {
	mu          sync.Mutex
	createResp  *EscrowCreateResponse
	createErr   error
	getResp     *EscrowState
	getErr      error
	createCalls int
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

func newTestServer(t *testing.T, node NodeClient) (*Server, *SQLiteStore, *WebhookQueue) {
	t.Helper()
	store, err := NewSQLiteStore("file:testdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	auth := NewAuthenticator([]APIKeyConfig{{Key: "test", Secret: "secret"}}, time.Minute, func() time.Time {
		return time.Unix(1700000000, 0).UTC()
	})
	queue := NewWebhookQueue()
	server := NewServer(auth, node, store, queue, NewPayIntentBuilder())
	return server, store, queue
}

func signHeaders(secret, method, path string, body []byte, ts time.Time) (timestamp, signature string) {
	timestamp = fmt.Sprintf("%d", ts.Unix())
	signature = computeSignature(secret, timestamp, method, path, body)
	return
}

func TestAuthenticateRejectsInvalidSignature(t *testing.T) {
	node := &mockNodeClient{}
	server, store, _ := newTestServer(t, node)
	defer store.Close()

	body := []byte(`{"payer":"a","payee":"b","token":"NHB","amount":"1","feeBps":0,"deadline":1700000500}`)
	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, "1700000000")
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
	server, store, queue := newTestServer(t, node)
	defer store.Close()

	payload := EscrowCreateRequest{
		Payer:    "payer",
		Payee:    "payee",
		Token:    "NHB",
		Amount:   "10",
		FeeBps:   0,
		Deadline: 1700000500,
	}
	body, _ := json.Marshal(payload)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts)

	req1 := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req1.Header.Set(headerAPIKey, "test")
	req1.Header.Set(headerTimestamp, timestamp)
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

	req2 := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req2.Header.Set(headerAPIKey, "test")
	req2.Header.Set(headerTimestamp, timestamp)
	req2.Header.Set(headerSignature, sig)
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
	server, store, _ := newTestServer(t, node)
	defer store.Close()

	body := []byte(`{"payee":"payee"}`)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts)

	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
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
