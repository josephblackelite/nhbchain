//go:build posreadiness

package security

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/rpc"
	"nhbchain/storage"

	"nhooyr.io/websocket"
)

func RunTransportSuite(t *testing.T) {
	t.Helper()

	t.Run("PlaintextRejected", testPlaintextRejected)
	t.Run("TLSAndMTLSRequired", testTLSAndMTLSRequired)
	t.Run("HMACReplayBlocked", testHMACReplayBlocked)
}

func testPlaintextRejected(t *testing.T) {
	t.Helper()

	assets := generateTLSAssets(t)
	dir := t.TempDir()
	certPath := writeTLSFile(t, dir, "server.crt", assets.serverCertPEM)
	keyPath := writeTLSFile(t, dir, "server.key", assets.serverKeyPEM)

	srv := newRPCTestServer(t, rpc.ServerConfig{
		TLSCertFile: certPath,
		TLSKeyFile:  keyPath,
	})

	addr := srv.Addr()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial plaintext: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprintf(conn, "GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr); err != nil {
		t.Fatalf("write plaintext request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	reader := bufio.NewReader(conn)
	line, readErr := reader.ReadString('\n')
	if readErr != nil {
		if !errors.Is(readErr, net.ErrClosed) && !errors.Is(readErr, io.EOF) && !strings.Contains(readErr.Error(), "reset") && !strings.Contains(readErr.Error(), "closed") && !strings.Contains(readErr.Error(), "timeout") {
			t.Fatalf("unexpected plaintext read error: %v", readErr)
		}
		return
	}
	if !strings.Contains(line, " 400 ") {
		t.Fatalf("expected HTTP 400 response, got %q", strings.TrimSpace(line))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws/pos/finality", nil); err == nil {
		t.Fatalf("expected websocket plaintext dial to fail")
	}
}

func testTLSAndMTLSRequired(t *testing.T) {
	t.Helper()

	assets := generateTLSAssets(t)
	dir := t.TempDir()
	certPath := writeTLSFile(t, dir, "server.crt", assets.serverCertPEM)
	keyPath := writeTLSFile(t, dir, "server.key", assets.serverKeyPEM)
	caPath := writeTLSFile(t, dir, "ca.crt", assets.caCertPEM)

	srv := newRPCTestServer(t, rpc.ServerConfig{
		TLSCertFile:     certPath,
		TLSKeyFile:      keyPath,
		TLSClientCAFile: caPath,
	})

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(assets.caCertPEM) {
		t.Fatalf("failed to append CA cert")
	}

	insecureClient := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	insecureURL := "https://" + srv.Addr()
	if _, err := insecureClient.Post(insecureURL, "application/json", bytes.NewReader([]byte("{}"))); err == nil {
		t.Fatalf("expected mTLS handshake to fail without client certificate")
	}

	clientKeyPair, err := tls.X509KeyPair(assets.clientCertPEM, assets.clientKeyPEM)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	secureTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientKeyPair}}}
	secureClient := &http.Client{Timeout: time.Second, Transport: secureTransport}

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"nhb_getLatestBlocks","params":[]}`)
	req, err := http.NewRequest(http.MethodPost, insecureURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build https request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := secureClient.Do(req)
	if err != nil {
		t.Fatalf("mTLS request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected success status, got %d", resp.StatusCode)
	}

	wsClient := &http.Client{Timeout: time.Second, Transport: secureTransport}
	wsCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsCtx, "wss://"+srv.Addr()+"/ws/pos/finality", &websocket.DialOptions{HTTPClient: wsClient})
	if err != nil {
		t.Fatalf("mTLS websocket dial: %v", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "mtls verified")

	wsInsecureCtx, wsInsecureCancel := context.WithTimeout(context.Background(), time.Second)
	defer wsInsecureCancel()
	if _, _, err := websocket.Dial(wsInsecureCtx, "wss://"+srv.Addr()+"/ws/pos/finality", &websocket.DialOptions{HTTPClient: insecureClient}); err == nil {
		t.Fatalf("expected websocket dial without client cert to fail")
	}
}

func testHMACReplayBlocked(t *testing.T) {
	t.Helper()

	assets := generateTLSAssets(t)
	dir := t.TempDir()
	certPath := writeTLSFile(t, dir, "server.crt", assets.serverCertPEM)
	keyPath := writeTLSFile(t, dir, "server.key", assets.serverKeyPEM)
	srv := newRPCTestServer(t, rpc.ServerConfig{
		TLSCertFile: certPath,
		TLSKeyFile:  keyPath,
		SwapAuth: rpc.SwapAuthConfig{
			Secrets: map[string]string{"partner": "secret"},
		},
	})

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(assets.caCertPEM) {
		t.Fatalf("append CA cert: failed")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	client := &http.Client{Timeout: time.Second, Transport: transport}

	baseURL := "https://" + srv.Addr()
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "nonce-1"

	resp := sendSignedSwapRequest(t, client, baseURL, "partner", "secret", timestamp, nonce)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("initial signed request should be accepted, got status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	replay := sendSignedSwapRequest(t, client, baseURL, "partner", "secret", timestamp, nonce)
	if replay.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(replay.Body)
		replay.Body.Close()
		t.Fatalf("expected replay to be rejected with 401, got %d body=%s", replay.StatusCode, string(body))
	}
	replay.Body.Close()
}

func sendSignedSwapRequest(t *testing.T, client *http.Client, baseURL, apiKey, secret, timestamp, nonce string) *http.Response {
	t.Helper()

	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"swap_voucher_list","params":[0,0]}`)
	req, err := http.NewRequest(http.MethodPost, baseURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gatewayauth.HeaderAPIKey, apiKey)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)

	path := gatewayauth.CanonicalRequestPath(req)
	signature := gatewayauth.ComputeSignature(secret, timestamp, nonce, http.MethodPost, path, payload)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

