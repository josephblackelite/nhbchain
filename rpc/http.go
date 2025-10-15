package rpc

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	"nhbchain/core/epoch"
	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/observability"
	"nhbchain/p2p"
	posv1 "nhbchain/proto/pos"
	"nhbchain/rpc/modules"
	"nhbchain/services/swapd/stable"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jsonRPCVersion          = "2.0"
	maxRequestBytes         = 1 << 20 // 1 MiB
	rateLimitWindow         = time.Minute
	maxTxPerWindow          = 5
	txSeenTTL               = 15 * time.Minute
	rateLimiterMaxEntries   = 512
	rateLimiterStaleAfter   = 10 * rateLimitWindow
	rateLimiterSweepBackoff = rateLimitWindow
	maxForwardedForAddrs    = 5
	maxTrustedProxyEntries  = 32

	swapMaxTimestampSkew  = 2 * time.Minute
	swapDefaultTimestamp  = swapMaxTimestampSkew
	swapMaxNonceTTL       = 10 * time.Minute
	swapDefaultNonceTTL   = swapMaxNonceTTL
	swapDefaultNonceCache = 4096
	swapMaxNonceCache     = 65536
)

const (
	codeParseError              = -32700
	codeInvalidRequest          = -32600
	codeMethodNotFound          = -32601
	codeInvalidParams           = -32602
	codeUnauthorized            = -32001
	codeServerError             = -32000
	codeDuplicateTx             = -32010
	codeRateLimited             = -32020
	codeMempoolFull             = -32030
	codeInvalidPolicyInvariants = -32040
	codeModulePaused            = -32050
)

type rateLimiter struct {
	count       int
	windowStart time.Time
	lastSeen    time.Time
}

type txSeenEntry struct {
	hash   string
	seenAt time.Time
}

// SwapAuthConfig configures API key authentication and per-partner quotas for swap requests.
type SwapAuthConfig struct {
	Secrets              map[string]string
	AllowedTimestampSkew time.Duration
	NonceTTL             time.Duration
	NonceCapacity        int
	RateLimitWindow      time.Duration
	PartnerRateLimits    map[string]int
	Now                  func() time.Time
}

// ProxyHeaderMode defines how the server treats reverse proxy headers that can
// influence client IP resolution.
type ProxyHeaderMode string

const (
	// ProxyHeaderModeIgnore instructs the server to reject requests that
	// attempt to supply the corresponding header.
	ProxyHeaderModeIgnore ProxyHeaderMode = "ignore"
	// ProxyHeaderModeSingle trusts the header only when a single client
	// address is provided.
	ProxyHeaderModeSingle ProxyHeaderMode = "single"
)

// ProxyHeadersConfig captures header handling policies for reverse proxy
// metadata that can influence client attribution.
type ProxyHeadersConfig struct {
	XForwardedFor ProxyHeaderMode
	XRealIP       ProxyHeaderMode
}

// JWTConfig configures bearer token validation for RPC requests.
type JWTConfig struct {
	Enable           bool
	Alg              string
	HSSecretEnv      string
	RSAPublicKeyFile string
	Issuer           string
	Audience         []string
	MaxSkewSeconds   int64
}

// ServerConfig controls optional behaviours of the RPC server.
type ServerConfig struct {
	// TrustProxyHeaders, when set, will cause the server to honour proxy
	// forwarding headers such as X-Forwarded-For regardless of the caller's
	// remote address. Use with caution when the server is guaranteed to be
	// behind a trusted reverse proxy.
	TrustProxyHeaders bool
	// TrustedProxies enumerates remote addresses that are authorised to relay
	// client requests. When a request originates from one of these proxies the
	// server will honour X-Forwarded-For headers.
	TrustedProxies []string
	// AllowlistCIDRs enumerates client IP ranges permitted to access the RPC
	// server. When empty, all clients are allowed.
	AllowlistCIDRs []string
	// ProxyHeaders configures handling of reverse proxy headers such as
	// X-Forwarded-For and X-Real-IP.
	ProxyHeaders ProxyHeadersConfig
	// JWT configures bearer token authentication for RPC requests.
	JWT JWTConfig
	// ReadHeaderTimeout specifies how long the server waits for headers.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds the duration permitted to read the full request.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long a handler may take to write a response.
	WriteTimeout time.Duration
	// IdleTimeout defines how long to keep idle connections open.
	IdleTimeout time.Duration
	// TLSCertFile is the path to a PEM-encoded certificate chain.
	TLSCertFile string
	// TLSKeyFile is the path to the PEM-encoded private key for TLSCertFile.
	TLSKeyFile string
	// TLSClientCAFile enables mutual TLS by providing the path to a PEM-encoded
	// certificate authority bundle used to verify client certificates.
	TLSClientCAFile string
	// AllowInsecure permits plaintext HTTP when running on loopback interfaces.
	// This should only be enabled for local development.
	AllowInsecure bool
	// AllowInsecureUnspecified treats unspecified listener addresses (0.0.0.0 or
	// ::) as loopback when AllowInsecure is enabled. This override exists solely
	// for tightly controlled lab setups such as container port-forwarding.
	AllowInsecureUnspecified bool
	// SwapAuth configures API authentication and rate limiting for swap RPC methods.
	SwapAuth SwapAuthConfig
}

// NetworkService abstracts the network control plane used by RPC handlers to
// interrogate the peer-to-peer daemon.
type NetworkService interface {
	NetworkView(ctx context.Context) (p2p.NetworkView, []string, error)
	NetworkPeers(ctx context.Context) ([]p2p.PeerNetInfo, error)
	Dial(ctx context.Context, target string) error
	Ban(ctx context.Context, nodeID string, duration time.Duration) error
}

type Server struct {
	node *core.Node
	net  NetworkService

	mu                       sync.Mutex
	txSeen                   map[string]time.Time
	txSeenQueue              []txSeenEntry
	rateLimiters             map[string]*rateLimiter
	rateLimiterSweep         time.Time
	potsoEvidence            *modules.PotsoEvidenceModule
	transactions             *modules.TransactionsModule
	escrow                   *modules.EscrowModule
	lending                  *modules.LendingModule
	trustProxyHeaders        bool
	trustedProxies           map[string]struct{}
	readHeaderTimeout        time.Duration
	readTimeout              time.Duration
	writeTimeout             time.Duration
	idleTimeout              time.Duration
	tlsCertFile              string
	tlsKeyFile               string
	clientCAFile             string
	requireClientCert        bool
	allowInsecure            bool
	allowInsecureUnspecified bool
	proxyPolicy              proxyPolicy
	allowlist                []*net.IPNet
	jwtVerifier              *jwtVerifier
	jwtVerifierErr           error

	swapAuth          *gatewayauth.Authenticator
	swapPartnerLimits map[string]int
	swapRateWindow    time.Duration
	swapRateCounters  map[string]*rateLimiter
	swapRateMu        sync.Mutex
	swapNowFn         func() time.Time

	swapStableMu sync.RWMutex
	swapStable   struct {
		engine *stable.Engine
		limits stable.Limits
		assets map[string]stable.Asset
		now    func() time.Time
	}

	callerNonceMu sync.Mutex
	callerNonces  map[string]callerNonceState

	serverMu    sync.Mutex
	httpServer  *http.Server
	grpcServer  *grpc.Server
	posRealtime *FinalityStream
}

type proxyPolicy struct {
	xForwardedFor ProxyHeaderMode
	xRealIP       ProxyHeaderMode
}

type jwtVerifier struct {
	method   jwt.SigningMethod
	key      interface{}
	issuer   string
	audience []string
	leeway   time.Duration
	now      func() time.Time
}

type contextKey string

const clientIPContextKey contextKey = "rpc_client_ip"
const clientIdentityContextKey contextKey = "rpc_client_identity"

func normalizeProxyMode(mode ProxyHeaderMode) ProxyHeaderMode {
	switch strings.ToLower(string(mode)) {
	case "", string(ProxyHeaderModeIgnore):
		return ProxyHeaderModeIgnore
	case string(ProxyHeaderModeSingle):
		return ProxyHeaderModeSingle
	default:
		return ProxyHeaderModeIgnore
	}
}

