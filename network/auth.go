package network

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Authenticator evaluates the incoming RPC context and returns an error when the
// request should be rejected.
type Authenticator interface {
	Authorize(ctx context.Context) error
}

type authenticatorFunc func(context.Context) error

func (f authenticatorFunc) Authorize(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f(ctx)
}

// ChainAuthenticators combines multiple authenticators, short-circuiting on the
// first failure. When no authenticators are supplied the returned instance
// always authorizes the request.
func ChainAuthenticators(auths ...Authenticator) Authenticator {
	filtered := make([]Authenticator, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			filtered = append(filtered, auth)
		}
	}
	if len(filtered) == 0 {
		return authenticatorFunc(func(context.Context) error { return nil })
	}
	return authenticatorFunc(func(ctx context.Context) error {
		for _, auth := range filtered {
			if err := auth.Authorize(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

// NewTokenAuthenticator validates that the supplied metadata header carries the
// configured shared secret. Bearer tokens ("Bearer <token>") are accepted as a
// convenience for deployments that piggyback on HTTP-style headers.
func NewTokenAuthenticator(header, secret string) Authenticator {
	cleanedHeader := strings.ToLower(strings.TrimSpace(header))
	if cleanedHeader == "" {
		cleanedHeader = "authorization"
	}
	trimmedSecret := strings.TrimSpace(secret)
	if trimmedSecret == "" {
		return nil
	}
	return authenticatorFunc(func(ctx context.Context) error {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return status.Error(codes.Unauthenticated, "network: missing metadata")
		}
		values := md.Get(cleanedHeader)
		for _, value := range values {
			token := strings.TrimSpace(value)
			if token == trimmedSecret {
				return nil
			}
			if strings.HasPrefix(strings.ToLower(token), "bearer ") {
				if strings.TrimSpace(token[len("bearer "):]) == trimmedSecret {
					return nil
				}
			}
		}
		return status.Error(codes.Unauthenticated, "network: invalid or missing shared secret")
	})
}

// NewTLSAuthorizer ensures the peer negotiated TLS and, when a non-empty allow
// list is provided, that at least one presented client certificate matches the
// configured common names.
func NewTLSAuthorizer(allowed []string) Authenticator {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, cn := range allowed {
		trimmed := strings.TrimSpace(cn)
		if trimmed == "" {
			continue
		}
		allowedSet[strings.ToLower(trimmed)] = struct{}{}
	}
	return authenticatorFunc(func(ctx context.Context) error {
		p, ok := peer.FromContext(ctx)
		if !ok {
			return status.Error(codes.Unauthenticated, "network: missing peer info")
		}
		info, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok {
			return status.Error(codes.Unauthenticated, "network: connection is not using TLS")
		}
		if len(info.State.PeerCertificates) == 0 {
			return status.Error(codes.Unauthenticated, "network: no client certificate presented")
		}
		if len(allowedSet) == 0 {
			return nil
		}
		for _, cert := range info.State.PeerCertificates {
			cn := strings.ToLower(strings.TrimSpace(cert.Subject.CommonName))
			if _, ok := allowedSet[cn]; ok {
				return nil
			}
		}
		return status.Error(codes.PermissionDenied, "network: client certificate not authorised")
	})
}
