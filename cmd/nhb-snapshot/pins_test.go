package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	repoConfig  = "../../config.toml"
	repoGenesis = "../../config/genesis.relaunch.json"
)

// The config a new validator is built from must carry the values the live
// validators run with. This is the guard that keeps config.toml from drifting
// away from the genesis it is shipped with (it once named the treasury of an
// earlier network, which made a node started from a snapshot diverge at the
// first reward epoch).
func TestShippedConfigCarriesTheLiveConsensusValues(t *testing.T) {
	bad, err := checkLiveConfig(repoConfig, repoGenesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("config.toml disagrees with %s:\n  %s", repoGenesis, strings.Join(bad, "\n  "))
	}
}

func mutatedConfig(t *testing.T, edit func(string) string) string {
	t.Helper()
	raw, err := os.ReadFile(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	out := edit(string(raw))
	if out == string(raw) {
		t.Fatalf("the edit changed nothing")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigWithADriftedValueIsRefused(t *testing.T) {
	const (
		oldAdmin    = "nhb10lephh6ffd79cc7lk6edc6rkxe9ha8xekt0y8h"
		oldTreasury = "znhb10lephh6ffd79cc7lk6edc6rkxe9ha8xemef0ak"
		liveAdmin   = "nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj"
		liveZNHB    = "znhb1spruw63528zhhys2zxfgu2yf5ulcrlclx6hfhn"
	)
	cases := map[string]struct {
		edit func(string) string
		want string
	}{
		"quorum certificate height": {
			func(s string) string {
				return strings.Replace(s, "QuorumCertActivationHeight = 1380200", "QuorumCertActivationHeight = 451949", 1)
			},
			"QuorumCertActivationHeight",
		},
		"quorum certificate left off (zero)": {
			func(s string) string {
				return strings.Replace(s, "QuorumCertActivationHeight = 1380200", "QuorumCertActivationHeight = 0", 1)
			},
			"QuorumCertActivationHeight",
		},
		"epoch rewards paid by stake from another epoch than the live validators": {
			func(s string) string {
				return strings.Replace(s, "RewardsByStakeFromEpoch = 17280", "RewardsByStakeFromEpoch = 16700", 1)
			},
			"RewardsByStakeFromEpoch",
		},
		"POS expiry sweep skip switched on": {
			func(s string) string {
				return strings.Replace(s, "POSSweepSkipActivationHeight = 1718500", "POSSweepSkipActivationHeight = 1800000", 1)
			},
			"POSSweepSkipActivationHeight",
		},
		"POS hold unstake guard switched on": {
			func(s string) string {
				return strings.Replace(s, "POSHoldUnstakeGuardActivationHeight = 1718500", "POSHoldUnstakeGuardActivationHeight = 1800000", 1)
			},
			"POSHoldUnstakeGuardActivationHeight",
		},
		"governance credit preservation switched on": {
			func(s string) string {
				return strings.Replace(s, "GovCreditPreservationActivationHeight = 1719700", "GovCreditPreservationActivationHeight = 1800000", 1)
			},
			"GovCreditPreservationActivationHeight",
		},
		"NHB fee route aliasing fix switched on": {
			func(s string) string {
				return strings.Replace(s, "FeeRouteAliasActivationHeight = 1720900", "FeeRouteAliasActivationHeight = 1800000", 1)
			},
			"FeeRouteAliasActivationHeight",
		},
		"owner-scoped fixed-term lending IDs switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingFixedTermIDActivationHeight = 1723300", "LendingFixedTermIDActivationHeight = 1800000", 1)
			},
			"LendingFixedTermIDActivationHeight",
		},
		"NHB supply genesis correction switched on": {
			func(s string) string {
				return strings.Replace(s, "NHBSupplyGenesisCorrectionActivationHeight = 0", "NHBSupplyGenesisCorrectionActivationHeight = 1800000", 1)
			},
			"NHBSupplyGenesisCorrectionActivationHeight",
		},
		"monthly free tier for plain transfers switched on": {
			func(s string) string {
				return strings.Replace(s, "TransferMonthlyFreeTierActivationHeight = 0", "TransferMonthlyFreeTierActivationHeight = 1800000", 1)
			},
			"TransferMonthlyFreeTierActivationHeight",
		},
		"base-reward budget floor switched on": {
			func(s string) string {
				return strings.Replace(s, "LoyaltyBudgetFloorActivationHeight = 0", "LoyaltyBudgetFloorActivationHeight = 1800000", 1)
			},
			"LoyaltyBudgetFloorActivationHeight",
		},
		"governance weight snapshot for delegators and engaged accounts switched on": {
			func(s string) string {
				return strings.Replace(s, "PotsoGovernanceWeightActivationHeight = 0", "PotsoGovernanceWeightActivationHeight = 1800000", 1)
			},
			"PotsoGovernanceWeightActivationHeight",
		},
		"zero-value validator register and deregister consume the nonce": {
			func(s string) string {
				return strings.Replace(s, "ValidatorFlagNonceActivationHeight = 2726000", "ValidatorFlagNonceActivationHeight = 1732000", 1)
			},
			"ValidatorFlagNonceActivationHeight",
		},
		"mint vouchers naming the running chain id accepted": {
			func(s string) string {
				return strings.Replace(s, "MintRunningChainIDActivationHeight = 0", "MintRunningChainIDActivationHeight = 1733000", 1)
			},
			"MintRunningChainIDActivationHeight",
		},
		"mint vouchers naming the retired Phase E chain id refused": {
			func(s string) string {
				return strings.Replace(s, "MintPhaseEChainIDRetireHeight = 0", "MintPhaseEChainIDRetireHeight = 1734000", 1)
			},
			"MintPhaseEChainIDRetireHeight",
		},
		"transactions must carry the network chain id": {
			func(s string) string {
				return strings.Replace(s, "TxNetworkChainIDActivationHeight = 0", "TxNetworkChainIDActivationHeight = 1735000", 1)
			},
			"TxNetworkChainIDActivationHeight",
		},
		"consensus votes bound to the genesis hash": {
			func(s string) string {
				return strings.Replace(s, "VoteChainBindingActivationHeight = 2726000", "VoteChainBindingActivationHeight = 1736000", 1)
			},
			"VoteChainBindingActivationHeight",
		},
		"slashed validators re-evaluated in the slashing block": {
			func(s string) string {
				return strings.Replace(s, "SlashEligibilityRefreshActivationHeight = 2726000", "SlashEligibilityRefreshActivationHeight = 1800000", 1)
			},
			"SlashEligibilityRefreshActivationHeight",
		},
		"slash reaching pending unbonds switched on": {
			func(s string) string {
				return strings.Replace(s, "SlashPendingUnbondsActivationHeight = 2726000", "SlashPendingUnbondsActivationHeight = 1800000", 1)
			},
			"SlashPendingUnbondsActivationHeight",
		},
		"evidence intake guard switched on": {
			func(s string) string {
				return strings.Replace(s, "EvidenceIntakeGuardActivationHeight = 2726000", "EvidenceIntakeGuardActivationHeight = 1800000", 1)
			},
			"EvidenceIntakeGuardActivationHeight",
		},
		"stake reward rate read from the chain switched on": {
			func(s string) string {
				return strings.Replace(s, "StakeAPRFromStateActivationHeight = 0", "StakeAPRFromStateActivationHeight = 1800000", 1)
			},
			"StakeAPRFromStateActivationHeight",
		},
		"validator admission guard switched on": {
			func(s string) string {
				return strings.Replace(s, "ValidatorAdmissionGuardActivationHeight = 0", "ValidatorAdmissionGuardActivationHeight = 1800000", 1)
			},
			"ValidatorAdmissionGuardActivationHeight",
		},
		"validator admission probation set": {
			func(s string) string {
				return strings.Replace(s, "ValidatorAdmissionProbationSeconds = 0", "ValidatorAdmissionProbationSeconds = 1800000", 1)
			},
			"ValidatorAdmissionProbationSeconds",
		},
		"treasury shortfall detection switched on": {
			func(s string) string {
				return strings.Replace(s, "PotsoTreasuryShortfallActivationHeight = 0", "PotsoTreasuryShortfallActivationHeight = 1800000", 1)
			},
			"PotsoTreasuryShortfallActivationHeight",
		},
		"lending accounting fixes switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingAccountingFixesActivationHeight = 0", "LendingAccountingFixesActivationHeight = 1800000", 1)
			},
			"LendingAccountingFixesActivationHeight",
		},
		"lending pool limits switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingPoolLimitsActivationHeight = 0", "LendingPoolLimitsActivationHeight = 1800000", 1)
			},
			"LendingPoolLimitsActivationHeight",
		},
		"lending pool count limit set": {
			func(s string) string {
				return strings.Replace(s, "LendingMaxPools = 0", "LendingMaxPools = 1800000", 1)
			},
			"LendingMaxPools",
		},
		"lending pool id length limit set": {
			func(s string) string {
				return strings.Replace(s, "LendingPoolIDMaxLength = 0", "LendingPoolIDMaxLength = 1800000", 1)
			},
			"LendingPoolIDMaxLength",
		},
		"lending reference-price guards switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingRefPriceGuardsActivationHeight = 0", "LendingRefPriceGuardsActivationHeight = 1800000", 1)
			},
			"LendingRefPriceGuardsActivationHeight",
		},
		"lending reference-price future bound set": {
			func(s string) string {
				return strings.Replace(s, "LendingRefPriceMaxAheadSeconds = 0", "LendingRefPriceMaxAheadSeconds = 1800000", 1)
			},
			"LendingRefPriceMaxAheadSeconds",
		},
		"lending reference-price age bound set": {
			func(s string) string {
				return strings.Replace(s, "LendingRefPriceMaxAgeSeconds = 0", "LendingRefPriceMaxAgeSeconds = 1800000", 1)
			},
			"LendingRefPriceMaxAgeSeconds",
		},
		"lending risk limits switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingRiskActivationHeight = 0", "LendingRiskActivationHeight = 1800000", 1)
			},
			"LendingRiskActivationHeight",
		},
		"lending protocol fee withdrawal switched on": {
			func(s string) string {
				return strings.Replace(s, "LendingFeeWithdrawActivationHeight = 0", "LendingFeeWithdrawActivationHeight = 1800000", 1)
			},
			"LendingFeeWithdrawActivationHeight",
		},
		"sponsor signature bound to the sender switched on": {
			func(s string) string {
				return strings.Replace(s, "PaymasterSponsorBindingActivationHeight = 0", "PaymasterSponsorBindingActivationHeight = 1800000", 1)
			},
			"PaymasterSponsorBindingActivationHeight",
		},
		"loyalty reward-time paymaster consent switched on": {
			func(s string) string {
				return strings.Replace(s, "LoyaltyPaymasterConsentActivationHeight = 0", "LoyaltyPaymasterConsentActivationHeight = 1800000", 1)
			},
			"LoyaltyPaymasterConsentActivationHeight",
		},
		"loyalty per-counterparty reward cap switched on": {
			func(s string) string {
				return strings.Replace(s, "LoyaltyCounterpartyCapActivationHeight = 0", "LoyaltyCounterpartyCapActivationHeight = 1800000", 1)
			},
			"LoyaltyCounterpartyCapActivationHeight",
		},
		"buyback minimum ask raised": {
			func(s string) string {
				return strings.Replace(s, "BuybackMinAskActivationHeight = 0", "BuybackMinAskActivationHeight = 1800000", 1)
			},
			"BuybackMinAskActivationHeight",
		},
		"subscription billing stops while the module is paused": {
			func(s string) string {
				return strings.Replace(s, "SubscriptionsPauseSettlementActivationHeight = 0", "SubscriptionsPauseSettlementActivationHeight = 1800000", 1)
			},
			"SubscriptionsPauseSettlementActivationHeight",
		},
		"subscription charge history read through from the old key": {
			func(s string) string {
				return strings.Replace(s, "SubscriptionsChargeHistoryMigrationActivationHeight = 0", "SubscriptionsChargeHistoryMigrationActivationHeight = 1800000", 1)
			},
			"SubscriptionsChargeHistoryMigrationActivationHeight",
		},
		"sponsored NHB transfer fee credited to the collector": {
			func(s string) string {
				return strings.Replace(s, "SponsoredTransferFeeCreditActivationHeight = 2674600", "SponsoredTransferFeeCreditActivationHeight = 1800000", 1)
			},
			"SponsoredTransferFeeCreditActivationHeight",
		},
		"voucher mint budget read only from signed fields": {
			func(s string) string {
				return strings.Replace(s, "SwapVoucherSignedBudgetActivationHeight = 2674600", "SwapVoucherSignedBudgetActivationHeight = 1800000", 1)
			},
			"SwapVoucherSignedBudgetActivationHeight",
		},
		"buyback epoch with no computable price refunds its asks": {
			func(s string) string {
				return strings.Replace(s, "BuybackUnpriceableEpochRefundActivationHeight = 2674600", "BuybackUnpriceableEpochRefundActivationHeight = 1800000", 1)
			},
			"BuybackUnpriceableEpochRefundActivationHeight",
		},
		"uniform transfer fee for fee-domain transfers": {
			func(s string) string {
				return strings.Replace(s, "UniformTransferFeeActivationHeight = 2770000", "UniformTransferFeeActivationHeight = 1800000", 1)
			},
			"UniformTransferFeeActivationHeight",
		},
		"transfer fee schedule under governance": {
			func(s string) string {
				return strings.Replace(s, "TransferFeeGovernanceActivationHeight = 2770000", "TransferFeeGovernanceActivationHeight = 1800000", 1)
			},
			"TransferFeeGovernanceActivationHeight",
		},
		"transfer fee schedule delay": {
			func(s string) string {
				return strings.Replace(s, "TransferFeeGovernanceDelayBlocks = 41760", "TransferFeeGovernanceDelayBlocks = 5000", 1)
			},
			"TransferFeeGovernanceDelayBlocks",
		},
		"buyback price guard switched on": {
			func(s string) string {
				return strings.Replace(s, "BuybackPriceGuardActivationHeight = 2770000", "BuybackPriceGuardActivationHeight = 1800000", 1)
			},
			"BuybackPriceGuardActivationHeight",
		},
		"epoch settlement failsafe switched on": {
			func(s string) string {
				return strings.Replace(s, "EpochSettlementFailsafeActivationHeight = 2770000", "EpochSettlementFailsafeActivationHeight = 1800000", 1)
			},
			"EpochSettlementFailsafeActivationHeight",
		},
		"transfer fee strict allowance": {
			func(s string) string {
				return strings.Replace(s, "TransferFeeStrictAllowanceActivationHeight = 2770000", "TransferFeeStrictAllowanceActivationHeight = 1800000", 1)
			},
			"TransferFeeStrictAllowanceActivationHeight",
		},
		"sale reference switched on": {
			func(s string) string {
				return strings.Replace(s, "SaleReferenceActivationHeight = 0", "SaleReferenceActivationHeight = 1800000", 1)
			},
			"SaleReferenceActivationHeight",
		},
		"sale reference maximum age": {
			func(s string) string {
				return strings.Replace(s, "SaleReferenceMaxAgeSeconds = 0", "SaleReferenceMaxAgeSeconds = 3600", 1)
			},
			"SaleReferenceMaxAgeSeconds",
		},
		"sale reference step bound": {
			func(s string) string {
				return strings.Replace(s, "SaleReferenceMaxStepBps = 0", "SaleReferenceMaxStepBps = 500", 1)
			},
			"SaleReferenceMaxStepBps",
		},
		"duplicate parameter keys refused from a time": {
			func(s string) string {
				return strings.Replace(s, "RejectAmbiguousParamKeysFromUnix = 1792936800", "RejectAmbiguousParamKeysFromUnix = 1900000000", 1)
			},
			"[governance] RejectAmbiguousParamKeysFromUnix",
		},
		"swap payout receipt rules switched on": {
			func(s string) string {
				return strings.Replace(s, "PayoutReceiptActivationHeight = 0", "PayoutReceiptActivationHeight = 1800000", 1)
			},
			"[swap] PayoutReceiptActivationHeight",
		},
		"bridge escrow switched on": {
			func(s string) string {
				return strings.Replace(s, "BridgeActivationHeight = 0", "BridgeActivationHeight = 1800000", 1)
			},
			"[bridge] BridgeActivationHeight",
		},
		"native pool switched on": {
			func(s string) string {
				return strings.Replace(s, "AmmActivationHeight = 0", "AmmActivationHeight = 1800000", 1)
			},
			"[amm] AmmActivationHeight",
		},
		"native pool swaps opened": {
			func(s string) string {
				return strings.Replace(s, "SwapStartHeight = 0", "SwapStartHeight = 1800000", 1)
			},
			"[amm] SwapStartHeight",
		},
		"potso treasury": {
			func(s string) string {
				return strings.Replace(s, "TreasuryAddress = \""+liveZNHB, "TreasuryAddress = \""+oldTreasury, 1)
			},
			"[potso.rewards] TreasuryAddress",
		},
		"subscriptions treasury": {
			func(s string) string {
				return strings.Replace(s, "Treasury = \""+liveAdmin, "Treasury = \""+oldAdmin, 1)
			},
			"[subscriptions] Treasury",
		},
		"default fee owner": {
			func(s string) string {
				return strings.Replace(s, "OwnerWallet = \""+liveAdmin, "OwnerWallet = \""+oldAdmin, 1)
			},
			"[global.Fees] OwnerWallet",
		},
		"znhb fee owner": {
			func(s string) string {
				return strings.Replace(s, "OwnerWallet = \""+liveZNHB, "OwnerWallet = \""+oldTreasury, 1)
			},
			"ZNHB OwnerWallet",
		},
		"nhb asset fee owner": {
			func(s string) string {
				i := strings.Index(s, "Asset = \"NHB\"")
				return s[:i] + strings.Replace(s[i:], "OwnerWallet = \""+liveAdmin, "OwnerWallet = \""+oldAdmin, 1)
			},
			"NHB OwnerWallet",
		},
		"not an address": {
			func(s string) string {
				return strings.Replace(s, "TreasuryAddress = \""+liveZNHB+"\"", "TreasuryAddress = \"\"", 1)
			},
			"[potso.rewards] TreasuryAddress",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bad, err := checkLiveConfig(mutatedConfig(t, tc.edit), repoGenesis)
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), tc.want) {
				t.Fatalf("expected a complaint about %q, got %v", tc.want, bad)
			}
		})
	}
}