func NewServer(node *core.Node, netClient NetworkService, cfg ServerConfig) (*Server, error) {
	trusted := make(map[string]struct{}, len(cfg.TrustedProxies))
	count := 0
	for _, entry := range cfg.TrustedProxies {
		if count >= maxTrustedProxyEntries {
			break
		}
		trimmed := canonicalHost(entry)
		if trimmed == "" {
			continue
		}
		trusted[trimmed] = struct{}{}
		count++
	}
	policy := proxyPolicy{
		xForwardedFor: normalizeProxyMode(cfg.ProxyHeaders.XForwardedFor),
		xRealIP:       normalizeProxyMode(cfg.ProxyHeaders.XRealIP),
	}
	allowlist := make([]*net.IPNet, 0, len(cfg.AllowlistCIDRs))
	for _, entry := range cfg.AllowlistCIDRs {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "/") {
			if _, network, err := net.ParseCIDR(trimmed); err == nil {
				allowlist = append(allowlist, network)
			}
			continue
		}
		ip := net.ParseIP(trimmed)
		if ip == nil {
			continue
		}
		bits := 128
		if v4 := ip.To4(); v4 != nil {
			ip = v4
			bits = 32
		}
		allowlist = append(allowlist, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	var jwtVerifier *jwtVerifier
	var jwtErr error
	clientCAPath := strings.TrimSpace(cfg.TLSClientCAFile)
	requireClientCert := clientCAPath != ""
	if cfg.JWT.Enable {
		jwtVerifier, jwtErr = newJWTVerifier(cfg.JWT)
	} else if !requireClientCert {
		return nil, fmt.Errorf("JWT authentication must be enabled unless mutual TLS is configured")
	}
	var swapAuth *gatewayauth.Authenticator
	swapLimits := make(map[string]int)
	swapWindow := cfg.SwapAuth.RateLimitWindow
	swapNow := cfg.SwapAuth.Now
	if swapNow == nil {
		swapNow = time.Now
	}
	if swapWindow <= 0 {
		swapWindow = time.Minute
	}
	if len(cfg.SwapAuth.Secrets) > 0 {
		secrets := make(map[string]string, len(cfg.SwapAuth.Secrets))
		for key, secret := range cfg.SwapAuth.Secrets {
			trimmedKey := strings.TrimSpace(key)
			trimmedSecret := strings.TrimSpace(secret)
			if trimmedKey == "" || trimmedSecret == "" {
				continue
			}
			secrets[trimmedKey] = trimmedSecret
		}
		if len(secrets) > 0 {
			allowedSkew := cfg.SwapAuth.AllowedTimestampSkew
			if allowedSkew <= 0 {
				allowedSkew = swapDefaultTimestamp
			}
			if allowedSkew > swapMaxTimestampSkew {
				allowedSkew = swapMaxTimestampSkew
			}
			nonceTTL := cfg.SwapAuth.NonceTTL
			if nonceTTL <= 0 {
				nonceTTL = swapDefaultNonceTTL
			}
			if nonceTTL > swapMaxNonceTTL {
				nonceTTL = swapMaxNonceTTL
			}
			nonceCapacity := cfg.SwapAuth.NonceCapacity
			if nonceCapacity <= 0 {
				nonceCapacity = swapDefaultNonceCache
			}
			if nonceCapacity > swapMaxNonceCache {
				nonceCapacity = swapMaxNonceCache
			}
			swapAuth = gatewayauth.NewAuthenticator(secrets, allowedSkew, nonceTTL, nonceCapacity, swapNow)
		}
	}
	if len(cfg.SwapAuth.PartnerRateLimits) > 0 {
		for key, limit := range cfg.SwapAuth.PartnerRateLimits {
			trimmedKey := strings.TrimSpace(key)
			if trimmedKey == "" || limit <= 0 {
				continue
			}
			swapLimits[trimmedKey] = limit
		}
	}
	srv := &Server{
		node:                     node,
		net:                      netClient,
		txSeen:                   make(map[string]time.Time),
		rateLimiters:             make(map[string]*rateLimiter),
		potsoEvidence:            modules.NewPotsoEvidenceModule(node),
		transactions:             modules.NewTransactionsModule(node),
		escrow:                   modules.NewEscrowModule(node),
		lending:                  modules.NewLendingModule(node),
		trustProxyHeaders:        cfg.TrustProxyHeaders,
		trustedProxies:           trusted,
		readHeaderTimeout:        cfg.ReadHeaderTimeout,
		readTimeout:              cfg.ReadTimeout,
		writeTimeout:             cfg.WriteTimeout,
		idleTimeout:              cfg.IdleTimeout,
		tlsCertFile:              strings.TrimSpace(cfg.TLSCertFile),
		tlsKeyFile:               strings.TrimSpace(cfg.TLSKeyFile),
		clientCAFile:             clientCAPath,
		requireClientCert:        requireClientCert,
		allowInsecure:            cfg.AllowInsecure,
		allowInsecureUnspecified: cfg.AllowInsecureUnspecified,
		proxyPolicy:              policy,
		allowlist:                allowlist,
		jwtVerifier:              jwtVerifier,
		jwtVerifierErr:           jwtErr,
		swapAuth:                 swapAuth,
		swapPartnerLimits:        swapLimits,
		swapRateWindow:           swapWindow,
		swapRateCounters:         make(map[string]*rateLimiter),
		swapNowFn:                swapNow,
		callerNonces:             make(map[string]callerNonceState),
	}
	srv.swapStable.assets = make(map[string]stable.Asset)
	srv.swapStable.now = time.Now
	if node != nil {
		srv.posRealtime = NewFinalityStream(node)
	}
	return srv, nil
}

func newJWTVerifier(cfg JWTConfig) (*jwtVerifier, error) {
	method := strings.ToUpper(strings.TrimSpace(cfg.Alg))
	if method == "" {
		method = jwt.SigningMethodHS256.Alg()
	}

	issuer := strings.TrimSpace(cfg.Issuer)
	if issuer == "" {
		return nil, errors.New("JWT issuer is required")
	}
	audiences := make([]string, 0, len(cfg.Audience))
	for _, aud := range cfg.Audience {
		trimmed := strings.TrimSpace(aud)
		if trimmed != "" {
			audiences = append(audiences, trimmed)
		}
	}
	if len(audiences) == 0 {
		return nil, errors.New("at least one JWT audience is required")
	}

	var signingMethod jwt.SigningMethod
	var key interface{}
	switch method {
	case jwt.SigningMethodHS256.Alg():
		envKey := strings.TrimSpace(cfg.HSSecretEnv)
		if envKey == "" {
			return nil, errors.New("HS256 requires HSSecretEnv to be set")
		}
		secret := strings.TrimSpace(os.Getenv(envKey))
		if secret == "" {
			return nil, fmt.Errorf("JWT secret environment variable %s is empty", envKey)
		}
		signingMethod = jwt.SigningMethodHS256
		key = []byte(secret)
	case jwt.SigningMethodRS256.Alg():
		path := strings.TrimSpace(cfg.RSAPublicKeyFile)
		if path == "" {
			return nil, errors.New("RS256 requires RSAPublicKeyFile to be set")
		}
		pemData, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read RSA public key: %w", err)
		}
		rsaKey, err := parseRSAPublicKey(pemData)
		if err != nil {
			return nil, err
		}
		signingMethod = jwt.SigningMethodRS256
		key = rsaKey
	default:
		return nil, fmt.Errorf("unsupported JWT algorithm %q", method)
	}

	leeway := time.Duration(cfg.MaxSkewSeconds) * time.Second
	if cfg.MaxSkewSeconds <= 0 {
		leeway = 30 * time.Second
	}
	verifier := &jwtVerifier{
		method:   signingMethod,
		key:      key,
		issuer:   issuer,
		audience: audiences,
		leeway:   leeway,
		now:      time.Now,
	}
	return verifier, nil
}

func parseRSAPublicKey(data []byte) (*rsa.PublicKey, error) {
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		switch block.Type {
		case "PUBLIC KEY":
			pub, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse RSA public key: %w", err)
			}
			rsaKey, ok := pub.(*rsa.PublicKey)
			if !ok {
				return nil, errors.New("parsed public key is not RSA")
			}
			return rsaKey, nil
		case "RSA PUBLIC KEY":
			rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse PKCS1 RSA public key: %w", err)
			}
			return rsaKey, nil
		}
	}
	return nil, errors.New("no RSA public key found in PEM data")
}

