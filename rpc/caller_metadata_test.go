package rpc

import (
	"strings"
	"testing"
	"time"
)

func TestParseMetadataExpiryTTLLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limit := 5 * time.Minute

	short := int64(60)
	expiry, err := parseMetadataExpiry(now, nil, &short, limit)
	if err != nil {
		t.Fatalf("unexpected error for short ttl: %v", err)
	}
	want := now.Add(time.Minute)
	if !expiry.Equal(want) {
		t.Fatalf("unexpected expiry: got %v want %v", expiry, want)
	}

	long := int64(int(limit/time.Second) + 1)
	if _, err := parseMetadataExpiry(now, nil, &long, limit); err == nil {
		t.Fatalf("expected error for ttl beyond limit")
	} else if !strings.Contains(err.Error(), "ttl exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseMetadataExpiryAbsoluteLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limit := 10 * time.Minute

	within := now.Add(5 * time.Minute).Unix()
	expiry, err := parseMetadataExpiry(now, &within, nil, limit)
	if err != nil {
		t.Fatalf("unexpected error for within limit: %v", err)
	}
	if !expiry.Equal(time.Unix(within, 0)) {
		t.Fatalf("unexpected expiry: got %v want %v", expiry, time.Unix(within, 0))
	}

	beyond := now.Add(15 * time.Minute).Unix()
	if _, err := parseMetadataExpiry(now, &beyond, nil, limit); err == nil {
		t.Fatalf("expected error for expiry beyond limit")
	} else if !strings.Contains(err.Error(), "maximum ttl") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseMetadataExpiryNoLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ttl := int64(3600)
	expiry, err := parseMetadataExpiry(now, nil, &ttl, 0)
	if err != nil {
		t.Fatalf("unexpected error without limit: %v", err)
	}
	want := now.Add(time.Hour)
	if !expiry.Equal(want) {
		t.Fatalf("unexpected expiry: got %v want %v", expiry, want)
	}
}
