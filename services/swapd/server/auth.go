package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// AuthConfig configures bearer token and mTLS authentication options.
type AuthConfig struct {
	BearerToken string
	AllowMTLS   bool
}

// Authenticator verifies admin requests before they reach handlers.
type Authenticator struct {
	bearerToken string
	allowBearer bool
	allowMTLS   bool
}

// Principal describes an authenticated actor accessing the admin API.
type Principal struct {
	Method string
}

type principalContextKey struct{}

// PrincipalFromContext extracts the authenticated principal from the request context.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	if ctx == nil {
		return nil, false
	}
	principal, ok := ctx.Value(principalContextKey{}).(*Principal)
	if !ok || principal == nil {
		return nil, false
	}
	return principal, true
}

// NewAuthenticator constructs an authenticator from configuration.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	token := strings.TrimSpace(cfg.BearerToken)
	allowBearer := token != ""
	allowMTLS := cfg.AllowMTLS
	if !allowBearer && !allowMTLS {
		return nil, fmt.Errorf("at least one authentication mechanism must be configured")
	}
	return &Authenticator{bearerToken: token, allowBearer: allowBearer, allowMTLS: allowMTLS}, nil
}

// Middleware enforces authentication for admin endpoints.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			http.Error(w, "authentication unavailable", http.StatusInternalServerError)
			return
		}
		principal, ok := a.authenticate(r)
		if ok {
			ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

func (a *Authenticator) authenticate(r *http.Request) (*Principal, bool) {
	if a == nil {
		return nil, false
	}
	if a.allowBearer {
		if principal := a.authenticateByBearer(r); principal != nil {
			return principal, true
		}
	}
	if a.allowMTLS {
		if principal := a.authenticateByMTLS(r); principal != nil {
			return principal, true
		}
	}
	return nil, false
}

func (a *Authenticator) authenticateByBearer(r *http.Request) *Principal {
	if r == nil {
		return nil
	}
	header := r.Header.Get("Authorization")
	token := parseBearerToken(header)
	if token == "" {
		return nil
	}
	provided := []byte(token)
	expected := []byte(a.bearerToken)
	if subtle.ConstantTimeCompare(provided, expected) != 1 {
		return nil
	}
	return &Principal{Method: "bearer"}
}

func (a *Authenticator) authenticateByMTLS(r *http.Request) *Principal {
	if r == nil {
		return nil
	}
	state := r.TLS
	if state == nil {
		return nil
	}
	if len(state.VerifiedChains) > 0 {
		return &Principal{Method: "mtls"}
	}
	if len(state.PeerCertificates) > 0 && state.HandshakeComplete {
		return &Principal{Method: "mtls"}
	}
	return nil
}

func parseBearerToken(header string) string {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
