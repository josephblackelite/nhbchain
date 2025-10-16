package config

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nhbchain/crypto"

	"github.com/BurntSushi/toml"
)

const testKeystorePassphrase = "test-passphrase"

var (
	testTreasuryAllowAddrBytes = func() [20]byte {
		var addr [20]byte
		addr[0] = 0x42
		addr[len(addr)-1] = 0x24
		return addr
	}()
	testTreasuryAllowAddrString = crypto.MustNewAddress(crypto.NHBPrefix, testTreasuryAllowAddrBytes[:]).String()
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
RPCAllowlistCIDRs = ["203.0.113.0/24"]
RPCReadHeaderTimeout = 6
RPCReadTimeout = 20
RPCWriteTimeout = 18
RPCIdleTimeout = 45
RPCMaxTxPerWindow = 10
RPCRateLimitWindow = 120
RPCAllowInsecure = true
RPCTLSCertFile = "/path/to/cert.pem"
RPCTLSKeyFile = "/path/to/key.pem"
RPCTLSClientCAFile = "/path/to/clients.pem"

[RPCProxyHeaders]
XForwardedFor = "single"
XRealIP = "ignore"

[RPCJWT]
Enable = true
Alg = "RS256"
RSAPublicKeyFile = "/path/to/jwt.pub"
Issuer = "rpc-service"
Audience = ["wallets", "partners"]
MaxSkewSeconds = 90

[network_security]
SharedSecret = "topsecret"
SharedSecretFile = "./secret.txt"
SharedSecretEnv = "NHB_TEST_SECRET"
AuthorizationHeader = "x-test-token"
ServerTLSCertFile = "./tls/server.crt"
ServerTLSKeyFile = "./tls/server.key"
ServerCAFile = "./tls/ca.pem"
ClientCAFile = "./tls/client-ca.pem"
ClientTLSCertFile = "./tls/client.crt"
ClientTLSKeyFile = "./tls/client.key"
AllowedClientCommonNames = ["consensusd"]
ServerName = "p2pd.internal"

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

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
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
	if len(cfg.RPCAllowlistCIDRs) != 1 || cfg.RPCAllowlistCIDRs[0] != "203.0.113.0/24" {
		t.Fatalf("unexpected RPC allowlist: %v", cfg.RPCAllowlistCIDRs)
	}
	if cfg.RPCProxyHeaders.XForwardedFor != "single" || cfg.RPCProxyHeaders.XRealIP != "ignore" {
		t.Fatalf("unexpected proxy header policy: %+v", cfg.RPCProxyHeaders)
	}
	if !cfg.RPCJWT.Enable || cfg.RPCJWT.Alg != "RS256" || cfg.RPCJWT.RSAPublicKeyFile != "/path/to/jwt.pub" {
		t.Fatalf("unexpected RPC JWT config: %+v", cfg.RPCJWT)
	}
	if cfg.RPCJWT.Issuer != "rpc-service" || len(cfg.RPCJWT.Audience) != 2 {
		t.Fatalf("unexpected RPC JWT issuer/audience: %+v", cfg.RPCJWT)
	}
	if cfg.RPCJWT.MaxSkewSeconds != 90 {
		t.Fatalf("unexpected RPC JWT skew: %d", cfg.RPCJWT.MaxSkewSeconds)
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
	if cfg.RPCMaxTxPerWindow != 10 {
		t.Fatalf("unexpected RPC max tx per window: %d", cfg.RPCMaxTxPerWindow)
	}
	if cfg.RPCRateLimitWindow != 120 {
		t.Fatalf("unexpected RPC rate limit window: %d", cfg.RPCRateLimitWindow)
	}
	if !cfg.RPCAllowInsecure {
		t.Fatalf("expected RPCAllowInsecure to be true")
	}
	if cfg.RPCTLSCertFile != "/path/to/cert.pem" || cfg.RPCTLSKeyFile != "/path/to/key.pem" {
		t.Fatalf("unexpected RPC TLS paths: %s %s", cfg.RPCTLSCertFile, cfg.RPCTLSKeyFile)
	}
	if cfg.RPCTLSClientCAFile != "/path/to/clients.pem" {
		t.Fatalf("unexpected RPC client CA file: %s", cfg.RPCTLSClientCAFile)
	}
	if header := cfg.NetworkSecurity.AuthorizationHeaderName(); header != "x-test-token" {
		t.Fatalf("unexpected auth header: %s", header)
	}
	if cfg.NetworkSecurity.SharedSecret != "topsecret" {
		t.Fatalf("unexpected inline shared secret: %s", cfg.NetworkSecurity.SharedSecret)
	}
	if cfg.NetworkSecurity.SharedSecretFile != "./secret.txt" {
		t.Fatalf("unexpected shared secret file: %s", cfg.NetworkSecurity.SharedSecretFile)
	}
	if cfg.NetworkSecurity.SharedSecretEnv != "NHB_TEST_SECRET" {
		t.Fatalf("unexpected shared secret env: %s", cfg.NetworkSecurity.SharedSecretEnv)
	}
	if cfg.NetworkSecurity.ServerTLSCertFile != "./tls/server.crt" || cfg.NetworkSecurity.ServerTLSKeyFile != "./tls/server.key" {
		t.Fatalf("unexpected server tls paths: %+v", cfg.NetworkSecurity)
	}
	if cfg.NetworkSecurity.ServerCAFile != "./tls/ca.pem" {
		t.Fatalf("unexpected server CA file: %s", cfg.NetworkSecurity.ServerCAFile)
	}
	if cfg.NetworkSecurity.ClientCAFile != "./tls/client-ca.pem" {
		t.Fatalf("unexpected client CA file: %s", cfg.NetworkSecurity.ClientCAFile)
	}
	if cfg.NetworkSecurity.ClientTLSCertFile != "./tls/client.crt" || cfg.NetworkSecurity.ClientTLSKeyFile != "./tls/client.key" {
		t.Fatalf("unexpected client tls paths: %+v", cfg.NetworkSecurity)
	}
	if cfg.NetworkSecurity.AllowInsecure {
		t.Fatalf("expected AllowInsecure to default to false")
	}
	if cfg.NetworkSecurity.AllowUnauthenticatedReads {
		t.Fatalf("expected AllowUnauthenticatedReads to default to false")
	}
	if cfg.NetworkSecurity.ServerName != "p2pd.internal" {
		t.Fatalf("unexpected server name: %s", cfg.NetworkSecurity.ServerName)
	}
	if len(cfg.NetworkSecurity.AllowedClientCommonNames) != 1 || cfg.NetworkSecurity.AllowedClientCommonNames[0] != "consensusd" {
		t.Fatalf("unexpected allowed common names: %v", cfg.NetworkSecurity.AllowedClientCommonNames)
	}
	if cfg.NetworkSecurity.StreamQueueSize != defaultStreamQueueSize {
		t.Fatalf("unexpected stream queue size: %d", cfg.NetworkSecurity.StreamQueueSize)
	}
	if cfg.NetworkSecurity.RelayDropLogRatio != defaultRelayDropLogRatio {
		t.Fatalf("unexpected relay drop log ratio: %f", cfg.NetworkSecurity.RelayDropLogRatio)
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

func TestEnsureGlobalDefaultsLoyaltyDynamic(t *testing.T) {
	var cfg Config
	cfg.ensureGlobalDefaults(toml.MetaData{})

	dyn := cfg.Global.Loyalty.Dynamic
	if dyn.TargetBPS != defaultLoyaltyTargetBPS {
		t.Fatalf("unexpected target bps: %d", dyn.TargetBPS)
	}
	if dyn.MinBPS != defaultLoyaltyMinBPS || dyn.MaxBPS != defaultLoyaltyMaxBPS {
		t.Fatalf("unexpected min/max bps: %d/%d", dyn.MinBPS, dyn.MaxBPS)
	}
	if dyn.SmoothingStepBPS != defaultLoyaltySmoothingStepBPS {
		t.Fatalf("unexpected smoothing step: %d", dyn.SmoothingStepBPS)
	}
	if dyn.CoverageMax != defaultLoyaltyCoverageMax {
		t.Fatalf("unexpected coverage max: %f", dyn.CoverageMax)
	}
	if dyn.CoverageLookbackDays != defaultLoyaltyCoverageLookbackDays {
		t.Fatalf("unexpected coverage lookback: %d", dyn.CoverageLookbackDays)
	}
	if dyn.DailyCapPctOf7dFees != defaultLoyaltyDailyCapPctOf7dFees {
		t.Fatalf("unexpected daily cap pct: %f", dyn.DailyCapPctOf7dFees)
	}
	if dyn.DailyCapUSD != defaultLoyaltyDailyCapUSD {
		t.Fatalf("unexpected daily cap USD: %f", dyn.DailyCapUSD)
	}
	if dyn.YearlyCapPctOfInitialSupply != defaultLoyaltyYearlyCapPctOfInitialSupply {
		t.Fatalf("unexpected yearly cap pct: %f", dyn.YearlyCapPctOfInitialSupply)
	}
	guard := dyn.PriceGuard
	if guard.PricePair != defaultLoyaltyPricePair {
		t.Fatalf("unexpected price pair: %s", guard.PricePair)
	}
	if guard.TwapWindowSeconds != defaultLoyaltyPriceGuardTwapWindowSeconds {
		t.Fatalf("unexpected twap window: %d", guard.TwapWindowSeconds)
	}
	if guard.MaxDeviationBPS != defaultLoyaltyPriceGuardMaxDeviation {
		t.Fatalf("unexpected max deviation: %d", guard.MaxDeviationBPS)
	}
	if guard.PriceMaxAgeSeconds != defaultLoyaltyPriceGuardMaxAgeSeconds {
		t.Fatalf("unexpected price max age: %d", guard.PriceMaxAgeSeconds)
	}
	if guard.FallbackMinEmissionZNHBWei != defaultLoyaltyPriceGuardFallbackMinEmission {
		t.Fatalf("unexpected fallback min emission: %s", guard.FallbackMinEmissionZNHBWei)
	}
	if !guard.UseLastGoodPriceFallback {
		t.Fatalf("expected last good price fallback enabled by default")
	}
	if !dyn.EnforceProRate {
		t.Fatalf("expected pro-rate enforcement default enabled")
	}
}

func TestEnsureGlobalDefaultsLoyaltyDynamicOverride(t *testing.T) {
	const raw = `
[global.loyalty.Dynamic]
TargetBPS = 75
MinBPS = 15
MaxBPS = 250
SmoothingStepBPS = 12
CoverageMax = 0.42
CoverageLookbackDays = 21
DailyCapPctOf7dFees = 0.37
DailyCapUSD = 3210.5
YearlyCapPctOfInitialSupply = 12.5
EnableProRate = false
EnforceProRate = false

  [global.loyalty.Dynamic.PriceGuard]
  Enabled = true
  PricePair = "ZNHB/EUR"
  TwapWindowSeconds = 7200
  MaxDeviationBPS = 275
  PriceMaxAgeSeconds = 450
  FallbackMinEmissionZNHBWei = "250000000000000000"
  UseLastGoodPriceFallback = false
`

	var cfg Config
	meta, err := toml.Decode(raw, &cfg)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}

	cfg.ensureGlobalDefaults(meta)

	dyn := cfg.Global.Loyalty.Dynamic
	if dyn.TargetBPS != 75 || dyn.MinBPS != 15 || dyn.MaxBPS != 250 {
		t.Fatalf("unexpected bps band: %+v", dyn)
	}
	if dyn.SmoothingStepBPS != 12 {
		t.Fatalf("unexpected smoothing step: %d", dyn.SmoothingStepBPS)
	}
	if dyn.CoverageMax != 0.42 || dyn.CoverageLookbackDays != 21 {
		t.Fatalf("unexpected coverage values: %f/%d", dyn.CoverageMax, dyn.CoverageLookbackDays)
	}
	if dyn.DailyCapPctOf7dFees != 0.37 || dyn.DailyCapUSD != 3210.5 {
		t.Fatalf("unexpected daily caps: %f/%f", dyn.DailyCapPctOf7dFees, dyn.DailyCapUSD)
	}
	if dyn.YearlyCapPctOfInitialSupply != 12.5 {
		t.Fatalf("unexpected yearly cap: %f", dyn.YearlyCapPctOfInitialSupply)
	}
	guard := dyn.PriceGuard
	if !guard.Enabled {
		t.Fatalf("expected price guard enabled override")
	}
	if guard.PricePair != "ZNHB/EUR" {
		t.Fatalf("unexpected price pair: %s", guard.PricePair)
	}
	if guard.TwapWindowSeconds != 7200 || guard.MaxDeviationBPS != 275 || guard.PriceMaxAgeSeconds != 450 {
		t.Fatalf("unexpected price guard values: %+v", guard)
	}
	if guard.FallbackMinEmissionZNHBWei != "250000000000000000" {
		t.Fatalf("unexpected fallback min emission override: %s", guard.FallbackMinEmissionZNHBWei)
	}
	if guard.UseLastGoodPriceFallback {
		t.Fatalf("expected last good price fallback override to disable")
	}
	if dyn.EnableProRate {
		t.Fatalf("expected pro-rate toggle override to disable queueing")
	}
	if dyn.EnforceProRate {
		t.Fatalf("expected enforcement override to disable guard")
	}
}

func TestLoadAppliesDefaultMempoolLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = ":6001"
RPCAddress = ":8080"
DataDir = "%s"
ValidatorKeystorePath = "%s"
`, dir, keystorePath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Mempool.MaxTransactions != DefaultMempoolMaxTransactions {
		t.Fatalf("expected default mempool limit %d, got %d", DefaultMempoolMaxTransactions, cfg.Mempool.MaxTransactions)
	}
	if cfg.Mempool.AllowUnlimited {
		t.Fatalf("expected unlimited opt-in to remain disabled")
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

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
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

func TestGovPolicyParsingInvalidTreasuryAddress(t *testing.T) {
	cfg := GovConfig{TreasuryAllowList: []string{"nhb1invalid"}}

	_, err := cfg.Policy()
	if err == nil {
		t.Fatalf("expected error for malformed treasury address")
	}
	if !strings.Contains(err.Error(), "invalid TreasuryAllowList entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSetsGlobalDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:6001"
ValidatorKeystorePath = "%s"
`, keystorePath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := defaultGlobalConfig()
	if !reflect.DeepEqual(cfg.Global, want) {
		t.Fatalf("unexpected global defaults: %+v", cfg.Global)
	}
}

func TestLoadOverridesStakingAndPauses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:6001"
ValidatorKeystorePath = "%s"

[global.staking]
AprBps = 2400
PayoutPeriodDays = 14
UnbondingDays = 21
MinStakeWei = "1000000000000000000"
MaxEmissionPerYearWei = "2000000000000000000"
RewardAsset = "TNHB"
CompoundDefault = true

[global.pauses]
Staking = true
TransferZNHB = true
`, keystorePath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	staking := cfg.Global.Staking
	if staking.AprBps != 2400 || staking.PayoutPeriodDays != 14 || staking.UnbondingDays != 21 {
		t.Fatalf("unexpected staking periods: %+v", staking)
	}
	if staking.MinStakeWei != "1000000000000000000" {
		t.Fatalf("unexpected min stake: %s", staking.MinStakeWei)
	}
	if staking.MaxEmissionPerYearWei != "2000000000000000000" {
		t.Fatalf("unexpected max emission: %s", staking.MaxEmissionPerYearWei)
	}
	if staking.RewardAsset != "TNHB" {
		t.Fatalf("unexpected reward asset: %s", staking.RewardAsset)
	}
	if !staking.CompoundDefault {
		t.Fatalf("expected compound default to be true")
	}
	if !cfg.Global.Pauses.Staking || !cfg.Global.Pauses.TransferZNHB {
		t.Fatalf("unexpected pauses: %+v", cfg.Global.Pauses)
	}
}

func TestLoadSetsConsensusDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:6001"
ValidatorKeystorePath = "%s"
`, keystorePath)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	want := defaultConsensusConfig()
	if cfg.Consensus != want {
		t.Fatalf("unexpected consensus defaults: %+v", cfg.Consensus)
	}
}

func TestLoadWithoutPassphraseFailsToCreateDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if _, err := Load(path); err == nil {
		t.Fatalf("expected error when no keystore passphrase is provided")
	}
}

func TestLoadCreatesKeystoreWithPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	passphrase := "strong-passphrase"

	cfg, err := Load(path, WithKeystorePassphrase(passphrase))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ValidatorKeystorePath == "" {
		t.Fatalf("expected validator keystore path to be set")
	}
	if _, err := os.Stat(cfg.ValidatorKeystorePath); err != nil {
		t.Fatalf("expected keystore file to exist: %v", err)
	}

	key, err := crypto.LoadFromKeystore(cfg.ValidatorKeystorePath, passphrase)
	if err != nil {
		t.Fatalf("failed to decrypt keystore: %v", err)
	}
	if key == nil {
		t.Fatalf("expected decrypted key")
	}
}
