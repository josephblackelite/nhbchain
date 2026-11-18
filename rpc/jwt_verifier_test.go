package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signVerifierToken signs the given claims with the test HMAC secret, so each
// case below controls exactly which registered claims the token carries.
func signVerifierToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

// The node's bearer-token verifier must refuse a token that carries no expiry,
// since such a token would never lapse, and must keep refusing a token that has
// expired or whose audience is absent or wrong.
func TestJWTVerifierRequiresExpiryAndAudience(t *testing.T) {
	t.Setenv(testJWTEnvVar, testJWTSecret)
	verifier, err := newJWTVerifier(JWTConfig{
		Enable:         true,
		Alg:            "HS256",
		HSSecretEnv:    testJWTEnvVar,
		Issuer:         "rpc-tests",
		Audience:       []string{"unit-tests"},
		MaxSkewSeconds: 60,
	})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	verifier.now = func() time.Time { return now }

	tests := []struct {
		name    string
		claims  jwt.MapClaims
		wantErr string
	}{
		{
			name: "valid token with expiry",
			claims: jwt.MapClaims{
				"iss": "rpc-tests",
				"aud": []string{"unit-tests"},
				"iat": now.Unix(),
				"exp": now.Add(time.Hour).Unix(),
			},
		},
		{
			name: "no expiry",
			claims: jwt.MapClaims{
				"iss": "rpc-tests",
				"aud": []string{"unit-tests"},
				"iat": now.Unix(),
			},
			wantErr: "exp claim is required",
		},
		{
			name: "expired beyond the allowed skew",
			claims: jwt.MapClaims{
				"iss": "rpc-tests",
				"aud": []string{"unit-tests"},
				"iat": now.Add(-2 * time.Hour).Unix(),
				"exp": now.Add(-time.Hour).Unix(),
			},
			wantErr: "expired",
		},
		{
			name: "no audience",
			claims: jwt.MapClaims{
				"iss": "rpc-tests",
				"exp": now.Add(time.Hour).Unix(),
			},
			wantErr: "audience mismatch",
		},
		{
			name: "wrong audience",
			claims: jwt.MapClaims{
				"iss": "rpc-tests",
				"aud": []string{"someone-else"},
				"exp": now.Add(time.Hour).Unix(),
			},
			wantErr: "audience mismatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifier.Verify(signVerifierToken(t, tc.claims))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the token to be accepted, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected the token to be rejected with %q, but it was accepted", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected rejection mentioning %q, got %v", tc.wantErr, err)
			}
		})
	}
}
