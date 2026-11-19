package main

import (
	"strings"
	"time"

	"nhbchain/config"
	"nhbchain/rpc"
)

// swapAuthConfigFrom turns the [RPCSwapAuth] section into the settings the RPC
// server takes, except the nonce persistence backend, which the caller opens.
// Keys are trimmed and an entry with an empty key or secret is dropped, which is
// how the server matches a partner when a request arrives; PartnerProviders (the
// providers each partner key may read back from the voucher ledger, PL-EXT-32)
// passes through, copied.
func swapAuthConfigFrom(auth config.RPCSwapAuth) rpc.SwapAuthConfig {
	secrets := make(map[string]string, len(auth.Secrets))
	for key, secret := range auth.Secrets {
		trimmedKey := strings.TrimSpace(key)
		trimmedSecret := strings.TrimSpace(secret)
		if trimmedKey == "" || trimmedSecret == "" {
			continue
		}
		secrets[trimmedKey] = trimmedSecret
	}
	limits := make(map[string]int, len(auth.PartnerRateLimits))
	for key, limit := range auth.PartnerRateLimits {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || limit <= 0 {
			continue
		}
		limits[trimmedKey] = limit
	}
	providers := make(map[string][]string, len(auth.PartnerProviders))
	for key, names := range auth.PartnerProviders {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		providers[trimmedKey] = append([]string(nil), names...)
	}
	return rpc.SwapAuthConfig{
		Secrets:              secrets,
		AllowedTimestampSkew: time.Duration(auth.AllowedTimestampSkewSecs) * time.Second,
		NonceTTL:             time.Duration(auth.NonceTTLSeconds) * time.Second,
		NonceCapacity:        auth.NonceCapacity,
		RateLimitWindow:      time.Duration(auth.RateLimitWindowSeconds) * time.Second,
		PartnerRateLimits:    limits,
		PartnerProviders:     providers,
	}
}
