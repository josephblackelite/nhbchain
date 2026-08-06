package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePotsoHeartbeatDisabled(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoHeartbeat(recorder, env.newRequest(), req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: got %d want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected rpc error")
	}
	if rpcErr.Message == "" {
		t.Fatalf("expected rpc error message")
	}
}
