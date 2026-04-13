package payoutd

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"nhbchain/services/payoutd/wallet"
)

// TreasuryAssetConfig captures operator thresholds for a payout asset.
type TreasuryAssetConfig struct {
	Asset            string
	ColdAddress      string
	HotMinBalance    *big.Int
	HotTargetBalance *big.Int
}

// TreasuryAssetStatus summarises one asset's treasury reconciliation state.
type TreasuryAssetStatus struct {
	Asset                  string `json:"asset"`
	Native                 bool   `json:"native"`
	TokenAddress           string `json:"token_address,omitempty"`
	ColdAddress            string `json:"cold_address,omitempty"`
	DailyCapRemaining      string `json:"daily_cap_remaining"`
	SoftInventoryRemaining string `json:"soft_inventory_remaining"`
	OnChainBalance         string `json:"on_chain_balance,omitempty"`
	HotMinBalance          string `json:"hot_min_balance,omitempty"`
	HotTargetBalance       string `json:"hot_target_balance,omitempty"`
	CoverageDelta          string `json:"coverage_delta,omitempty"`
	Action                 string `json:"action"`
	RecommendedAmount      string `json:"recommended_amount,omitempty"`
	Healthy                bool   `json:"healthy"`
	Error                  string `json:"error,omitempty"`
}

// TreasurySnapshot captures the hot-wallet treasury state used for operator reconciliation.
type TreasurySnapshot struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Wallet      *wallet.Status        `json:"wallet,omitempty"`
	Assets      []TreasuryAssetStatus `json:"assets"`
}

// TreasurySweepPlan lists only assets that currently require operator action.
type TreasurySweepPlan struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Plans       []TreasuryAssetStatus `json:"plans"`
}

// TreasuryInstructionRequest captures the input for creating an auditable treasury action.
type TreasuryInstructionRequest struct {
	Action      string
	Asset       string
	Amount      *big.Int
	RequestedBy string
	Notes       string
}

// TreasuryInstructionDecision captures an approval or rejection review.
type TreasuryInstructionDecision struct {
	ID     string
	Actor  string
	Notes  string
	Reject bool
}

// WithTreasuryAssets configures hot/cold wallet thresholds for treasury reporting.
func WithTreasuryAssets(configs map[string]TreasuryAssetConfig) ProcessorOption {
	return func(p *Processor) {
		if len(configs) == 0 {
			return
		}
		p.treasuryAssets = make(map[string]TreasuryAssetConfig, len(configs))
		for asset, cfg := range configs {
			key := strings.ToUpper(strings.TrimSpace(asset))
			cfg.Asset = key
			p.treasuryAssets[key] = cfg
		}
	}
}

// BuildTreasuryAssets parses treasury thresholds from wallet configuration.
func BuildTreasuryAssets(input []WalletAssetConfig) (map[string]TreasuryAssetConfig, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make(map[string]TreasuryAssetConfig, len(input))
	for _, asset := range input {
		key := strings.ToUpper(strings.TrimSpace(asset.Symbol))
		if key == "" {
			return nil, fmt.Errorf("treasury asset symbol required")
		}
		minBalance, err := parseOptionalAmount(asset.HotMinBalance)
		if err != nil {
			return nil, fmt.Errorf("treasury asset %s hot_min_balance: %w", key, err)
		}
		targetBalance, err := parseOptionalAmount(asset.HotTargetBalance)
		if err != nil {
			return nil, fmt.Errorf("treasury asset %s hot_target_balance: %w", key, err)
		}
		if minBalance != nil && targetBalance != nil && targetBalance.Cmp(minBalance) < 0 {
			return nil, fmt.Errorf("treasury asset %s hot_target_balance must be >= hot_min_balance", key)
		}
		out[key] = TreasuryAssetConfig{
			Asset:            key,
			ColdAddress:      strings.TrimSpace(asset.ColdAddress),
			HotMinBalance:    minBalance,
			HotTargetBalance: targetBalance,
		}
	}
	return out, nil
}

