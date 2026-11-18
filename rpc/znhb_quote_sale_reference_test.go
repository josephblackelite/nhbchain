package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/core"
)

// PL-BC-D: znhb_quoteBuy. With the sale-follows-market rule on and no fresh outside reference price the sale is
// paused, and the quote says so (HTTP 503) instead of quoting the curve; with the rule off it quotes the curve
// as it always did.

func quoteBuyRequest(t *testing.T, amount string) *http.Request {
	t.Helper()
	body := buildRPCRequestBody(t, 5, "znhb_quoteBuy", []json.RawMessage{marshalParam(t, amount)})
	return httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
}

func TestZNHBQuoteBuyAnswersTheCurveWhileTheRuleIsOff(t *testing.T) {
	env := newTestEnv(t)
	env.configureAdminWallet(t)
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, quoteBuyRequest(t, "1000000000000000000"))
	if rec.Code != http.StatusOK {
		t.Fatalf("quote with the rule off: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error %+v", rpcErr)
	}
	if !strings.Contains(string(result), "nhbCostWei") {
		t.Fatalf("no quote in %s", result)
	}
}

func TestZNHBQuoteBuyReportsPausedSalesWhenTheRuleIsOnWithNoReference(t *testing.T) {
	env := newTestEnv(t)
	env.configureAdminWallet(t)
	env.node.SetValidatorRules(core.ValidatorRules{SaleReferenceActivationHeight: 1, SaleReferenceMaxAgeSeconds: 3600})
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, quoteBuyRequest(t, "1000000000000000000"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("quote with the rule on and no reference: HTTP %d, want 503: %s", rec.Code, rec.Body.String())
	}
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "sales are paused") {
		t.Fatalf("rpc error %+v, want a message saying sales are paused", rpcErr)
	}
}
