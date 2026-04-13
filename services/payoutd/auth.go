package payoutd

import (
	"fmt"
	"net/http"
	"strings"
)

// AuthConfig describes admin authentication options.
type AuthConfig struct {
	BearerToken string
	AllowMTLS   bool
}

// Authenticator validates incoming admin requests.
type Authenticator struct {
	bearerToken string
	allowBearer bool
	allowMTLS   bool
}

// NewAuthenticator constructs an Authenticator from configuration.
func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	token := strings.TrimSpace(cfg.BearerToken)
	allowBearer := token != ""
	allowMTLS := cfg.AllowMTLS
	if !allowBearer && !allowMTLS {
		return nil, fmt.Errorf("at least one authentication mechanism must be configured")
	}
	return &Authenticator{bearerToken: token, allowBearer: allowBearer, allowMTLS: allowMTLS}, nil
}

// Middleware enforces authentication for admin handlers.
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
	token := parseBearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return false
	}
	return token == a.bearerToken
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
