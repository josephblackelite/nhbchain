package main

import (
	"os"
	"strings"
	"testing"
)

// Ledger PL-EXT-88: the validators' heartbeat device id carried the host name,
// which on the live validators is the private network address.
func TestValidatorHeartbeatDeviceIDCarriesNothingAboutTheHost(t *testing.T) {
	addr := []byte{0xd9, 0xdf, 0x12, 0x5c, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	got := validatorHeartbeatDeviceID(addr)
	if got != "validator-d9df125c" {
		t.Fatalf("device id = %q, want validator-d9df125c", got)
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" && strings.Contains(strings.ToLower(got), strings.ToLower(strings.TrimSpace(host))) {
		t.Fatalf("device id %q contains the host name %q", got, host)
	}
	if strings.Contains(got, "172-") || strings.Contains(got, "ip-") {
		t.Fatalf("device id %q looks like a private address", got)
	}
	if validatorHeartbeatDeviceID(nil) != "validator-" {
		t.Fatalf("an empty address must not panic and must give the bare prefix")
	}
	if validatorHeartbeatDeviceID(addr[:2]) != "validator-d9df" {
		t.Fatalf("a short address must be used as it is")
	}
}
