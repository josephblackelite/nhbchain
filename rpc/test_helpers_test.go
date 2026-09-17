package rpc

import (
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/core"
	gatewayauth "nhbchain/gateway/auth"
)

const testJWTEnvVar = "RPC_TEST_JWT_SECRET"
const testJWTSecret = "rpc-test-secret"

func newTestServer(t testing.TB, node *core.Node, net NetworkService, cfg ServerConfig) *Server {
	t.Helper()
	if !cfg.JWT.Enable && strings.TrimSpace(cfg.TLSClientCAFile) == "" {
		t.Setenv(testJWTEnvVar, testJWTSecret)
		cfg.JWT = JWTConfig{
			Enable:         true,
			Alg:            "HS256",
			HSSecretEnv:    testJWTEnvVar,
			Issuer:         "rpc-tests",
			Audience:       []string{"unit-tests"},
			MaxSkewSeconds: 60,
		}
	}
	if len(cfg.SwapAuth.Secrets) > 0 && cfg.SwapAuth.Persistence == nil {
		dir := t.TempDir()
		store, err := gatewayauth.NewLevelDBNoncePersistence(filepath.Join(dir, "nonces"))
		if err != nil {
			t.Fatalf("new swap persistence: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		cfg.SwapAuth.Persistence = store
	}
	srv, err := NewServer(node, net, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}
