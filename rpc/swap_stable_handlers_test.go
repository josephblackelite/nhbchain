package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel/trace"

	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/services/swapd/stable"
)

func TestStableRPCHandlersFlow(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newStableRPCTestEngine(t, base)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	srv := newTestServer(t, nil, nil, ServerConfig{
		SwapAuth: SwapAuthConfig{
			Secrets:              map[string]string{"partner": "secret"},
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             5 * time.Minute,
			NonceCapacity:        32,
			RateLimitWindow:      time.Minute,
			Now: func() time.Time {
				return base
			},
		},
	})
	srv.ConfigureStableEngine(engine, limits, []stable.Asset{asset}, func() time.Time { return base.Add(10 * time.Second) })

	traceCtx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xA0, 0xB0, 0xC0, 0xD0, 0xE0, 0xF0, 0x01},
		SpanID:     trace.SpanID{0x02, 0x04, 0x06, 0x08, 0x0A, 0x0C, 0x0E, 0x10},
		TraceFlags: trace.FlagsSampled,
	}))
	if srv.jwtVerifier != nil {
		srv.jwtVerifier.now = func() time.Time { return base }
	}

	quoteReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "nhb_requestSwapApproval",
		"params": []any{
			map[string]any{"asset": "ZNHB", "amount": 100, "account": "merchant-123"},
		},
	}
	token := signStableJWT(t, base)

	quoteResp := doSignedStableRPCRequest(t, srv, traceCtx, quoteReq, base, "nonce-1", token, http.StatusOK)
	var quoteRPC RPCResponse
	if err := json.Unmarshal(quoteResp, &quoteRPC); err != nil {
		t.Fatalf("unmarshal quote response: %v", err)
	}
	if quoteRPC.Error != nil {
		t.Fatalf("quote error: %+v", quoteRPC.Error)
	}
	quoteResult, ok := quoteRPC.Result.(map[string]any)
	if !ok {
		t.Fatalf("quote result type %T", quoteRPC.Result)
	}
	quoteID := quoteResult["quoteId"].(string)
	if quoteID != "q-1717787718000000000" {
		t.Fatalf("unexpected quoteId %s", quoteID)
	}
	if quoteResult["expiresAt"].(string) != "2024-06-07T19:16:18Z" {
		t.Fatalf("unexpected quote expiresAt %v", quoteResult["expiresAt"])
	}
	if quoteResult["traceId"].(string) != "102030405060708090a0b0c0d0e0f001" {
		t.Fatalf("unexpected quote traceId %v", quoteResult["traceId"])
	}

	reserveReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "nhb_swapMint",
		"params": []any{
			map[string]any{"quoteId": quoteID, "amountIn": 100, "account": "merchant-123"},
		},
	}
	reserveResp := doSignedStableRPCRequest(t, srv, traceCtx, reserveReq, base.Add(time.Second), "nonce-2", token, http.StatusOK)
	var reserveRPC RPCResponse
	if err := json.Unmarshal(reserveResp, &reserveRPC); err != nil {
		t.Fatalf("unmarshal reserve response: %v", err)
	}
	if reserveRPC.Error != nil {
		t.Fatalf("reserve error: %+v", reserveRPC.Error)
	}
	reserveResult, ok := reserveRPC.Result.(map[string]any)
	if !ok {
		t.Fatalf("reserve result type %T", reserveRPC.Result)
	}
	reservationID := reserveResult["reservationId"].(string)
	if reservationID != quoteID {
		t.Fatalf("unexpected reservationId %s", reservationID)
	}
	if reserveResult["expiresAt"].(string) != "2024-06-07T19:16:18Z" {
		t.Fatalf("unexpected reserve expiresAt %v", reserveResult["expiresAt"])
	}

	burnReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "nhb_swapBurn",
		"params": []any{
			map[string]any{"reservationId": reservationID},
		},
	}
	burnResp := doSignedStableRPCRequest(t, srv, traceCtx, burnReq, base.Add(2*time.Second), "nonce-3", token, http.StatusOK)
	var burnRPC RPCResponse
	if err := json.Unmarshal(burnResp, &burnRPC); err != nil {
		t.Fatalf("unmarshal burn response: %v", err)
	}
	if burnRPC.Error != nil {
		t.Fatalf("burn error: %+v", burnRPC.Error)
	}
	burnResult, ok := burnRPC.Result.(map[string]any)
	if !ok {
		t.Fatalf("burn result type %T", burnRPC.Result)
	}
	if burnResult["intentId"].(string) != "i-1717787724000000000" {
		t.Fatalf("unexpected intentId %v", burnResult["intentId"])
	}
	if burnResult["createdAt"].(string) != "2024-06-07T19:15:24Z" {
		t.Fatalf("unexpected createdAt %v", burnResult["createdAt"])
	}

	statusReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "nhb_getSwapStatus",
	}
	statusResp := doSignedStableRPCRequest(t, srv, traceCtx, statusReq, base.Add(3*time.Second), "nonce-4", token, http.StatusOK)
	var statusRPC RPCResponse
	if err := json.Unmarshal(statusResp, &statusRPC); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if statusRPC.Error != nil {
		t.Fatalf("status error: %+v", statusRPC.Error)
	}
	statusResult, ok := statusRPC.Result.(map[string]any)
	if !ok {
		t.Fatalf("status result type %T", statusRPC.Result)
	}
	if statusResult["quotes"].(float64) != 0 || statusResult["reservations"].(float64) != 0 || statusResult["assets"].(float64) != 1 {
		t.Fatalf("unexpected status counters: %+v", statusResult)
	}
	if statusResult["updatedAt"].(string) != "2024-06-07T19:15:27Z" {
		t.Fatalf("unexpected updatedAt %v", statusResult["updatedAt"])
	}
}

