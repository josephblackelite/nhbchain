package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/network"
	"nhbchain/services/governd/config"
)

func consensusDialOptions(cfg config.ClientConfig) ([]grpc.DialOption, error) {
	creds, hasTLS, err := loadConsensusCredentials(cfg.TLS)
	if err != nil {
		return nil, err
	}
	hasSharedSecret := strings.TrimSpace(cfg.SharedSecret.Token) != ""
	if !cfg.AllowInsecure && !hasTLS && !hasSharedSecret {
		return nil, fmt.Errorf("consensus client security requires tls material or shared-secret authentication; set allow_insecure=true for development")
	}

	var opts []grpc.DialOption
	if hasTLS {
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if hasSharedSecret {
		header := cfg.SharedSecret.Header
		token := cfg.SharedSecret.Token
		if hasTLS {
			opts = append(opts, grpc.WithPerRPCCredentials(network.NewStaticTokenCredentials(header, token)))
		} else {
			opts = append(opts, grpc.WithPerRPCCredentials(network.NewStaticTokenCredentialsAllowInsecure(header, token)))
		}
	}
	return opts, nil
}

func loadConsensusCredentials(cfg config.ClientTLSConfig) (credentials.TransportCredentials, bool, error) {
	certPath := strings.TrimSpace(cfg.CertPath)
	keyPath := strings.TrimSpace(cfg.KeyPath)
	caPath := strings.TrimSpace(cfg.CAPath)

	hasCert := certPath != ""
	hasKey := keyPath != ""
	if hasCert != hasKey {
		return nil, false, fmt.Errorf("consensus client tls requires both cert and key when enabling mTLS")
	}

	if !hasCert && !hasKey && caPath == "" {
		return nil, false, nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if hasCert {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, false, fmt.Errorf("load consensus client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, false, fmt.Errorf("read consensus client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, false, fmt.Errorf("parse consensus client ca: invalid pem data")
		}
		tlsCfg.RootCAs = pool
	}

	return credentials.NewTLS(tlsCfg), true, nil
}
