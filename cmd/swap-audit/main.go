package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"nhbchain/config"
)

type auditReport struct {
	AllowedFiat []string `json:"allowedFiat"`
	Risk        struct {
		PerAddressDailyCapWei   string `json:"perAddressDailyCapWei"`
		PerAddressMonthlyCapWei string `json:"perAddressMonthlyCapWei"`
		PerTxMinWei             string `json:"perTxMinWei"`
		PerTxMaxWei             string `json:"perTxMaxWei"`
		VelocityWindowSeconds   uint64 `json:"velocityWindowSeconds"`
		VelocityMaxMints        uint64 `json:"velocityMaxMints"`
		SanctionsCheckEnabled   bool   `json:"sanctionsCheckEnabled"`
	} `json:"risk"`
	Providers []string `json:"providers"`
}

func main() {
	configPath := flag.String("config", "./config.toml", "Path to node configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	swapCfg := cfg.SwapSettings()
	params, err := swapCfg.Risk.Parameters()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse risk parameters: %v\n", err)
		os.Exit(1)
	}

	report := auditReport{AllowedFiat: swapCfg.AllowedFiat, Providers: swapCfg.Providers.AllowList()}
	report.Risk.PerAddressDailyCapWei = swapCfg.Risk.PerAddressDailyCapWei
	report.Risk.PerAddressMonthlyCapWei = swapCfg.Risk.PerAddressMonthlyCapWei
	report.Risk.PerTxMinWei = swapCfg.Risk.PerTxMinWei
	report.Risk.PerTxMaxWei = swapCfg.Risk.PerTxMaxWei
	report.Risk.VelocityWindowSeconds = params.VelocityWindowSeconds
	report.Risk.VelocityMaxMints = params.VelocityMaxMints
	report.Risk.SanctionsCheckEnabled = params.SanctionsCheckEnabled

	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode report: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}
