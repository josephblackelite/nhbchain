package main

// Ledger PL-EXT-07: with ESCROW_GATEWAY_ENVELOPE_CHAIN_ID set the gateway builds the
// create and action envelopes with the network's chain id as their last key, so what
// a participant signs names the network. Unset, the signed bytes are the ones they
// always were.

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbcrypto "nhbchain/crypto"
)

const testEnvelopeChainID = "99070"

func signRawEnvelope(t *testing.T, priv *ecdsa.PrivateKey, payload []byte) string {
	t.Helper()
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256Hash(payload).Bytes(), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return hex.EncodeToString(sig)
}

// signedCreateBytes is the create envelope the gateway builds for req, with chainID as
// its last key when it is not empty.
func signedCreateBytes(t *testing.T, payerBech, payeeBech string, req EscrowCreateRequest, chainID string) []byte {
	t.Helper()
	payer, err := nhbcrypto.DecodeAddress(payerBech)
	if err != nil {
		t.Fatalf("decode payer: %v", err)
	}
	payee, err := nhbcrypto.DecodeAddress(payeeBech)
	if err != nil {
		t.Fatalf("decode payee: %v", err)
	}
	out, err := json.Marshal(escrowCreateEnvelopeWire{
		Action: "create", Payer: hex.EncodeToString(payer.Bytes()), Payee: hex.EncodeToString(payee.Bytes()),
		Token: req.Token, Amount: req.Amount, FeeBps: req.FeeBps, Deadline: req.Deadline, Nonce: req.Nonce,
		Meta: req.Meta, Realm: req.Realm, ChainID: chainID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestSetEnvelopeChainIDValidation(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()

	for _, ok := range []string{"", "1", "99070", " 99070 ", "18446744073709551615"} {
		if err := server.SetEnvelopeChainID(ok); err != nil {
			t.Fatalf("%q was refused: %v", ok, err)
		}
		if want := strings.TrimSpace(ok); server.envelopeChainID != want {
			t.Fatalf("%q stored as %q, want %q", ok, server.envelopeChainID, want)
		}
	}
	server.envelopeChainID = "kept"
	for _, bad := range []string{"0", "099070", "00", "-1", "+1", "1e5", "0x1", "abc", "12 34", "18446744073709551616"} {
		if err := server.SetEnvelopeChainID(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if server.envelopeChainID != "kept" {
			t.Fatalf("a refused id changed the setting: %q", server.envelopeChainID)
		}
	}
}

func TestEnvelopeChainIDIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("ESCROW_GATEWAY_NODE_URL", "http://127.0.0.1:1")
	t.Setenv("ESCROW_GATEWAY_RELAYER_KMS_ENV", "TEST_RELAYER_KEY_VAR")
	t.Setenv("ESCROW_GATEWAY_API_KEYS", `[{"key":"test","secret":"secret"}]`)

	t.Setenv("ESCROW_GATEWAY_ENVELOPE_CHAIN_ID", "")
	cfg, err := LoadConfigFromEnv()
	if err != nil || cfg.EnvelopeChainID != "" {
		t.Fatalf("unset: %q, %v", cfg.EnvelopeChainID, err)
	}
	t.Setenv("ESCROW_GATEWAY_ENVELOPE_CHAIN_ID", " "+testEnvelopeChainID+" ")
	if cfg, err = LoadConfigFromEnv(); err != nil || cfg.EnvelopeChainID != testEnvelopeChainID {
		t.Fatalf("set: %q, %v", cfg.EnvelopeChainID, err)
	}
	for _, bad := range []string{"0", "099070", "abc", "-5"} {
		t.Setenv("ESCROW_GATEWAY_ENVELOPE_CHAIN_ID", bad)
		if _, err := LoadConfigFromEnv(); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

// postRelease sends a release for the payee wallet with wallet signature walletSig.
func postRelease(t *testing.T, server *Server, addr, walletSig, nonceTag string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"escrowId":"0xdeadbeef"}`)
	ts := time.Unix(1700000000, 0).UTC()
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/release", body, ts, "nonce-"+nonceTag)
	req := httptest.NewRequest(http.MethodPost, "/escrow/release", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "idem-"+nonceTag)
	req.Header.Set(headerWalletAddress, addr)
	req.Header.Set(headerWalletSig, walletSig)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func TestEscrowActionEnvelopeCarriesTheChainID(t *testing.T) {
	priv, addr := newWallet(t)
	oldBytes := []byte(`{"escrowId":"0xdeadbeef","action":"release"}`)
	newBytes := []byte(`{"escrowId":"0xdeadbeef","action":"release","chainId":"` + testEnvelopeChainID + `"}`)

	for _, tc := range []struct {
		name      string
		chainID   string
		signed    []byte
		wantCode  int
		forwarded []byte
	}{
		{"unset, the old bytes", "", oldBytes, http.StatusAccepted, oldBytes},
		{"unset, bytes with a chain id", "", newBytes, http.StatusForbidden, nil},
		{"set, the bytes with the chain id", testEnvelopeChainID, newBytes, http.StatusAccepted, newBytes},
		{"set, the old bytes", testEnvelopeChainID, oldBytes, http.StatusForbidden, nil},
		{"set, another network's id", testEnvelopeChainID, []byte(`{"escrowId":"0xdeadbeef","action":"release","chainId":"1337"}`), http.StatusForbidden, nil},
	} {
		func() {
			node := &mockNodeClient{getResp: &EscrowState{ID: "0xdeadbeef", Payer: "nhb1payer", Payee: addr, Status: "funded"}}
			server, store, _ := newTestServer(t, node, nil)
			defer store.Close()
			if err := server.SetEnvelopeChainID(tc.chainID); err != nil {
				t.Errorf("%s: %v", tc.name, err)
				return
			}
			rec := postRelease(t, server, addr, signRawEnvelope(t, priv, tc.signed), "env")
			if rec.Code != tc.wantCode {
				t.Errorf("%s: HTTP %d (%s), want %d", tc.name, rec.Code, rec.Body.String(), tc.wantCode)
				return
			}
			if tc.forwarded == nil {
				if node.releaseCalls != 0 {
					t.Errorf("%s: a refused release reached the node", tc.name)
				}
				return
			}
			if !bytes.Equal(node.lastReleasePayload, tc.forwarded) {
				t.Errorf("%s: forwarded %s, want %s", tc.name, node.lastReleasePayload, tc.forwarded)
			}
		}()
	}
}

func TestEscrowCreateEnvelopeCarriesTheChainID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chainID string
	}{{"unset", ""}, {"set", testEnvelopeChainID}} {
		func() {
			payerPriv, payerAddr := newWallet(t)
			_, payeeAddr := newWallet(t)
			node := &mockNodeClient{createResp: &EscrowCreateResponse{ID: "0xabc"}}
			server, store, _ := newTestServer(t, node, nil)
			defer store.Close()
			if err := server.SetEnvelopeChainID(tc.chainID); err != nil {
				t.Errorf("%s: %v", tc.name, err)
				return
			}
			payload := EscrowCreateRequest{Payer: payerAddr, Payee: payeeAddr, Token: "NHB", Amount: "10", Deadline: 1700000500, Nonce: 1}

			calls := 0
			post := func(walletSig, tag string) *httptest.ResponseRecorder {
				calls++ // the gateway wants each request's timestamp later than the last
				body, _ := json.Marshal(payload)
				ts := time.Unix(1700000000, 0).UTC().Add(time.Duration(calls) * time.Second)
				timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, ts, "nonce-"+tag)
				req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
				req.Header.Set(headerAPIKey, "test")
				req.Header.Set(headerTimestamp, timestamp)
				req.Header.Set(headerNonce, nonce)
				req.Header.Set(headerSignature, sig)
				req.Header.Set(headerIdempotencyKey, "idem-"+tag)
				req.Header.Set(headerWalletAddress, payerAddr)
				req.Header.Set(headerWalletSig, walletSig)
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)
				return rec
			}

			// The helper signs the envelope with no chain id: the old bytes.
			oldSig := signEscrowCreateEnvelope(t, payerPriv, payload)
			if tc.chainID == "" {
				if rec := post(oldSig, "old"); rec.Code != http.StatusCreated {
					t.Errorf("%s: old bytes: HTTP %d (%s)", tc.name, rec.Code, rec.Body.String())
					return
				}
				if bytes.Contains(node.lastCreatePayload, []byte("chainId")) {
					t.Errorf("%s: the forwarded create carries a chain id: %s", tc.name, node.lastCreatePayload)
				}
				return
			}
			if rec := post(oldSig, "old"); rec.Code != http.StatusForbidden || node.createCalls != 0 {
				t.Errorf("%s: the old bytes: HTTP %d, node calls %d, want 403 and none", tc.name, rec.Code, node.createCalls)
				return
			}
			// Sign the bytes the gateway now builds: the old envelope plus the last key.
			oldPayload := signedCreateBytes(t, payerAddr, payeeAddr, payload, "")
			newPayload := signedCreateBytes(t, payerAddr, payeeAddr, payload, tc.chainID)
			if !strings.HasSuffix(string(newPayload), `,"chainId":"`+tc.chainID+`"}`) || len(newPayload) <= len(oldPayload) {
				t.Errorf("%s: unexpected envelope %s", tc.name, newPayload)
				return
			}
			if rec := post(signRawEnvelope(t, payerPriv, newPayload), "new"); rec.Code != http.StatusCreated {
				t.Errorf("%s: new bytes: HTTP %d (%s)", tc.name, rec.Code, rec.Body.String())
				return
			}
			if !bytes.Equal(node.lastCreatePayload, newPayload) {
				t.Errorf("%s: forwarded %s, want %s", tc.name, node.lastCreatePayload, newPayload)
			}
		}()
	}
}