func (v *jwtVerifier) Verify(token string) (*jwt.RegisteredClaims, error) {
	if v == nil {
		return nil, errors.New("JWT verifier not configured")
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{v.method.Alg()}),
		jwt.WithIssuer(v.issuer),
	}
	if v.leeway > 0 {
		opts = append(opts, jwt.WithLeeway(v.leeway))
	}
	if v.now != nil {
		opts = append(opts, jwt.WithTimeFunc(func() time.Time { return v.now() }))
	}
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return v.key, nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("token validation failed")
	}
	if len(v.audience) > 0 {
		if claims, ok := parsed.Claims.(*jwt.RegisteredClaims); ok {
			matched := false
			for _, aud := range v.audience {
				for _, claimAud := range claims.Audience {
					if strings.EqualFold(claimAud, aud) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				return nil, errors.New("token audience mismatch")
			}
		}
	}
	return claims, nil
}

// ConfigureStableEngine wires the experimental stable engine into the RPC surface.
func (s *Server) ConfigureStableEngine(engine *stable.Engine, limits stable.Limits, assets []stable.Asset, now func() time.Time) {
	if s == nil {
		return
	}
	s.swapStableMu.Lock()
	defer s.swapStableMu.Unlock()
	s.swapStable.engine = engine
	s.swapStable.limits = limits
	if s.swapStable.assets == nil {
		s.swapStable.assets = make(map[string]stable.Asset)
	} else {
		for k := range s.swapStable.assets {
			delete(s.swapStable.assets, k)
		}
	}
	for _, asset := range assets {
		symbol := strings.ToUpper(strings.TrimSpace(asset.Symbol))
		if symbol == "" {
			continue
		}
		s.swapStable.assets[symbol] = asset
	}
	if now != nil {
		s.swapStable.now = now
	} else {
		s.swapStable.now = time.Now
	}
}

func (s *Server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("Starting JSON-RPC server on %s\n", listener.Addr())
	return s.Serve(listener)
}

// Serve runs the RPC server using the provided listener. The listener is
// closed when Serve returns.
func (s *Server) Serve(listener net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/ws/pos/finality", s.handlePOSFinalityWS)

	grpcServer := grpc.NewServer()
	if s.posRealtime != nil {
		posv1.RegisterRealtimeServer(grpcServer, s.posRealtime)
	}

	baseHandler := grpcHandler(grpcServer, mux)
	srv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           baseHandler,
		ReadHeaderTimeout: s.readHeaderTimeout,
		ReadTimeout:       s.readTimeout,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       s.idleTimeout,
	}

	tlsConfig, err := s.buildTLSConfig()
	if err != nil {
		_ = listener.Close()
		return err
	}
	if tlsConfig != nil {
		srv.TLSConfig = tlsConfig
		if err := http2.ConfigureServer(srv, &http2.Server{}); err != nil {
			_ = listener.Close()
			return fmt.Errorf("configure http2: %w", err)
		}
	} else {
		if !s.allowInsecure {
			_ = listener.Close()
			return errors.New("TLS is required for RPC server; configure certificates or enable AllowInsecure")
		}
		loopback := isLoopback(listener.Addr(), s.allowInsecureUnspecified)
		observability.Security().RecordInsecureBind("rpc", loopback)
		fmt.Printf("AllowInsecure enabled; plaintext RPC binding to %s (loopback=%t)\n", listener.Addr(), loopback)
		if !loopback {
			_ = listener.Close()
			return errors.New("plaintext RPC is only permitted on loopback interfaces")
		}
		srv.Handler = h2c.NewHandler(baseHandler, &http2.Server{})
	}

	s.serverMu.Lock()
	s.httpServer = srv
	s.grpcServer = grpcServer
	s.serverMu.Unlock()

	defer func() {
		grpcServer.GracefulStop()
		s.serverMu.Lock()
		s.httpServer = nil
		s.grpcServer = nil
		s.serverMu.Unlock()
	}()

	if tlsConfig != nil {
		return srv.Serve(tls.NewListener(listener, tlsConfig))
	}
	return srv.Serve(listener)
}

// Shutdown gracefully terminates the RPC server if it is running.
func (s *Server) Shutdown(ctx context.Context) error {
	s.serverMu.Lock()
	srv := s.httpServer
	grpcSrv := s.grpcServer
	s.serverMu.Unlock()

	if grpcSrv != nil {
		done := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-ctx.Done():
			grpcSrv.Stop()
		case <-done:
		}
	}

	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func grpcHandler(grpcServer *grpc.Server, other http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		other.ServeHTTP(w, r)
	})
}

