package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/cmd/internal/passphrase"
	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/consensus/service"
	"nhbchain/core"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
	"nhbchain/native/subscriptions"
	swap "nhbchain/native/swap"
	"nhbchain/network"
	"nhbchain/observability"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/storage"
)

const (
	validatorPassEnv    = "NHB_VALIDATOR_PASS"
	genesisPathEnv      = "NHB_GENESIS"
	allowAutogenesisEnv = "NHB_ALLOW_AUTOGENESIS"
	proposalTimeoutEnv  = "NHB_CONSENSUS_TIMEOUT_PROPOSAL"
	prevoteTimeoutEnv   = "NHB_CONSENSUS_TIMEOUT_PREVOTE"
	precommitTimeoutEnv = "NHB_CONSENSUS_TIMEOUT_PRECOMMIT"
	commitTimeoutEnv    = "NHB_CONSENSUS_TIMEOUT_COMMIT"
)

type durationFlag struct {
	value time.Duration
	set   bool
}

func (d *durationFlag) String() string {
	if d == nil {
		return ""
	}
	return d.value.String()
}

func (d *durationFlag) Set(raw string) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	d.value = parsed
	d.set = true
	return nil
}

func convertQuota(q config.Quota) nativecommon.Quota {
	return nativecommon.Quota{
		MaxRequestsPerMin: q.MaxRequestsPerMin,
		MaxNHBPerEpoch:    q.MaxNHBPerEpoch,
		EpochSeconds:      q.EpochSeconds,
	}
}

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file (overrides NHB_GENESIS and config GenesisFile)")
	allowAutogenesisFlag := flag.Bool("allow-autogenesis", false, "DEV ONLY: allow automatic genesis creation when no stored genesis exists")
	allowMigrateFlag := flag.Bool("allow-migrate", false, "Allow starting with a mismatched state schema (manual migrations only)")
	grpcAddress := flag.String("grpc", "127.0.0.1:9090", "Address for the consensus gRPC server")
	networkAddress := flag.String("p2p", "localhost:9091", "Address of the p2p daemon network service")
	allowInsecureFlag := flag.Bool("allow-insecure", false, "DEV ONLY: permit plaintext loopback connections to p2pd")
	var proposalTimeoutFlag durationFlag
	var prevoteTimeoutFlag durationFlag
	var precommitTimeoutFlag durationFlag
	var commitTimeoutFlag durationFlag
	flag.Var(&proposalTimeoutFlag, "consensus-timeout-proposal", "Time to wait for a proposal before prevoting (e.g. 2s)")
	flag.Var(&prevoteTimeoutFlag, "consensus-timeout-prevote", "Time to wait after prevote before moving to precommit (e.g. 2s)")
	flag.Var(&precommitTimeoutFlag, "consensus-timeout-precommit", "Time to wait after precommit before attempting commit (e.g. 2s)")
	flag.Var(&commitTimeoutFlag, "consensus-timeout-commit", "Total time to wait for commit before starting a new round (e.g. 4s)")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("consensusd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "consensusd",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to initialise telemetry: %v", err))
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	allowAutogenesisCLISet := flagWasProvided("allow-autogenesis")

	passSource := passphrase.NewSource(validatorPassEnv)

	cfg, err := config.Load(*configFile, config.WithKeystorePassphraseSource(passSource.Get))
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	if err := config.ValidateConfig(cfg.Global); err != nil {
		log.Fatal("invalid configuration", "err", err)
	}
	// The [governance] block is the one the proposal engine reads (the global block
	// above is a different one): warn about a window below its floor, and refuse
	// to start if the operator turned on EnforceWindowFloors.
	warnGovernanceWindows(slog.Default(), cfg.Governance)
	if err := config.EnforceGovernanceWindowFloors(cfg.Governance); err != nil {
		log.Fatal("invalid configuration", "err", err)
	}
	// The transfer fee schedule under governance: a delay below its floor or a rate above the
	// ceiling is a warning, and a refusal under the same EnforceWindowFloors setting.
	for _, problem := range config.FeeScheduleProblems(cfg) {
		slog.Warn("configuration problem", slog.String("section", "fees"), slog.String("problem", problem.Error()))
	}
	if err := config.EnforceFeeScheduleFloors(cfg); err != nil {
		log.Fatal("invalid configuration", "err", err)
	}

	consensusTimeouts, err := applyConsensusTimeoutOverrides(cfg.Consensus, proposalTimeoutFlag, prevoteTimeoutFlag, precommitTimeoutFlag, commitTimeoutFlag, os.LookupEnv)
	if err != nil {
		log.Fatal("invalid consensus timeout override", "err", err)
	}
	cfg.Consensus = consensusTimeouts
	if err := config.ValidateConsensus(cfg.Consensus); err != nil {
		log.Fatal("invalid consensus timeouts", "err", err)
	}
	warnConsensusAdvisories(slog.Default(), cfg.Consensus)

	allowAutogenesis, err := resolveAllowAutogenesis(cfg.AllowAutogenesis, allowAutogenesisCLISet, *allowAutogenesisFlag, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve autogenesis setting: %v\n", err)
		os.Exit(1)
	}

	genesisPath, err := resolveGenesisPath(*genesisFlag, cfg.GenesisFile, allowAutogenesis, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve genesis path: %v\n", err)
		os.Exit(1)
	}

	db, err := storage.NewLevelDB(cfg.DataDir)
	if err != nil {
		panic(fmt.Sprintf("Failed to open database: %v", err))
	}
	defer db.Close()

	// The hash of every setting two validators must hold alike, logged and left in
	// the data directory (PL-R5T-16, PL-R5T-30). It never stops the node.
	if _, digestErr := cfg.PublishConsensusDigest(slog.Default(), os.Getenv, cfg.DataDir); digestErr != nil {
		slog.Warn("Could not compute the consensus settings digest", slog.Any("error", digestErr))
	}

	privKey, err := loadValidatorKey(cfg, passSource.Get)
	if err != nil {
		panic(fmt.Sprintf("Failed to load validator key: %v", err))
	}

	node, err := core.NewNode(db, privKey, genesisPath, allowAutogenesis, *allowMigrateFlag)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

	if err := node.SetGlobalConfig(cfg.Global); err != nil {
		log.Fatal("invalid global configuration", "err", err)
	}
	// PL-EXT-73: without an admin wallet in the genesis and without TransferFeeCollector, every node
	// credits the transfer fee to its OWN validator address and the validators diverge at the first
	// fee. Refuse to start; a development node on an auto-created genesis only gets a warning.
	if err := node.CheckTransferFeeCollector(cfg.Global.Fees); err != nil {
		if allowAutogenesis {
			slog.Warn("transfer fee collector is not configured", slog.Any("error", err))
		} else {
			log.Fatal("refusing to start", "err", err)
		}
	}
	node.SetMempoolUnlimitedOptIn(cfg.Mempool.AllowUnlimited)
	// NHB-AUDIT-C6: this binary's untrusted P2P block-sync path never
	// enabled the NHB-TRIAGE-C1 quorum-certificate check at all, unlike
	// cmd/nhb/main.go (see its matching QuorumCertActivationHeight guard
	// and config.Config.QuorumCertActivationHeight's doc comment) --
	// consensusd permanently accepted any structurally-valid synced block
	// with zero validator-quorum verification, at every height, forever.
	// Left unset (0) here, exactly as before, still means the check stays
	// disabled -- enabling it remains a deliberate, coordinated,
	// every-validator-at-once decision made in config.toml, never
	// inferred.
	if cfg.QuorumCertActivationHeight > 0 {
		node.SetQuorumCertActivationHeight(cfg.QuorumCertActivationHeight)
	} else {
		slog.Warn("QuorumCertActivationHeight is not set: this node does not check quorum certificates on blocks it receives through peer sync, so a peer could make it accept a block the validators never agreed on; set it to the value the network you follow uses (docs/validators/onboarding.md)")
	}
	if cfg.RewardsByStakeFromEpoch > 0 {
		node.SetRewardsByStakeFromEpoch(cfg.RewardsByStakeFromEpoch)
	}
	// Same setting and rule as in cmd/nhb/main.go: 0 (unset) keeps the
	// earlier POS expiry sweep, and a value must be identical on every
	// validator.
	if cfg.POSSweepSkipActivationHeight > 0 {
		node.SetPOSSweepSkipActivationHeight(cfg.POSSweepSkipActivationHeight)
		slog.Info("POS expiry sweep skips holds the payer cannot cover from block height",
			slog.Uint64("height", cfg.POSSweepSkipActivationHeight))
	}
	// Same setting as cmd/nhb/main.go: zero (the default) leaves the POS hold
	// unstake guard off; see config.Config.POSHoldUnstakeGuardActivationHeight.
	node.SetPOSHoldUnstakeGuardActivationHeight(cfg.POSHoldUnstakeGuardActivationHeight)
	// See cmd/nhb/main.go's matching GovCreditPreservationActivationHeight
	// guard and config.Config.GovCreditPreservationActivationHeight's doc
	// comment. Left unset (0) here means PL-R1-GOVREFUND's fix stays
	// disabled, exactly like every prior release.
	if cfg.GovCreditPreservationActivationHeight > 0 {
		node.SetGovCreditPreservationActivationHeight(cfg.GovCreditPreservationActivationHeight)
	}
	// Same setting and rule as in cmd/nhb/main.go: 0 (unset) keeps the NHB
	// fee-route credit exactly as every prior release.
	if cfg.FeeRouteAliasActivationHeight > 0 {
		node.SetFeeRouteAliasActivationHeight(cfg.FeeRouteAliasActivationHeight)
	}
	// Same setting as cmd/nhb/main.go: zero (the default) keeps the transaction
	// hash as the fixed-term loan and deposit ID.
	node.SetLendingFixedTermIDActivationHeight(cfg.LendingFixedTermIDActivationHeight)
	// Same setting as cmd/nhb/main.go: zero (the default) leaves the NHB supply
	// counter as it is.
	node.SetNHBSupplyGenesisCorrectionActivationHeight(cfg.NHBSupplyGenesisCorrectionActivationHeight)
	// Both default to off (zero); see config.Config.TransferMonthlyFreeTierActivationHeight
	// and config.Config.LoyaltyBudgetFloorActivationHeight. Chain rules, so the
	// values are set identically on every validator in config.toml.
	node.SetTransferMonthlyFreeTier(cfg.TransferMonthlyFreeTierActivationHeight, cfg.TransferMonthlyFreeTierTxPerMonth)
	loyaltyFloor, floorErr := config.ParseAmount(cfg.LoyaltyBudgetFloorZNHBWei)
	if floorErr != nil {
		log.Fatalf("Invalid LoyaltyBudgetFloorZNHBWei: %v", floorErr)
	}
	node.SetLoyaltyBudgetFloor(cfg.LoyaltyBudgetFloorActivationHeight, loyaltyFloor)
	// Off (zero) by default; see config.Config.PotsoGovernanceWeightActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetPotsoGovernanceWeights(cfg.PotsoGovernanceWeightActivationHeight)
	// Off (zero) by default; see config.Config.ValidatorFlagNonceActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetValidatorFlagNonceActivationHeight(cfg.ValidatorFlagNonceActivationHeight)
	// Off (zero) by default; see config.Config.MintRunningChainIDActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetMintRunningChainIDActivationHeight(cfg.MintRunningChainIDActivationHeight)
	// Off (zero) by default; see config.Config.MintPhaseEChainIDRetireHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetMintPhaseEChainIDRetireHeight(cfg.MintPhaseEChainIDRetireHeight)
	// Off (zero) by default; see config.Config.TxNetworkChainIDActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetTxNetworkChainIDActivationHeight(cfg.TxNetworkChainIDActivationHeight)
	// Off (zero) by default; see config.Config.VoteChainBindingActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetVoteChainBindingActivationHeight(cfg.VoteChainBindingActivationHeight)
	// The validator-set, slashing and staking chain rules; every one is off (zero) by
	// default and set identically on every validator (core.ValidatorRules).
	node.SetValidatorRules(core.ValidatorRulesFromConfig(cfg))
	// Off (zero) by default; see config.Config.LendingFeeWithdrawActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingFeeWithdrawActivationHeight(cfg.LendingFeeWithdrawActivationHeight)
	// Off (zero) by default; see config.Config.LendingRiskActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingRiskActivationHeight(cfg.LendingRiskActivationHeight)
	// Off (zero) by default; see config.Config.LendingRefPriceMaxAgeSeconds.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingRefPriceMaxAgeSeconds(cfg.LendingRefPriceMaxAgeSeconds)
	// Off (zero) by default; see config.Config.LendingRefPriceMaxAheadSeconds.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingRefPriceMaxAheadSeconds(cfg.LendingRefPriceMaxAheadSeconds)
	// Off (zero) by default; see config.Config.LendingRefPriceGuardsActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingRefPriceGuardsActivationHeight(cfg.LendingRefPriceGuardsActivationHeight)
	// Off (zero) by default; see config.Config.LendingPoolIDMaxLength.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingPoolIDMaxLength(cfg.LendingPoolIDMaxLength)
	// Off (zero) by default; see config.Config.LendingMaxPools.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingMaxPools(cfg.LendingMaxPools)
	// Off (zero) by default; see config.Config.LendingPoolLimitsActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingPoolLimitsActivationHeight(cfg.LendingPoolLimitsActivationHeight)
	// Off (zero) by default; see config.Config.LendingAccountingFixesActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLendingAccountingFixesActivationHeight(cfg.LendingAccountingFixesActivationHeight)
	// Off (zero) by default; see config.Config.PaymasterSponsorBindingActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetPaymasterSponsorBinding(cfg.PaymasterSponsorBindingActivationHeight)
	// Off (zero) by default; see config.Config.LoyaltyPaymasterConsentActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetLoyaltyPaymasterConsent(cfg.LoyaltyPaymasterConsentActivationHeight)
	// Off (zero) by default; see config.Config.LoyaltyCounterpartyCapActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	counterpartyCap, counterpartyCapErr := config.ParseAmount(cfg.LoyaltyCounterpartyCapZNHBWei)
	if counterpartyCapErr != nil {
		log.Fatalf("Invalid LoyaltyCounterpartyCapZNHBWei: %v", counterpartyCapErr)
	}
	node.SetLoyaltyCounterpartyCap(cfg.LoyaltyCounterpartyCapActivationHeight, counterpartyCap)
	// Off (zero) by default; see config.Config.BuybackMinAskActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	buybackMinAsk, buybackMinAskErr := config.ParseAmount(cfg.BuybackMinAskZNHBWei)
	if buybackMinAskErr != nil {
		log.Fatalf("Invalid BuybackMinAskZNHBWei: %v", buybackMinAskErr)
	}
	node.SetBuybackMinAsk(cfg.BuybackMinAskActivationHeight, buybackMinAsk)
	// Off (zero) by default; see config.Config.SubscriptionsPauseSettlementActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetSubscriptionsPauseSettlement(cfg.SubscriptionsPauseSettlementActivationHeight)
	// Off (zero) by default; see config.Config.SubscriptionsChargeHistoryMigrationActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetSubscriptionsChargeHistoryMigration(cfg.SubscriptionsChargeHistoryMigrationActivationHeight)
	// Off (zero) by default; see config.Config.BuybackUnpriceableEpochRefundActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetBuybackUnpriceableEpochRefund(cfg.BuybackUnpriceableEpochRefundActivationHeight)
	// The ZNHB bridge escrow and the native pool: off (activation height zero) by default; chain rules, so the
	// values are set identically on every validator. A block that is activated with a bad value stops the node.
	bridgeSettings, ammSettings, bridgeAmmErr := core.BridgeAmmSettingsFromConfig(cfg)
	if bridgeAmmErr != nil {
		slog.Error("Refusing to start: invalid [bridge] or [amm] settings", slog.Any("error", bridgeAmmErr))
		os.Exit(1)
	}
	node.SetBridgeAndAmm(bridgeSettings, ammSettings)
	// Off (zero) by default; see config.Config.SwapVoucherSignedBudgetActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetSwapVoucherSignedBudget(cfg.SwapVoucherSignedBudgetActivationHeight)
	// Off (zero) by default; see config.Config.SponsoredTransferFeeCreditActivationHeight.
	// A chain rule, so the value is set identically on every validator.
	node.SetSponsoredTransferFeeCredit(cfg.SponsoredTransferFeeCreditActivationHeight)
	// All off (zero) by default; see config.Config.UniformTransferFeeActivationHeight and
	// config.Config.TransferFeeGovernanceActivationHeight. Chain rules, so the values are set
	// identically on every validator.
	node.SetUniformTransferFee(cfg.UniformTransferFeeActivationHeight)
	node.SetTransferFeeGovernance(cfg.TransferFeeGovernanceActivationHeight, cfg.TransferFeeGovernanceDelayBlocks)
	// Off (zero) by default; see config.Config.BuybackPriceGuardActivationHeight and
	// config.Config.EpochSettlementFailsafeActivationHeight. Chain rules, so the
	// values are set identically on every validator.
	node.SetBuybackPriceGuard(cfg.BuybackPriceGuardActivationHeight)
	node.SetEpochSettlementFailsafe(cfg.EpochSettlementFailsafeActivationHeight)
	// Off (zero) by default; see config.Config.TransferFeeStrictAllowanceActivationHeight. A chain rule, so the value is
	// set identically on every validator.
	node.SetTransferFeeStrictAllowance(cfg.TransferFeeStrictAllowanceActivationHeight)
	node.SetMempoolLimit(cfg.Mempool.MaxTransactions)

	paymasterLimits, err := cfg.Global.PaymasterLimits()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse paymaster limits: %v", err))
	}
	node.SetPaymasterLimits(core.PaymasterLimits{
		MerchantDailyCapWei: paymasterLimits.MerchantDailyCapWei,
		DeviceDailyTxCap:    paymasterLimits.DeviceDailyTxCap,
		GlobalDailyCapWei:   paymasterLimits.GlobalDailyCapWei,
	})
	autoTopUpCfg, err := cfg.Global.PaymasterAutoTopUpConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse paymaster auto top-up policy: %v", err))
	}
	autoPolicy := core.PaymasterAutoTopUpPolicy{
		Enabled:        autoTopUpCfg.Enabled,
		Token:          autoTopUpCfg.Token,
		Cooldown:       autoTopUpCfg.Cooldown,
		FundingAccount: autoTopUpCfg.FundingAccount,
		Minter:         autoTopUpCfg.Minter,
		Approver:       autoTopUpCfg.Approver,
		ApproverRole:   autoTopUpCfg.ApproverRole,
		MinterRole:     autoTopUpCfg.MinterRole,
		SponsorRole:    autoTopUpCfg.SponsorRole,
	}
	if autoTopUpCfg.AggregateDailyCapWei != nil {
		autoPolicy.AggregateDailyCapWei = new(big.Int).Set(autoTopUpCfg.AggregateDailyCapWei)
	}
	if autoTopUpCfg.MinBalanceWei != nil {
		autoPolicy.MinBalanceWei = new(big.Int).Set(autoTopUpCfg.MinBalanceWei)
	}
	if autoTopUpCfg.TopUpAmountWei != nil {
		autoPolicy.TopUpAmountWei = new(big.Int).Set(autoTopUpCfg.TopUpAmountWei)
	}
	if autoTopUpCfg.DailyCapWei != nil {
		autoPolicy.DailyCapWei = new(big.Int).Set(autoTopUpCfg.DailyCapWei)
	}
	node.SetPaymasterAutoTopUpPolicy(autoPolicy)

	govPolicy, err := cfg.Governance.Policy()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse governance policy: %v", err))
	}
	node.SetGovernancePolicy(govPolicy)

	potsoCfg, err := cfg.PotsoRewardConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse POTSO rewards config: %v", err))
	}
	if err := node.SetPotsoRewardConfig(potsoCfg); err != nil {
		panic(fmt.Sprintf("Failed to apply POTSO rewards config: %v", err))
	}
	weightCfg, err := cfg.PotsoWeightConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse POTSO weight config: %v", err))
	}
	if err := node.SetPotsoWeightConfig(weightCfg); err != nil {
		panic(fmt.Sprintf("Failed to apply POTSO weight config: %v", err))
	}

	// Both entry points build the engine's limits from the one mapping, so they cannot
	// disagree; the limits that the engine applies only from
	// LendingRiskActivationHeight are included here and cleared by core before it
	// (ledger PL-DC-19).
	lendingRisk, riskErr := cfg.Lending.RiskParameters()
	if riskErr != nil {
		panic(fmt.Sprintf("Invalid [lending] risk limits: %v", riskErr))
	}
	node.SetLendingRiskParameters(lendingRisk)

	node.SetLendingAccrualConfig(cfg.Lending.ReserveFactorBps, cfg.Lending.ProtocolFeeBps, lending.DefaultInterestModel)

	devCollectorStr := strings.TrimSpace(cfg.Lending.DeveloperFeeCollector)
	var devCollector crypto.Address
	if devCollectorStr != "" {
		decoded, err := crypto.DecodeAddress(devCollectorStr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending developer fee collector: %v", err))
		}
		devCollector = decoded
	} else if cfg.Lending.DeveloperFeeBps > 0 {
		panic("Lending DeveloperFeeCollector must be configured when DeveloperFeeBps is non-zero")
	}
	node.SetLendingDeveloperFee(cfg.Lending.DeveloperFeeBps, devCollector)

	routingCfg := cfg.Lending.CollateralRouting
	var developerCollateral crypto.Address
	if routingCfg.DeveloperBps > 0 {
		addr := strings.TrimSpace(routingCfg.DeveloperAddress)
		if addr == "" {
			panic("Lending collateral routing requires DeveloperAddress when DeveloperBps is non-zero")
		}
		decoded, err := crypto.DecodeAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending developer collateral address: %v", err))
		}
		developerCollateral = decoded
	}
	var protocolCollateral crypto.Address
	if routingCfg.ProtocolBps > 0 {
		addr := strings.TrimSpace(routingCfg.ProtocolAddress)
		if addr == "" {
			panic("Lending collateral routing requires ProtocolAddress when ProtocolBps is non-zero")
		}
		decoded, err := crypto.DecodeAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending protocol collateral address: %v", err))
		}
		protocolCollateral = decoded
	}
	totalRoutingBps := routingCfg.LiquidatorBps + routingCfg.DeveloperBps + routingCfg.ProtocolBps
	if totalRoutingBps > 10_000 {
		panic("Lending collateral routing shares must not exceed 10000 basis points")
	}
	node.SetLendingCollateralRouting(lending.CollateralRouting{
		LiquidatorBps:   routingCfg.LiquidatorBps,
		DeveloperBps:    routingCfg.DeveloperBps,
		DeveloperTarget: developerCollateral,
		ProtocolBps:     routingCfg.ProtocolBps,
		ProtocolTarget:  protocolCollateral,
	})

	subscriptionsTreasuryStr := strings.TrimSpace(cfg.Subscriptions.Treasury)
	var subscriptionsTreasury [20]byte
	if subscriptionsTreasuryStr != "" {
		decoded, err := crypto.DecodeAddress(subscriptionsTreasuryStr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode subscriptions treasury address: %v", err))
		}
		copy(subscriptionsTreasury[:], decoded.Bytes())
	} else if cfg.Subscriptions.ManagementFeeBps > 0 {
		panic("Subscriptions Treasury must be configured when ManagementFeeBps is non-zero")
	}
	if err := node.SetSubscriptionsConfig(subscriptions.Config{
		ManagementFeeBps:     cfg.Subscriptions.ManagementFeeBps,
		ManagementFeeCapBps:  cfg.Subscriptions.ManagementFeeCapBps,
		Treasury:             subscriptionsTreasury,
		MaxRetries:           cfg.Subscriptions.MaxRetries,
		RetryIntervalSeconds: cfg.Subscriptions.RetryIntervalSeconds,
	}); err != nil {
		panic(fmt.Sprintf("Failed to configure subscriptions engine: %v", err))
	}

	swapCfg := cfg.SwapSettings()
	node.SetSwapConfig(swapCfg)
	manualOracle := swap.NewManualOracle()
	aggregator := swap.NewOracleAggregator(swapCfg.OraclePriority, swapCfg.MaxQuoteAge())
	aggregator.SetTWAPWindow(swapCfg.TwapWindow())
	aggregator.SetTWAPSampleCap(swapCfg.TwapSampleCap)
	aggregator.SetPriority(swapCfg.OraclePriority)
	aggregator.Register("manual", manualOracle)
	npAPIKey := strings.TrimSpace(os.Getenv("NHB_NOWPAYMENTS_API_KEY"))
	aggregator.Register("nowpayments", swap.NewNowPaymentsOracle(nil, "", npAPIKey))
	// Neither NHB nor ZNHB has a public CoinGecko listing -- both are
	// private-chain assets, not tradeable anywhere CoinGecko can see. An
	// empty id map means CoinGeckoOracle.assetID() fails fast with
	// "unmapped asset" for both instead of guessing nonexistent ids ("nhb",
	// "znhb") and making a doomed network call. See cmd/nhb/main.go's
	// matching oracle setup for the fuller rationale.
	aggregator.Register("coingecko", swap.NewCoinGeckoOracle(nil, "", map[string]string{}))
	node.SetSwapOracle(aggregator)
	node.SetSwapManualOracle(manualOracle)
	// The swap sanctions checker, as cmd/nhb sets it: without it this node would accept a swap
	// the other binary refuses (ledger PL-EXT-47).
	sanctionsParams, err := swapCfg.Sanctions.Parameters()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse swap sanctions config: %v", err))
	}
	node.SetSwapSanctionsChecker(sanctionsParams.Checker())

	node.SetModulePauses(cfg.Global.Pauses)
	if !cfg.Global.Pauses.Staking {
		if onChainPaused, err := node.StakingPauseOnChain(); err != nil {
			log.Printf("warning: failed to check on-chain staking pause state: %v", err)
		} else if onChainPaused {
			log.Printf("warning: local config expects staking unpaused, but the network's last governance-set value is paused -- this validator enforces the on-chain value, not local config; resolve the mismatch via a real governance action if it is unintentional")
		}
	}
	node.SetModuleQuotas(map[string]nativecommon.Quota{
		"lending":       convertQuota(cfg.Global.Quotas.Lending),
		"swap":          convertQuota(cfg.Global.Quotas.Swap),
		"escrow":        convertQuota(cfg.Global.Quotas.Escrow),
		"trade":         convertQuota(cfg.Global.Quotas.Trade),
		"loyalty":       convertQuota(cfg.Global.Quotas.Loyalty),
		"potso":         convertQuota(cfg.Global.Quotas.POTSO),
		"subscriptions": convertQuota(cfg.Global.Quotas.Subscriptions),
		"market":        convertQuota(cfg.Global.Quotas.Market),
	})

	// The staking rate and its validation, as cmd/nhb does them: without SyncStakingParams the
	// processor keeps its default rate of 0 and this node accrues no stake rewards while the other
	// binary accrues at the configured rate, so the same stake transaction gives different state
	// roots (ledger PL-EXT-47, PL-R5T-27).
	if err := node.SyncStakingParams(); err != nil {
		panic(fmt.Sprintf("Failed to apply staking params: %v", err))
	}
	if err := node.ValidateStakingConfig(); err != nil {
		panic(fmt.Sprintf("Failed to validate staking config: %v", err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	broadcaster := newResilientBroadcaster(ctx)
	// The node's own gossip goes out through the same stream the engine's messages do, as cmd/nhb hands
	// the node its peer-to-peer server. Without it a transaction submitted to this validator is never
	// forwarded: it waits in this node's mempool until this validator itself proposes, and is lost if
	// the node restarts (ledger PL-EXT-47).
	node.SetNetworkBroadcaster(broadcaster)
	baseDir := filepath.Dir(*configFile)
	allowInsecureNetwork, networkDialOpts, err := buildNetworkDialOptions(cfg, baseDir, *networkAddress, *allowInsecureFlag)
	if err != nil {
		panic(fmt.Sprintf("failed to initialise network client security: %v", err))
	}

	go maintainNetworkStream(ctx, *networkAddress, broadcaster, node, allowInsecureNetwork, networkDialOpts, cfg.NetworkSecurity.StreamQueueSize)

	bftEngine := bft.NewEngine(node, privKey, broadcaster, bft.WithTimeouts(bft.TimeoutConfig{
		Proposal:  cfg.Consensus.ProposalTimeout,
		Prevote:   cfg.Consensus.PrevoteTimeout,
		Precommit: cfg.Consensus.PrecommitTimeout,
		Commit:    cfg.Consensus.CommitTimeout,
	}), bft.WithMinBlockInterval(cfg.Consensus.MinBlockInterval),
		// Votes at or above the configured height are signed, and checked, over the genesis hash
		// (PL-R5T-31); 0, the shipped value, keeps the legacy digest. A chain rule: the same value
		// on every validator.
		bft.WithVoteBinding(node.GenesisHash(), cfg.VoteChainBindingActivationHeight),
		bft.WithLockSnapshotPath(filepath.Join(cfg.DataDir, "polc_lock.json")),
		// What this validator has voted, so a restart cannot make it sign a
		// different vote for a round it already voted in (see cmd/nhb/main.go).
		bft.WithSignStatePath(filepath.Join(cfg.DataDir, "bft_sign_state.json")),
		// How many weak (no-quorum-otherwise) round jumps this validator has
		// already granted at its current height, so a restart of THIS validator
		// cannot hand a fresh maxFutureRounds allowance to a validator it was
		// already following (see cmd/nhb/main.go and round_sync.go's
		// admitWeakRoundJumpLocked).
		bft.WithWeakJumpSnapshotPath(filepath.Join(cfg.DataDir, "bft_weak_jump.json")))
	slog.Info("consensus: minimum block interval", slog.Duration("interval", bftEngine.MinBlockInterval()))
	node.SetBftEngine(bftEngine)
	// A block committed from a peer's blocks tells the engine, so a round still aimed at an
	// already-final height does not run out its timers (see cmd/nhb/main.go; ledger PL-EXT-47).
	node.SetExternalCommitNotifier(bftEngine.NotifyExternalCommit)
	// Bound one block build to half the proposal timeout (see cmd/nhb/main.go)
	// and start the optional Prometheus listener (NHB_METRICS_ADDR); a failure
	// to start it is logged and never stops the validator.
	node.SetProposalBuildBudget(cfg.Consensus.ProposalTimeout / 2)
	// Keep the blocks this node proposes small enough for one network frame, whatever frame limit is
	// configured, as cmd/nhb does (ledger PL-EXT-47).
	node.SetProposalFrameLimit(cfg.MaxMsgBytes)
	observability.StartMetricsServerFromEnv(slog.Default())

	grpcListener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		panic(fmt.Sprintf("Failed to listen on %s: %v", *grpcAddress, err))
	}
	serverCreds, serverAuth, err := buildConsensusServerSecurity(cfg, baseDir, *allowInsecureFlag, grpcListener.Addr())
	if err != nil {
		panic(fmt.Sprintf("failed to configure consensus server security: %v", err))
	}

	grpcServer := grpc.NewServer(
		grpc.Creds(serverCreds),
		grpc.ChainUnaryInterceptor(
			service.UnaryAuthInterceptor(serverAuth),
			otelgrpc.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			service.StreamAuthInterceptor(serverAuth),
			otelgrpc.StreamServerInterceptor(),
		),
	)
	srv := service.NewServer(node, service.WithAuthorizer(serverAuth))
	consensusv1.RegisterConsensusServiceServer(grpcServer, srv)
	consensusv1.RegisterQueryServiceServer(grpcServer, srv)

	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			panic(fmt.Sprintf("gRPC server failed: %v", err))
		}
	}()

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	go node.StartConsensus()

	fmt.Println("--- Consensus node initialised and running ---")
	<-ctx.Done()
	fmt.Println("--- Consensus node shutting down ---")
}

