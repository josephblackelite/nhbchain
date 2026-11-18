package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The settings an operator turns on in the shipped config.toml (see its comments and docs/consensus/invariants.md) must be read by the real loader under the
// names the file gives them. A misspelt or misplaced key would be silently ignored and the setting would stay off on that validator only, so every validator
// would not carry the same value. This sets each of them in the shipped file, loads it and checks what the loader holds.
func TestShippedConfigReadsEveryActivationSettingOperatorsTurnOn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	text := string(raw)

	set := func(key string, value string) {
		t.Helper()
		pattern := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(key) + `\s*=\s*\d+\s*$`)
		if len(pattern.FindAllStringIndex(text, -1)) != 1 {
			t.Fatalf("expected exactly one line setting %s in the shipped config.toml, found %d", key, len(pattern.FindAllStringIndex(text, -1)))
		}
		text = pattern.ReplaceAllString(text, "${1}"+key+" = "+value)
	}
	set("RewardsByStakeFromEpoch", "17300")
	set("POSSweepSkipActivationHeight", "1722000")
	set("POSHoldUnstakeGuardActivationHeight", "1723000")
	set("GovCreditPreservationActivationHeight", "1724000")
	set("FeeRouteAliasActivationHeight", "1725000")
	set("LendingFixedTermIDActivationHeight", "1726000")
	set("NHBSupplyGenesisCorrectionActivationHeight", "1728000")
	set("TransferMonthlyFreeTierActivationHeight", "1729000")
	set("TransferMonthlyFreeTierTxPerMonth", "100")
	set("LoyaltyBudgetFloorActivationHeight", "1730000")
	set("PotsoGovernanceWeightActivationHeight", "1731000")
	set("ValidatorFlagNonceActivationHeight", "1732000")
	set("MintRunningChainIDActivationHeight", "1733000")
	set("MintPhaseEChainIDRetireHeight", "1734000")
	set("TxNetworkChainIDActivationHeight", "1735000")
	set("VoteChainBindingActivationHeight", "1736000")
	set("SlashEligibilityRefreshActivationHeight", "1751000")
	set("SlashPendingUnbondsActivationHeight", "1752000")
	set("EvidenceIntakeGuardActivationHeight", "1746000")
	set("StakeAPRFromStateActivationHeight", "1747000")
	set("ValidatorAdmissionGuardActivationHeight", "1748000")
	set("ValidatorAdmissionProbationSeconds", "1749000")
	set("PotsoTreasuryShortfallActivationHeight", "1750000")
	set("LendingAccountingFixesActivationHeight", "1753000")
	set("LendingPoolLimitsActivationHeight", "1754000")
	set("LendingMaxPools", "64")
	set("LendingPoolIDMaxLength", "48")
	set("LendingRefPriceGuardsActivationHeight", "1755000")
	set("LendingRefPriceMaxAheadSeconds", "120")
	set("LendingRefPriceMaxAgeSeconds", "3600")
	set("LendingRiskActivationHeight", "1756000")
	set("LendingFeeWithdrawActivationHeight", "1757000")
	set("PaymasterSponsorBindingActivationHeight", "1758000")
	set("LoyaltyPaymasterConsentActivationHeight", "1759000")
	set("LoyaltyCounterpartyCapActivationHeight", "1760000")
	set("BuybackMinAskActivationHeight", "1761000")
	set("SubscriptionsPauseSettlementActivationHeight", "1762000")
	set("SubscriptionsChargeHistoryMigrationActivationHeight", "1737000")
	set("BuybackUnpriceableEpochRefundActivationHeight", "1766000")
	set("SwapVoucherSignedBudgetActivationHeight", "1765000")
	set("SponsoredTransferFeeCreditActivationHeight", "1764000")
	set("UniformTransferFeeActivationHeight", "1767000")
	set("TransferFeeGovernanceActivationHeight", "1768000")
	set("TransferFeeGovernanceDelayBlocks", "4321")
	set("BuybackPriceGuardActivationHeight", "1732000")
	set("EpochSettlementFailsafeActivationHeight", "1733000")
	set("TransferFeeStrictAllowanceActivationHeight", "1769000")
	set("SaleReferenceActivationHeight", "1770000")
	set("SaleReferenceMaxAgeSeconds", "7200")
	set("SaleReferenceMaxStepBps", "1500")
	set("PayoutReceiptActivationHeight", "1727000")
	set("RejectAmbiguousParamKeysFromUnix", "1900000000")
	if !strings.Contains(text, "LoyaltyCounterpartyCapZNHBWei = \"\"\n") {
		t.Fatalf("the shipped config.toml does not carry LoyaltyCounterpartyCapZNHBWei empty")
	}
	text = strings.Replace(text, "LoyaltyCounterpartyCapZNHBWei = \"\"\n", "LoyaltyCounterpartyCapZNHBWei = \"50e18\"\n", 1)
	if !strings.Contains(text, "BuybackMinAskZNHBWei = \"\"\n") {
		t.Fatalf("the shipped config.toml does not carry BuybackMinAskZNHBWei empty")
	}
	text = strings.Replace(text, "BuybackMinAskZNHBWei = \"\"\n", "BuybackMinAskZNHBWei = \"100e18\"\n", 1)

	dir := t.TempDir()
	keystorePath := filepath.Join(dir, "validator.keystore")
	text = regexp.MustCompile(`(?m)^ValidatorKeystorePath\s*=.*$`).ReplaceAllString(text, "ValidatorKeystorePath = "+tomlQuoted(keystorePath))
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load the shipped config with every setting on: %v", err)
	}

	for name, got := range map[string][2]uint64{
		"RewardsByStakeFromEpoch":                             {cfg.RewardsByStakeFromEpoch, 17300},
		"POSSweepSkipActivationHeight":                        {cfg.POSSweepSkipActivationHeight, 1722000},
		"POSHoldUnstakeGuardActivationHeight":                 {cfg.POSHoldUnstakeGuardActivationHeight, 1723000},
		"GovCreditPreservationActivationHeight":               {cfg.GovCreditPreservationActivationHeight, 1724000},
		"FeeRouteAliasActivationHeight":                       {cfg.FeeRouteAliasActivationHeight, 1725000},
		"LendingFixedTermIDActivationHeight":                  {cfg.LendingFixedTermIDActivationHeight, 1726000},
		"NHBSupplyGenesisCorrectionActivationHeight":          {cfg.NHBSupplyGenesisCorrectionActivationHeight, 1728000},
		"TransferMonthlyFreeTierActivationHeight":             {cfg.TransferMonthlyFreeTierActivationHeight, 1729000},
		"TransferMonthlyFreeTierTxPerMonth":                   {cfg.TransferMonthlyFreeTierTxPerMonth, 100},
		"LoyaltyBudgetFloorActivationHeight":                  {cfg.LoyaltyBudgetFloorActivationHeight, 1730000},
		"PotsoGovernanceWeightActivationHeight":               {cfg.PotsoGovernanceWeightActivationHeight, 1731000},
		"ValidatorFlagNonceActivationHeight":                  {cfg.ValidatorFlagNonceActivationHeight, 1732000},
		"MintRunningChainIDActivationHeight":                  {cfg.MintRunningChainIDActivationHeight, 1733000},
		"MintPhaseEChainIDRetireHeight":                       {cfg.MintPhaseEChainIDRetireHeight, 1734000},
		"TxNetworkChainIDActivationHeight":                    {cfg.TxNetworkChainIDActivationHeight, 1735000},
		"VoteChainBindingActivationHeight":                    {cfg.VoteChainBindingActivationHeight, 1736000},
		"SlashEligibilityRefreshActivationHeight":             {cfg.SlashEligibilityRefreshActivationHeight, 1751000},
		"SlashPendingUnbondsActivationHeight":                 {cfg.SlashPendingUnbondsActivationHeight, 1752000},
		"EvidenceIntakeGuardActivationHeight":                 {cfg.EvidenceIntakeGuardActivationHeight, 1746000},
		"StakeAPRFromStateActivationHeight":                   {cfg.StakeAPRFromStateActivationHeight, 1747000},
		"ValidatorAdmissionGuardActivationHeight":             {cfg.ValidatorAdmissionGuardActivationHeight, 1748000},
		"ValidatorAdmissionProbationSeconds":                  {cfg.ValidatorAdmissionProbationSeconds, 1749000},
		"PotsoTreasuryShortfallActivationHeight":              {cfg.PotsoTreasuryShortfallActivationHeight, 1750000},
		"LendingAccountingFixesActivationHeight":              {cfg.LendingAccountingFixesActivationHeight, 1753000},
		"LendingPoolLimitsActivationHeight":                   {cfg.LendingPoolLimitsActivationHeight, 1754000},
		"LendingMaxPools":                                     {cfg.LendingMaxPools, 64},
		"LendingPoolIDMaxLength":                              {cfg.LendingPoolIDMaxLength, 48},
		"LendingRefPriceGuardsActivationHeight":               {cfg.LendingRefPriceGuardsActivationHeight, 1755000},
		"LendingRefPriceMaxAheadSeconds":                      {cfg.LendingRefPriceMaxAheadSeconds, 120},
		"LendingRefPriceMaxAgeSeconds":                        {cfg.LendingRefPriceMaxAgeSeconds, 3600},
		"LendingRiskActivationHeight":                         {cfg.LendingRiskActivationHeight, 1756000},
		"LendingFeeWithdrawActivationHeight":                  {cfg.LendingFeeWithdrawActivationHeight, 1757000},
		"PaymasterSponsorBindingActivationHeight":             {cfg.PaymasterSponsorBindingActivationHeight, 1758000},
		"LoyaltyPaymasterConsentActivationHeight":             {cfg.LoyaltyPaymasterConsentActivationHeight, 1759000},
		"LoyaltyCounterpartyCapActivationHeight":              {cfg.LoyaltyCounterpartyCapActivationHeight, 1760000},
		"BuybackMinAskActivationHeight":                       {cfg.BuybackMinAskActivationHeight, 1761000},
		"SubscriptionsPauseSettlementActivationHeight":        {cfg.SubscriptionsPauseSettlementActivationHeight, 1762000},
		"SubscriptionsChargeHistoryMigrationActivationHeight": {cfg.SubscriptionsChargeHistoryMigrationActivationHeight, 1737000},
		"BuybackUnpriceableEpochRefundActivationHeight":       {cfg.BuybackUnpriceableEpochRefundActivationHeight, 1766000},
		"SwapVoucherSignedBudgetActivationHeight":             {cfg.SwapVoucherSignedBudgetActivationHeight, 1765000},
		"SponsoredTransferFeeCreditActivationHeight":          {cfg.SponsoredTransferFeeCreditActivationHeight, 1764000},
		"UniformTransferFeeActivationHeight":                  {cfg.UniformTransferFeeActivationHeight, 1767000},
		"TransferFeeGovernanceActivationHeight":               {cfg.TransferFeeGovernanceActivationHeight, 1768000},
		"TransferFeeGovernanceDelayBlocks":                    {cfg.TransferFeeGovernanceDelayBlocks, 4321},
		"BuybackPriceGuardActivationHeight":                   {cfg.BuybackPriceGuardActivationHeight, 1732000},
		"EpochSettlementFailsafeActivationHeight":             {cfg.EpochSettlementFailsafeActivationHeight, 1733000},
		"TransferFeeStrictAllowanceActivationHeight":          {cfg.TransferFeeStrictAllowanceActivationHeight, 1769000},
		"SaleReferenceActivationHeight":                       {cfg.SaleReferenceActivationHeight, 1770000},
		"SaleReferenceMaxAgeSeconds":                          {cfg.SaleReferenceMaxAgeSeconds, 7200},
		"SaleReferenceMaxStepBps":                             {cfg.SaleReferenceMaxStepBps, 1500},
		"[swap] PayoutReceiptActivationHeight":                {cfg.Swap.PayoutReceiptActivationHeight, 1727000},
		"[governance] RejectAmbiguousParamKeysFromUnix":       {cfg.Governance.RejectAmbiguousParamKeysFromUnix, 1900000000},
	} {
		if got[0] != got[1] {
			t.Errorf("%s: the loader holds %d, the file says %d (a key in the wrong place is ignored)", name, got[0], got[1])
		}
	}
	if cfg.BuybackMinAskZNHBWei != "100e18" {
		t.Errorf("BuybackMinAskZNHBWei is %q, the file says 100e18", cfg.BuybackMinAskZNHBWei)
	}
	if cfg.LoyaltyCounterpartyCapZNHBWei != "50e18" {
		t.Errorf("LoyaltyCounterpartyCapZNHBWei is %q, the file says 50e18", cfg.LoyaltyCounterpartyCapZNHBWei)
	}
	if cfg.QuorumCertActivationHeight != 1380200 {
		t.Errorf("QuorumCertActivationHeight is %d, the shipped file says 1380200", cfg.QuorumCertActivationHeight)
	}
}

