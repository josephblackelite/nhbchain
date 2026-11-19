package rpc

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A JWT verifier that cannot be built (for example because its secret variable
// is empty) used to be stored and to surface only as a 401 on the first protected
// call, so the node started with its authentication silently broken. NewServer
// now refuses to build a server in that state unless mutual TLS is the
// authentication in use (PL-DC-32).
func TestNewServerRefusesAJWTVerifierThatCannotBeBuilt(t *testing.T) {
	const envVar = "RPC_TEST_UNSET_JWT_SECRET"
	t.Setenv(envVar, "")
	cfg := ServerConfig{JWT: JWTConfig{
		Enable:         true,
		Alg:            "HS256",
		HSSecretEnv:    envVar,
		Issuer:         "rpc-tests",
		Audience:       []string{"unit-tests"},
		MaxSkewSeconds: 60,
	}}
	srv, err := NewServer(nil, nil, cfg)
	if err == nil {
		t.Fatalf("NewServer built a server (%T) whose JWT verifier is broken", srv)
	}
	if !strings.Contains(err.Error(), envVar) {
		t.Fatalf("the startup error must name the empty variable so the operator can fix it, got %q", err)
	}
}

func TestNewServerRefusesJWTWithoutIssuerOrAudience(t *testing.T) {
	t.Setenv(testJWTEnvVar, testJWTSecret)
	for name, cfg := range map[string]JWTConfig{
		"no issuer":   {Enable: true, Alg: "HS256", HSSecretEnv: testJWTEnvVar, Audience: []string{"a"}},
		"no audience": {Enable: true, Alg: "HS256", HSSecretEnv: testJWTEnvVar, Issuer: "i"},
		"no secret":   {Enable: true, Alg: "HS256", Issuer: "i", Audience: []string{"a"}},
		"bad alg":     {Enable: true, Alg: "none", Issuer: "i", Audience: []string{"a"}},
	} {
		if _, err := NewServer(nil, nil, ServerConfig{JWT: cfg}); err == nil {
			t.Errorf("%s: NewServer must refuse the configuration", name)
		}
	}
}

// With mutual TLS configured, a broken JWT verifier does not stop the node (the
// client certificate is the authentication), but a request without a certificate
// is answered without revealing the name of the server's secret variable.
func TestBrokenJWTVerifierWithMutualTLSDoesNotNameTheSecretVariable(t *testing.T) {
	s := &Server{jwtVerifierErr: errJWTTestBroken}
	_, rpcErr := s.requireAuth(httptest.NewRequest("POST", "/", nil))
	if rpcErr == nil {
		t.Fatalf("a request with no credential must be refused")
	}
	if rpcErr.Data != nil {
		t.Fatalf("the refusal must not carry the verifier error to an unauthenticated caller, got %v", rpcErr.Data)
	}
}

var errJWTTestBroken = errString("JWT secret environment variable SECRET_VAR_NAME is empty")

type errString string

func (e errString) Error() string { return string(e) }

// With a client CA configured, the broken verifier is not a startup error: the
// refusal must not come from the JWT check. (NewServer is given no node here, so
// anything that happens after the JWT check may fail or panic; only the JWT
// check's own error is looked for.)
func TestNewServerWithMutualTLSIsNotStoppedByABrokenJWTVerifier(t *testing.T) {
	const envVar = "RPC_TEST_UNSET_JWT_SECRET_MTLS"
	t.Setenv(envVar, "")
	cfg := ServerConfig{
		TLSClientCAFile: "client-ca.pem",
		JWT:             JWTConfig{Enable: true, Alg: "HS256", HSSecretEnv: envVar, Issuer: "i", Audience: []string{"a"}},
	}
	var err error
	func() {
		defer func() { _ = recover() }()
		_, err = NewServer(nil, nil, cfg)
	}()
	if err != nil && strings.Contains(err.Error(), "verifier cannot be built") {
		t.Fatalf("mutual TLS is configured, so the JWT verifier error must not stop the server: %v", err)
	}
}
