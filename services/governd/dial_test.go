package main

import (
	"path/filepath"
	"testing"

	"nhbchain/services/governd/config"
)

func TestConsensusDialOptionsRequireSecurity(t *testing.T) {
	if _, err := consensusDialOptions(config.ClientConfig{}); err == nil {
		t.Fatalf("expected consensus dial configuration to fail without security settings")
	}
}

func TestConsensusDialOptionsAllowInsecureOverride(t *testing.T) {
	opts, err := consensusDialOptions(config.ClientConfig{AllowInsecure: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected insecure dial option to be returned")
	}
}

func TestConsensusDialOptionsWithTLS(t *testing.T) {
	caPath := filepath.Join("config", "server.crt")
	opts, err := consensusDialOptions(config.ClientConfig{
		TLS: config.ClientTLSConfig{CAPath: caPath},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected tls dial option to be returned")
	}
}

func TestConsensusDialOptionsWithSharedSecret(t *testing.T) {
	opts, err := consensusDialOptions(config.ClientConfig{
		SharedSecret: config.SharedSecretConfig{Header: "x-test", Token: "value"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected dial options to include shared-secret credentials")
	}
}