func (s *Server) buildTLSConfig() (*tls.Config, error) {
	certPath := strings.TrimSpace(s.tlsCertFile)
	keyPath := strings.TrimSpace(s.tlsKeyFile)
	if certPath == "" && keyPath == "" {
		return nil, nil
	}
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("both TLS certificate and key paths must be provided")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	config := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if s.clientCAFile != "" {
		caPEM, err := os.ReadFile(s.clientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read TLS client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("failed to parse TLS client CA file")
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

func isLoopback(addr net.Addr, allowUnspecified bool) bool {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip := tcpAddr.IP
	if ip == nil || ip.IsUnspecified() {
		return allowUnspecified
	}
	if ip.IsLoopback() {
		return true
	}
	return false
}

type RPCRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
	ID      int               `json:"id"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type rpcResponseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *rpcResponseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func writeError(w http.ResponseWriter, status int, id interface{}, code int, message string, data interface{}) {
	if status <= 0 {
		status = http.StatusBadRequest
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	errObj := &RPCError{Code: code, Message: message}
	if data != nil {
		errObj.Data = data
	}
	resp := RPCResponse{JSONRPC: jsonRPCVersion, ID: id, Error: errObj}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := RPCResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result}
	_ = json.NewEncoder(w).Encode(resp)
}

func moduleAndMethod(method string) (string, string) {
	trimmed := strings.TrimSpace(method)
	if trimmed == "" {
		return "", ""
	}
	if idx := strings.Index(trimmed, "_"); idx > 0 {
		module := trimmed[:idx]
		action := trimmed[idx+1:]
		if action == "" {
			action = "call"
		}
		return module, action
	}
	return trimmed, "call"
}

func isPublicSwapMethod(method string) bool {
	switch strings.TrimSpace(method) {
	case "swap_submitVoucher", "swap_voucher_get", "swap_voucher_list", "swap_voucher_export",
		"nhb_requestSwapApproval", "nhb_swapMint", "nhb_swapBurn", "nhb_getSwapStatus":
		return true
	default:
		return false
	}
}

type BalanceResponse struct {
	Address            string                `json:"address"`
	BalanceNHB         *big.Int              `json:"balanceNHB"`
	BalanceZNHB        *big.Int              `json:"balanceZNHB"`
	Stake              *big.Int              `json:"stake"`
	LockedZNHB         *big.Int              `json:"lockedZNHB"`
	DelegatedValidator string                `json:"delegatedValidator,omitempty"`
	PendingUnbonds     []StakeUnbondResponse `json:"pendingUnbonds,omitempty"`
	Username           string                `json:"username"`
	Nonce              uint64                `json:"nonce"`
	EngagementScore    uint64                `json:"engagementScore"`
}

type StakeUnbondResponse struct {
	ID          uint64   `json:"id"`
	Validator   string   `json:"validator"`
	Amount      *big.Int `json:"amount"`
	ReleaseTime uint64   `json:"releaseTime"`
}

type posSweepParams struct {
	Timestamp *int64 `json:"timestamp,omitempty"`
}

type EpochSummaryResult struct {
	Epoch                  uint64   `json:"epoch"`
	Height                 uint64   `json:"height"`
	FinalizedAt            int64    `json:"finalizedAt"`
	TotalWeight            string   `json:"totalWeight"`
	ActiveValidators       []string `json:"activeValidators"`
	EligibleValidatorCount int      `json:"eligibleValidatorCount"`
}

type EpochWeightResult struct {
	Address    string `json:"address"`
	Stake      string `json:"stake"`
	Engagement uint64 `json:"engagement"`
	Composite  string `json:"compositeWeight"`
}

type EpochSnapshotResult struct {
	Epoch       uint64              `json:"epoch"`
	Height      uint64              `json:"height"`
	FinalizedAt int64               `json:"finalizedAt"`
	TotalWeight string              `json:"totalWeight"`
	Weights     []EpochWeightResult `json:"weights"`
	Selected    []string            `json:"selectedValidators"`
}

func parseEpochParam(raw json.RawMessage) (uint64, bool, error) {
	if raw == nil {
		return 0, false, nil
	}

	var direct uint64
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, true, nil
	}

	var wrapper struct {
		Epoch *uint64 `json:"epoch"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Epoch != nil {
		return *wrapper.Epoch, true, nil
	}

	return 0, false, fmt.Errorf("invalid epoch parameter")
}

// handle is the main request handler that routes to specific handlers.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer func() {
		_ = reader.Close()
	}()

	w.Header().Set("Content-Type", "application/json")

	clientIP, err := s.resolveClientIP(r)
	if err != nil {
		writeError(w, http.StatusForbidden, nil, codeUnauthorized, "invalid client address", err.Error())
		return
	}
	if !s.isClientAllowed(clientIP) {
		writeError(w, http.StatusForbidden, nil, codeUnauthorized, "client address not allowed", nil)
		return
	}
	ctx := context.WithValue(r.Context(), clientIPContextKey, clientIP)
	r = r.WithContext(ctx)

	body, err := io.ReadAll(reader)
	if err != nil {
		status := http.StatusBadRequest
		message := "failed to read request body"
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			message = fmt.Sprintf("request body exceeds %d bytes", maxRequestBytes)
		}
		writeError(w, status, nil, codeInvalidRequest, message, err.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeError(w, http.StatusBadRequest, nil, codeInvalidRequest, "request body required", nil)
		return
	}

	req := &RPCRequest{}
	if err := json.Unmarshal(body, req); err != nil {
		writeError(w, http.StatusBadRequest, nil, codeParseError, "invalid JSON payload", err.Error())
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != jsonRPCVersion {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "unsupported jsonrpc version", req.JSONRPC)
		return
	}
	if req.Method == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "method required", nil)
		return
	}

	moduleName, methodName := moduleAndMethod(req.Method)
	recorder := &rpcResponseRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	defer func() {
		if moduleName == "" {
			return
		}
		metrics := observability.ModuleMetrics()
		metrics.Observe(moduleName, methodName, recorder.status, time.Since(start))
		if recorder.status == http.StatusTooManyRequests {
			metrics.RecordThrottle(moduleName, "rate_limit")
		}
	}()

	if s.swapAuth != nil && isPublicSwapMethod(req.Method) {
		principal, err := s.authenticateSwapRequest(r, body)
		if err != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, codeUnauthorized, err.Error(), nil)
			return
		}
		if !s.allowSwapPrincipal(principal.APIKey, s.swapNow()) {
			writeError(recorder, http.StatusTooManyRequests, req.ID, codeRateLimited, "swap partner rate limit exceeded", nil)
			return
		}
	}

	switch req.Method {
	case "nhb_sendTransaction":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSendTransaction(recorder, r, req)
	case "tx_previewSponsorship":
		s.handleTxPreviewSponsorship(recorder, r, req)
	case "tx_getSponsorshipConfig":
		s.handleTxGetSponsorshipConfig(recorder, r, req)
	case "tx_setSponsorshipEnabled":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleTxSetSponsorshipEnabled(recorder, r, req)
	case "nhb_getBalance":
		s.handleGetBalance(recorder, r, req)
	case "nhb_getLatestBlocks":
		s.handleGetLatestBlocks(recorder, r, req)
	case "nhb_getLatestTransactions":
		s.handleGetLatestTransactions(recorder, r, req)
	case "nhb_getTransaction":
		s.handleGetTransaction(recorder, r, req)
	case "nhb_getTransactionReceipt":
		s.handleGetTransactionReceipt(recorder, r, req)
	case "nhb_getEpochSummary":
		s.handleGetEpochSummary(recorder, r, req)
	case "nhb_getEpochSnapshot":
		s.handleGetEpochSnapshot(recorder, r, req)
	case "nhb_getRewardEpoch":
		s.handleGetRewardEpoch(recorder, r, req)
	case "nhb_getRewardPayout":
		s.handleGetRewardPayout(recorder, r, req)
	case "mint_with_sig":
		s.handleMintWithSig(recorder, r, req)
	case "swap_submitVoucher":
		s.handleSwapSubmitVoucher(recorder, r, req)
	case "swap_voucher_get":
		s.handleSwapVoucherGet(recorder, r, req)
	case "swap_voucher_list":
		s.handleSwapVoucherList(recorder, r, req)
	case "swap_voucher_export":
		s.handleSwapVoucherExport(recorder, r, req)
	case "nhb_requestSwapApproval":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleStableRequestSwapApproval(recorder, r, req)
	case "nhb_swapMint":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleStableSwapMint(recorder, r, req)
	case "nhb_swapBurn":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleStableSwapBurn(recorder, r, req)
	case "nhb_getSwapStatus":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleStableGetSwapStatus(recorder, r, req)
	case "fees_listTotals":
		s.handleFeesListTotals(recorder, r, req)
	case "fees_getMonthlyStatus":
		s.handleFeesGetMonthlyStatus(recorder, r, req)
	case "swap_limits":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapLimits(recorder, r, req)
	case "swap_provider_status":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapProviderStatus(recorder, r, req)
	case "swap_burn_list":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapBurnList(recorder, r, req)
	case "swap_voucher_reverse":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapVoucherReverse(recorder, r, req)
	case "pos_sweepVoids":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePOSSweepVoids(recorder, r, req)
	case "lending_getMarket":
		s.handleLendingGetMarket(recorder, r, req)
	case "lend_getPools":
		s.handleLendGetPools(recorder, r, req)
	case "lend_createPool":
		s.handleLendCreatePool(recorder, r, req)
	case "lending_getUserAccount":
		s.handleLendingGetUserAccount(recorder, r, req)
	case "lending_supplyNHB":
		s.handleLendingSupplyNHB(recorder, r, req)
	case "lending_withdrawNHB":
		s.handleLendingWithdrawNHB(recorder, r, req)
	case "lending_depositZNHB":
		s.handleLendingDepositZNHB(recorder, r, req)
	case "lending_withdrawZNHB":
		s.handleLendingWithdrawZNHB(recorder, r, req)
	case "lending_borrowNHB":
		s.handleLendingBorrowNHB(recorder, r, req)
	case "lending_borrowNHBWithFee":
		s.handleLendingBorrowNHBWithFee(recorder, r, req)
	case "lending_repayNHB":
		s.handleLendingRepayNHB(recorder, r, req)
	case "lending_liquidate":
		s.handleLendingLiquidate(recorder, r, req)
	case "stake_delegate":
		s.handleStakeDelegate(recorder, r, req)
	case "stake_undelegate":
		s.handleStakeUndelegate(recorder, r, req)
	case "stake_claim":
		s.handleStakeClaim(recorder, r, req)
	case "stake_claimRewards":
		s.handleStakeClaimRewards(recorder, r, req)
	case "stake_getPosition":
		s.handleStakeGetPosition(recorder, r, req)
	case "stake_previewClaim":
		s.handleStakePreviewClaim(recorder, r, req)
	case "loyalty_createBusiness":
		s.handleLoyaltyCreateBusiness(recorder, r, req)
	case "loyalty_setPaymaster":
		s.handleLoyaltySetPaymaster(recorder, r, req)
	case "loyalty_addMerchant":
		s.handleLoyaltyAddMerchant(recorder, r, req)
	case "loyalty_removeMerchant":
		s.handleLoyaltyRemoveMerchant(recorder, r, req)
	case "loyalty_createProgram":
		s.handleLoyaltyCreateProgram(recorder, r, req)
	case "loyalty_updateProgram":
		s.handleLoyaltyUpdateProgram(recorder, r, req)
	case "loyalty_pauseProgram":
		s.handleLoyaltyPauseProgram(recorder, r, req)
	case "loyalty_resumeProgram":
		s.handleLoyaltyResumeProgram(recorder, r, req)
	case "loyalty_getBusiness":
		s.handleLoyaltyGetBusiness(recorder, r, req)
	case "loyalty_listPrograms":
		s.handleLoyaltyListPrograms(recorder, r, req)
	case "loyalty_programStats":
		s.handleLoyaltyProgramStats(recorder, r, req)
	case "loyalty_userDaily":
		s.handleLoyaltyUserDaily(recorder, r, req)
	case "loyalty_paymasterBalance":
		s.handleLoyaltyPaymasterBalance(recorder, r, req)
	case "loyalty_resolveUsername":
		s.handleLoyaltyResolveUsername(recorder, r, req)
	case "loyalty_userQR":
		s.handleLoyaltyUserQR(recorder, r, req)
	case "creator_publish":
		s.handleCreatorPublish(recorder, r, req)
	case "creator_tip":
		s.handleCreatorTip(recorder, r, req)
	case "creator_stake":
		s.handleCreatorStake(recorder, r, req)
	case "creator_unstake":
		s.handleCreatorUnstake(recorder, r, req)
	case "creator_payouts":
		s.handleCreatorPayouts(recorder, r, req)
	case "identity_setAlias":
		s.handleIdentitySetAlias(recorder, r, req)
	case "identity_setAvatar":
		s.handleIdentitySetAvatar(recorder, r, req)
	case "identity_addAddress":
		s.handleIdentityAddAddress(recorder, r, req)
	case "identity_removeAddress":
		s.handleIdentityRemoveAddress(recorder, r, req)
	case "identity_setPrimary":
		s.handleIdentitySetPrimary(recorder, r, req)
	case "identity_rename":
		s.handleIdentityRename(recorder, r, req)
	case "identity_resolve":
		s.handleIdentityResolve(recorder, r, req)
	case "identity_reverse":
		s.handleIdentityReverse(recorder, r, req)
	case "identity_createClaimable":
		s.handleIdentityCreateClaimable(recorder, r, req)
	case "identity_claim":
		s.handleIdentityClaim(recorder, r, req)
	case "claimable_create":
		s.handleClaimableCreate(recorder, r, req)
	case "claimable_claim":
		s.handleClaimableClaim(recorder, r, req)
	case "claimable_cancel":
		s.handleClaimableCancel(recorder, r, req)
	case "claimable_get":
		s.handleClaimableGet(recorder, r, req)
	case "escrow_create":
		s.handleEscrowCreate(recorder, r, req)
	case "escrow_get":
		s.handleEscrowGet(recorder, r, req)
	case "escrow_getRealm":
		s.handleEscrowGetRealm(recorder, r, req)
	case "escrow_getSnapshot":
		s.handleEscrowGetSnapshot(recorder, r, req)
	case "escrow_listEvents":
		s.handleEscrowListEvents(recorder, r, req)
	case "escrow_fund":
		s.handleEscrowFund(recorder, r, req)
	case "escrow_release":
		s.handleEscrowRelease(recorder, r, req)
	case "escrow_refund":
		s.handleEscrowRefund(recorder, r, req)
	case "escrow_expire":
		s.handleEscrowExpire(recorder, r, req)
	case "escrow_dispute":
		s.handleEscrowDispute(recorder, r, req)
	case "escrow_resolve":
		s.handleEscrowResolve(recorder, r, req)
	case "escrow_milestoneCreate":
		s.handleEscrowMilestoneCreate(recorder, r, req)
	case "escrow_milestoneGet":
		s.handleEscrowMilestoneGet(recorder, r, req)
	case "escrow_milestoneFund":
		s.handleEscrowMilestoneFund(recorder, r, req)
	case "escrow_milestoneRelease":
		s.handleEscrowMilestoneRelease(recorder, r, req)
	case "escrow_milestoneCancel":
		s.handleEscrowMilestoneCancel(recorder, r, req)
	case "escrow_milestoneSubscriptionUpdate":
		s.handleEscrowMilestoneSubscriptionUpdate(recorder, r, req)
	case "net_info":
		s.handleNetInfo(recorder, r, req)
	case "net_peers":
		s.handleNetPeers(recorder, r, req)
	case "net_dial":
		s.handleNetDial(recorder, r, req)
	case "net_ban":
		s.handleNetBan(recorder, r, req)
	case "sync_snapshot_export":
		s.handleSyncSnapshotExport(recorder, r, req)
	case "sync_snapshot_import":
		s.handleSyncSnapshotImport(recorder, r, req)
	case "sync_status":
		s.handleSyncStatus(recorder, r, req)
	case "p2p_info":
		s.handleP2PInfo(recorder, r, req)
	case "p2p_peers":
		s.handleP2PPeers(recorder, r, req)
	case "p2p_createTrade":
		s.handleP2PCreateTrade(recorder, r, req)
	case "p2p_getTrade":
		s.handleP2PGetTrade(recorder, r, req)
	case "p2p_settle":
		s.handleP2PSettle(recorder, r, req)
	case "p2p_dispute":
		s.handleP2PDispute(recorder, r, req)
	case "p2p_resolve":
		s.handleP2PResolve(recorder, r, req)
	case "engagement_register_device":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementRegisterDevice(recorder, r, req)
	case "engagement_submit_heartbeat":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementSubmitHeartbeat(recorder, r, req)
	case "potso_heartbeat":
		s.handlePotsoHeartbeat(recorder, r, req)
	case "potso_userMeters":
		s.handlePotsoUserMeters(recorder, r, req)
	case "potso_top":
		s.handlePotsoTop(recorder, r, req)
	case "potso_leaderboard":
		s.handlePotsoLeaderboard(recorder, r, req)
	case "potso_params":
		s.handlePotsoParams(recorder, r, req)
	case "potso_stake_lock":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeLock(recorder, r, req)
	case "potso_stake_unbond":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeUnbond(recorder, r, req)
	case "potso_stake_withdraw":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeWithdraw(recorder, r, req)
	case "potso_stake_info":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeInfo(recorder, r, req)
	case "potso_epoch_info":
		s.handlePotsoEpochInfo(recorder, r, req)
	case "potso_epoch_payouts":
		s.handlePotsoEpochPayouts(recorder, r, req)
	case "potso_reward_claim":
		if authErr := s.requireAuthInto(&r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoRewardClaim(recorder, r, req)
	case "potso_rewards_history":
		s.handlePotsoRewardsHistory(recorder, r, req)
	case "potso_export_epoch":
		s.handlePotsoExportEpoch(recorder, r, req)
	case "potso_submitEvidence":
		s.handlePotsoSubmitEvidence(recorder, r, req)
	case "potso_getEvidence":
		s.handlePotsoGetEvidence(recorder, r, req)
	case "potso_listEvidence":
		s.handlePotsoListEvidence(recorder, r, req)
	case "gov_propose":
		s.handleGovernancePropose(recorder, r, req)
	case "gov_vote":
		s.handleGovernanceVote(recorder, r, req)
	case "gov_proposal":
		s.handleGovernanceProposal(recorder, r, req)
	case "gov_list":
		s.handleGovernanceList(recorder, r, req)
	case "gov_finalize":
		s.handleGovernanceFinalize(recorder, r, req)
	case "gov_queue":
		s.handleGovernanceQueue(recorder, r, req)
	case "gov_execute":
		s.handleGovernanceExecute(recorder, r, req)
	case "reputation_verifySkill":
		s.handleReputationVerifySkill(recorder, r, req)
	default:
		writeError(recorder, http.StatusNotFound, req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %s", req.Method), nil)
	}
}

// ServeHTTP allows the RPC server to satisfy the http.Handler interface for testing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handle(w, r)
}

