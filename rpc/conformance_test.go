package rpc

// Small conformance points of the JSON-RPC surface: which ids a request may
// carry and how they are echoed, how nhb_getValidatorInfo reads an address, and
// what the p2p sentinel errors of net_dial and net_ban come out as. They go
// through the real dispatch.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

// sendRaw posts a raw JSON body and returns the status and the raw id of the
// response.
func sendRaw(t *testing.T, srv *Server, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var resp struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, string(resp.ID)
}

func TestRequestIDIsEchoedAsSent(t *testing.T) {
	// A public read that answers without a node behind the server.
	srv := newTestServer(t, nil, nil, ServerConfig{MaxTxPerWindow: 1000, MaxTxPerIP: 1000})
	call := func(id string) string {
		return `{"jsonrpc":"2.0","method":"nhb_getNetworkStats","params":[]` + id + `}`
	}
	cases := []struct {
		name string
		id   string // the "id" member as sent, with its leading comma
		want string // the id echoed
	}{
		{"an integer", `,"id":7`, `7`},
		{"zero", `,"id":0`, `0`},
		{"a string", `,"id":"abc-1"`, `"abc-1"`},
		{"a numeric string", `,"id":"42"`, `"42"`},
		{"a fraction", `,"id":1.5`, `1.5`},
		{"the largest int", `,"id":9223372036854775807`, `9223372036854775807`},
		{"beyond int", `,"id":18446744073709551615`, `18446744073709551615`},
		{"none, answered as 0 always has been", ``, `0`},
		{"null, answered as 0 always has been", `,"id":null`, `0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, got := sendRaw(t, srv, call(tc.id))
			if code != http.StatusOK {
				t.Fatalf("HTTP %d, want 200", code)
			}
			if got != tc.want {
				t.Fatalf("id echoed as %s, want %s", got, tc.want)
			}
		})
	}
	for _, bad := range []string{`,"id":{}`, `,"id":[1]`, `,"id":true`} {
		if code, _ := sendRaw(t, srv, call(bad)); code != http.StatusBadRequest {
			t.Fatalf("id %s: HTTP %d, want 400", bad, code)
		}
	}
}

func TestValidatorInfoReadsBech32AndHexAddresses(t *testing.T) {
	env := newTestEnv(t)
	var raw [20]byte
	for i := range raw {
		raw[i] = byte(0xA0 + i)
	}
	bech := crypto.MustNewAddress(crypto.NHBPrefix, raw[:]).String()
	want := strings.ToLower(common.BytesToAddress(raw[:]).Hex())

	address := func(param string) (int, string) {
		rec, rpcErr := serveBearer(t, env.server, "", "nhb_getValidatorInfo", param)
		if rpcErr != nil {
			return rec.Code, ""
		}
		result, _ := decodeRPCResponse(t, rec)
		var out struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatalf("decode %s: %v", result, err)
		}
		return rec.Code, strings.ToLower(out.Address)
	}

	for _, param := range []string{bech, strings.ToUpper(bech), common.BytesToAddress(raw[:]).Hex()} {
		if code, got := address(param); code != http.StatusOK || got != want {
			t.Fatalf("address %q: HTTP %d, answered for %q, want %q", param, code, got, want)
		}
	}
	// A bech32 address that does not decode is refused, not read as hex.
	bad := bech[:len(bech)-2] + "qq"
	if code, got := address(bad); code != http.StatusBadRequest {
		t.Fatalf("a bech32 address with a bad checksum: HTTP %d (%q), want 400", code, got)
	}
}

// errNetwork is a network whose dial and ban fail with the errors set.
type errNetwork struct {
	stubNetwork
	dialErr, banErr error
}

func (n *errNetwork) Dial(context.Context, string) error { return n.dialErr }

func (n *errNetwork) Ban(context.Context, string, time.Duration) error { return n.banErr }

func TestNetErrorsMapToTheirTypedResponses(t *testing.T) {
	env := newTestEnv(t)
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   int
	}{
		{"an unknown peer", fmt.Errorf("%w: 0xabc", p2p.ErrPeerUnknown), http.StatusNotFound, codeNetUnknownPeer},
		{"a banned peer", fmt.Errorf("%w: banned until later", p2p.ErrPeerBanned), http.StatusConflict, codeNetPeerBanned},
		{"an empty target", fmt.Errorf("%w", p2p.ErrDialTargetEmpty), http.StatusBadRequest, codeNetInvalidParams},
		{"a bad address", fmt.Errorf("%w: missing port", p2p.ErrInvalidAddress), http.StatusBadRequest, codeNetInvalidParams},
		{"anything else", errors.New("disk on fire"), http.StatusInternalServerError, codeServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			net := &errNetwork{dialErr: tc.err, banErr: tc.err}
			srv := bearerOnlyServer(t, env, net, ServerConfig{})
			for _, call := range []struct {
				method string
				params any
			}{
				{"net_dial", map[string]string{"target": "203.0.113.5:6001"}},
				{"net_ban", map[string]any{"nodeId": "0xabc", "secs": 60}},
			} {
				rec, rpcErr := serveBearer(t, srv, env.token, call.method, call.params)
				if rec.Code != tc.wantStatus || rpcErr == nil || rpcErr.Code != tc.wantCode {
					t.Fatalf("%s: HTTP %d, error %+v; want HTTP %d, code %d", call.method, rec.Code, rpcErr, tc.wantStatus, tc.wantCode)
				}
			}
		})
	}
}
