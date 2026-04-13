package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gatewayauth "nhbchain/gateway/auth"
)

// Partner captures partner metadata required for authentication and quotas.
type Partner struct {
	ID         string
	APIKey     string
	Secret     string
	DailyQuota int64
}

// PartnerPrincipal identifies the authenticated partner for the request.
type PartnerPrincipal struct {
	ID         string
	APIKey     string
	DailyQuota int64
}

type partnerContextKey struct{}

// PartnerFromContext extracts the partner principal from the context.
func PartnerFromContext(ctx context.Context) (*PartnerPrincipal, bool) {
	if ctx == nil {
		return nil, false
	}
	principal, ok := ctx.Value(partnerContextKey{}).(*PartnerPrincipal)
	if !ok || principal == nil {
		return nil, false
	}
	return principal, true
}

// PartnerAuthenticator wraps the gateway HMAC authenticator with partner lookups.
type PartnerAuthenticator struct {
	auth      *gatewayauth.Authenticator
	partners  map[string]Partner
	hydrateMu chan struct{}
}

// NewPartnerAuthenticator builds a PartnerAuthenticator from partner configuration.
func NewPartnerAuthenticator(partners []Partner, nowFn func() time.Time, persistence gatewayauth.NoncePersistence) (*PartnerAuthenticator, error) {
	if len(partners) == 0 {
		return nil, fmt.Errorf("at least one partner must be configured")
	}
	secrets := make(map[string]string, len(partners))
	keyed := make(map[string]Partner, len(partners))
	for _, partner := range partners {
		id := strings.TrimSpace(partner.ID)
		apiKey := strings.TrimSpace(partner.APIKey)
		secret := strings.TrimSpace(partner.Secret)
		if id == "" {
			return nil, fmt.Errorf("partner id required")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("partner api key required")
		}
		if secret == "" {
			return nil, fmt.Errorf("partner secret required")
		}
		if _, exists := secrets[apiKey]; exists {
			return nil, fmt.Errorf("duplicate partner api key: %s", apiKey)
		}
		if _, exists := keyed[apiKey]; exists {
			return nil, fmt.Errorf("duplicate partner entry: %s", apiKey)
		}
		secrets[apiKey] = secret
		keyed[apiKey] = Partner{ID: id, APIKey: apiKey, Secret: secret, DailyQuota: partner.DailyQuota}
	}
	authenticator := gatewayauth.NewAuthenticator(secrets, 0, 0, 0, nowFn, persistence)
	return &PartnerAuthenticator{
		auth:      authenticator,
		partners:  keyed,
		hydrateMu: make(chan struct{}, 1),
	}, nil
}

// Hydrate warms the HMAC nonce cache from persistence to avoid replay false positives.
func (p *PartnerAuthenticator) Hydrate(ctx context.Context) error {
	if p == nil || p.auth == nil {
		return nil
	}
	select {
	case p.hydrateMu <- struct{}{}:
		defer func() { <-p.hydrateMu }()
	default:
		// Another goroutine is already hydrating.
		return nil
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	return p.auth.HydrateNonces(ctx, cutoff)
}

// Middleware authenticates partner HMAC headers and adds the partner principal to the request context.
func (p *PartnerAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p == nil || p.auth == nil {
			http.Error(w, "partner authentication unavailable", http.StatusInternalServerError)
			return
		}
		body, err := readRequestBody(r)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errBodyTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, err.Error(), status)
			return
		}
		principal, err := p.authenticateRequest(r, body)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, errPartnerForbidden) {
				status = http.StatusForbidden
			}
			http.Error(w, err.Error(), status)
			return
		}
		ctx := context.WithValue(r.Context(), partnerContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var (
	errBodyTooLarge     = errors.New("request body exceeds signature limit")
	errPartnerForbidden = errors.New("partner not allowed")
)

func readRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	original := r.Body
	limited := io.LimitReader(original, int64(gatewayauth.MaxBodyForSignature)+1)
	data, err := io.ReadAll(limited)
	original.Close()
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(data) > gatewayauth.MaxBodyForSignature {
		return nil, errBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

func (p *PartnerAuthenticator) authenticateRequest(r *http.Request, body []byte) (*PartnerPrincipal, error) {
	principal, err := p.auth.Authenticate(r, body)
	if err != nil {
		if strings.Contains(err.Error(), "unknown API key") {
			return nil, errPartnerForbidden
		}
		return nil, err
	}
	partner, ok := p.partners[principal.APIKey]
	if !ok {
		return nil, errPartnerForbidden
	}
	providedSig := strings.TrimSpace(r.Header.Get(gatewayauth.HeaderSignature))
	if providedSig == "" {
		return nil, errors.New("missing signature")
	}
	if _, err := hex.DecodeString(providedSig); err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}
	return &PartnerPrincipal{ID: partner.ID, APIKey: partner.APIKey, DailyQuota: partner.DailyQuota}, nil
}