// warnConsensusAdvisories logs a WARN for each consensus setting the node starts
// on but does not run as written, and returns how many it found. Today that is a
// minimum block interval above half the commit timeout, which the engine lowers.
func warnConsensusAdvisories(logger *slog.Logger, c config.Consensus) int {
	if logger == nil {
		logger = slog.Default()
	}
	advisories := config.ConsensusAdvisories(c)
	for _, advisory := range advisories {
		logger.Warn("consensus: configuration advisory",
			slog.String("advisory", advisory.Error()),
			slog.Duration("commitTimeout", c.CommitTimeout),
			slog.Duration("minBlockInterval", c.MinBlockInterval))
	}
	return len(advisories)
}

func applyConsensusTimeoutOverrides(base config.Consensus, proposal, prevote, precommit, commit durationFlag, lookup envLookupFunc) (config.Consensus, error) {
	result := base
	var err error
	result.ProposalTimeout, err = resolveDurationOverride(base.ProposalTimeout, proposal, "--consensus-timeout-proposal", proposalTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.PrevoteTimeout, err = resolveDurationOverride(base.PrevoteTimeout, prevote, "--consensus-timeout-prevote", prevoteTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.PrecommitTimeout, err = resolveDurationOverride(base.PrecommitTimeout, precommit, "--consensus-timeout-precommit", precommitTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.CommitTimeout, err = resolveDurationOverride(base.CommitTimeout, commit, "--consensus-timeout-commit", commitTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	return result, nil
}

func resolveDurationOverride(current time.Duration, flag durationFlag, flagName, envKey string, lookup envLookupFunc) (time.Duration, error) {
	if flag.set {
		if flag.value <= 0 {
			return 0, fmt.Errorf("%s must be positive", flagName)
		}
		return flag.value, nil
	}
	if lookup != nil {
		if raw, ok := lookup(envKey); ok {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" {
				parsed, err := time.ParseDuration(trimmed)
				if err != nil {
					return 0, fmt.Errorf("parse %s: %w", envKey, err)
				}
				if parsed <= 0 {
					return 0, fmt.Errorf("%s must be positive", envKey)
				}
				return parsed, nil
			}
		}
	}
	return current, nil
}

const (
	networkReconnectBaseDelay = 500 * time.Millisecond
	networkReconnectMaxDelay  = 30 * time.Second
)

func maintainNetworkStream(ctx context.Context, target string, broadcaster *resilientBroadcaster, node *core.Node, allowInsecure bool, dialOpts []grpc.DialOption, queueSize int) {
	if broadcaster == nil || node == nil {
		return
	}

	backoff := networkReconnectBaseDelay
	for {
		if ctx.Err() != nil {
			return
		}

		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		client, err := network.Dial(dialCtx, target, allowInsecure, dialOpts...)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect to p2pd at %s: %v\n", target, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > networkReconnectMaxDelay {
				backoff = networkReconnectMaxDelay
			}
			continue
		}

		client.SetSendQueueSize(queueSize)
		broadcaster.SetClient(client)
		backoff = networkReconnectBaseDelay

		// HandlePeerMessage answers a chain data request to the peer that
		// made it, when p2pd says which peer that was.
		streamErr := client.RunPeers(ctx, node.HandlePeerMessage, nil)
		broadcaster.SetClient(nil)
		client.Close()
		if streamErr != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Network stream terminated: %v\n", streamErr)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > networkReconnectMaxDelay {
			backoff = networkReconnectMaxDelay
		}
	}
}

