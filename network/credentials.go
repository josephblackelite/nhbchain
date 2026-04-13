package network

import (
	"context"
	"strings"

	"google.golang.org/grpc/credentials"
)

type staticTokenCredentials struct {
	header        string
	token         string
	allowInsecure bool
}

// NewStaticTokenCredentials returns a grpc.PerRPCCredentials implementation
// that injects the configured shared-secret header on every request.
func NewStaticTokenCredentials(header, token string) credentials.PerRPCCredentials {
	normalizedHeader := strings.ToLower(strings.TrimSpace(header))
	if normalizedHeader == "" {
		normalizedHeader = "authorization"
	}
	return staticTokenCredentials{
		header: normalizedHeader,
		token:  strings.TrimSpace(token),
	}
}

// NewStaticTokenCredentialsAllowInsecure returns the same credentials as
// NewStaticTokenCredentials but allows plaintext transports even when a token is
// configured. This helper exists strictly for test harnesses that rely on the
// insecure gRPC transport; production code should prefer
// NewStaticTokenCredentials so that TLS is always required when a token is set.
func NewStaticTokenCredentialsAllowInsecure(header, token string) credentials.PerRPCCredentials {
	normalizedHeader := strings.ToLower(strings.TrimSpace(header))
	if normalizedHeader == "" {
		normalizedHeader = "authorization"
	}
	return staticTokenCredentials{
		header:        normalizedHeader,
		token:         strings.TrimSpace(token),
		allowInsecure: true,
	}
}

func (c staticTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c.token == "" {
		return map[string]string{}, nil
	}
	return map[string]string{c.header: c.token}, nil
}

func (c staticTokenCredentials) RequireTransportSecurity() bool {
	if c.allowInsecure {
		return false
	}
	return c.token != ""
}
