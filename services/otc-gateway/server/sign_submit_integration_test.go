package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	nhbcore "nhbchain/core"
	nhbstate "nhbchain/core/state"
	nhbcrypto "nhbchain/crypto"
	gatewayauth "nhbchain/gateway/auth"
	swap "nhbchain/native/swap"
	"nhbchain/rpc"
	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/priceproofclient"
	"nhbchain/services/otc-gateway/swaprpc"
	"nhbchain/storage"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// integrationSigner signs digests with a real secp256k1 key, matching what
// the gateway's production HSM client produces (a genuine 65-byte
// recoverable signature), unlike stubSigner's fixed byte pattern used by
// the other tests in this file.
type integrationSigner struct {
	key *nhbcrypto.PrivateKey
}

func (s *integrationSigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	sig, err := ethcrypto.Sign(digest, s.key.PrivateKey)
	if err != nil {
		return nil, "", err
	}
	return sig, "CN=integration-test-signer", nil
}

// integrationPriceProofSource wraps a real priceproofclient.Client (the
// same client type otc-gateway's production main.go wires in) rather than a
// canned stub, so this test exercises the gateway's real HTTP call to a
// price-proof endpoint too. The upstream server is a plain httptest server
// returning a fixed, but genuinely signed, proof.
type integrationPriceProofSource struct {
	client *priceproofclient.Client
}

func (s *integrationPriceProofSource) PriceProof(ctx context.Context, pair string) (*swap.PriceProof, error) {
	return s.client.PriceProof(ctx, pair)
}

// newSignedPriceProofHandler stands in for swapd's real POST
// /v1/price-proof endpoint (services/swapd/server's own tests already cover
// that handler and its partner-auth middleware in depth). It returns a
// freshly-built, genuinely signed swap.PriceProof every call -- signed with
// a real secp256k1 key exactly as services/swapd/priceproof.Service does --
// so this test's chain-side verification (native/swap.PriceProofEngine,
// invoked inside applySwapVoucherMintTransaction) is exercised for real.
func newSignedPriceProofHandler(t *testing.T, key *nhbcrypto.PrivateKey, provider string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", "0.10", time.Now().UTC().Unix(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hash, err := proof.Hash()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sig, err := ethcrypto.Sign(hash, key.PrivateKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rate := ""
		if proof.Rate != nil {
			rate = proof.Rate.FloatString(18)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"domain":    proof.Domain,
			"provider":  proof.Provider,
			"pair":      proof.Base + "/" + proof.Quote,
			"rate":      rate,
			"timestamp": proof.Timestamp.UTC().Unix(),
			"signature": "0x" + hex.EncodeToString(sig),
		})
	})
}