type envLookupFunc func(string) (string, bool)

func buildNetworkDialOptions(cfg *config.Config, baseDir string, target string, allowInsecureFlag bool) (bool, []grpc.DialOption, error) {
	if cfg == nil {
		if !allowInsecureFlag {
			return false, nil, fmt.Errorf("plaintext network bridge requires --allow-insecure runtime flag")
		}
		if !isLoopbackTarget(target) {
			return false, nil, fmt.Errorf("plaintext network bridge requires loopback target; refusing %q", target)
		}
		return true, []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, nil
	}
	sec := cfg.NetworkSecurity

	secret, err := sec.ResolveSharedSecret(baseDir, os.LookupEnv)
	if err != nil {
		return false, nil, fmt.Errorf("resolve shared secret: %w", err)
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	useTLS := false

	if caPath := resolvePath(baseDir, sec.ServerCAFile); caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return false, nil, fmt.Errorf("read server CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return false, nil, fmt.Errorf("failed to parse server CA certificates from %s", caPath)
		}
		tlsConfig.RootCAs = pool
		useTLS = true
	}

	certPath := resolvePath(baseDir, sec.ClientTLSCertFile)
	keyPath := resolvePath(baseDir, sec.ClientTLSKeyFile)
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return false, nil, fmt.Errorf("ClientTLSCertFile and ClientTLSKeyFile must both be configured when using client certificates")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return false, nil, fmt.Errorf("load client TLS keypair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		useTLS = true
	}

	if serverName := strings.TrimSpace(sec.ServerName); serverName != "" {
		tlsConfig.ServerName = serverName
		useTLS = true
	}

	if !useTLS && strings.TrimSpace(sec.ServerTLSCertFile) != "" {
		useTLS = true
	}

	allowInsecure := false
	var opts []grpc.DialOption
	if useTLS {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else if sec.AllowInsecure {
		if !allowInsecureFlag {
			return false, nil, fmt.Errorf("plaintext network bridge requires --allow-insecure runtime flag")
		}
		if !isLoopbackTarget(target) {
			return false, nil, fmt.Errorf("plaintext network bridge requires loopback target; refusing %q", target)
		}
		allowInsecure = true
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		return false, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=false or provide certificates")
	}

	if secret != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(network.NewStaticTokenCredentials(sec.AuthorizationHeaderName(), secret)))
	}

	return allowInsecure, opts, nil
}