// TreasurySnapshot returns a reconciliation view of hot-wallet balances versus payout policy.
func (p *Processor) TreasurySnapshot(ctx context.Context) (TreasurySnapshot, error) {
	now := p.now
	if now == nil {
		now = time.Now
	}
	snapshot := TreasurySnapshot{GeneratedAt: now()}
	if reporter, ok := p.wallet.(wallet.StatusProvider); ok {
		walletStatus := reporter.Status()
		snapshot.Wallet = &walletStatus
	}
	inspector, ok := p.wallet.(wallet.BalanceProvider)
	if !ok {
		return snapshot, fmt.Errorf("payoutd: wallet balance inspection unavailable")
	}
	assetSet := make(map[string]struct{})
	if p.policies != nil {
		for _, asset := range p.policies.Assets() {
			assetSet[asset] = struct{}{}
		}
	}
	for asset := range p.treasuryAssets {
		assetSet[asset] = struct{}{}
	}
	if snapshot.Wallet != nil {
		for asset := range snapshot.Wallet.Assets {
			assetSet[strings.ToUpper(strings.TrimSpace(asset))] = struct{}{}
		}
	}
	assets := make([]string, 0, len(assetSet))
	for asset := range assetSet {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		status := TreasuryAssetStatus{
			Asset:                  asset,
			Action:                 "none",
			Healthy:                true,
			DailyCapRemaining:      "0",
			SoftInventoryRemaining: "0",
		}
		if snapshot.Wallet != nil {
			if route, ok := snapshot.Wallet.Assets[asset]; ok {
				status.Native = route.Native
				status.TokenAddress = route.TokenAddress
			}
		}
		if p.policies != nil {
			status.DailyCapRemaining = p.policies.RemainingCap(asset, now()).String()
			status.SoftInventoryRemaining = p.policies.RemainingInventory(asset).String()
		}
		if cfg, ok := p.treasuryAssets[asset]; ok {
			status.ColdAddress = cfg.ColdAddress
			if cfg.HotMinBalance != nil {
				status.HotMinBalance = cfg.HotMinBalance.String()
			}
			if cfg.HotTargetBalance != nil {
				status.HotTargetBalance = cfg.HotTargetBalance.String()
			}
		}
		balance, err := inspector.Balance(ctx, asset)
		if err != nil {
			status.Healthy = false
			status.Action = "inspect_wallet"
			status.Error = err.Error()
			snapshot.Assets = append(snapshot.Assets, status)
			continue
		}
		if balance == nil {
			balance = big.NewInt(0)
		}
		status.OnChainBalance = balance.String()
		inventory := big.NewInt(0)
		if p.policies != nil {
			inventory = p.policies.RemainingInventory(asset)
		}
		status.CoverageDelta = new(big.Int).Sub(new(big.Int).Set(balance), inventory).String()
		if balance.Cmp(inventory) < 0 {
			status.Healthy = false
			status.Action = "refill_hot"
			status.RecommendedAmount = new(big.Int).Sub(inventory, balance).String()
		}
		if cfg, ok := p.treasuryAssets[asset]; ok {
			if cfg.HotMinBalance != nil && balance.Cmp(cfg.HotMinBalance) < 0 {
				status.Healthy = false
				status.Action = "refill_hot"
				recommended := new(big.Int).Sub(cfg.HotMinBalance, balance)
				if cfg.HotTargetBalance != nil && cfg.HotTargetBalance.Cmp(balance) > 0 {
					recommended = new(big.Int).Sub(cfg.HotTargetBalance, balance)
				}
				status.RecommendedAmount = recommended.String()
			} else if cfg.HotTargetBalance != nil && cfg.ColdAddress != "" && balance.Cmp(cfg.HotTargetBalance) > 0 {
				status.Action = "sweep_to_cold"
				status.RecommendedAmount = new(big.Int).Sub(balance, cfg.HotTargetBalance).String()
			}
		}
		snapshot.Assets = append(snapshot.Assets, status)
	}
	return snapshot, nil
}

// TreasurySweepPlan returns the subset of treasury assets that need operator action.
func (p *Processor) TreasurySweepPlan(ctx context.Context) (TreasurySweepPlan, error) {
	snapshot, err := p.TreasurySnapshot(ctx)
	if err != nil {
		return TreasurySweepPlan{GeneratedAt: snapshot.GeneratedAt}, err
	}
	plan := TreasurySweepPlan{GeneratedAt: snapshot.GeneratedAt}
	for _, asset := range snapshot.Assets {
		if asset.Action != "none" {
			plan.Plans = append(plan.Plans, asset)
		}
	}
	return plan, nil
}

