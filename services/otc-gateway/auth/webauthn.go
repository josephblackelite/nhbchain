package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// WebAuthnVerifier validates attestation assertions for privileged operations.
type WebAuthnVerifier interface {
	Verify(ctx context.Context, claims *Claims, assertion string) error
}

// WebAuthnVerifierFunc adapts a function to the WebAuthnVerifier interface.
type WebAuthnVerifierFunc func(context.Context, *Claims, string) error

// Verify implements WebAuthnVerifier.
func (f WebAuthnVerifierFunc) Verify(ctx context.Context, claims *Claims, assertion string) error {
	return f(ctx, claims, assertion)
}

type remoteWebAuthnVerifier struct {
	endpoint string
	apiKey   *refreshableSecret
	rpID     string
	origin   string
	client   *http.Client
}

func newRemoteWebAuthnVerifier(cfg WebAuthnOptions, secrets SecretProvider) (WebAuthnVerifier, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, errors.New("WebAuthn endpoint is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	var apiKeySource *refreshableSecret
	if cfg.APIKeySecret != "" {
		if secrets == nil {
			return nil, fmt.Errorf("secret provider required for secret %s", cfg.APIKeySecret)
		}
		fetch := func(ctx context.Context) (interface{}, error) {
			secret, err := secrets.GetSecret(ctx, cfg.APIKeySecret)
			if err != nil {
				return nil, err
			}
			return strings.TrimSpace(secret), nil
		}
		source, err := newRefreshableSecret(context.Background(), cfg.APIKeyRefresh, func(ctx context.Context) (interface{}, error) {
			value, err := fetch(ctx)
			if err != nil {
				return nil, fmt.Errorf("resolve WebAuthn API key: %w", err)
			}
			return value, nil
		})
		if err != nil {
			return nil, err
		}
		apiKeySource = source
	} else if cfg.APIKeyEnv != "" {
		fetch := func(context.Context) (interface{}, error) {
			return strings.TrimSpace(os.Getenv(cfg.APIKeyEnv)), nil
		}
		source, err := newRefreshableSecret(context.Background(), cfg.APIKeyRefresh, fetch)
		if err != nil {
			return nil, err
		}
		apiKeySource = source
	}

	rpID := strings.TrimSpace(cfg.RPID)
	if rpID == "" {
		return nil, errors.New("WebAuthn RPID is required")
	}

	return &remoteWebAuthnVerifier{
		endpoint: endpoint,
		apiKey:   apiKeySource,
		rpID:     rpID,
		origin:   strings.TrimSpace(cfg.Origin),
		client: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

type webAuthnRequest struct {
	Subject   string        `json:"subject"`
	Role      string        `json:"role"`
	Assertion string        `json:"assertion"`
	RPID      string        `json:"rpId"`
	Origin    string        `json:"origin,omitempty"`
	Claims    jwt.MapClaims `json:"claims,omitempty"`
}

func (v *remoteWebAuthnVerifier) Verify(ctx context.Context, claims *Claims, assertion string) error {
	if v == nil {
		return errors.New("webauthn verifier not configured")
	}
	payload := webAuthnRequest{
		Subject:   claims.Subject,
		Role:      string(claims.Role),
		Assertion: assertion,
		RPID:      v.rpID,
	}
	if v.origin != "" {
		payload.Origin = v.origin
	}
	if len(claims.Attributes) > 0 {
		payload.Claims = claims.Attributes
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode WebAuthn payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("create WebAuthn request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if v.apiKey != nil {
		if key, _ := v.apiKey.Value().(string); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("call WebAuthn verifier: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("webauthn verifier returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (v *remoteWebAuthnVerifier) Close() error {
	if v == nil || v.apiKey == nil {
		return nil
	}
	return v.apiKey.Close()
}