// Everything a node operator changes (paths, ports, keys, peers, the network
// name) is not consensus-relevant and must not trip the check.
func TestNodeLocalEditsPassTheConfigCheck(t *testing.T) {
	path := mutatedConfig(t, func(s string) string {
		s = strings.Replace(s, `ListenAddress = "127.0.0.1:6001"`, `ListenAddress = "0.0.0.0:7777"`, 1)
		s = strings.Replace(s, `DataDir = "./nhb-data"`, `DataDir = "/var/lib/nhbchain/nhb-data"`, 1)
		s = strings.Replace(s, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_VALIDATOR_RAW_KEY"`, 1)
		s = strings.Replace(s, `NetworkName = "nhb-local"`, `NetworkName = "nhb-mainnet-validator"`, 1)
		s = strings.Replace(s, `ExternalAddress = ""`, `ExternalAddress = "203.0.113.7:6001"`, 1)
		s = strings.Replace(s, `Bootnodes = []`, `Bootnodes = ["198.51.100.1:6001"]`, 1)
		return s
	})
	bad, err := checkLiveConfig(path, repoGenesis)
	if err != nil || len(bad) > 0 {
		t.Fatalf("a config with only node-local edits was refused: %v %v", bad, err)
	}
}

func TestCheckConfigCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"check-config", "--config", repoConfig, "--genesis", repoGenesis}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "ok") {
		t.Fatalf("the shipped config: exit %d: %s %s", code, out.String(), errOut.String())
	}
	drifted := mutatedConfig(t, func(s string) string {
		return strings.Replace(s, "QuorumCertActivationHeight = 1380200", "QuorumCertActivationHeight = 12", 1)
	})
	out.Reset()
	errOut.Reset()
	if code := run([]string{"check-config", "--config", drifted, "--genesis", repoGenesis}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "QuorumCertActivationHeight") {
		t.Fatalf("a drifted config: exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing.toml"), "--genesis", repoGenesis}, &out, &errOut); code != 1 {
		t.Fatalf("a missing config: exit %d", code)
	}
	if code := run([]string{"check-config", "--config", repoConfig}, &out, &errOut); code != 2 {
		t.Fatalf("without --genesis: exit %d", code)
	}
}
