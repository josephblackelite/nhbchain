package dial

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// DialOption controls how SDK clients construct gRPC connections.
type DialOption interface {
	apply(*config)
}

type optionFunc func(*config)

func (f optionFunc) apply(cfg *config) {
	f(cfg)
}

type config struct {
	transport credentials.TransportCredentials
	extra     []grpc.DialOption
}

// WithTransportCredentials configures the client to use the provided transport credentials.
func WithTransportCredentials(creds credentials.TransportCredentials) DialOption {
	return optionFunc(func(cfg *config) {
		cfg.transport = creds
	})
}

// WithTLSConfig configures the client to use the provided TLS configuration.
// The configuration is cloned to avoid mutation and defaults the minimum TLS
// version to TLS 1.2 when unset.
func WithTLSConfig(tlsCfg *tls.Config) DialOption {
	return optionFunc(func(cfg *config) {
		var clone *tls.Config
		if tlsCfg == nil {
			clone = defaultTLSConfig()
		} else {
			clone = tlsCfg.Clone()
			if clone.MinVersion == 0 || clone.MinVersion < tls.VersionTLS12 {
				clone.MinVersion = tls.VersionTLS12
			}
		}
		cfg.transport = credentials.NewTLS(clone)
	})
}

// WithTLSFromFiles loads TLS material from the provided filesystem paths and
// configures the client to use it. Empty paths are ignored.
func WithTLSFromFiles(certPath, keyPath, caPath string) (DialOption, error) {
	creds, err := TLSCredentialsFromFiles(certPath, keyPath, caPath)
	if err != nil {
		return nil, err
	}
	return WithTransportCredentials(creds), nil
}

// WithSystemCertPool configures the client to trust the host certificate pool.
// The optional serverName allows overriding SNI when the gRPC target does not
// match the certificate subject.
func WithSystemCertPool(serverName string) (DialOption, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system cert pool: %w", err)
	}
	cfg := defaultTLSConfig()
	cfg.RootCAs = pool
	cfg.ServerName = strings.TrimSpace(serverName)
	return WithTLSConfig(cfg), nil
}

// WithInsecure configures the client to use plaintext connections. This should
// only be used for local development.
func WithInsecure() DialOption {
	return optionFunc(func(cfg *config) {
		cfg.transport = insecure.NewCredentials()
	})
}

// WithContextDialer attaches a custom context-based dialer to the connection.
func WithContextDialer(dialer func(context.Context, string) (net.Conn, error)) DialOption {
	return optionFunc(func(cfg *config) {
		cfg.extra = append(cfg.extra, grpc.WithContextDialer(dialer))
	})
}

// WithPerRPCCredentials attaches per-RPC credentials to outgoing calls.
func WithPerRPCCredentials(creds credentials.PerRPCCredentials) DialOption {
	return optionFunc(func(cfg *config) {
		cfg.extra = append(cfg.extra, grpc.WithPerRPCCredentials(creds))
	})
}

// WithDialOptions forwards arbitrary gRPC dial options to the connection builder.
func WithDialOptions(opts ...grpc.DialOption) DialOption {
	return optionFunc(func(cfg *config) {
		cfg.extra = append(cfg.extra, opts...)
	})
}

// Resolve builds the final grpc.DialOption slice after applying the provided
// SDK DialOption values. When no explicit transport credentials are supplied,
// the resolver injects a TLS configuration that trusts the system certificate
// pool.
func Resolve(opts ...DialOption) ([]grpc.DialOption, error) {
	cfg := config{}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt.apply(&cfg)
	}

	if cfg.transport == nil {
		cfg.transport = credentials.NewTLS(defaultTLSConfig())
	}

	dialOpts := make([]grpc.DialOption, 0, len(cfg.extra)+1)
	dialOpts = append(dialOpts, grpc.WithTransportCredentials(cfg.transport))
	dialOpts = append(dialOpts, cfg.extra...)
	return dialOpts, nil
}

// TLSCredentialsFromFiles loads TLS credentials from disk and builds a
// transport credential set suitable for mTLS when the cert/key pair is
// provided. When only a CA file is supplied the credentials are configured for
// server authentication only.
func TLSCredentialsFromFiles(certPath, keyPath, caPath string) (credentials.TransportCredentials, error) {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	caPath = strings.TrimSpace(caPath)

	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("tls requires both certificate and key when enabling mutual authentication")
	}

	cfg := defaultTLSConfig()

	if certPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read ca certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("parse ca certificate: invalid pem data")
		}
		cfg.RootCAs = pool
	}

	return credentials.NewTLS(cfg), nil
}

func defaultTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}