// --- NEW HANDLER: Get Latest Blocks ---
func (s *Server) handleGetLatestBlocks(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	count := 10
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &count); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "count must be an integer", err.Error())
			return
		}
	}
	if count <= 0 {
		count = 10
	} else if count > 20 {
		count = 20
	}

	latestHeight := s.node.Chain().GetHeight()
	blocks := make([]*types.Block, 0, count)

	for i := 0; i < count && uint64(i) <= latestHeight; i++ {
		height := latestHeight - uint64(i)
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err != nil {
			break // Stop if we go past the genesis block
		}
		blocks = append(blocks, block)
	}
	writeResult(w, req.ID, blocks)
}

// --- NEW HANDLER: Get Latest Transactions ---
func (s *Server) handleGetLatestTransactions(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	count := 20
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &count); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "count must be an integer", err.Error())
			return
		}
	}
	if count <= 0 {
		count = 20
	} else if count > 50 {
		count = 50
	}

	latestHeight := s.node.Chain().GetHeight()
	var txs []*types.Transaction

	// Iterate backwards from the latest block until we have enough transactions
	for i := uint64(0); i <= latestHeight && len(txs) < count; i++ {
		height := latestHeight - i
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err != nil {
			break
		}
		txs = append(txs, block.Transactions...)
	}

	// Ensure we only return the requested number of transactions
	if len(txs) > count {
		txs = txs[:count]
	}
	writeResult(w, req.ID, txs)
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction hash required", nil)
		return
	}
	var hash string
	if err := json.Unmarshal(req.Params[0], &hash); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction hash must be a string", err.Error())
		return
	}
	tx, canonicalHash, blockHash, blockNumber, err := s.findTransaction(hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to resolve transaction", err.Error())
		return
	}
	if tx == nil {
		writeResult(w, req.ID, nil)
		return
	}
	result, err := buildTransactionResult(tx, canonicalHash, blockHash, blockNumber)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to encode transaction", err.Error())
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleGetTransactionReceipt(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction hash required", nil)
		return
	}
	var hash string
	if err := json.Unmarshal(req.Params[0], &hash); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction hash must be a string", err.Error())
		return
	}
	tx, canonicalHash, blockHash, blockNumber, err := s.findTransaction(hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to resolve transaction", err.Error())
		return
	}
	if tx == nil {
		writeResult(w, req.ID, nil)
		return
	}
	receipt, err := s.buildReceiptResult(tx, canonicalHash, blockHash, blockNumber)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to encode receipt", err.Error())
		return
	}
	writeResult(w, req.ID, receipt)
}