// An amount that is not an amount is refused when the config is loaded, not when a
// validator first reaches the height.
func TestShippedConfigRefusesABadCounterpartyCapAmount(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	text := strings.Replace(string(raw), "LoyaltyCounterpartyCapZNHBWei = \"\"\n", "LoyaltyCounterpartyCapZNHBWei = \"lots\"\n", 1)
	dir := t.TempDir()
	text = regexp.MustCompile(`(?m)^ValidatorKeystorePath\s*=.*$`).ReplaceAllString(text, "ValidatorKeystorePath = "+tomlQuoted(filepath.Join(dir, "validator.keystore")))
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase)); err == nil || !strings.Contains(err.Error(), "LoyaltyCounterpartyCapZNHBWei") {
		t.Fatalf("loading a bad amount: err = %v, want a LoyaltyCounterpartyCapZNHBWei error", err)
	}
}

// RPCSwapOperatorNoHMAC ships off and is read by the loader under that name.
func TestShippedConfigCarriesTheSwapOperatorSettingOffAndReadsItOn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	if !strings.Contains(string(raw), "\nRPCSwapOperatorNoHMAC = false\n") {
		t.Fatalf("the shipped config.toml does not carry RPCSwapOperatorNoHMAC = false")
	}
	load := func(text string) *Config {
		t.Helper()
		dir := t.TempDir()
		text = regexp.MustCompile(`(?m)^ValidatorKeystorePath\s*=.*$`).ReplaceAllString(text, "ValidatorKeystorePath = "+tomlQuoted(filepath.Join(dir, "validator.keystore")))
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return cfg
	}
	if load(string(raw)).RPCSwapOperatorNoHMAC {
		t.Fatalf("the shipped config turns the setting on")
	}
	on := strings.Replace(string(raw), "\nRPCSwapOperatorNoHMAC = false\n", "\nRPCSwapOperatorNoHMAC = true\n", 1)
	if !load(on).RPCSwapOperatorNoHMAC {
		t.Fatalf("the loader did not read RPCSwapOperatorNoHMAC = true")
	}
}