func buildConsensusServerSecurity(cfg *config.Config, baseDir string, allowInsecureFlag bool, listener net.Addr) (credentials.TransportCredentials, service.Authorizer, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("missing configuration for consensus security")
	}
	sec := cfg.NetworkSecurity

	secret, err := sec.ResolveSharedSecret(baseDir, os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve shared secret: %w", err)
	}

	var auths []network.Authenticator
	if secret != "" {
		auths = append(auths, network.NewTokenAuthenticator(sec.AuthorizationHeaderName(), secret))
	}

	certPath := resolvePath(baseDir, sec.ServerTLSCertFile)
	keyPath := resolvePath(baseDir, sec.ServerTLSKeyFile)

	var tlsConfig *tls.Config
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return nil, nil, fmt.Errorf("consensus security requires both ServerTLSCertFile and ServerTLSKeyFile when enabling TLS")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("load consensus TLS keypair: %w", err)
		}
		tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	if caPath := resolvePath(baseDir, sec.ClientCAFile); caPath != "" {
		if tlsConfig == nil {
			return nil, nil, fmt.Errorf("ClientCAFile requires ServerTLSCertFile and ServerTLSKeyFile to be configured")
		}
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("failed to parse client CA certificates from %s", caPath)
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		auths = append(auths, network.NewTLSAuthorizer(sec.AllowedClientCommonNames))
	}

	if len(auths) == 0 {
		return nil, nil, fmt.Errorf("consensus security requires a shared secret or client certificate authentication")
	}

	var creds credentials.TransportCredentials
	switch {
	case tlsConfig != nil:
		creds = credentials.NewTLS(tlsConfig)
	case sec.AllowInsecure:
		if !allowInsecureFlag {
			return nil, nil, fmt.Errorf("plaintext consensus server requires --allow-insecure runtime flag")
		}
		if listener == nil || !isLoopbackListener(listener) {
			return nil, nil, fmt.Errorf("plaintext consensus server is restricted to loopback listeners; refusing %v", listener)
		}
		creds = insecure.NewCredentials()
	default:
		return nil, nil, fmt.Errorf("consensus security requires TLS material; set AllowInsecure=false or provide certificates")
	}

	return creds, network.ChainAuthenticators(auths...), nil
}

