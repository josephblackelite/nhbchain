package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

type validatorSetPageResult struct {
	Validators []struct {
		Address string `json:"address"`
		Stake   string `json:"stake"`
	} `json:"validators"`
	TotalCount int  `json:"totalCount"`
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	HasMore    bool `json:"hasMore"`
}

// buildValidatorSetTestNode seeds a node with `count` validators, each with a
// distinct, deterministic address and a distinct stake so ordering is
// verifiable, without needing to run any real block/epoch lifecycle --
// ReplaceValidatorSet writes directly to the in-memory state
// Node.GetValidatorSet reads from.
func buildValidatorSetTestNode(t *testing.T, count int) *core.Node {
	t.Helper()
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

	set := make(map[string]*big.Int, count)
	for i := 0; i < count; i++ {
		var addr [20]byte
		addr[19] = byte(i)
		set[string(addr[:])] = big.NewInt(int64(1000 + i))
	}
	if err := node.ReplaceValidatorSet(set); err != nil {
		t.Fatalf("replace validator set: %v", err)
	}
	return node
}

func fetchValidatorSetPage(t *testing.T, server *Server, offset, limit *int) validatorSetPageResult {
	t.Helper()
	var params []json.RawMessage
	if offset != nil {
		raw, err := json.Marshal(*offset)
		if err != nil {
			t.Fatalf("marshal offset: %v", err)
		}
		params = append(params, raw)
	}
	if limit != nil {
		raw, err := json.Marshal(*limit)
		if err != nil {
			t.Fatalf("marshal limit: %v", err)
		}
		params = append(params, raw)
	}
	req := &RPCRequest{ID: 1, Params: params}
	recorder := httptest.NewRecorder()
	server.handleGetValidatorSet(recorder, httptest.NewRequest(http.MethodPost, "/", nil), req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp RPCResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var page validatorSetPageResult
	if err := json.Unmarshal(resultBytes, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return page
}

// TestGetValidatorSetNoParamsReturnsEverythingForASmallSet confirms the
// existing zero-arg calling convention (every caller before this feature
// added pagination) still returns the complete validator set unchanged for
// any set smaller than validatorSetDefaultLimit -- true of this network
// today (a handful of validators) -- so this is a non-breaking addition in
// practice, not just in theory.
func TestGetValidatorSetNoParamsReturnsEverythingForASmallSet(t *testing.T) {
	node := buildValidatorSetTestNode(t, 5)
	server := newTestServer(t, node, nil, ServerConfig{})

	page := fetchValidatorSetPage(t, server, nil, nil)
	if page.TotalCount != 5 || len(page.Validators) != 5 {
		t.Fatalf("expected all 5 validators with no params, got totalCount=%d len=%d", page.TotalCount, len(page.Validators))
	}
	if page.HasMore {
		t.Fatalf("expected hasMore false when every validator fit in one page")
	}
}

// TestGetValidatorSetPaginatesAndStaysStableAcrossPages is the "thousands of
// validators" scenario the pagination contract exists for: with a limit
// smaller than the total set, repeated calls advancing offset by limit must
// walk the FULL set exactly once each (no duplicates, no gaps) -- which
// requires the underlying map (inherently unordered in Go) to be sorted
// before slicing, not just chunked in whatever order range happens to
// produce.
func TestGetValidatorSetPaginatesAndStaysStableAcrossPages(t *testing.T) {
	const total = 23
	node := buildValidatorSetTestNode(t, total)
	server := newTestServer(t, node, nil, ServerConfig{})

	limit := 7
	seen := make(map[string]bool, total)
	offset := 0
	pages := 0
	for {
		off := offset
		lim := limit
		page := fetchValidatorSetPage(t, server, &off, &lim)
		if page.TotalCount != total {
			t.Fatalf("expected totalCount %d on every page, got %d at offset %d", total, page.TotalCount, offset)
		}
		for _, v := range page.Validators {
			if seen[v.Address] {
				t.Fatalf("validator %s returned on more than one page", v.Address)
			}
			seen[v.Address] = true
		}
		pages++
		if pages > total {
			t.Fatalf("paginated more times than there are validators -- pagination is not converging")
		}
		if !page.HasMore {
			break
		}
		offset += len(page.Validators)
	}
	if len(seen) != total {
		t.Fatalf("expected to see all %d validators across pages, saw %d", total, len(seen))
	}
}

// TestGetValidatorSetLimitIsCapped confirms an oversized requested limit is
// clamped to validatorSetMaxLimit rather than honored verbatim -- the actual
// protection against "thousands of validators" turning one request into an
// unbounded response.
func TestGetValidatorSetLimitIsCapped(t *testing.T) {
	node := buildValidatorSetTestNode(t, 3)
	server := newTestServer(t, node, nil, ServerConfig{})

	offset := 0
	hugeLimit := validatorSetMaxLimit * 10
	page := fetchValidatorSetPage(t, server, &offset, &hugeLimit)
	if page.Limit != validatorSetMaxLimit {
		t.Fatalf("expected limit clamped to %d, got %d", validatorSetMaxLimit, page.Limit)
	}
}