type rpcTestServer struct {
	server   *rpc.Server
	listener net.Listener
	done     chan error
	addr     string
	db       storage.Database
	node     *core.Node
}

func newRPCTestServer(t *testing.T, cfg rpc.ServerConfig) *rpcTestServer {
	t.Helper()

	db := storage.NewMemDB()
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		db.Close()
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		db.Close()
		t.Fatalf("new node: %v", err)
	}

	if !cfg.JWT.Enable && strings.TrimSpace(cfg.TLSClientCAFile) == "" {
		const jwtEnv = "POSREADINESS_RPC_JWT_SECRET"
		if err := os.Setenv(jwtEnv, "posreadiness-secret"); err != nil {
			db.Close()
			t.Fatalf("set jwt secret: %v", err)
		}
		cfg.JWT = rpc.JWTConfig{
			Enable:      true,
			Alg:         "HS256",
			HSSecretEnv: jwtEnv,
			Issuer:      "posreadiness",
			Audience:    []string{"transport-suite"},
		}
	}

	if len(cfg.SwapAuth.Secrets) > 0 && cfg.SwapAuth.Persistence == nil {
		store, err := gatewayauth.NewLevelDBNoncePersistence(filepath.Join(t.TempDir(), "swap-nonces"))
		if err != nil {
			db.Close()
			t.Fatalf("new swap persistence: %v", err)
		}
		cfg.SwapAuth.Persistence = store
		t.Cleanup(func() { _ = store.Close() })
	}

	server, err := rpc.NewServer(node, nil, cfg)
	if err != nil {
		db.Close()
		t.Fatalf("new rpc server: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		db.Close()
		t.Fatalf("listen: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	if err := waitForTCPReady(listener.Addr().String(), time.Second); err != nil {
		listener.Close()
		db.Close()
		t.Fatalf("wait for server: %v", err)
	}

	srv := &rpcTestServer{server: server, listener: listener, done: done, addr: listener.Addr().String(), db: db, node: node}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Fatalf("shutdown rpc server: %v", err)
		}
	})
	return srv
}

func (s *rpcTestServer) Addr() string {
	return s.addr
}

func (s *rpcTestServer) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
	_ = s.listener.Close()

	select {
	case err := <-s.done:
		if err != nil && err != http.ErrServerClosed && !strings.Contains(err.Error(), "use of closed") {
			return err
		}
	case <-time.After(time.Second):
		return fmt.Errorf("server did not shut down in time")
	}
	if s.db != nil {
		s.db.Close()
	}
	return nil
}

func waitForTCPReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type tlsAssets struct {
	caCertPEM     []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCertPEM []byte
	clientKeyPEM  []byte
}

func generateTLSAssets(t *testing.T) tlsAssets {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "nhb-pos-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "nhb-pos-server"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "nhb-pos-client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}

	caPEM := pemEncodeBlock("CERTIFICATE", caDER)
	serverCertPEM := pemEncodeBlock("CERTIFICATE", serverDER)
	serverKeyPEM := pemEncodeECPrivateKey(t, serverKey)
	clientCertPEM := pemEncodeBlock("CERTIFICATE", clientDER)
	clientKeyPEM := pemEncodeECPrivateKey(t, clientKey)

	return tlsAssets{caCertPEM: caPEM, serverCertPEM: serverCertPEM, serverKeyPEM: serverKeyPEM, clientCertPEM: clientCertPEM, clientKeyPEM: clientKeyPEM}
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	return serial
}

func pemEncodeBlock(typ string, der []byte) []byte {
	var buf bytes.Buffer
	_ = pem.Encode(&buf, &pem.Block{Type: typ, Bytes: der})
	return buf.Bytes()
}

func pemEncodeECPrivateKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return pemEncodeBlock("PRIVATE KEY", der)
}

func writeTLSFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