func (s *Server) findTransaction(hash string) (*types.Transaction, string, []byte, uint64, error) {
	if s == nil || s.node == nil {
		return nil, "", nil, 0, fmt.Errorf("node unavailable")
	}
	normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(hash), "0x"))
	if normalized == "" {
		return nil, "", nil, 0, nil
	}
	chain := s.node.Chain()
	if chain == nil {
		return nil, "", nil, 0, fmt.Errorf("chain unavailable")
	}
	latest := chain.GetHeight()
	for height := uint64(0); height <= latest; height++ {
		block, err := chain.GetBlockByHeight(height)
		if err != nil || block == nil {
			continue
		}
		blockHash, err := block.Header.Hash()
		if err != nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx == nil {
				continue
			}
			hashBytes, err := tx.Hash()
			if err != nil {
				continue
			}
			canonical := hex.EncodeToString(hashBytes)
			if strings.EqualFold(canonical, normalized) {
				return tx, ensureHexPrefix(canonical), blockHash, height, nil
			}
		}
	}
	return nil, "", nil, 0, nil
}

func buildTransactionResult(tx *types.Transaction, txHash string, blockHash []byte, blockNumber uint64) (*TransactionResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction nil")
	}
	result := &TransactionResult{
		Hash:        txHash,
		Type:        formatTxType(tx.Type),
		Asset:       assetLabel(tx.Type),
		BlockNumber: hexString(blockNumber),
		Nonce:       hexString(tx.Nonce),
		GasLimit:    hexString(tx.GasLimit),
		GasPrice:    hexBig(tx.GasPrice),
		Value:       hexBig(tx.Value),
	}
	if len(blockHash) > 0 {
		result.BlockHash = ensureHexPrefix(hex.EncodeToString(blockHash))
	}
	if from, err := tx.From(); err == nil {
		result.From = crypto.MustNewAddress(crypto.NHBPrefix, from).String()
	}
	if len(tx.To) == 20 {
		result.To = crypto.MustNewAddress(crypto.NHBPrefix, tx.To).String()
	}
	if len(tx.Data) > 0 {
		result.Input = "0x" + strings.ToLower(hex.EncodeToString(tx.Data))
	} else {
		result.Input = "0x"
	}
	return result, nil
}

func (s *Server) buildReceiptResult(tx *types.Transaction, txHash string, blockHash []byte, blockNumber uint64) (*ReceiptResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction nil")
	}
	receipt := &ReceiptResult{
		TransactionHash: txHash,
		BlockNumber:     hexString(blockNumber),
		Status:          "0x1",
		GasUsed:         hexString(0),
		Logs:            []ReceiptLog{},
	}
	if len(blockHash) > 0 {
		receipt.BlockHash = ensureHexPrefix(hex.EncodeToString(blockHash))
	}
	if sim, err := s.simulateTransaction(tx); err == nil && sim != nil {
		if sim.GasUsed > 0 {
			receipt.GasUsed = hexString(sim.GasUsed)
		}
		receipt.Logs = convertEventsToLogs(sim.Events)
	} else if tx.GasLimit > 0 {
		receipt.GasUsed = hexString(tx.GasLimit)
	}
	if len(receipt.Logs) == 0 {
		if fallback := buildFallbackTransferLog(tx); fallback != nil {
			receipt.Logs = append(receipt.Logs, fallback)
		}
	}
	return receipt, nil
}

func (s *Server) simulateTransaction(tx *types.Transaction) (*core.SimulationResult, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	protoTx, err := codec.TransactionToProto(tx)
	if err != nil {
		return nil, err
	}
	if protoTx == nil {
		return nil, fmt.Errorf("transaction payload unavailable")
	}
	payload, err := proto.Marshal(protoTx)
	if err != nil {
		return nil, err
	}
	return s.node.SimulateTx(payload)
}

func convertEventsToLogs(eventsList []types.Event) []ReceiptLog {
	if len(eventsList) == 0 {
		return nil
	}
	logs := make([]ReceiptLog, 0, len(eventsList))
	for _, evt := range eventsList {
		log := ReceiptLog{"event": eventDisplayName(evt.Type)}
		for key, value := range evt.Attributes {
			switch evt.Type {
			case events.TypeTransfer:
				switch key {
				case "asset":
					log["asset"] = strings.ToUpper(strings.TrimSpace(value))
					continue
				case "amount":
					log["value"] = decimalToHex(value)
					continue
				}
			case events.TypeFeeApplied:
				switch key {
				case "asset":
					log["asset"] = strings.ToUpper(strings.TrimSpace(value))
					continue
				case "payer":
					log["payer"] = ensureHexPrefix(value)
					continue
				case "ownerWallet":
					log["ownerWallet"] = ensureHexPrefix(value)
					continue
				case "grossWei":
					log["gross"] = decimalToHex(value)
					continue
				case "feeWei":
					log["fee"] = decimalToHex(value)
					continue
				case "netWei":
					log["net"] = decimalToHex(value)
					continue
				}
			}
			log[key] = value
		}
		if _, ok := log["asset"]; !ok {
			if asset := strings.TrimSpace(evt.Attributes["asset"]); asset != "" {
				log["asset"] = strings.ToUpper(asset)
			}
		}
		logs = append(logs, log)
	}
	return logs
}

func buildFallbackTransferLog(tx *types.Transaction) ReceiptLog {
	asset := assetLabel(tx.Type)
	if asset == "" {
		return nil
	}
	log := ReceiptLog{"event": "Transfer", "asset": asset}
	if from, err := tx.From(); err == nil {
		log["from"] = crypto.MustNewAddress(crypto.NHBPrefix, from).String()
	}
	if len(tx.To) == 20 {
		log["to"] = crypto.MustNewAddress(crypto.NHBPrefix, tx.To).String()
	}
	log["value"] = hexBig(tx.Value)
	return log
}

func decimalToHex(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "0x0"
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		return trimmed
	}
	if parsed, ok := new(big.Int).SetString(trimmed, 10); ok {
		return fmt.Sprintf("0x%x", parsed)
	}
	return trimmed
}

func eventDisplayName(eventType string) string {
	switch eventType {
	case events.TypeTransfer:
		return "Transfer"
	case events.TypeFeeApplied:
		return "FeeApplied"
	default:
		return eventType
	}
}