// CreateTreasuryInstruction records a maker-submitted treasury movement request.
func (p *Processor) CreateTreasuryInstruction(req TreasuryInstructionRequest) (TreasuryInstruction, error) {
	if p == nil || p.treasuryStore == nil {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury instruction store unavailable")
	}
	action := strings.TrimSpace(req.Action)
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	requestedBy := strings.TrimSpace(req.RequestedBy)
	if action == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury action required")
	}
	if asset == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury asset required")
	}
	if requestedBy == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: requested_by required")
	}
	if req.Amount == nil || req.Amount.Sign() <= 0 {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury amount must be positive")
	}
	cfg, ok := p.treasuryAssets[asset]
	if !ok {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury asset %s not configured", asset)
	}
	instruction := TreasuryInstruction{
		ID:          "ti-" + uuid.NewString(),
		Action:      action,
		Asset:       asset,
		Amount:      req.Amount.String(),
		Status:      TreasuryInstructionPending,
		RequestedBy: requestedBy,
		Notes:       strings.TrimSpace(req.Notes),
		CreatedAt:   p.now(),
	}
	if reporter, ok := p.wallet.(wallet.StatusProvider); ok {
		walletStatus := reporter.Status()
		instruction.Source = strings.TrimSpace(walletStatus.FromAddress)
	}
	switch action {
	case "sweep_to_cold":
		if cfg.ColdAddress == "" {
			return TreasuryInstruction{}, fmt.Errorf("payoutd: cold address not configured for %s", asset)
		}
		instruction.Destination = cfg.ColdAddress
	case "refill_hot":
		if cfg.ColdAddress == "" {
			return TreasuryInstruction{}, fmt.Errorf("payoutd: cold address not configured for %s", asset)
		}
		instruction.Source = cfg.ColdAddress
		if reporter, ok := p.wallet.(wallet.StatusProvider); ok {
			instruction.Destination = strings.TrimSpace(reporter.Status().FromAddress)
		}
	default:
		return TreasuryInstruction{}, fmt.Errorf("payoutd: unsupported treasury action %s", action)
	}
	if strings.TrimSpace(instruction.Source) == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury source unresolved for %s", asset)
	}
	if strings.TrimSpace(instruction.Destination) == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury destination unresolved for %s", asset)
	}
	if err := p.treasuryStore.Put(instruction); err != nil {
		return TreasuryInstruction{}, err
	}
	return instruction, nil
}

// ListTreasuryInstructions returns treasury instructions filtered by status when provided.
func (p *Processor) ListTreasuryInstructions(status string) ([]TreasuryInstruction, error) {
	if p == nil || p.treasuryStore == nil {
		return nil, fmt.Errorf("payoutd: treasury instruction store unavailable")
	}
	items, err := p.treasuryStore.List()
	if err != nil {
		return nil, err
	}
	filter := strings.TrimSpace(strings.ToLower(status))
	if filter == "" {
		return items, nil
	}
	filtered := make([]TreasuryInstruction, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(string(item.Status), filter) {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

// ReviewTreasuryInstruction approves or rejects a pending treasury instruction with maker-checker enforcement.
func (p *Processor) ReviewTreasuryInstruction(decision TreasuryInstructionDecision) (TreasuryInstruction, error) {
	if p == nil || p.treasuryStore == nil {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury instruction store unavailable")
	}
	id := strings.TrimSpace(decision.ID)
	actor := strings.TrimSpace(decision.Actor)
	if id == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury instruction id required")
	}
	if actor == "" {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury reviewer required")
	}
	instruction, ok, err := p.treasuryStore.Get(id)
	if err != nil {
		return TreasuryInstruction{}, err
	}
	if !ok {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury instruction %s not found", id)
	}
	if instruction.Status != TreasuryInstructionPending {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury instruction %s already reviewed", id)
	}
	if strings.EqualFold(instruction.RequestedBy, actor) {
		return TreasuryInstruction{}, fmt.Errorf("payoutd: treasury maker-checker violation")
	}
	now := p.now()
	instruction.ReviewNotes = strings.TrimSpace(decision.Notes)
	if decision.Reject {
		instruction.Status = TreasuryInstructionRejected
		instruction.RejectedBy = actor
		instruction.RejectedAt = &now
	} else {
		instruction.Status = TreasuryInstructionApproved
		instruction.ApprovedBy = actor
		instruction.ApprovedAt = &now
	}
	if err := p.treasuryStore.Put(instruction); err != nil {
		return TreasuryInstruction{}, err
	}
	return instruction, nil
}

func parseOptionalAmount(raw string) (*big.Int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer amount %q", raw)
	}
	if value.Sign() < 0 {
		return nil, fmt.Errorf("amount must be non-negative")
	}
	return value, nil
}