func resolvePath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if baseDir != "" && !filepath.IsAbs(trimmed) {
		return filepath.Join(baseDir, trimmed)
	}
	return trimmed
}

func resolveGenesisPath(cliPath string, cfgPath string, allowAutogenesis bool, lookup envLookupFunc) (string, error) {
	trimmedCLI := strings.TrimSpace(cliPath)
	if trimmedCLI != "" {
		return trimmedCLI, nil
	}

	if lookup != nil {
		if value, ok := lookup(genesisPathEnv); ok {
			trimmedEnv := strings.TrimSpace(value)
			if trimmedEnv != "" {
				return trimmedEnv, nil
			}
		}
	}

	trimmedCfg := strings.TrimSpace(cfgPath)
	if trimmedCfg != "" {
		if _, err := os.Stat(trimmedCfg); os.IsNotExist(err) && !allowAutogenesis {
			fmt.Printf("Default genesis file %s not found, writing embedded mainnet genesis...\n", trimmedCfg)
			if err := os.MkdirAll(filepath.Dir(trimmedCfg), 0755); err != nil {
				return "", fmt.Errorf("failed to create genesis parent directory: %w", err)
			}
			if err := os.WriteFile(trimmedCfg, config.MainnetGenesis, 0644); err != nil {
				return "", fmt.Errorf("failed to write embedded genesis: %w", err)
			}
		}
		return trimmedCfg, nil
	}

	if allowAutogenesis {
		return "", nil
	}

	return "", fmt.Errorf("no genesis file provided; supply one via --genesis, %s, or config, or explicitly enable autogenesis (--allow-autogenesis / %s / config)", genesisPathEnv, allowAutogenesisEnv)
}