func TestStableRPCHandlersRequireRPCAuth(t *testing.T) {
	base := time.Date(2024, time.June, 7, 19, 15, 17, 0, time.UTC)
	engine := newStableRPCTestEngine(t, base)
	limits := stable.Limits{DailyCap: 1_000_000}
	asset := stable.Asset{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}
	srv := newTestServer(t, nil, nil, ServerConfig{
		SwapAuth: SwapAuthConfig{
			Secrets:              map[string]string{"partner": "secret"},
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             5 * time.Minute,
			NonceCapacity:        32,
			RateLimitWindow:      time.Minute,
			Now: func() time.Time {
				return base
			},
		},
	})
	srv.ConfigureStableEngine(engine, limits, []stable.Asset{asset}, func() time.Time { return base.Add(10 * time.Second) })

	traceCtx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xA0, 0xB0, 0xC0, 0xD0, 0xE0, 0xF0, 0x01},
		SpanID:     trace.SpanID{0x02, 0x04, 0x06, 0x08, 0x0A, 0x0C, 0x0E, 0x10},
		TraceFlags: trace.FlagsSampled,
	}))

	tests := []struct {
		name    string
		payload map[string]any
		nonce   string
		offset  time.Duration
	}{
		{
			name: "request approval",
			payload: map[string]any{
				"jsonrpc": "2.0",
				"id":      10,
				"method":  "nhb_requestSwapApproval",
				"params": []any{
					map[string]any{"asset": "ZNHB", "amount": 100, "account": "merchant-123"},
				},
			},
			nonce:  "unauth-1",
			offset: 0,
		},
		{
			name: "swap mint",
			payload: map[string]any{
				"jsonrpc": "2.0",
				"id":      11,
				"method":  "nhb_swapMint",
				"params": []any{
					map[string]any{"quoteId": "q-1717787718000000000", "amountIn": 100, "account": "merchant-123"},
				},
			},
			nonce:  "unauth-2",
			offset: time.Second,
		},
		{
			name: "swap burn",
			payload: map[string]any{
				"jsonrpc": "2.0",
				"id":      12,
				"method":  "nhb_swapBurn",
				"params": []any{
					map[string]any{"reservationId": "q-1717787718000000000"},
				},
			},
			nonce:  "unauth-3",
			offset: 2 * time.Second,
		},
		{
			name: "swap status",
			payload: map[string]any{
				"jsonrpc": "2.0",
				"id":      13,
				"method":  "nhb_getSwapStatus",
			},
			nonce:  "unauth-4",
			offset: 3 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doSignedStableRPCRequest(t, srv, traceCtx, tc.payload, base.Add(tc.offset), tc.nonce, "", http.StatusUnauthorized)
			var rpcResp RPCResponse
			if err := json.Unmarshal(resp, &rpcResp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if rpcResp.Error == nil {
				t.Fatalf("expected error response")
			}
			if rpcResp.Error.Code != codeUnauthorized {
				t.Fatalf("unexpected error code %d", rpcResp.Error.Code)
			}
		})
	}
}

func doSignedStableRPCRequest(t *testing.T, srv *Server, ctx context.Context, payload map[string]any, ts time.Time, nonce string, authToken string, wantStatus int) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	timestamp := fmt.Sprintf("%d", ts.Unix())
	req.Header.Set(gatewayauth.HeaderAPIKey, "partner")
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	signature := gatewayauth.ComputeSignature("secret", timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), body)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	recorder := httptest.NewRecorder()
	srv.handle(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("unexpected status %d body %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func newStableRPCTestEngine(t *testing.T, base time.Time) *stable.Engine {
	t.Helper()
	assets := []stable.Asset{{
		Symbol:         "ZNHB",
		BasePair:       "ZNHB",
		QuotePair:      "USD",
		QuoteTTL:       time.Minute,
		MaxSlippageBps: 50,
		SoftInventory:  1_000_000,
	}}
	engine, err := stable.NewEngine(assets, stable.Limits{DailyCap: 1_000_000}, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	var counter int
	engine.WithClock(func() time.Time {
		ts := base.Add(time.Duration(counter) * time.Second)
		counter++
		return ts
	})
	engine.RecordPrice("ZNHB", "USD", 1.0, base)
	engine.SetPriceMaxAge(0)
	return engine
}

func signStableJWT(t *testing.T, now time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Issuer:    "rpc-tests",
		Audience:  jwt.ClaimStrings([]string{"unit-tests"}),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}
