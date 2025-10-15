package auth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Context keys for storing authenticated user information.
type contextKey string

const (
	contextKeyClaims contextKey = "jwt_claims"
	contextKeyUserID contextKey = "user_id"
	contextKeyRole   contextKey = "user_role"
)

// Role represents an authorized persona within the OTC system.
type Role string

// Supported roles for the OTC service.
const (
	RoleTeller       Role = "teller"
	RoleSupervisor   Role = "supervisor"
	RoleCompliance   Role = "compliance"
	RoleSuperAdmin   Role = "superadmin"
	RoleAuditor      Role = "auditor"
	RolePartner      Role = "partner"
	RolePartnerAdmin Role = "partneradmin"
	RoleRootAdmin    Role = "rootadmin"
)

var allowedRoles = map[Role]struct{}{
	RoleTeller:       {},
	RoleSupervisor:   {},
	RoleCompliance:   {},
	RoleSuperAdmin:   {},
	RoleAuditor:      {},
	RolePartner:      {},
	RolePartnerAdmin: {},
	RoleRootAdmin:    {},
}

var (
	rootAdminSubjects = map[string]struct{}{}
	rootAdminMu       sync.RWMutex
)

// SetRootAdmins configures the allowlist of identities permitted to assume the root admin role.
func SetRootAdmins(subjects []string) {
	rootAdminMu.Lock()
	defer rootAdminMu.Unlock()
	rootAdminSubjects = make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		trimmed := strings.TrimSpace(subject)
		if trimmed == "" {
			continue
		}
		rootAdminSubjects[trimmed] = struct{}{}
	}
}

// IsRootAdmin reports whether the provided subject is in the root admin allowlist.
func IsRootAdmin(subject string) bool {
	rootAdminMu.RLock()
	defer rootAdminMu.RUnlock()
	_, ok := rootAdminSubjects[strings.TrimSpace(subject)]
	return ok
}

// Claims represents identity data extracted from the inbound request.
type Claims struct {
	Subject    string
	Role       Role
	Token      *jwt.Token
	Attributes jwt.MapClaims
}

// SecretProvider exposes a lookup for external secrets such as Vault or cloud KMS stores.
type SecretProvider interface {
	GetSecret(ctx context.Context, name string) (string, error)
}

// SecretProviderFunc adapts a function to the SecretProvider interface.
type SecretProviderFunc func(context.Context, string) (string, error)

// GetSecret implements SecretProvider.
func (f SecretProviderFunc) GetSecret(ctx context.Context, name string) (string, error) {
	return f(ctx, name)
}

// JWTOptions controls signature verification and claim handling.
type JWTOptions struct {
	Enable             bool
	Alg                string
	Issuer             string
	Audience           []string
	MaxSkewSeconds     int
	HSSecretEnv        string
	HSSecretName       string
	RSAPublicKeyFile   string
	RSAPublicKeySecret string
	RoleClaim          string
	RoleMap            map[string]Role
	RefreshInterval    time.Duration
}

// WebAuthnOptions configures WebAuthn attestation requirements.
type WebAuthnOptions struct {
	Enable          bool
	Endpoint        string
	Timeout         time.Duration
	APIKeyEnv       string
	APIKeySecret    string
	RPID            string
	Origin          string
	AssertionHeader string
	RequireRoles    []Role
	APIKeyRefresh   time.Duration
}

// MiddlewareConfig bundles dependencies for constructing the authenticator middleware.
type MiddlewareConfig struct {
	JWT               JWTOptions
	WebAuthn          WebAuthnOptions
	RootAdminSubjects []string
	SecretProvider    SecretProvider
	WebAuthnVerifier  WebAuthnVerifier
}

// Middleware provides HTTP middleware that enforces JWT + WebAuthn authentication.
type Middleware struct {
	jwtVerifier          *jwtVerifier
	webAuthnVerifier     WebAuthnVerifier
	assertionHeader      string
	requireWebAuthnRoles map[Role]struct{}
	closers              []func() error
}

