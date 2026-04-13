package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"nhbchain/tests/support/cluster"
)

type lendingResponse struct {
	Position struct {
		Borrowed int64 `json:"borrowed"`
		Supplied int64 `json:"supplied"`
	} `json:"position"`
}

type swapResponse struct {
	Balance int64 `json:"balance"`
}

func TestLendingFlowRecoversAfterServiceRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cl, err := cluster.New(ctx)
	if err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cl.Stop(shutdownCtx); err != nil {
			t.Fatalf("stop cluster: %v", err)
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	base := cl.GatewayURL()
	account := "borrower-one"

	// seed collateral
	payload := map[string]any{"account": account, "amount": 900, "request_id": "seed-1"}
	requirePost(t, client, fmt.Sprintf("%s/v1/lending/supply", base), payload, &lendingResponse{})

	start := time.Now()

	borrow := map[string]any{"account": account, "amount": 400, "request_id": "borrow-2"}
	// stop lending service to simulate crash
	if err := cl.Kill(ctx, cluster.ServiceLending); err != nil {
		t.Fatalf("kill lending: %v", err)
	}
	err = postExpectError(client, fmt.Sprintf("%s/v1/lending/borrow", base), borrow)
	if err == nil {
		t.Fatalf("expected borrow to fail during outage")
	}

	if _, err := cl.Restart(ctx, cluster.ServiceLending); err != nil {
		t.Fatalf("restart lending: %v", err)
	}

	// retry should succeed and be idempotent
	var borrowResp lendingResponse
	requirePost(t, client, fmt.Sprintf("%s/v1/lending/borrow", base), borrow, &borrowResp)
	if borrowResp.Position.Borrowed != 400 {
		t.Fatalf("unexpected borrow result: %+v", borrowResp.Position)
	}

	// repeated retry with same request id should not change balance
	requirePost(t, client, fmt.Sprintf("%s/v1/lending/borrow", base), borrow, &borrowResp)
	if borrowResp.Position.Borrowed != 400 {
		t.Fatalf("idempotent retry changed state: %+v", borrowResp.Position)
	}

	repay := map[string]any{"account": account, "amount": 200, "request_id": "repay-2"}
	requirePost(t, client, fmt.Sprintf("%s/v1/lending/repay", base), repay, &borrowResp)
	if borrowResp.Position.Borrowed != 200 {
		t.Fatalf("unexpected repay result: %+v", borrowResp.Position)
	}

	if time.Since(start) > 30*time.Second {
		t.Fatalf("recovery exceeded MTTR target: %v", time.Since(start))
	}
}

func TestSwapMintRedeemWithChaos(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cl, err := cluster.New(ctx)
	if err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cl.Stop(shutdownCtx); err != nil {
			t.Fatalf("stop cluster: %v", err)
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	base := cl.GatewayURL()
	account := "swapper-two"

	mint := map[string]any{"account": account, "amount": 600, "request_id": "mint-chaos"}
	requirePost(t, client, fmt.Sprintf("%s/v1/swap/mint", base), mint, &swapResponse{})

	redeem := map[string]any{"account": account, "amount": 250, "request_id": "redeem-chaos"}
	if err := cl.Kill(ctx, cluster.ServiceSwap); err != nil {
		t.Fatalf("kill swapd: %v", err)
	}
	if err := postExpectError(client, fmt.Sprintf("%s/v1/swap/redeem", base), redeem); err == nil {
		t.Fatalf("expected redeem failure while service stopped")
	}

	if _, err := cl.Restart(ctx, cluster.ServiceSwap); err != nil {
		t.Fatalf("restart swapd: %v", err)
	}

	var redeemResp swapResponse
	requirePost(t, client, fmt.Sprintf("%s/v1/swap/redeem", base), redeem, &redeemResp)
	if redeemResp.Balance != 350 {
		t.Fatalf("unexpected redeem balance: %+v", redeemResp)
	}

	// duplicate redeem request should not reduce balance again
	requirePost(t, client, fmt.Sprintf("%s/v1/swap/redeem", base), redeem, &redeemResp)
	if redeemResp.Balance != 350 {
		t.Fatalf("idempotent redeem altered balance: %+v", redeemResp)
	}
}

func requirePost(t *testing.T, client *http.Client, url string, payload map[string]any, out any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("post %s status %d: %s", url, resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
}

func postExpectError(client *http.Client, url string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return fmt.Errorf("expected non-200 status")
	}
	return fmt.Errorf("status %d", resp.StatusCode)
}
