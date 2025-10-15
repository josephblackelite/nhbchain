package server

import (
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
		if a.authenticate(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

func (a *Authenticator) authenticate(r *http.Request) bool {
	if a == nil {
		return false
	}
	if a.allowBearer && a.authenticateByBearer(r) {
		return true
	}
	if a.allowMTLS && a.authenticateByMTLS(r) {
		return true
	}
	return false
}

func (a *Authenticator) authenticateByBearer(r *http.Request) bool {
	if r == nil {
		return false
	}
	header := r.Header.Get("Authorization")
	token := parseBearerToken(header)
	if token == "" {
		return false
	}
	provided := []byte(token)
	expected := []byte(a.bearerToken)
	return subtle.ConstantTimeCompare(provided, expected) == 1
}

func (a *Authenticator) authenticateByMTLS(r *http.Request) bool {
	if r == nil {
		return false
	}
	state := r.TLS
	if state == nil {
		return false
	}
	if len(state.VerifiedChains) > 0 {
		return true
	}
	if len(state.PeerCertificates) > 0 && state.HandshakeComplete {
		return true
	}
	return false
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