// NewMiddleware constructs a Middleware using the supplied configuration.
func NewMiddleware(cfg MiddlewareConfig) (*Middleware, error) {
	if !cfg.JWT.Enable {
		return nil, errors.New("JWT authentication must be enabled")
	}

	SetRootAdmins(cfg.RootAdminSubjects)

	verifier, err := newJWTVerifier(cfg.JWT, cfg.SecretProvider)
	if err != nil {
		return nil, err
	}

	mw := &Middleware{jwtVerifier: verifier}
	mw.closers = append(mw.closers, verifier.Close)

	if cfg.WebAuthnVerifier != nil {
		mw.webAuthnVerifier = cfg.WebAuthnVerifier
	} else if cfg.WebAuthn.Enable {
		webVerifier, err := newRemoteWebAuthnVerifier(cfg.WebAuthn, cfg.SecretProvider)
		if err != nil {
			return nil, err
		}
		mw.webAuthnVerifier = webVerifier
		if closer, ok := webVerifier.(interface{ Close() error }); ok {
			mw.closers = append(mw.closers, closer.Close)
		}
	}

	header := strings.TrimSpace(cfg.WebAuthn.AssertionHeader)
	if header == "" {
		header = "X-WebAuthn-Attestation"
	}
	mw.assertionHeader = header

	if len(cfg.WebAuthn.RequireRoles) > 0 {
		mw.requireWebAuthnRoles = make(map[Role]struct{}, len(cfg.WebAuthn.RequireRoles))
		for _, role := range cfg.WebAuthn.RequireRoles {
			mw.requireWebAuthnRoles[role] = struct{}{}
		}
	}

	return mw, nil
}