func resolveAllowAutogenesis(cfgValue bool, cliSet bool, cliValue bool, lookup envLookupFunc) (bool, error) {
	allow := cfgValue

	if lookup != nil {
		if value, ok := lookup(allowAutogenesisEnv); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return false, fmt.Errorf("invalid %s value %q: %w", allowAutogenesisEnv, trimmed, err)
				}
				allow = parsed
			}
		}
	}

	if cliSet {
		allow = cliValue
	}

	return allow, nil
}

func isLoopbackListener(addr net.Addr) bool {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip := tcpAddr.IP
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func isLoopbackTarget(target string) bool {
	host := strings.TrimSpace(target)
	if host == "" {
		return false
	}
	// Strip scheme if provided.
	if idx := strings.Index(host, "://"); idx != -1 {
		host = host[idx+3:]
	}
	// Separate host from port for IPv4/hostname style targets.
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end != -1 {
			host = host[1:end]
		}
	} else if colon := strings.LastIndex(host, ":"); colon != -1 {
		host = host[:colon]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

func flagWasProvided(name string) bool {
	provided := false
	flag.CommandLine.Visit(func(f *flag.Flag) {
		if f.Name == name {
			provided = true
		}
	})
	return provided
}

func loadValidatorKey(cfg *config.Config, resolvePassphrase func() (string, error)) (*crypto.PrivateKey, error) {
	if cfg.ValidatorKMSURI != "" || cfg.ValidatorKMSEnv != "" {
		return loadFromKMS(cfg)
	}

	if cfg.ValidatorKeystorePath == "" {
		return nil, fmt.Errorf("validator keystore path not configured")
	}

	if resolvePassphrase == nil {
		return nil, fmt.Errorf("validator keystore passphrase required; set %s or run interactively", validatorPassEnv)
	}

	passphrase, err := resolvePassphrase()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain validator keystore passphrase: %w", err)
	}
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("validator keystore passphrase cannot be empty")
	}

	key, err := crypto.LoadFromKeystore(cfg.ValidatorKeystorePath, passphrase)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt keystore %s: %w", cfg.ValidatorKeystorePath, err)
	}
	return key, nil
}

func loadFromKMS(cfg *config.Config) (*crypto.PrivateKey, error) {
	if envName := cfg.ValidatorKMSEnv; envName != "" {
		return keyFromEnv(envName)
	}

	if cfg.ValidatorKMSURI == "" {
		return nil, fmt.Errorf("validator KMS URI not configured")
	}

	parsed, err := url.Parse(cfg.ValidatorKMSURI)
	if err != nil {
		return nil, fmt.Errorf("invalid validator KMS URI: %w", err)
	}

	switch parsed.Scheme {
	case "env":
		return keyFromEnv(parsed.Opaque)
	default:
		return nil, fmt.Errorf("unsupported validator KMS scheme %q", parsed.Scheme)
	}
}

func keyFromEnv(name string) (*crypto.PrivateKey, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("validator KMS environment variable not provided")
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("validator KMS environment variable %s not set", name)
	}
	return parsePrivateKeyMaterial(value)
}

func parsePrivateKeyMaterial(material string) (*crypto.PrivateKey, error) {
	trimmed := strings.TrimSpace(material)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" {
		return nil, fmt.Errorf("empty private key material")
	}
	bytes, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex private key: %w", err)
	}
	return crypto.PrivateKeyFromBytes(bytes)
}
