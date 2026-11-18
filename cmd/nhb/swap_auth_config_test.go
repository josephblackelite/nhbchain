package main

import (
	"reflect"
	"testing"
	"time"

	"nhbchain/config"
)

// What the file says about swap partners must reach the RPC server as written;
// in particular the providers each key may read back from the voucher ledger
// (PL-EXT-32), since a key that arrives with none reads nothing.
func TestSwapAuthConfigFromPassesEverySetting(t *testing.T) {
	got := swapAuthConfigFrom(config.RPCSwapAuth{
		Secrets:                  map[string]string{" alpha ": " s1 ", "beta": "s2", "blank": "  ", " ": "s3"},
		AllowedTimestampSkewSecs: 90,
		NonceTTLSeconds:          300,
		NonceCapacity:            2048,
		RateLimitWindowSeconds:   30,
		PartnerRateLimits:        map[string]int{" alpha ": 5, "beta": 0, "": 9},
		PartnerProviders:         map[string][]string{" alpha ": {"alpha-pay", " Alpha-Cards "}, "beta": {"*"}, " ": {"x"}},
	})
	if want := map[string]string{"alpha": "s1", "beta": "s2"}; !reflect.DeepEqual(got.Secrets, want) {
		t.Fatalf("Secrets = %v, want %v", got.Secrets, want)
	}
	if want := map[string]int{"alpha": 5}; !reflect.DeepEqual(got.PartnerRateLimits, want) {
		t.Fatalf("PartnerRateLimits = %v, want %v", got.PartnerRateLimits, want)
	}
	want := map[string][]string{"alpha": {"alpha-pay", " Alpha-Cards "}, "beta": {"*"}}
	if !reflect.DeepEqual(got.PartnerProviders, want) {
		t.Fatalf("PartnerProviders = %v, want %v (the RPC server normalises the names)", got.PartnerProviders, want)
	}
	if got.AllowedTimestampSkew != 90*time.Second || got.NonceTTL != 300*time.Second || got.NonceCapacity != 2048 || got.RateLimitWindow != 30*time.Second {
		t.Fatalf("timing and capacity settings not passed through: %+v", got)
	}
	if got.Persistence != nil {
		t.Fatalf("the persistence backend is opened by the caller, got %v", got.Persistence)
	}
}

func TestSwapAuthConfigFromCopiesTheProviderLists(t *testing.T) {
	cfg := config.RPCSwapAuth{
		Secrets:          map[string]string{"alpha": "s1"},
		PartnerProviders: map[string][]string{"alpha": {"alpha-pay"}},
	}
	got := swapAuthConfigFrom(cfg)
	cfg.PartnerProviders["alpha"][0] = "changed-later"
	if got.PartnerProviders["alpha"][0] != "alpha-pay" {
		t.Fatalf("the server's provider list shares memory with the config: %v", got.PartnerProviders)
	}
}

func TestSwapAuthConfigFromWithNothingConfigured(t *testing.T) {
	got := swapAuthConfigFrom(config.RPCSwapAuth{})
	if len(got.Secrets) != 0 || len(got.PartnerRateLimits) != 0 || len(got.PartnerProviders) != 0 {
		t.Fatalf("an empty section must give an empty config, got %+v", got)
	}
}
