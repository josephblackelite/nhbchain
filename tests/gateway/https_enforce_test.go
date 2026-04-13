package gateway_test

import (
	"net/url"
	"testing"

	gatewayconfig "nhbchain/gateway/config"
)

func TestEnforceSecureSchemeRejectsHTTPInProd(t *testing.T) {
	raw, err := url.Parse("http://node.internal:26657")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if _, _, err := gatewayconfig.EnforceSecureScheme("prod", raw, false); err == nil {
		t.Fatalf("expected rejection for plaintext endpoint in prod")
	}
}

func TestEnforceSecureSchemeAllowsDevHTTP(t *testing.T) {
	raw, err := url.Parse("http://localhost:26657")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	secured, upgraded, err := gatewayconfig.EnforceSecureScheme("dev", raw, false)
	if err != nil {
		t.Fatalf("enforce scheme: %v", err)
	}
	if upgraded {
		t.Fatalf("expected no upgrade in dev environment")
	}
	if secured.Scheme != "http" {
		t.Fatalf("expected http scheme, got %s", secured.Scheme)
	}
}

func TestEnforceSecureSchemeUpgradesWhenEnabled(t *testing.T) {
	raw, err := url.Parse("http://node.internal:26657")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	secured, upgraded, err := gatewayconfig.EnforceSecureScheme("prod", raw, true)
	if err != nil {
		t.Fatalf("enforce scheme: %v", err)
	}
	if !upgraded {
		t.Fatalf("expected upgrade flag")
	}
	if secured.Scheme != "https" {
		t.Fatalf("expected https scheme, got %s", secured.Scheme)
	}
}

func TestEnforceSecureSchemeAcceptsHTTPS(t *testing.T) {
	raw, err := url.Parse("https://rpc.nhbchain.com")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	secured, upgraded, err := gatewayconfig.EnforceSecureScheme("prod", raw, false)
	if err != nil {
		t.Fatalf("enforce scheme: %v", err)
	}
	if upgraded {
		t.Fatalf("did not expect upgrade for HTTPS endpoint")
	}
	if secured.String() != raw.String() {
		t.Fatalf("expected same URL, got %s", secured.String())
	}
}
