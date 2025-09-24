package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesP2PSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:7000"
RPCAddress = "0.0.0.0:9000"
DataDir = "./data"
GenesisFile = "genesis.json"
ValidatorKeystorePath = "%s"
NetworkName = "testnet"
PeerBanSeconds = 120
ReadTimeout = 30
WriteTimeout = 4
MaxMsgBytes = 2048
MaxMsgsPerSecond = 12.5
ClientVersion = "nhbchain/test"

[p2p]
NetworkId = 187001
MaxPeers = 42
MaxInbound = 21
MaxOutbound = 20
Bootnodes = ["1.1.1.1:6001"]
PersistentPeers = ["2.2.2.2:6001"]
BanScore = 90
GreyScore = 45
RateMsgsPerSec = 60
Burst = 240
HandshakeTimeoutMs = 7000
`, keystorePath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.MaxPeers != 42 || cfg.MaxInbound != 21 || cfg.MaxOutbound != 20 {
		t.Fatalf("unexpected peer limits: %+v", cfg)
	}
	if cfg.PeerBanSeconds != 120 {
		t.Fatalf("unexpected ban seconds: %d", cfg.PeerBanSeconds)
	}
	if cfg.ReadTimeout != 30 || cfg.WriteTimeout != 4 {
		t.Fatalf("unexpected timeouts: read=%d write=%d", cfg.ReadTimeout, cfg.WriteTimeout)
	}
	if cfg.MaxMsgBytes != 2048 {
		t.Fatalf("unexpected max msg bytes: %d", cfg.MaxMsgBytes)
	}
	if cfg.MaxMsgsPerSecond != 12.5 {
		t.Fatalf("unexpected max msgs per second: %f", cfg.MaxMsgsPerSecond)
	}
	if cfg.ClientVersion != "nhbchain/test" {
		t.Fatalf("unexpected client version: %s", cfg.ClientVersion)
	}
	if len(cfg.Bootnodes) != 1 || cfg.Bootnodes[0] != "1.1.1.1:6001" {
		t.Fatalf("bootnodes not parsed: %v", cfg.Bootnodes)
	}
	if len(cfg.PersistentPeers) != 1 || cfg.PersistentPeers[0] != "2.2.2.2:6001" {
		t.Fatalf("persistent peers not parsed: %v", cfg.PersistentPeers)
	}
	if cfg.P2P.BanScore != 90 || cfg.P2P.GreyScore != 45 {
		t.Fatalf("unexpected reputation thresholds: %+v", cfg.P2P)
	}
	if cfg.P2P.RateMsgsPerSec != 12.5 || cfg.P2P.Burst != 240 {
		t.Fatalf("unexpected rate limits: %+v", cfg.P2P)
	}
	if cfg.P2P.HandshakeTimeoutMs != 7000 {
		t.Fatalf("unexpected handshake timeout: %d", cfg.P2P.HandshakeTimeoutMs)
	}
}
