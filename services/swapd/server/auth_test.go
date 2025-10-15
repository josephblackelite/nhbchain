package server

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticateByBearerValidToken(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "topsecret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.Header.Set("Authorization", "Bearer topsecret")
	if principal := auth.authenticateByBearer(request); principal == nil {
		t.Fatalf("expected token to be accepted")
	}
}

func TestAuthenticateByBearerInvalidToken(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "topsecret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.Header.Set("Authorization", "Bearer notsecret")
	if principal := auth.authenticateByBearer(request); principal != nil {
		t.Fatalf("expected token to be rejected")
	}
}

func TestAuthenticatorAuthenticateBearerTokens(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "topsecret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "valid token", header: "Bearer topsecret", want: true},
		{name: "invalid token", header: "Bearer notsecret", want: false},
		{name: "missing token", header: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/admin", nil)
			if tt.header != "" {
				request.Header.Set("Authorization", tt.header)
			}
			if _, got := auth.authenticate(request); got != tt.want {
				t.Fatalf("authenticate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthenticatorAllowsBearer(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.Header.Set("Authorization", "Bearer secret")
	called := false
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatalf("expected handler to be called")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
}

func TestAuthenticatorAllowsMTLS(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{AllowMTLS: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin", nil)
	request.TLS = &tls.ConnectionState{
		HandshakeComplete: true,
		PeerCertificates:  []*x509.Certificate{{}},
	}
	called := false
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(recorder, request)
	if !called {
		t.Fatalf("expected handler to be called")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
}

func TestAuthenticatorRejectsUnauthenticated(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "secret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler should not be called")
	}))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestNewAuthenticatorRequiresConfig(t *testing.T) {
	if _, err := NewAuthenticator(AuthConfig{}); err == nil {
		t.Fatalf("expected error when no auth mechanisms configured")
	}
}