// Close releases any background resources associated with the middleware.
func (m *Middleware) Close() error {
	if m == nil {
		return nil
	}
	var errs []string
	for _, closer := range m.closers {
		if closer == nil {
			continue
		}
		if err := closer(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("auth middleware shutdown: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Middleware applies JWT and WebAuthn enforcement before invoking the next handler.
func (m *Middleware) Middleware(next http.Handler) http.Handler {
	if m == nil {
		panic("auth middleware is nil")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := strings.TrimSpace(r.Header.Get("Authorization"))
		if authz == "" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authz, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			http.Error(w, "invalid authorization scheme", http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(parts[1])
		if token == "" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		claims, err := m.jwtVerifier.Verify(token)
		if err != nil {
			http.Error(w, "invalid authorization token", http.StatusUnauthorized)
			return
		}

		if m.requiresWebAuthn(claims.Role) {
			assertion := strings.TrimSpace(r.Header.Get(m.assertionHeader))
			if assertion == "" {
				http.Error(w, "webauthn attestation required", http.StatusUnauthorized)
				return
			}
			if err := m.webAuthnVerifier.Verify(r.Context(), claims, assertion); err != nil {
				http.Error(w, "webauthn verification failed", http.StatusUnauthorized)
				return
			}
		}

		ctx := context.WithValue(r.Context(), contextKeyClaims, claims)
		ctx = context.WithValue(ctx, contextKeyUserID, claims.Subject)
		ctx = context.WithValue(ctx, contextKeyRole, string(claims.Role))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Middleware) requiresWebAuthn(role Role) bool {
	if m.webAuthnVerifier == nil {
		return false
	}
	if len(m.requireWebAuthnRoles) == 0 {
		return true
	}
	_, ok := m.requireWebAuthnRoles[role]
	return ok
}

// FromContext extracts the Claims previously attached by Authenticate middleware.
func FromContext(ctx context.Context) (*Claims, error) {
	if ctx == nil {
		return nil, errors.New("missing context")
	}
	if claims, ok := ctx.Value(contextKeyClaims).(*Claims); ok && claims != nil {
		return claims, nil
	}
	userID, ok := ctx.Value(contextKeyUserID).(string)
	if !ok || userID == "" {
		return nil, errors.New("missing user id in context")
	}
	roleStr, ok := ctx.Value(contextKeyRole).(string)
	if !ok || roleStr == "" {
		return nil, errors.New("missing role in context")
	}
	return &Claims{Subject: userID, Role: Role(roleStr)}, nil
}

// RequireRole ensures the authenticated user has at least one of the allowed roles.
func RequireRole(roles ...Role) func(http.Handler) http.Handler {
	allowed := make(map[Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := FromContext(r.Context())
			if err != nil {
				http.Error(w, "missing identity", http.StatusUnauthorized)
				return
			}
			if _, ok := allowed[claims.Role]; !ok {
				http.Error(w, "insufficient role", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type jwtVerifier struct {
	method    jwt.SigningMethod
	keySource *refreshableSecret
	issuer    string
	audience  []string
	leeway    time.Duration
	roleClaim string
	roleMap   map[string]Role
	now       func() time.Time
}

func newJWTVerifier(cfg JWTOptions, secrets SecretProvider) (*jwtVerifier, error) {
	method := strings.ToUpper(strings.TrimSpace(cfg.Alg))
	if method == "" {
		method = jwt.SigningMethodHS256.Alg()
	}

	issuer := strings.TrimSpace(cfg.Issuer)
	if issuer == "" {
		return nil, errors.New("JWT issuer is required")
	}
	if len(cfg.Audience) == 0 {
		return nil, errors.New("at least one JWT audience is required")
	}
	audiences := make([]string, 0, len(cfg.Audience))
	for _, aud := range cfg.Audience {
		trimmed := strings.TrimSpace(aud)
		if trimmed != "" {
			audiences = append(audiences, trimmed)
		}
	}
	if len(audiences) == 0 {
		return nil, errors.New("JWT audience entries must be non-empty")
	}

	roleClaim := strings.TrimSpace(cfg.RoleClaim)
	if roleClaim == "" {
		roleClaim = "role"
	}

	var signingMethod jwt.SigningMethod
	var keySource *refreshableSecret
	refresh := cfg.RefreshInterval
	switch method {
	case jwt.SigningMethodHS256.Alg():
		fetch := func(ctx context.Context) (interface{}, error) {
			secret, err := resolveSecret(ctx, cfg.HSSecretName, cfg.HSSecretEnv, secrets)
			if err != nil {
				return nil, err
			}
			if secret == "" {
				return nil, errors.New("HS256 secret must not be empty")
			}
			return []byte(secret), nil
		}
		source, err := newRefreshableSecret(context.Background(), refresh, fetch)
		if err != nil {
			return nil, fmt.Errorf("resolve HS256 secret: %w", err)
		}
		signingMethod = jwt.SigningMethodHS256
		keySource = source
	case jwt.SigningMethodRS256.Alg():
		fetch := func(ctx context.Context) (interface{}, error) {
			pub, err := resolveRSAPublicKey(ctx, cfg.RSAPublicKeySecret, cfg.RSAPublicKeyFile, secrets)
			if err != nil {
				return nil, err
			}
			return pub, nil
		}
		source, err := newRefreshableSecret(context.Background(), refresh, fetch)
		if err != nil {
			return nil, fmt.Errorf("resolve RS256 public key: %w", err)
		}
		signingMethod = jwt.SigningMethodRS256
		keySource = source
	default:
		return nil, fmt.Errorf("unsupported JWT algorithm %q", method)
	}

	leeway := time.Duration(cfg.MaxSkewSeconds) * time.Second
	if cfg.MaxSkewSeconds <= 0 {
		leeway = 30 * time.Second
	}

	roleMap := make(map[string]Role, len(cfg.RoleMap))
	for raw, mapped := range cfg.RoleMap {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		if normalized == "" {
			continue
		}
		roleMap[normalized] = mapped
	}

	return &jwtVerifier{
		method:    signingMethod,
		keySource: keySource,
		issuer:    issuer,
		audience:  audiences,
		leeway:    leeway,
		roleClaim: roleClaim,
		roleMap:   roleMap,
		now:       time.Now,
	}, nil
}

func (v *jwtVerifier) Verify(token string) (*Claims, error) {
	if v == nil {
		return nil, errors.New("JWT verifier not configured")
	}
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{v.method.Alg()}), jwt.WithIssuer(v.issuer)}
	if v.leeway > 0 {
		opts = append(opts, jwt.WithLeeway(v.leeway))
	}
	if v.now != nil {
		opts = append(opts, jwt.WithTimeFunc(func() time.Time { return v.now() }))
	}

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return v.signingKey()
	}, opts...)
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("token validation failed")
	}

	subject := ""
	if sub, ok := claims["sub"].(string); ok {
		subject = strings.TrimSpace(sub)
	}
	if subject == "" {
		return nil, errors.New("token subject missing")
	}

	if len(v.audience) > 0 {
		tokenAud := extractStringSlice(claims["aud"])
		if len(tokenAud) == 0 {
			return nil, errors.New("token audience missing")
		}
		matched := false
		for _, expected := range v.audience {
			for _, actual := range tokenAud {
				if strings.EqualFold(actual, expected) {
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

	role, err := v.extractRole(claims)
	if err != nil {
		return nil, err
	}
	if role == RoleRootAdmin && !IsRootAdmin(subject) {
		return nil, errors.New("root admin not allowlisted")
	}

	return &Claims{
		Subject:    subject,
		Role:       role,
		Token:      parsed,
		Attributes: claims,
	}, nil
}

func (v *jwtVerifier) signingKey() (interface{}, error) {
	if v == nil || v.keySource == nil {
		return nil, errors.New("signing key unavailable")
	}
	key := v.keySource.Value()
	if key == nil {
		return nil, errors.New("signing key not loaded")
	}
	return key, nil
}

func (v *jwtVerifier) Close() error {
	if v == nil || v.keySource == nil {
		return nil
	}
	return v.keySource.Close()
}

func (v *jwtVerifier) extractRole(claims jwt.MapClaims) (Role, error) {
	candidates := extractStringSlice(claims[v.roleClaim])
	if len(candidates) == 0 && v.roleClaim != "roles" {
		candidates = extractStringSlice(claims["roles"])
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("missing role claim %q", v.roleClaim)
	}
	for _, candidate := range candidates {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if normalized == "" {
			continue
		}
		if mapped, ok := v.roleMap[normalized]; ok {
			if _, allowed := allowedRoles[mapped]; allowed {
				return mapped, nil
			}
			return "", fmt.Errorf("mapped role %s is not permitted", mapped)
		}
		role := Role(normalized)
		if _, ok := allowedRoles[role]; ok {
			return role, nil
		}
	}
	return "", errors.New("no permitted roles found in token claims")
}

func extractStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	case []string:
		out := make([]string, 0, len(v))
		for _, entry := range v {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, entry := range v {
			if s, ok := entry.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func resolveSecret(ctx context.Context, secretName, envKey string, provider SecretProvider) (string, error) {
	if secretName != "" {
		if provider == nil {
			return "", fmt.Errorf("secret provider required for secret %s", secretName)
		}
		secret, err := provider.GetSecret(ctx, secretName)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(secret), nil
	}
	if envKey == "" {
		return "", nil
	}
	value := strings.TrimSpace(os.Getenv(envKey))
	if value == "" {
		return "", fmt.Errorf("environment variable %s is empty", envKey)
	}
	return value, nil
}

func resolveRSAPublicKey(ctx context.Context, secretName, filePath string, provider SecretProvider) (*rsa.PublicKey, error) {
	var pemData []byte
	if secretName != "" {
		if provider == nil {
			return nil, fmt.Errorf("secret provider required for secret %s", secretName)
		}
		secret, err := provider.GetSecret(ctx, secretName)
		if err != nil {
			return nil, err
		}
		pemData = []byte(secret)
	} else {
		path := strings.TrimSpace(filePath)
		if path == "" {
			return nil, errors.New("RSA public key file path is empty")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		pemData = data
	}
	for {
		block, rest := pem.Decode(pemData)
		if block == nil {
			break
		}
		pemData = rest
		switch block.Type {
		case "PUBLIC KEY":
			pub, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse RSA public key: %w", err)
			}
			rsaKey, ok := pub.(*rsa.PublicKey)
			if !ok {
				return nil, errors.New("parsed key is not RSA")
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

type refreshableSecret struct {
	value    atomic.Value
	fetch    func(context.Context) (interface{}, error)
	refresh  time.Duration
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newRefreshableSecret(ctx context.Context, refresh time.Duration, fetch func(context.Context) (interface{}, error)) (*refreshableSecret, error) {
	if fetch == nil {
		return nil, errors.New("refreshable secret requires fetch function")
	}
	initial, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	rs := &refreshableSecret{fetch: fetch, refresh: refresh}
	rs.value.Store(initial)
	if refresh > 0 {
		rs.stopCh = make(chan struct{})
		rs.doneCh = make(chan struct{})
		go rs.loop()
	}
	return rs, nil
}

func (r *refreshableSecret) loop() {
	ticker := time.NewTicker(r.refresh)
	defer ticker.Stop()
	defer close(r.doneCh)
	for {
		select {
		case <-ticker.C:
			value, err := r.fetch(context.Background())
			if err != nil {
				log.Printf("auth: refresh secret failed: %v", err)
				continue
			}
			if value != nil {
				r.value.Store(value)
			}
		case <-r.stopCh:
			return
		}
	}
}

func (r *refreshableSecret) Value() interface{} {
	if r == nil {
		return nil
	}
	return r.value.Load()
}

func (r *refreshableSecret) Close() error {
	if r == nil || r.refresh <= 0 {
		return nil
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	<-r.doneCh
	return nil
}
