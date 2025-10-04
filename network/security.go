package network

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nhbchain/config"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// BuildServerSecurity constructs transport credentials and authenticators for the
// p2pd gRPC server based on the provided network security configuration. When
// AllowUnauthenticatedReads is false the returned read authenticator matches the
// write authenticator so callers must pass the returned values directly into
// network.NewService.
func BuildServerSecurity(sec *config.NetworkSecurity, baseDir string, lookup func(string) (string, bool)) (credentials.TransportCredentials, Authenticator, Authenticator, error) {
	if sec == nil {
		return nil, nil, nil, fmt.Errorf("network security configuration is missing")
	}

	secret, err := sec.ResolveSharedSecret(baseDir, lookup)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve shared secret: %w", err)
	}

	var auths []Authenticator
	if secret != "" {
		auths = append(auths, NewTokenAuthenticator(sec.AuthorizationHeaderName(), secret))
	}

	certPath := resolveSecurityPath(baseDir, sec.ServerTLSCertFile)
	keyPath := resolveSecurityPath(baseDir, sec.ServerTLSKeyFile)

	var tlsConfig *tls.Config
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return nil, nil, nil, fmt.Errorf("network security requires both ServerTLSCertFile and ServerTLSKeyFile when enabling TLS")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("load network TLS keypair: %w", err)
		}
		tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	if caPath := resolveSecurityPath(baseDir, sec.ClientCAFile); caPath != "" {
		if tlsConfig == nil {
			return nil, nil, nil, fmt.Errorf("ClientCAFile requires ServerTLSCertFile and ServerTLSKeyFile to be configured")
		}
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, nil, fmt.Errorf("failed to parse client CA certificates from %s", caPath)
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		auths = append(auths, NewTLSAuthorizer(sec.AllowedClientCommonNames))
	}

	if len(auths) == 0 {
		return nil, nil, nil, fmt.Errorf("network security requires a shared secret or client certificate authentication")
	}

	var creds credentials.TransportCredentials
	switch {
	case tlsConfig != nil:
		creds = credentials.NewTLS(tlsConfig)
	case sec.AllowInsecure:
		creds = insecure.NewCredentials()
	default:
		return nil, nil, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=true only for development")
	}

	writeAuth := ChainAuthenticators(auths...)
	readAuth := writeAuth
	if sec.AllowUnauthenticatedReads {
		readAuth = nil
	}
	return creds, writeAuth, readAuth, nil
}

func resolveSecurityPath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if baseDir != "" && !filepath.IsAbs(trimmed) {
		return filepath.Join(baseDir, trimmed)
	}
	return trimmed
}
