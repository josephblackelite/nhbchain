package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Config captures the settings required to construct gRPC server options.
type Config struct {
	TLSCertFile      string
	TLSKeyFile       string
	TLSClientCAFile  string
	AllowInsecure    bool
	MTLSRequired     bool
	AllowedClientCNs []string
	RateLimitPerMin  int
	APITokens        []string
	Logger           *slog.Logger
}

// GrpcServerCreds builds the grpc.ServerOption configuring TLS credentials.
func GrpcServerCreds(cfg Config) (grpc.ServerOption, error) {
	certPath := strings.TrimSpace(cfg.TLSCertFile)
	keyPath := strings.TrimSpace(cfg.TLSKeyFile)
	clientCAPath := strings.TrimSpace(cfg.TLSClientCAFile)

	if certPath == "" || keyPath == "" {
		if cfg.MTLSRequired || len(cfg.AllowedClientCNs) > 0 {
			return nil, fmt.Errorf("mtls requires server certificate, key, and client ca configuration")
		}
		if cfg.AllowInsecure {
			return nil, nil
		}
		return nil, fmt.Errorf("tls certificate and key are required")
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}

	requireClientCert := cfg.MTLSRequired || len(cfg.AllowedClientCNs) > 0
	if clientCAPath != "" {
		pem, err := os.ReadFile(clientCAPath)
		if err != nil {
			return nil, fmt.Errorf("read client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parse client ca: invalid pem data")
		}
		tlsCfg.ClientCAs = pool
	}

	if requireClientCert {
		if tlsCfg.ClientCAs == nil {
			return nil, fmt.Errorf("client ca bundle required for mtls")
		}
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else if tlsCfg.ClientCAs != nil {
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	} else {
		tlsCfg.ClientAuth = tls.NoClientCert
	}

	if len(cfg.AllowedClientCNs) > 0 {
		allowed := make(map[string]struct{}, len(cfg.AllowedClientCNs))
		for _, name := range cfg.AllowedClientCNs {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				allowed[trimmed] = struct{}{}
			}
		}
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			for _, chain := range cs.VerifiedChains {
				if len(chain) == 0 {
					continue
				}
				if _, ok := allowed[strings.TrimSpace(chain[0].Subject.CommonName)]; ok {
					return nil
				}
			}
			for _, cert := range cs.PeerCertificates {
				if _, ok := allowed[strings.TrimSpace(cert.Subject.CommonName)]; ok {
					return nil
				}
			}
			return fmt.Errorf("client certificate common name not allowed")
		}
	}

	return grpc.Creds(credentials.NewTLS(tlsCfg)), nil
}

// Interceptors constructs the grpc.ServerOptions installing recovery, logging,
// authentication, and rate-limiting middleware.
func Interceptors(cfg Config) ([]grpc.ServerOption, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	authUnary, authStream := NewAuthInterceptors(AuthConfig{
		APITokens:        cfg.APITokens,
		AllowedClientCNs: cfg.AllowedClientCNs,
		MTLSRequired:     cfg.MTLSRequired,
	})

	unaryInterceptors := []grpc.UnaryServerInterceptor{
		loggingUnaryInterceptor(logger),
		recoveryUnaryInterceptor(logger),
	}
	streamInterceptors := []grpc.StreamServerInterceptor{
		loggingStreamInterceptor(logger),
		recoveryStreamInterceptor(logger),
	}

	if limiter := newRequestLimiter(cfg.RateLimitPerMin); limiter != nil {
		unaryInterceptors = append(unaryInterceptors, limiter.unaryInterceptor())
		streamInterceptors = append(streamInterceptors, limiter.streamInterceptor())
	}

	unaryInterceptors = append(unaryInterceptors, authUnary)
	streamInterceptors = append(streamInterceptors, authStream)

	options := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	}
	return options, nil
}

func loggingUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ interface{}, err error) {
		start := time.Now()
		defer func() {
			code := status.Code(err)
			logger.Info("grpc unary", "method", info.FullMethod, "code", code.String(), "duration", time.Since(start))
		}()
		return handler(ctx, req)
	}
}

func loggingStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		start := time.Now()
		defer func() {
			code := status.Code(err)
			logger.Info("grpc stream", "method", info.FullMethod, "code", code.String(), "duration", time.Since(start))
		}()
		return handler(srv, ss)
	}
}

func recoveryUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in unary handler", "method", info.FullMethod, "panic", r)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

func recoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in stream handler", "method", info.FullMethod, "panic", r)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}

type requestLimiter struct {
	limiter *rate.Limiter
}

func newRequestLimiter(perMinute int) *requestLimiter {
	if perMinute <= 0 {
		return nil
	}
	limit := rate.Every(time.Minute / time.Duration(perMinute))
	return &requestLimiter{limiter: rate.NewLimiter(limit, perMinute)}
}

func (r *requestLimiter) allow() bool {
	if r == nil || r.limiter == nil {
		return true
	}
	return r.limiter.Allow()
}

func (r *requestLimiter) unaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !r.allow() {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

func (r *requestLimiter) streamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !r.allow() {
			return status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}

// InsecureServerOption exposes grpc insecure credentials for tests when TLS is disabled.
func InsecureServerOption() grpc.ServerOption {
	return grpc.Creds(insecure.NewCredentials())
}
