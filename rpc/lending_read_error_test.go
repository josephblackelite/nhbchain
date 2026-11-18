package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A lending read that the index of lending transactions could not answer yet is
// refused with HTTP 503 and a Retry-After hint, like a query that ran out of
// time; any other failure carries no hint.
func TestLendingReadErrorAsksToRetryOnlyWhenTheIndexIsBehind(t *testing.T) {
	const message = "lending index is catching up with the chain; retry shortly"
	rec := httptest.NewRecorder()
	writeLendingReadError(rec, 7, http.StatusServiceUnavailable, codeServerError, message, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatalf("a 503 from a lending read carries no Retry-After hint")
	}
	var resp RPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != codeServerError || resp.Error.Message != message {
		t.Fatalf("error = %+v", resp.Error)
	}

	rec = httptest.NewRecorder()
	writeLendingReadError(rec, 8, http.StatusInternalServerError, codeServerError, "state unavailable", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("a 500 from a lending read carries Retry-After %q", got)
	}
}
