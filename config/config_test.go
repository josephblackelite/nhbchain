package config

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"nhbchain/crypto"
)

var (
	testTreasuryAllowAddrBytes = func() [20]byte {
		var addr [20]byte
		addr[0] = 0x42
		addr[len(addr)-1] = 0x24
		return addr
	}()
	testTreasuryAllowAddrString = crypto.NewAddress(crypto.NHBPrefix, testTreasuryAllowAddrBytes[:]).String()
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
RPCTrustedProxies = ["10.0.0.1"]
RPCTrustProxyHeaders = true
RPCReadHeaderTimeout = 6
RPCReadTimeout = 20
RPCWriteTimeout = 18
RPCIdleTimeout = 45
RPCTLSCertFile = "/path/to/cert.pem"
RPCTLSKeyFile = "/path/to/key.pem"

[p2p]
NetworkId = 187001
MaxPeers = 42
MaxInbound = 21
MaxOutbound = 20
MinPeers = 18
OutboundPeers = 12
Bootnodes = ["1.1.1.1:6001"]
PersistentPeers = ["2.2.2.2:6001"]
Seeds = ["0xabc123@seed-1.nhb.local:7000"]
BanScore = 90
GreyScore = 45
RateMsgsPerSec = 60
Burst = 240
HandshakeTimeoutMs = 7000
BanDurationSeconds = 1800
DialBackoffSeconds = 45
PEX = false
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
	if cfg.MinPeers != 18 || cfg.OutboundPeers != 12 {
		t.Fatalf("unexpected connection targets: min=%d outbound=%d", cfg.MinPeers, cfg.OutboundPeers)
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
	if len(cfg.RPCTrustedProxies) != 1 || cfg.RPCTrustedProxies[0] != "10.0.0.1" {
		t.Fatalf("unexpected RPC trusted proxies: %v", cfg.RPCTrustedProxies)
	}
	if !cfg.RPCTrustProxyHeaders {
		t.Fatalf("expected RPCTrustProxyHeaders to be true")
	}
	if cfg.RPCReadHeaderTimeout != 6 {
		t.Fatalf("unexpected RPC read header timeout: %d", cfg.RPCReadHeaderTimeout)
	}
	if cfg.RPCReadTimeout != 20 || cfg.RPCWriteTimeout != 18 {
		t.Fatalf("unexpected RPC read/write timeouts: %d/%d", cfg.RPCReadTimeout, cfg.RPCWriteTimeout)
	}
	if cfg.RPCIdleTimeout != 45 {
		t.Fatalf("unexpected RPC idle timeout: %d", cfg.RPCIdleTimeout)
	}
	if cfg.RPCTLSCertFile != "/path/to/cert.pem" || cfg.RPCTLSKeyFile != "/path/to/key.pem" {
		t.Fatalf("unexpected RPC TLS paths: %s %s", cfg.RPCTLSCertFile, cfg.RPCTLSKeyFile)
	}
	if len(cfg.Bootnodes) != 1 || cfg.Bootnodes[0] != "1.1.1.1:6001" {
		t.Fatalf("bootnodes not parsed: %v", cfg.Bootnodes)
	}
	if len(cfg.PersistentPeers) != 1 || cfg.PersistentPeers[0] != "2.2.2.2:6001" {
		t.Fatalf("persistent peers not parsed: %v", cfg.PersistentPeers)
	}
	if len(cfg.P2P.Seeds) != 1 || cfg.P2P.Seeds[0] != "0xabc123@seed-1.nhb.local:7000" {
		t.Fatalf("unexpected seeds: %v", cfg.P2P.Seeds)
	}
	if cfg.P2P.MinPeers != 18 || cfg.P2P.OutboundPeers != 12 {
		t.Fatalf("unexpected p2p targets: %+v", cfg.P2P)
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
	if cfg.P2P.BanDurationSeconds != 1800 {
		t.Fatalf("unexpected ban duration: %d", cfg.P2P.BanDurationSeconds)
	}
	if cfg.P2P.DialBackoffSeconds != 45 {
		t.Fatalf("unexpected dial backoff: %d", cfg.P2P.DialBackoffSeconds)
	}
	if cfg.P2P.PEX == nil || *cfg.P2P.PEX != false {
		t.Fatalf("unexpected pex flag: %+v", cfg.P2P.PEX)
	}
}

func TestLoadParsesGovernanceSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:6001"
ValidatorKeystorePath = "%s"

[governance]
MinDepositWei = "5000e18"
VotingPeriodSeconds = 777600
TimelockSeconds = 259200
QuorumBps = 2500
PassThresholdBps = 5500
AllowedParams = ["fees.baseFee","escrow.maxOpenDisputes"]
AllowedRoles = ["compliance"]
TreasuryAllowList = ["%s"]
BlockTimestampToleranceSeconds = 12
`, keystorePath, testTreasuryAllowAddrString)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Governance.MinDepositWei != "5000e18" {
		t.Fatalf("unexpected min deposit: %s", cfg.Governance.MinDepositWei)
	}
	if cfg.Governance.VotingPeriodSeconds != 777600 {
		t.Fatalf("unexpected voting period: %d", cfg.Governance.VotingPeriodSeconds)
	}
	if cfg.Governance.TimelockSeconds != 259200 {
		t.Fatalf("unexpected timelock: %d", cfg.Governance.TimelockSeconds)
	}
	if cfg.Governance.QuorumBps != 2500 {
		t.Fatalf("unexpected quorum: %d", cfg.Governance.QuorumBps)
	}
	if cfg.Governance.PassThresholdBps != 5500 {
		t.Fatalf("unexpected threshold: %d", cfg.Governance.PassThresholdBps)
	}
	if len(cfg.Governance.AllowedParams) != 2 {
		t.Fatalf("unexpected allowed params: %v", cfg.Governance.AllowedParams)
	}
	if cfg.Governance.AllowedParams[1] != "escrow.maxOpenDisputes" {
		t.Fatalf("unexpected second allowed param: %s", cfg.Governance.AllowedParams[1])
	}
	if len(cfg.Governance.AllowedRoles) != 1 || cfg.Governance.AllowedRoles[0] != "compliance" {
		t.Fatalf("unexpected allowed roles: %v", cfg.Governance.AllowedRoles)
	}
	if len(cfg.Governance.TreasuryAllowList) != 1 || cfg.Governance.TreasuryAllowList[0] != testTreasuryAllowAddrString {
		t.Fatalf("unexpected treasury allow list: %v", cfg.Governance.TreasuryAllowList)
	}
	if cfg.Governance.BlockTimestampToleranceSeconds != 12 {
		t.Fatalf("unexpected timestamp tolerance: %d", cfg.Governance.BlockTimestampToleranceSeconds)
	}
}

func TestGovPolicyParsing(t *testing.T) {
	cfg := GovConfig{
		MinDepositWei:                  "1.25e3",
		VotingPeriodSeconds:            3600,
		TimelockSeconds:                7200,
		QuorumBps:                      1500,
		PassThresholdBps:               5500,
		AllowedParams:                  []string{"fees.baseFee", "escrow.maxOpenDisputes"},
		AllowedRoles:                   []string{"compliance"},
		TreasuryAllowList:              []string{testTreasuryAllowAddrString},
		BlockTimestampToleranceSeconds: 9,
	}
	policy, err := cfg.Policy()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.VotingPeriodSeconds != 3600 || policy.TimelockSeconds != 7200 {
		t.Fatalf("unexpected timers: %+v", policy)
	}
	if policy.QuorumBps != 1500 || policy.PassThresholdBps != 5500 {
		t.Fatalf("unexpected thresholds: %+v", policy)
	}
	if policy.BlockTimestampToleranceSeconds != 9 {
		t.Fatalf("unexpected block tolerance: %d", policy.BlockTimestampToleranceSeconds)
	}
	depositWant := new(big.Int)
	depositWant.SetString("1250", 10)
	if policy.MinDepositWei == nil || policy.MinDepositWei.Cmp(depositWant) != 0 {
		t.Fatalf("unexpected deposit: %v", policy.MinDepositWei)
	}
	if len(policy.AllowedParams) != 2 || policy.AllowedParams[0] != "fees.baseFee" {
		t.Fatalf("unexpected allowed params: %v", policy.AllowedParams)
	}
	if len(policy.AllowedRoles) != 1 || policy.AllowedRoles[0] != "compliance" {
		t.Fatalf("unexpected allowed roles: %v", policy.AllowedRoles)
	}
	if len(policy.TreasuryAllowList) != 1 {
		t.Fatalf("unexpected treasury list: %v", policy.TreasuryAllowList)
	}
	decoded, err := crypto.DecodeAddress(testTreasuryAllowAddrString)
	if err != nil {
		t.Fatalf("decode address: %v", err)
	}
	var wantAddr [20]byte
	copy(wantAddr[:], decoded.Bytes())
	if policy.TreasuryAllowList[0] != wantAddr {
		t.Fatalf("unexpected treasury address: %v", policy.TreasuryAllowList)
	}

	if _, err := (GovConfig{MinDepositWei: "-1"}).Policy(); err == nil {
		t.Fatalf("expected error for negative deposit")
	}
	if _, err := (GovConfig{MinDepositWei: "abc"}).Policy(); err == nil {
		t.Fatalf("expected error for invalid deposit")
	}
}
