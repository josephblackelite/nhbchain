package e2e

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

type lendingPosition struct {
	Account      string `json:"account"`
	Supplied     int64  `json:"supplied"`
	Borrowed     int64  `json:"borrowed"`
	HealthFactor string `json:"health_factor"`
}

type lendingResponse struct {
	Position lendingPosition `json:"position"`
}

type swapResponse struct {
	Account string `json:"account"`
	Balance int64  `json:"balance"`
}

type proposalResponse struct {
	Proposal struct {
		ID          int    `json:"id"`
		Status      string `json:"status"`
		Title       string `json:"title"`
		Description string `json:"description"`
		YesVotes    int    `json:"yes_votes"`
	} `json:"proposal"`
}

type applyResponse struct {
	ProposalID int   `json:"proposal_id"`
	Height     int64 `json:"height"`
}

type consensusSnapshot struct {
	Height    int   `json:"height"`
	Proposals []int `json:"proposals"`
}

func TestEndToEndFinancialFlows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cl, err := cluster.New(ctx)
	if err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cl.Stop(shutdownCtx); err != nil {
			t.Fatalf("shutdown cluster: %v", err)
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	base := cl.GatewayURL()
	account := "alice-account"

	supply := map[string]any{"account": account, "amount": 1000, "request_id": "supply-1"}
	var supplyResp lendingResponse
	doPost(t, client, fmt.Sprintf("%s/v1/lending/supply", base), supply, &supplyResp)
	if supplyResp.Position.Supplied != 1000 {
		t.Fatalf("unexpected supply: %+v", supplyResp.Position)
	}

	// ensure idempotent replays do not change state
	var replay lendingResponse
	doPost(t, client, fmt.Sprintf("%s/v1/lending/supply", base), supply, &replay)
	if replay.Position.Supplied != 1000 {
		t.Fatalf("idempotent supply mismatch: %+v", replay.Position)
	}

	borrow := map[string]any{"account": account, "amount": 400, "request_id": "borrow-1"}
	var borrowResp lendingResponse
	doPost(t, client, fmt.Sprintf("%s/v1/lending/borrow", base), borrow, &borrowResp)
	if borrowResp.Position.Borrowed != 400 {
		t.Fatalf("unexpected borrow: %+v", borrowResp.Position)
	}

	position := queryPosition(t, client, base, account)
	if position.Position.Borrowed != 400 || position.Position.Supplied != 1000 {
		t.Fatalf("unexpected position after borrow: %+v", position.Position)
	}

	repay := map[string]any{"account": account, "amount": 250, "request_id": "repay-1"}
	var repayResp lendingResponse
	doPost(t, client, fmt.Sprintf("%s/v1/lending/repay", base), repay, &repayResp)
	if repayResp.Position.Borrowed != 150 {
		t.Fatalf("unexpected borrow after repay: %+v", repayResp.Position)
	}

	mint := map[string]any{"account": account, "amount": 500, "request_id": "mint-1"}
	var mintResp swapResponse
	doPost(t, client, fmt.Sprintf("%s/v1/swap/mint", base), mint, &mintResp)
	if mintResp.Balance != 500 {
		t.Fatalf("unexpected mint balance: %+v", mintResp)
	}

	redeem := map[string]any{"account": account, "amount": 200, "request_id": "redeem-1"}
	var redeemResp swapResponse
	doPost(t, client, fmt.Sprintf("%s/v1/swap/redeem", base), redeem, &redeemResp)
	if redeemResp.Balance != 300 {
		t.Fatalf("unexpected balance after redeem: %+v", redeemResp)
	}

	proposalPayload := map[string]any{"title": "raise-cap", "description": "increase limits", "request_id": "proposal-1"}
	var proposalResp proposalResponse
	doPost(t, client, fmt.Sprintf("%s/v1/gov/proposals", base), proposalPayload, &proposalResp)
	proposalID := proposalResp.Proposal.ID
	if proposalID == 0 {
		t.Fatalf("proposal id missing: %+v", proposalResp)
	}

	votePayload := map[string]any{"voter": "validator-1", "option": "yes", "request_id": "vote-1"}
	doPost(t, client, fmt.Sprintf("%s/v1/gov/proposals/%d/vote", base, proposalID), votePayload, &proposalResp)
	if proposalResp.Proposal.YesVotes != 1 {
		t.Fatalf("vote not recorded: %+v", proposalResp.Proposal)
	}

	applyPayload := map[string]any{"request_id": "apply-1"}
	var applied applyResponse
	doPost(t, client, fmt.Sprintf("%s/v1/gov/proposals/%d/apply", base, proposalID), applyPayload, &applied)
	if applied.ProposalID != proposalID || applied.Height == 0 {
		t.Fatalf("unexpected apply response: %+v", applied)
	}

	snapshot := queryConsensus(t, client, base)
	if snapshot.Height != int(applied.Height) || len(snapshot.Proposals) == 0 {
		t.Fatalf("unexpected consensus snapshot: %+v", snapshot)
	}
	found := false
	for _, id := range snapshot.Proposals {
		if id == proposalID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("proposal %d not applied in consensus: %+v", proposalID, snapshot)
	}
}

func doPost(t *testing.T, client *http.Client, url string, payload map[string]any, out any) {
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
			t.Fatalf("decode response from %s: %v", url, err)
		}
	}
}

func queryPosition(t *testing.T, client *http.Client, base, account string) lendingResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/lending/position?account=%s", base, account), nil)
	if err != nil {
		t.Fatalf("position request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("position status %d: %s", resp.StatusCode, string(data))
	}
	var result lendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode position: %v", err)
	}
	return result
}

func queryConsensus(t *testing.T, client *http.Client, base string) consensusSnapshot {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/consensus/applied", base), nil)
	if err != nil {
		t.Fatalf("consensus request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get consensus snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("consensus status %d: %s", resp.StatusCode, string(data))
	}
	var snapshot consensusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode consensus snapshot: %v", err)
	}
	return snapshot
}
