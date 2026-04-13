package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const middlewareTestSecret = "otc-middleware-secret"

func TestMiddlewareAcceptsValidJWTAndWebAuthn(t *testing.T) {
	now := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	mw := mustNewTestMiddleware(t, now, nil)
	defer func() { _ = mw.Close() }()

	token := signMiddlewareJWT(t, now, now.Add(time.Minute), RoleTeller)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-WebAuthn-Attestation", "attestation-ok")

	recorder := httptest.NewRecorder()
	mw.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTeapot {
		t.Fatalf("expected middleware to allow request, got %d", recorder.Code)
	}
}

func TestMiddlewareRejectsExpiredJWT(t *testing.T) {
	now := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	mw := mustNewTestMiddleware(t, now, nil)
	defer func() { _ = mw.Close() }()

	token := signMiddlewareJWT(t, now.Add(-2*time.Minute), now.Add(-time.Minute), RoleTeller)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-WebAuthn-Attestation", "attestation-ok")

	recorder := httptest.NewRecorder()
	mw.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("expired token should not reach handler")
	})).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized for expired token, got %d", recorder.Code)
	}
}

func TestMiddlewareRequiresWebAuthnForConfiguredRoles(t *testing.T) {
	now := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)
	mw := mustNewTestMiddleware(t, now, []Role{RoleSupervisor})
	defer func() { _ = mw.Close() }()

	token := signMiddlewareJWT(t, now, now.Add(time.Minute), RoleSupervisor)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	recorder := httptest.NewRecorder()
	mw.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("request without WebAuthn should not be processed")
	})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized when WebAuthn header missing, got %d", recorder.Code)
	}
}

func mustNewTestMiddleware(t *testing.T, now time.Time, requireRoles []Role) *Middleware {
	t.Helper()
	verifier := WebAuthnVerifierFunc(func(ctx context.Context, claims *Claims, assertion string) error {
		if assertion != "attestation-ok" {
			return fmt.Errorf("unexpected assertion %q", assertion)
		}
		return nil
	})
	provider := SecretProviderFunc(func(ctx context.Context, name string) (string, error) {
		if name != "jwt/primary" {
			return "", fmt.Errorf("unexpected secret %q", name)
		}
		return middlewareTestSecret, nil
	})
	mw, err := NewMiddleware(MiddlewareConfig{
		JWT: JWTOptions{
			Enable:         true,
			Alg:            "HS256",
			Issuer:         "otc-tests",
			Audience:       []string{"otc"},
			HSSecretName:   "jwt/primary",
			MaxSkewSeconds: 30,
		},
		WebAuthn: WebAuthnOptions{
			Enable:          true,
			AssertionHeader: "X-WebAuthn-Attestation",
			RequireRoles:    requireRoles,
		},
		SecretProvider:   provider,
		WebAuthnVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("new middleware: %v", err)
	}
	if mw.jwtVerifier != nil {
		mw.jwtVerifier.now = func() time.Time { return now }
	}
	return mw
}

func signMiddlewareJWT(t *testing.T, notBefore, expires time.Time, role Role) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":  "otc-tests",
		"sub":  "user-123",
		"aud":  []string{"otc"},
		"nbf":  notBefore.Unix(),
		"iat":  notBefore.Unix(),
		"exp":  expires.Unix(),
		"role": string(role),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(middlewareTestSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}