// TestSignAndSubmitAgainstRealSwapSubmitVoucherHandler is gap 1's proof
// obligation: it drives services/otc-gateway/server.SignAndSubmit end to
// end against a REAL nhbchain/rpc.Server's swap_submitVoucher handler (not
// core's stubSwapClient), verifying the correctly-shaped
// swap.VoucherV1 + PriceProof payload this fix produces is actually
// accepted -- through AddTransaction, into the mempool, and successfully
// minted after CreateBlock/CommitBlock, with the recipient's on-chain ZNHB
// balance increasing accordingly. This is the exact request/response shape
// that failed with "voucher: domain required" before this fix (see
// core/node.go's SwapSubmitVoucher doc comment).
func TestSignAndSubmitAgainstRealSwapSubmitVoucherHandler(t *testing.T) {
	// --- Build a real chain node with ZNHB configured for minting. ---
	t.Setenv("NHB_ENV", "dev")
	chainDB := storage.NewMemDB()
	t.Cleanup(func() { chainDB.Close() })
	validatorKey, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := nhbcore.NewNode(chainDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	minterKey, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter key: %v", err)
	}
	var minterAddr [20]byte
	copy(minterAddr[:], minterKey.PubKey().Address().Bytes())
	if err := node.WithState(func(m *nhbstate.Manager) error {
		if err := m.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
			return err
		}
		return m.SetTokenMintAuthority("ZNHB", minterAddr[:])
	}); err != nil {
		t.Fatalf("configure ZNHB token: %v", err)
	}
	node.SetSwapConfig(swap.Config{
		AllowedFiat:        []string{"USD"},
		MaxQuoteAgeSeconds: 120,
		SlippageBps:        50,
		OraclePriority:     []string{"manual"},
	})

	oracleKey, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate oracle key: %v", err)
	}
	var oracleAddr [20]byte
	copy(oracleAddr[:], oracleKey.PubKey().Address().Bytes())
	// This is the governance-provisioned signer registration (gap 2a) --
	// written directly here for test isolation from the RPC-driven
	// governance lifecycle, which core/swap_price_signer_governance_test.go
	// covers end to end separately.
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.SwapSetPriceSigner("otc-gateway", oracleAddr)
	}); err != nil {
		t.Fatalf("register price signer: %v", err)
	}

	// --- Wire a real rpc.Server on top of the node. ---
	swapAPIKey := "otc-gateway-key"
	swapAPISecret := "otc-gateway-secret"
	nonceDir := t.TempDir()
	noncePersistence, err := gatewayauth.NewLevelDBNoncePersistence(filepath.Join(nonceDir, "nonces"))
	if err != nil {
		t.Fatalf("nonce persistence: %v", err)
	}
	t.Cleanup(func() { _ = noncePersistence.Close() })

	// rpc.NewServer resolves HSSecretEnv's value at construction time (it
	// builds the JWT verifier synchronously inside NewServer), so the env
	// var must be set BEFORE the call below, even though this test never
	// actually presents a JWT bearer token (only swapAuth's HMAC scheme).
	t.Setenv("OTC_INTEGRATION_TEST_JWT_SECRET", "unused-in-this-test")

	rpcServer, err := rpc.NewServer(node, nil, rpc.ServerConfig{
		JWT: rpc.JWTConfig{
			Enable:         true,
			Alg:            "HS256",
			HSSecretEnv:    "OTC_INTEGRATION_TEST_JWT_SECRET",
			Issuer:         "otc-integration-test",
			Audience:       []string{"otc-integration-test"},
			MaxSkewSeconds: 60,
		},
		SwapAuth: rpc.SwapAuthConfig{
			Secrets:              map[string]string{swapAPIKey: swapAPISecret},
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             2 * time.Minute,
			NonceCapacity:        1024,
			RateLimitWindow:      time.Minute,
			PartnerRateLimits:    map[string]int{swapAPIKey: 100},
			Persistence:          noncePersistence,
		},
	})
	if err != nil {
		t.Fatalf("new rpc server: %v", err)
	}

	rpcHTTPServer := httptest.NewServer(rpcServer)
	t.Cleanup(rpcHTTPServer.Close)

	swapClient, err := swaprpc.NewClient(swaprpc.Config{
		URL:               rpcHTTPServer.URL,
		Provider:          "otc-gateway",
		APIKey:            swapAPIKey,
		APISecret:         swapAPISecret,
		AllowedMethods:    []string{"swap_submitVoucher", "swap_voucher_get"},
		RequestsPerMinute: 100,
	})
	if err != nil {
		t.Fatalf("new swap client: %v", err)
	}

	// --- Wire a real swapd price-proof HTTP endpoint the gateway calls. ---
	priceProofHTTPServer := httptest.NewServer(newSignedPriceProofHandler(t, oracleKey, "otc-gateway"))
	t.Cleanup(priceProofHTTPServer.Close)
	priceProofRPCClient, err := priceproofclient.NewClient(priceproofclient.Config{
		URL:       priceProofHTTPServer.URL,
		APIKey:    "gateway-to-swapd-key",
		APISecret: "gateway-to-swapd-secret",
	})
	if err != nil {
		t.Fatalf("new price proof client: %v", err)
	}

	// --- Wire the otc-gateway server under test. ---
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: "Integration-Branch", Region: "US", RegionCap: 1_000_000, InvoiceLimit: 100_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	creator := uuid.New()
	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, creator, approver, 100)

	gatewaySrv := New(Config{
		DB:             db,
		TZ:             testTZ(),
		ChainID:        node.ChainID(),
		S3Bucket:       "bucket",
		Signer:         &integrationSigner{key: minterKey},
		SwapClient:     swapClient,
		PriceProof:     &integrationPriceProofSource{client: priceProofRPCClient},
		PriceProofPair: "ZNHB/USD",
		VoucherTTL:     time.Minute,
		Provider:       "otc-gateway",
		Authenticator:  newTestMiddleware(t, nil),
	})
	handler := gatewaySrv.Handler()

	recipientKey, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	var recipientAddr [20]byte
	copy(recipientAddr[:], recipientKey.PubKey().Address().Bytes())
	recipient := nhbcrypto.MustNewAddress(nhbcrypto.NHBPrefix, recipientAddr[:]).String()

	payload := map[string]string{
		"recipient": recipient,
		// 0.10 ZNHB/USD * 100.00 USD => 1000 ZNHB, matching the price proof
		// the stub swapd endpoint below signs.
		"amount":        "1000000000000000000000",
		"fiat_amount":   "100.00",
		"fiat_currency": "USD",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(t, uuid.New(), auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 from real swap_submitVoucher round trip, got %d body=%s", resp.Code, resp.Body.String())
	}

	// The submission only enqueues the transaction (minted=false is
	// expected -- see Node.SwapSubmitVoucher's doc comment); drive the
	// block lifecycle explicitly to prove it actually mints.
	pending := node.GetMempool()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending transaction in the mempool, got %d", len(pending))
	}
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	account, err := node.GetAccount(recipientAddr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() <= 0 {
		t.Fatalf("expected recipient ZNHB balance to increase, got %v", account.BalanceZNHB)
	}
}
