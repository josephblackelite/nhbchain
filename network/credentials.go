package network

import (
	"context"
	"strings"

	"google.golang.org/grpc/credentials"
)

type staticTokenCredentials struct {
	header string
	token  string
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

func (c staticTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c.token == "" {
		return map[string]string{}, nil
	}
	return map[string]string{c.header: c.token}, nil
}

func (c staticTokenCredentials) RequireTransportSecurity() bool {
	return false
}
