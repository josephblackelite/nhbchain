package rpc

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type callerMetadataParams struct {
	Nonce     *uint64 `json:"nonce,omitempty"`
	ExpiresAt *int64  `json:"expiresAt,omitempty"`
	TTL       *int64  `json:"ttl,omitempty"`
	ChainID   string  `json:"chainId,omitempty"`
}

type callerNonceState struct {
	nonce   uint64
	expires time.Time
}

func callerKeyFromAddress(addr [20]byte) string {
	return hex.EncodeToString(addr[:])
}

func (s *Server) validateCallerMetadata(actorKey string, params callerMetadataParams) error {
	now := time.Now()
	chainKey, err := s.normalizeChainID(params.ChainID)
	if err != nil {
		return err
	}
	expiry, err := parseMetadataExpiry(now, params.ExpiresAt, params.TTL, s.callerMetadataMaxTTL)
	if err != nil {
		return err
	}
	if params.Nonce == nil {
		return nil
	}
	if *params.Nonce == 0 {
		return fmt.Errorf("nonce must be greater than zero")
	}
	if expiry.IsZero() {
		return fmt.Errorf("expiresAt or ttl required when nonce is provided")
	}
	return s.trackCallerNonce(actorKey, chainKey, *params.Nonce, expiry, now)
}

func (s *Server) normalizeChainID(input string) (string, error) {
	expected := strconv.FormatUint(s.node.ChainID(), 10)
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return expected, nil
	}
	original := trimmed
	base := 10
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "0x") {
		base = 16
		trimmed = lower[2:]
	}
	id, err := strconv.ParseUint(trimmed, base, 64)
	if err != nil {
		return "", fmt.Errorf("invalid chainId: %s", original)
	}
	if id != s.node.ChainID() {
		return "", fmt.Errorf("chainId mismatch: expected %s got %s", expected, original)
	}
	return strconv.FormatUint(id, 10), nil
}

func parseMetadataExpiry(now time.Time, expiresAt, ttl *int64, maxTTL time.Duration) (time.Time, error) {
	if expiresAt != nil && ttl != nil {
		return time.Time{}, fmt.Errorf("provide at most one of expiresAt or ttl")
	}
	var expiry time.Time
	if expiresAt != nil {
		if *expiresAt <= 0 {
			return time.Time{}, fmt.Errorf("expiresAt must be positive")
		}
		expiry = time.Unix(*expiresAt, 0)
	} else if ttl != nil {
		if *ttl <= 0 {
			return time.Time{}, fmt.Errorf("ttl must be positive seconds")
		}
		if *ttl > int64(math.MaxInt64/int64(time.Second)) {
			return time.Time{}, fmt.Errorf("ttl exceeds supported range")
		}
		duration := time.Duration(*ttl) * time.Second
		if maxTTL > 0 && duration > maxTTL {
			return time.Time{}, fmt.Errorf("ttl exceeds maximum of %d seconds", int64(maxTTL/time.Second))
		}
		expiry = now.Add(duration)
	}
	if !expiry.IsZero() {
		if maxTTL > 0 {
			limit := now.Add(maxTTL)
			if expiry.After(limit) {
				return time.Time{}, fmt.Errorf("expiry exceeds maximum ttl of %s", maxTTL)
			}
		}
		skew := time.Duration(deadlineSkewSeconds) * time.Second
		if expiry.Before(now.Add(-skew)) {
			return time.Time{}, fmt.Errorf("expiry must be in the future")
		}
	}
	return expiry, nil
}

func (s *Server) trackCallerNonce(actorKey, chainKey string, nonce uint64, expiry, now time.Time) error {
	s.callerNonceMu.Lock()
	defer s.callerNonceMu.Unlock()
	if s.callerNonces == nil {
		s.callerNonces = make(map[string]callerNonceState)
	}
	key := actorKey + "|" + chainKey
	if state, ok := s.callerNonces[key]; ok {
		if now.After(state.expires) {
			delete(s.callerNonces, key)
		} else if nonce <= state.nonce {
			return fmt.Errorf("nonce must be greater than %d", state.nonce)
		}
	}
	s.callerNonces[key] = callerNonceState{nonce: nonce, expires: expiry}
	return nil
}
