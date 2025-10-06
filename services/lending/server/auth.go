package server

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lendingd/config"
)

type authContextKey struct{}

// NewAuthInterceptors constructs unary and stream interceptors that enforce
// authentication on Msg RPCs. Requests must present either a configured API
// token or an mTLS client certificate with an allowed common name.
func NewAuthInterceptors(cfg config.AuthConfig) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	authenticator := newAuthenticator(cfg)
	return authenticator.unaryInterceptor(), authenticator.streamInterceptor()
}

func markAuthenticated(ctx context.Context) context.Context {
	return context.WithValue(ctx, authContextKey{}, true)
}

func isAuthenticated(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, ok := ctx.Value(authContextKey{}).(bool)
	return ok && value
}

type authenticator struct {
	tokens       map[string]struct{}
	commonNames  map[string]struct{}
	allowByToken bool
	allowByMTLS  bool
}

func newAuthenticator(cfg config.AuthConfig) *authenticator {
	tokens := make(map[string]struct{})
	for _, token := range cfg.APITokens {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		tokens[trimmed] = struct{}{}
	}
	commonNames := make(map[string]struct{})
	for _, name := range cfg.MTLS.AllowedCommonNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		commonNames[trimmed] = struct{}{}
	}
	return &authenticator{
		tokens:       tokens,
		commonNames:  commonNames,
		allowByToken: len(tokens) > 0,
		allowByMTLS:  len(commonNames) > 0,
	}
}

func (a *authenticator) unaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !isMsgMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := a.authenticate(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func (a *authenticator) streamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !isMsgMethod(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := a.authenticate(ss.Context())
		if err != nil {
			return err
		}
		wrapped := &authStream{ServerStream: ss, ctx: ctx}
		return handler(srv, wrapped)
	}
}

func (a *authenticator) authenticate(ctx context.Context) (context.Context, error) {
	if a == nil {
		return ctx, status.Error(codes.Internal, "authenticator unavailable")
	}
	if !a.allowByToken && !a.allowByMTLS {
		return ctx, status.Error(codes.PermissionDenied, "authentication is not configured")
	}
	if a.allowByToken && a.authenticateByToken(ctx) {
		return markAuthenticated(ctx), nil
	}
	if a.allowByMTLS && a.authenticateByMTLS(ctx) {
		return markAuthenticated(ctx), nil
	}
	return ctx, status.Error(codes.Unauthenticated, "authentication required")
}

func (a *authenticator) authenticateByToken(ctx context.Context) bool {
	if ctx == nil || len(a.tokens) == 0 {
		return false
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, header := range md.Get("authorization") {
		if token := parseBearerToken(header); token != "" {
			if _, exists := a.tokens[token]; exists {
				return true
			}
		}
	}
	for _, token := range md.Get("x-api-token") {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		if _, exists := a.tokens[trimmed]; exists {
			return true
		}
	}
	return false
}

func (a *authenticator) authenticateByMTLS(ctx context.Context) bool {
	if ctx == nil || len(a.commonNames) == 0 {
		return false
	}
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	info, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false
	}
	state := info.State
	for _, chain := range state.VerifiedChains {
		if len(chain) == 0 {
			continue
		}
		if a.commonNameAllowed(chain[0].Subject.CommonName) {
			return true
		}
	}
	for _, cert := range state.PeerCertificates {
		if a.commonNameAllowed(cert.Subject.CommonName) {
			return true
		}
	}
	return false
}

func (a *authenticator) commonNameAllowed(name string) bool {
	_, ok := a.commonNames[strings.TrimSpace(name)]
	return ok
}

func parseBearerToken(header string) string {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func isMsgMethod(fullMethod string) bool {
	switch fullMethod {
	case lendingv1.LendingService_SupplyAsset_FullMethodName,
		lendingv1.LendingService_WithdrawAsset_FullMethodName,
		lendingv1.LendingService_BorrowAsset_FullMethodName,
		lendingv1.LendingService_RepayAsset_FullMethodName:
		return true
	default:
		return false
	}
}

type authStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authStream) Context() context.Context {
	if s == nil {
		return nil
	}
	if s.ctx != nil {
		return s.ctx
	}
	return s.ServerStream.Context()
}