func (s *Server) handleGetEpochSummary(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	var epochNumber uint64
	var haveEpoch bool
	if len(req.Params) > 0 {
		value, ok, err := parseEpochParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
			return
		}
		if ok {
			epochNumber = value
			haveEpoch = true
		}
	}

	var (
		summary *epoch.Summary
		exists  bool
	)
	if haveEpoch {
		summary, exists = s.node.EpochSummary(epochNumber)
	} else {
		summary, exists = s.node.LatestEpochSummary()
	}
	if !exists || summary == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch summary not found", nil)
		return
	}

	active := make([]string, len(summary.ActiveValidators))
	for i := range summary.ActiveValidators {
		active[i] = "0x" + hex.EncodeToString(summary.ActiveValidators[i])
	}
	total := big.NewInt(0)
	if summary.TotalWeight != nil {
		total = new(big.Int).Set(summary.TotalWeight)
	}
	result := EpochSummaryResult{
		Epoch:                  summary.Epoch,
		Height:                 summary.Height,
		FinalizedAt:            summary.FinalizedAt,
		TotalWeight:            total.String(),
		ActiveValidators:       active,
		EligibleValidatorCount: summary.EligibleCount,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleGetEpochSnapshot(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	var epochNumber uint64
	var haveEpoch bool
	if len(req.Params) > 0 {
		value, ok, err := parseEpochParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
			return
		}
		if ok {
			epochNumber = value
			haveEpoch = true
		}
	}

	var (
		snapshot *epoch.Snapshot
		exists   bool
	)
	if haveEpoch {
		snapshot, exists = s.node.EpochSnapshot(epochNumber)
	} else {
		snapshot, exists = s.node.LatestEpochSnapshot()
	}
	if !exists || snapshot == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch snapshot not found", nil)
		return
	}

	weights := make([]EpochWeightResult, len(snapshot.Weights))
	for i := range snapshot.Weights {
		stake := big.NewInt(0)
		if snapshot.Weights[i].Stake != nil {
			stake = new(big.Int).Set(snapshot.Weights[i].Stake)
		}
		composite := big.NewInt(0)
		if snapshot.Weights[i].Composite != nil {
			composite = new(big.Int).Set(snapshot.Weights[i].Composite)
		}
		weights[i] = EpochWeightResult{
			Address:    "0x" + hex.EncodeToString(snapshot.Weights[i].Address),
			Stake:      stake.String(),
			Engagement: snapshot.Weights[i].Engagement,
			Composite:  composite.String(),
		}
	}

	selected := make([]string, len(snapshot.Selected))
	for i := range snapshot.Selected {
		selected[i] = "0x" + hex.EncodeToString(snapshot.Selected[i])
	}

	total := big.NewInt(0)
	if snapshot.TotalWeight != nil {
		total = new(big.Int).Set(snapshot.TotalWeight)
	}

	result := EpochSnapshotResult{
		Epoch:       snapshot.Epoch,
		Height:      snapshot.Height,
		FinalizedAt: snapshot.FinalizedAt,
		TotalWeight: total.String(),
		Weights:     weights,
		Selected:    selected,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePOSSweepVoids(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "at most one parameter object expected", nil)
		return
	}
	var params posSweepParams
	if len(req.Params) == 1 && len(req.Params[0]) > 0 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
			return
		}
	}
	now := time.Now().UTC()
	if params.Timestamp != nil {
		if *params.Timestamp < 0 {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "timestamp must be non-negative", nil)
			return
		}
		now = time.Unix(*params.Timestamp, 0).UTC()
	}
	count, err := s.node.SweepExpiredPOSAuthorizations(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, map[string]int{"voided": count})
}

func (s *Server) requireAuth(r *http.Request) (*http.Request, *RPCError) {
	if s.requireClientCert && hasVerifiedClientCert(r) {
		return r, nil
	}
	if s.jwtVerifierErr != nil {
		return nil, &RPCError{Code: codeUnauthorized, Message: "JWT authentication misconfigured", Data: s.jwtVerifierErr.Error()}
	}
	if s.jwtVerifier == nil {
		return nil, &RPCError{Code: codeUnauthorized, Message: "JWT authentication not configured"}
	}
	token, err := extractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return nil, &RPCError{Code: codeUnauthorized, Message: err.Error()}
	}
	claims, err := s.jwtVerifier.Verify(token)
	if err != nil {
		return nil, &RPCError{Code: codeUnauthorized, Message: "invalid JWT", Data: err.Error()}
	}
	if claims != nil {
		identity := strings.TrimSpace(claims.Subject)
		if identity != "" {
			ctx := context.WithValue(r.Context(), clientIdentityContextKey, identity)
			r = r.WithContext(ctx)
		}
	}
	return r, nil
}

func (s *Server) requireAuthInto(r **http.Request) *RPCError {
	if r == nil || *r == nil {
		return &RPCError{Code: codeUnauthorized, Message: "request unavailable"}
	}
	updated, err := s.requireAuth(*r)
	if err != nil {
		return err
	}
	*r = updated
	return nil
}

func extractBearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing Authorization header")
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return "", errors.New("Authorization header must use Bearer scheme")
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" {
		return "", errors.New("missing bearer token")
	}
	return token, nil
}

func hasVerifiedClientCert(r *http.Request) bool {
	if r == nil || r.TLS == nil {
		return false
	}
	if len(r.TLS.VerifiedChains) > 0 {
		return true
	}
	if len(r.TLS.PeerCertificates) > 0 && r.TLS.HandshakeComplete {
		return true
	}
	return false
}

// TestRequireAuth exposes the internal authentication helper for integration tests.
func (s *Server) TestRequireAuth(r *http.Request) (*http.Request, *RPCError) {
	return s.requireAuth(r)
}

// TestAuthenticateSwap exposes the swap authenticator for integration tests.
func (s *Server) TestAuthenticateSwap(r *http.Request, body []byte) (*gatewayauth.Principal, error) {
	return s.authenticateSwapRequest(r, body)
}

func (s *Server) authenticateSwapRequest(r *http.Request, body []byte) (*gatewayauth.Principal, error) {
	if s.swapAuth == nil {
		return nil, errors.New("swap authentication not configured")
	}
	return s.swapAuth.Authenticate(r, body)
}

func (s *Server) allowSwapPrincipal(apiKey string, now time.Time) bool {
	if len(s.swapPartnerLimits) == 0 {
		return true
	}
	normalized := strings.TrimSpace(apiKey)
	if normalized == "" {
		return true
	}
	limit, ok := s.swapPartnerLimits[normalized]
	if !ok || limit <= 0 {
		return true
	}
	s.swapRateMu.Lock()
	defer s.swapRateMu.Unlock()
	limiter, ok := s.swapRateCounters[normalized]
	if !ok {
		limiter = &rateLimiter{windowStart: now, lastSeen: now}
		s.swapRateCounters[normalized] = limiter
	}
	if now.Sub(limiter.windowStart) >= s.swapRateWindow {
		limiter.windowStart = now
		limiter.count = 0
	}
	if limiter.count >= limit {
		limiter.lastSeen = now
		return false
	}
	limiter.count++
	limiter.lastSeen = now
	return true
}

func (s *Server) swapNow() time.Time {
	if s.swapNowFn != nil {
		return s.swapNowFn()
	}
	return time.Now()
}

