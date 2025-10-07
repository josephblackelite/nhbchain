//go:build posreadiness

package posreadiness

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"nhbchain/core/events"
	"nhbchain/crypto"
	"nhbchain/native/fees"
)

type grafanaDashboard struct {
	Panels []struct {
		Title      string `json:"title"`
		Type       string `json:"type"`
		Datasource *struct {
			UID string `json:"uid"`
		} `json:"datasource"`
		Targets []struct {
			RefID string `json:"refId"`
			Query string `json:"query"`
		} `json:"targets"`
	} `json:"panels"`
	Annotations struct {
		List []struct {
			Name string `json:"name"`
		} `json:"list"`
	} `json:"annotations"`
}

func TestFeeEventReconciliation(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	routeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate route key: %v", err)
	}

	startBalance := big.NewInt(5_000_000)
	if err := seedAccount(node, payerKey, new(big.Int).Set(startBalance)); err != nil {
		t.Fatalf("seed payer: %v", err)
	}
	if err := seedAccount(node, merchantKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	if err := seedAccount(node, routeKey, big.NewInt(0)); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	var routeWallet [20]byte
	copy(routeWallet[:], routeKey.PubKey().Address().Bytes())
	policy := fees.Policy{
		Version: 1,
		Domains: map[string]fees.DomainPolicy{
			fees.DomainPOS: {
				FreeTierAllowance: 0,
				MDRBps:            200,
				RouteWallet:       routeWallet,
			},
		},
	}
	node.SetFeePolicy(policy)

	merchantAddr := merchantKey.PubKey().Address().Bytes()
	transfer := big.NewInt(25_000)

	expectedRouteHex := hex.EncodeToString(routeWallet[:])
	eventTotal := big.NewInt(0)
	lastEventIdx := 0

	for i := 0; i < 5; i++ {
		tx := buildPOSTransfer(t, payerKey, merchantAddr, uint64(i), transfer)
		if _, err := chain.FinalizeTxs(tx); err != nil {
			t.Fatalf("finalize tx %d: %v", i, err)
		}
		emitted := node.Events()
		for _, evt := range emitted[lastEventIdx:] {
			if evt.Type != events.TypeFeeApplied {
				continue
			}
			if evt.Attributes == nil {
				continue
			}
			if evt.Attributes["routeWallet"] != expectedRouteHex {
				continue
			}
			feeStr, ok := evt.Attributes["feeWei"]
			if !ok {
				continue
			}
			fee := new(big.Int)
			if _, ok := fee.SetString(feeStr, 10); !ok {
				t.Fatalf("parse fee %q", feeStr)
			}
			eventTotal.Add(eventTotal, fee)
		}
		lastEventIdx = len(emitted)
	}

	if eventTotal.Sign() == 0 {
		t.Fatalf("expected positive fee total from events")
	}

	routeBalance := accountBalance(t, node, routeKey.PubKey().Address().Bytes())
	if routeBalance.Sign() == 0 {
		t.Fatalf("route balance should be positive")
	}

	tolerance := big.NewInt(1)
	diff := new(big.Int).Sub(routeBalance, eventTotal)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	if diff.Cmp(tolerance) > 0 {
		t.Fatalf("route balance mismatch: got %s, events %s, diff %s", routeBalance.String(), eventTotal.String(), diff.String())
	}

	t.Logf("fee reconciliation PASS: route wallet %s balance %s matches event total %s", expectedRouteHex, routeBalance.String(), eventTotal.String())
}

func TestFeesDashboardSchema(t *testing.T) {
	dashboardPath := filepath.Join("..", "..", "..", "ops", "grafana", "dashboards", "fees.json")
	data, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}

	var dash grafanaDashboard
	if err := json.Unmarshal(data, &dash); err != nil {
		t.Fatalf("decode dashboard JSON: %v", err)
	}

	if len(dash.Panels) == 0 {
		t.Fatalf("dashboard must define panels")
	}
	datasourceCount := 0
	for idx, panel := range dash.Panels {
		if panel.Title == "" {
			t.Fatalf("panel %d missing title", idx)
		}
		if panel.Type == "" {
			t.Fatalf("panel %q missing type", panel.Title)
		}
		if panel.Datasource != nil && panel.Datasource.UID != "" {
			datasourceCount++
		} else if panel.Type != "row" && panel.Type != "text" {
			t.Fatalf("panel %q missing datasource uid", panel.Title)
		}
		if len(panel.Targets) == 0 && panel.Type != "row" && panel.Type != "text" {
			t.Fatalf("panel %q missing targets", panel.Title)
		}
		for _, target := range panel.Targets {
			if target.RefID == "" {
				t.Fatalf("panel %q has target missing refId", panel.Title)
			}
			if target.Query == "" {
				t.Fatalf("panel %q has target missing query", panel.Title)
			}
		}
	}
	if datasourceCount == 0 {
		t.Fatalf("expected at least one datasource with uid")
	}
	if len(dash.Annotations.List) == 0 {
		t.Fatalf("dashboard missing annotations")
	}
}
