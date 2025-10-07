package server

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