func (s *Server) allowSource(source, identity, chainNonce string, now time.Time) bool {
	normalized := canonicalHost(source)
	key := normalized
	trimmedIdentity := strings.TrimSpace(identity)
	if trimmedIdentity != "" {
		key = strings.ToLower(trimmedIdentity)
	}
	trimmedChain := strings.TrimSpace(chainNonce)
	if trimmedChain != "" {
		if key != "" {
			key = key + "|" + trimmedChain
		} else {
			key = trimmedChain
		}
	}
	if key == "" {
		if normalized == "" {
			key = "unknown"
		} else {
			key = normalized
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictRateLimitersLocked(now)

	limiter, ok := s.rateLimiters[key]
	if !ok {
		if len(s.rateLimiters) >= rateLimiterMaxEntries {
			s.evictOldestLimiterLocked()
		}
		limiter = &rateLimiter{windowStart: now, lastSeen: now}
		s.rateLimiters[key] = limiter
	}

	if now.Sub(limiter.windowStart) >= rateLimitWindow {
		limiter.windowStart = now
		limiter.count = 0
	}
	if limiter.count >= maxTxPerWindow {
		limiter.lastSeen = now
		return false
	}
	limiter.count++
	limiter.lastSeen = now
	return true
}

func (s *Server) evictRateLimitersLocked(now time.Time) {
	if len(s.rateLimiters) == 0 {
		return
	}
	if !s.rateLimiterSweep.IsZero() && now.Sub(s.rateLimiterSweep) < rateLimiterSweepBackoff && len(s.rateLimiters) < rateLimiterMaxEntries {
		return
	}
	for key, limiter := range s.rateLimiters {
		if limiter.lastSeen.IsZero() {
			continue
		}
		if now.Sub(limiter.lastSeen) > rateLimiterStaleAfter {
			delete(s.rateLimiters, key)
		}
	}
	s.rateLimiterSweep = now
}

func (s *Server) evictOldestLimiterLocked() {
	if len(s.rateLimiters) == 0 {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	hasOldest := false
	for key, limiter := range s.rateLimiters {
		switch {
		case !hasOldest:
			oldestKey = key
			oldestTime = limiter.lastSeen
			hasOldest = true
		case limiter.lastSeen.IsZero() && !oldestTime.IsZero():
			oldestKey = key
			oldestTime = limiter.lastSeen
		case limiter.lastSeen.Before(oldestTime):
			oldestKey = key
			oldestTime = limiter.lastSeen
		}
	}
	if hasOldest {
		delete(s.rateLimiters, oldestKey)
	}
}

func (s *Server) rememberTx(hash string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictExpiredTxLocked(now)

	if _, exists := s.txSeen[hash]; exists {
		return false
	}
	s.txSeen[hash] = now
	s.txSeenQueue = append(s.txSeenQueue, txSeenEntry{hash: hash, seenAt: now})
	return true
}

func (s *Server) evictExpiredTxLocked(now time.Time) {
	if len(s.txSeenQueue) == 0 {
		return
	}

	cutoff := now.Add(-txSeenTTL)
	idx := 0
	for idx < len(s.txSeenQueue) {
		entry := s.txSeenQueue[idx]
		if !entry.seenAt.Before(cutoff) {
			break
		}
		delete(s.txSeen, entry.hash)
		idx++
	}

	if idx == 0 {
		return
	}

	for i := 0; i < idx; i++ {
		s.txSeenQueue[i] = txSeenEntry{}
	}
	s.txSeenQueue = s.txSeenQueue[idx:]
	if len(s.txSeenQueue) == 0 {
		s.txSeenQueue = nil
	}
}

func (s *Server) resolveClientIP(r *http.Request) (string, error) {
	host := r.RemoteAddr
	if splitHost, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = splitHost
	}
	host = canonicalHost(host)
	if host == "" {
		return "", errors.New("unable to determine remote address")
	}

	trusted := s.trustProxyHeaders || s.isTrustedProxy(host)
	forwardedValues := r.Header.Values("X-Forwarded-For")
	if len(forwardedValues) > 0 {
		if s.proxyPolicy.xForwardedFor == ProxyHeaderModeIgnore {
			return "", errors.New("X-Forwarded-For header is not permitted")
		}
		if !trusted {
			return "", fmt.Errorf("X-Forwarded-For header received from untrusted peer %s", host)
		}
		parts := parseForwardedFor(forwardedValues)
		if len(parts) == 0 {
			return "", errors.New("X-Forwarded-For header did not contain any addresses")
		}
		if s.proxyPolicy.xForwardedFor == ProxyHeaderModeSingle && len(parts) != 1 {
			return "", errors.New("X-Forwarded-For must contain exactly one address")
		}
		if len(parts) > maxForwardedForAddrs {
			return "", fmt.Errorf("X-Forwarded-For contains more than %d addresses", maxForwardedForAddrs)
		}
		candidate := canonicalHost(parts[0])
		if candidate == "" {
			return "", errors.New("X-Forwarded-For contained an invalid address")
		}
		return candidate, nil
	}

	realIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if realIP != "" {
		if s.proxyPolicy.xRealIP == ProxyHeaderModeIgnore {
			return "", errors.New("X-Real-IP header is not permitted")
		}
		if !trusted {
			return "", fmt.Errorf("X-Real-IP header received from untrusted peer %s", host)
		}
		if strings.Contains(realIP, ",") {
			return "", errors.New("X-Real-IP header must not contain multiple addresses")
		}
		candidate := canonicalHost(realIP)
		if candidate == "" {
			return "", errors.New("X-Real-IP contained an invalid address")
		}
		return candidate, nil
	}

	return host, nil
}

func parseForwardedFor(values []string) []string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		segments := strings.Split(value, ",")
		for _, segment := range segments {
			trimmed := strings.TrimSpace(segment)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	return parts
}

func (s *Server) clientSource(r *http.Request) string {
	if value, ok := r.Context().Value(clientIPContextKey).(string); ok && value != "" {
		return value
	}
	source, err := s.resolveClientIP(r)
	if err != nil {
		return ""
	}
	return source
}

func (s *Server) isClientAllowed(ip string) bool {
	if len(s.allowlist) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, network := range s.allowlist {
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}

func (s *Server) isTrustedProxy(host string) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	normalized := canonicalHost(host)
	if normalized == "" {
		return false
	}
	_, ok := s.trustedProxies[normalized]
	return ok
}

func canonicalHost(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.String()
	}
	return strings.ToLower(trimmed)
}

// --- Existing Handlers ---
func (s *Server) handleSendTransaction(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction parameter required", nil)
		return
	}

	var tx types.Transaction
	if err := json.Unmarshal(req.Params[0], &tx); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction format", err.Error())
		return
	}
	if !types.IsValidChainID(tx.ChainID) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction chainId does not match NHBCoin network", tx.ChainID)
		return
	}
	if tx.GasLimit == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "gasLimit must be greater than zero", nil)
		return
	}
	if tx.GasPrice == nil || tx.GasPrice.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "gasPrice must be greater than zero", nil)
		return
	}

	from, err := tx.From()
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction signature", err.Error())
		return
	}

	account, err := s.node.GetAccount(from)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load sender account", err.Error())
		return
	}
	if tx.Nonce < account.Nonce {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, fmt.Sprintf("nonce %d has already been used; current account nonce is %d", tx.Nonce, account.Nonce), nil)
		return
	}

	now := time.Now()
	source := s.clientSource(r)
	identity, _ := r.Context().Value(clientIdentityContextKey).(string)
	chainKey := ""
	if tx.ChainID != nil {
		chainKey = strings.TrimSpace(tx.ChainID.String())
	}
	nonceKey := strconv.FormatUint(tx.Nonce, 10)
	if chainKey != "" {
		nonceKey = chainKey + ":" + nonceKey
	}
	if !s.allowSource(source, identity, nonceKey, now) {
		writeError(w, http.StatusTooManyRequests, req.ID, codeRateLimited, "transaction rate limit exceeded", source)
		return
	}

	hashBytes, err := tx.Hash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to hash transaction", err.Error())
		return
	}
	hash := hex.EncodeToString(hashBytes)
	if !s.rememberTx(hash, now) {
		writeError(w, http.StatusConflict, req.ID, codeDuplicateTx, "transaction has already been submitted", hash)
		return
	}

	if err := s.node.AddTransaction(&tx); err != nil {
		switch {
		case errors.Is(err, core.ErrInvalidTransaction):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction", err.Error())
			return
		case errors.Is(err, core.ErrMempoolFull):
			writeError(w, http.StatusServiceUnavailable, req.ID, codeMempoolFull, "mempool full", nil)
			return
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to add transaction", err.Error())
			return
		}
	}
	writeResult(w, req.ID, "Transaction received by node.")
}

func (s *Server) handleEscrowGetRealm(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	result, modErr := s.escrow.GetRealm(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleEscrowGetSnapshot(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	result, modErr := s.escrow.GetSnapshot(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleEscrowListEvents(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "too many parameters", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	var raw json.RawMessage
	if len(req.Params) == 1 {
		raw = req.Params[0]
	}
	result, modErr := s.escrow.ListEvents(raw)
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxPreviewSponsorship(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction parameter required", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.PreviewSponsorship(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxSetSponsorshipEnabled(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.SetSponsorshipEnabled(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxGetSponsorshipConfig(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.SponsorshipConfig()
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func balanceResponseFromAccount(addr string, account *types.Account) BalanceResponse {
	resp := BalanceResponse{
		Address:         addr,
		BalanceNHB:      account.BalanceNHB,
		BalanceZNHB:     account.BalanceZNHB,
		Stake:           account.Stake,
		Username:        account.Username,
		Nonce:           account.Nonce,
		EngagementScore: account.EngagementScore,
	}
	if account.LockedZNHB != nil {
		resp.LockedZNHB = account.LockedZNHB
	}
	if len(account.DelegatedValidator) > 0 {
		resp.DelegatedValidator = crypto.MustNewAddress(crypto.NHBPrefix, account.DelegatedValidator).String()
	}
	if len(account.PendingUnbonds) > 0 {
		resp.PendingUnbonds = make([]StakeUnbondResponse, len(account.PendingUnbonds))
		for i, entry := range account.PendingUnbonds {
			validator := ""
			if len(entry.Validator) > 0 {
				validator = crypto.MustNewAddress(crypto.NHBPrefix, entry.Validator).String()
			}
			amount := big.NewInt(0)
			if entry.Amount != nil {
				amount = new(big.Int).Set(entry.Amount)
			}
			resp.PendingUnbonds[i] = StakeUnbondResponse{
				ID:          entry.ID,
				Validator:   validator,
				Amount:      amount,
				ReleaseTime: entry.ReleaseTime,
			}
		}
	}
	return resp
}

func (s *Server) handleGetBalance(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(addrStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to decode address", err.Error())
		return
	}
	account, err := s.node.GetAccount(addr.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	resp := balanceResponseFromAccount(addrStr, account)
	writeResult(w, req.ID, resp)
}
