package payoutd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"nhbchain/native/swap"
	"nhbchain/services/payoutd/wallet"
)

// ErrProcessorPaused is returned when a payout is attempted while the processor is paused.
var ErrProcessorPaused = errors.New("payoutd: processor paused")

// ErrIntentAborted indicates that the intent has been explicitly aborted by an operator.
var ErrIntentAborted = errors.New("payoutd: intent aborted")

// ErrOnChainBalanceInsufficient indicates the hot wallet cannot actually settle the payout.
var ErrOnChainBalanceInsufficient = errors.New("payoutd: on-chain hot wallet balance insufficient")

// CashOutRequest bundles a cash-out intent with payout metadata.
type CashOutRequest struct {
	Intent      *swap.CashOutIntent
	Destination string
	EvidenceURI string
	PartnerID   string
	Region      string
	RequestedBy string
	Approval    ApprovalMetadata
}

// ApprovalMetadata records the human approval attached to a payout when policy requires review.
type ApprovalMetadata struct {
	ApprovedBy string
	Reference  string
	Notes      string
}

type processState struct {
	completed bool
	aborted   bool
	inFlight  bool
	txHash    string
	updatedAt time.Time
}

// Processor coordinates policy enforcement, treasury transfers, and attestation submission.
type Processor struct {
	wallet         wallet.ERC20Wallet
	attestor       Attestor
	policies       *PolicyEnforcer
	metrics        *Metrics
	waitInterval   time.Duration
	now            func() time.Time
	tracer         trace.Tracer
	treasuryAssets map[string]TreasuryAssetConfig
	treasuryStore  TreasuryInstructionStore
	executionStore PayoutExecutionStore
	holdStore      HoldStore

	mu        sync.Mutex
	paused    bool
	processed map[string]processState
}

// ProcessorOption customises the processor instance.
type ProcessorOption func(*Processor)

// WithWallet supplies the hot wallet implementation.
func WithWallet(w wallet.ERC20Wallet) ProcessorOption {
	return func(p *Processor) { p.wallet = w }
}

// WithAttestor supplies the attestor implementation.
func WithAttestor(a Attestor) ProcessorOption {
	return func(p *Processor) { p.attestor = a }
}

// WithMetrics overrides the default metrics registry.
func WithMetrics(m *Metrics) ProcessorOption {
	return func(p *Processor) { p.metrics = m }
}

// WithPollInterval configures the confirmation polling cadence.
func WithPollInterval(interval time.Duration) ProcessorOption {
	return func(p *Processor) { p.waitInterval = interval }
}

// WithClock sets the function used to derive timestamps.
func WithClock(clock func() time.Time) ProcessorOption {
	return func(p *Processor) { p.now = clock }
}

// WithTreasuryStore supplies the persistent treasury instruction store.
func WithTreasuryStore(store TreasuryInstructionStore) ProcessorOption {
	return func(p *Processor) { p.treasuryStore = store }
}

// WithExecutionStore supplies the persistent payout execution store.
func WithExecutionStore(store PayoutExecutionStore) ProcessorOption {
	return func(p *Processor) { p.executionStore = store }
}

// WithHoldStore supplies the persistent hold store.
func WithHoldStore(store HoldStore) ProcessorOption {
	return func(p *Processor) { p.holdStore = store }
}

// NewProcessor constructs a payout processor enforcing the supplied policies.
func NewProcessor(policies *PolicyEnforcer, opts ...ProcessorOption) *Processor {
	proc := &Processor{
		policies:     policies,
		metrics:      NewMetrics(),
		processed:    make(map[string]processState),
		waitInterval: 5 * time.Second,
		now:          time.Now,
		tracer:       otel.Tracer("payoutd/processor"),
	}
	for _, opt := range opts {
		opt(proc)
	}
	if proc.metrics == nil {
		proc.metrics = NewMetrics()
	}
	return proc
}

// Process executes the payout for the provided intent if the policy permits it.
func (p *Processor) Process(ctx context.Context, req CashOutRequest) error {
	if req.Intent == nil {
		return fmt.Errorf("payoutd: intent required")
	}
	intent := req.Intent
	intentID := strings.TrimSpace(intent.IntentID)
	if intentID == "" {
		return fmt.Errorf("payoutd: intent id required")
	}
	asset := strings.ToUpper(strings.TrimSpace(string(intent.StableAsset)))
	amount := intent.StableAmount
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("payoutd: stable amount required")
	}
	if strings.TrimSpace(req.Destination) == "" {
		return fmt.Errorf("payoutd: destination required")
	}
	if p.policies == nil {
		p.metrics.RecordError(asset, "policy")
		return fmt.Errorf("payoutd: policy enforcer not configured")
	}
	baseRecord := PayoutExecution{
		IntentID:     intentID,
		Account:      strings.TrimSpace(intent.Account),
		PartnerID:    strings.TrimSpace(req.PartnerID),
		Region:       strings.TrimSpace(req.Region),
		RequestedBy:  strings.TrimSpace(req.RequestedBy),
		ApprovedBy:   strings.TrimSpace(req.Approval.ApprovedBy),
		ApprovalRef:  strings.TrimSpace(req.Approval.Reference),
		StableAsset:  asset,
		StableAmount: safeBigIntString(amount),
		NhbAmount:    safeBigIntString(intent.NhbAmount),
		Destination:  strings.TrimSpace(req.Destination),
		EvidenceURI:  strings.TrimSpace(req.EvidenceURI),
		CreatedAt:    p.now(),
		UpdatedAt:    p.now(),
	}

	p.mu.Lock()
	if p.paused {
		p.mu.Unlock()
		p.metrics.RecordError(asset, "paused")
		return ErrProcessorPaused
	}
	state, exists := p.processed[intentID]
	if exists {
		if state.aborted {
			p.mu.Unlock()
			return ErrIntentAborted
		}
		if state.completed {
			p.mu.Unlock()
			return nil
		}
		if state.inFlight {
			p.mu.Unlock()
			return nil
		}
	}
	if err := p.validateHold(baseRecord); err != nil {
		p.mu.Unlock()
		p.metrics.RecordError(asset, "hold")
		p.recordFailure(baseRecord, err)
		return err
	}
	check := PolicyCheck{
		Asset:             asset,
		Amount:            amount,
		Account:           intent.Account,
		Destination:       req.Destination,
		PartnerID:         req.PartnerID,
		Region:            req.Region,
		RequestedBy:       req.RequestedBy,
		ApprovedBy:        req.Approval.ApprovedBy,
		ApprovalReference: req.Approval.Reference,
	}
	if err := p.policies.ValidateRequest(check, p.now()); err != nil {
		p.mu.Unlock()
		switch {
		case errors.Is(err, ErrDailyCapExceeded):
			p.metrics.RecordError(asset, "daily_cap")
		case errors.Is(err, ErrSoftBalanceExceeded):
			p.metrics.RecordError(asset, "inventory")
		case errors.Is(err, ErrManualReviewRequired):
			p.metrics.RecordError(asset, "manual_review")
		case errors.Is(err, ErrVelocityExceeded):
			p.metrics.RecordError(asset, "velocity")
		case errors.Is(err, ErrDestinationBlocked), errors.Is(err, ErrDestinationNotAllowed):
			p.metrics.RecordError(asset, "destination_screen")
		case errors.Is(err, ErrAccountBlocked), errors.Is(err, ErrRegionBlocked), errors.Is(err, ErrPartnerBlocked):
			p.metrics.RecordError(asset, "counterparty_screen")
		default:
			p.metrics.RecordError(asset, "policy")
		}
		p.recordFailure(baseRecord, err)
		return err
	}
	start := p.now()
	p.processed[intentID] = processState{inFlight: true, updatedAt: p.now()}
	p.mu.Unlock()
	baseRecord.Status = PayoutExecutionProcessing
	baseRecord.CreatedAt = start
	baseRecord.UpdatedAt = start
	p.recordExecution(baseRecord)

	ctx, span := p.tracer.Start(ctx, "payout.process_intent",
		trace.WithAttributes(
			attribute.String("intent.id", intentID),
			attribute.String("asset", asset),
		))
	defer span.End()
	if p.wallet == nil {
		p.finishFailure(intentID)
		err := fmt.Errorf("payoutd: wallet not configured")
		p.recordFailure(baseRecord, err)
		return err
	}
	if inspector, ok := p.wallet.(wallet.BalanceProvider); ok {
		balanceCtx, balanceSpan := p.tracer.Start(ctx, "payout.verify_balance")
		onChainBalance, balanceErr := inspector.Balance(balanceCtx, asset)
		balanceSpan.End()
		if balanceErr != nil {
			span.RecordError(balanceErr)
			span.SetStatus(codes.Error, "wallet balance check failed")
			p.finishFailure(intentID)
			p.recordFailure(baseRecord, balanceErr)
			p.metrics.RecordError(asset, "wallet_balance")
			return balanceErr
		}
		if onChainBalance == nil || onChainBalance.Cmp(amount) < 0 {
			err := ErrOnChainBalanceInsufficient
			span.RecordError(err)
			span.SetStatus(codes.Error, "wallet balance insufficient")
			p.finishFailure(intentID)
			p.recordFailure(baseRecord, fmt.Errorf("%w for %s: have %s need %s", err, asset, safeBigIntString(onChainBalance), amount.String()))
			p.metrics.RecordError(asset, "wallet_balance")
			return fmt.Errorf("%w for %s: have %s need %s", err, asset, safeBigIntString(onChainBalance), amount.String())
		}
	}
	transferCtx, transferSpan := p.tracer.Start(ctx, "payout.wallet_transfer")
	txHash, err := p.wallet.Transfer(transferCtx, asset, strings.TrimSpace(req.Destination), amount)
	transferSpan.End()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "wallet transfer failed")
		p.finishFailure(intentID)
		p.recordFailure(baseRecord, err)
		p.metrics.RecordError(asset, "transfer")
		return err
	}
	confirmations := p.policies.Confirmations(asset)
	interval := p.waitInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	confirmCtx, confirmSpan := p.tracer.Start(ctx, "payout.wait_confirmations",
		trace.WithAttributes(attribute.Int("confirmations", confirmations)))
	if err := p.wallet.WaitForConfirmations(confirmCtx, txHash, confirmations, interval); err != nil {
		confirmSpan.End()
		span.RecordError(err)
		span.SetStatus(codes.Error, "confirmation wait failed")
		p.finishFailure(intentID)
		p.recordFailure(baseRecord, err)
		p.metrics.RecordError(asset, "confirmations")
		return err
	}
	confirmSpan.End()
	if p.attestor == nil {
		p.finishFailure(intentID)
		p.recordFailure(baseRecord, fmt.Errorf("payoutd: attestor not configured"))
		return fmt.Errorf("payoutd: attestor not configured")
	}
	receipt := Receipt{
		ReceiptID:    fmt.Sprintf("%s-%d", intentID, start.Unix()),
		IntentID:     intentID,
		StableAsset:  asset,
		StableAmount: new(big.Int).Set(amount),
		NhbAmount:    cloneBigInt(intent.NhbAmount),
		TxHash:       txHash,
		EvidenceURI:  strings.TrimSpace(req.EvidenceURI),
		SettledAt:    p.now(),
	}
	attestCtx, attestSpan := p.tracer.Start(ctx, "payout.submit_receipt")
	if err := p.attestor.SubmitReceipt(attestCtx, receipt); err != nil {
		attestSpan.End()
		span.RecordError(err)
		span.SetStatus(codes.Error, "attestation failed")
		p.finishFailure(intentID)
		p.recordFailure(baseRecord, err)
		p.metrics.RecordError(asset, "attest")
		return err
	}
	attestSpan.End()

	p.mu.Lock()
	p.policies.RecordRequest(check, p.now())
	remaining := p.policies.RemainingCap(asset, p.now())
	total := p.policies.DailyCap(asset)
	p.metrics.RecordCap(asset, remaining, total)
	p.processed[intentID] = processState{
		completed: true,
		txHash:    txHash,
		updatedAt: p.now(),
	}
	p.mu.Unlock()

	p.metrics.ObserveLatency(asset, p.now().Sub(start))
	settledAt := p.now()
	baseRecord.TxHash = txHash
	baseRecord.Status = PayoutExecutionSettled
	baseRecord.CreatedAt = start
	baseRecord.UpdatedAt = settledAt
	baseRecord.SettledAt = &settledAt
	p.recordExecution(baseRecord)
	span.SetStatus(codes.Ok, "payout settled")
	span.SetAttributes(attribute.String("tx.hash", txHash))
	slog.InfoContext(ctx, "payout settled",
		slog.String("intent_id", intentID),
		slog.String("asset", asset),
		slog.String("tx_hash", txHash),
	)
	return nil
}

func (p *Processor) finishFailure(intentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.processed, intentID)
}

// Abort cancels an intent and returns the escrowed NHB to the requester.
func (p *Processor) Abort(ctx context.Context, intentID, reason string) error {
	trimmed := strings.TrimSpace(intentID)
	if trimmed == "" {
		return fmt.Errorf("payoutd: intent id required")
	}
	if p.attestor == nil {
		return fmt.Errorf("payoutd: attestor not configured")
	}
	p.mu.Lock()
	state, exists := p.processed[trimmed]
	if exists {
		if state.completed {
			p.mu.Unlock()
			return fmt.Errorf("payoutd: intent already settled")
		}
		if state.aborted {
			p.mu.Unlock()
			return nil
		}
		if state.inFlight {
			p.mu.Unlock()
			return fmt.Errorf("payoutd: intent in progress")
		}
	}
	p.processed[trimmed] = processState{aborted: true, updatedAt: p.now()}
	p.mu.Unlock()

	if err := p.attestor.AbortIntent(ctx, trimmed, reason); err != nil {
		p.mu.Lock()
		delete(p.processed, trimmed)
		p.mu.Unlock()
		return err
	}
	p.recordExecution(PayoutExecution{
		IntentID:  trimmed,
		Status:    PayoutExecutionAborted,
		Error:     strings.TrimSpace(reason),
		CreatedAt: p.now(),
		UpdatedAt: p.now(),
	})
	return nil
}

// Pause halts new payout processing.
func (p *Processor) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
	if p.metrics != nil {
		p.metrics.SetPause(true)
	}
}

// Resume re-enables payout processing.
func (p *Processor) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
	if p.metrics != nil {
		p.metrics.SetPause(false)
	}
}

// UpdateInventory sets the available soft balance for the provided asset.
func (p *Processor) UpdateInventory(asset string, balance *big.Int) {
	if p.policies == nil {
		return
	}
	p.policies.SetInventory(asset, balance)
	remaining := p.policies.RemainingCap(asset, p.now())
	total := p.policies.DailyCap(asset)
	p.metrics.RecordCap(strings.ToUpper(strings.TrimSpace(asset)), remaining, total)
}

// Status summarises processor state for administrative endpoints.
type Status struct {
	Paused                  bool              `json:"paused"`
	Processed               int               `json:"processed"`
	Aborted                 int               `json:"aborted"`
	InFlight                int               `json:"in_flight"`
	CapRemaining            map[string]string `json:"cap_remaining"`
	Wallet                  *wallet.Status    `json:"wallet,omitempty"`
	TreasuryPendingActions  int               `json:"treasury_pending_actions,omitempty"`
	TreasuryApprovedActions int               `json:"treasury_approved_actions,omitempty"`
	PayoutExecutionCount    int               `json:"payout_execution_count,omitempty"`
	ActiveHolds             int               `json:"active_holds,omitempty"`
}

// Status reports the current processor status snapshot.
func (p *Processor) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := Status{
		Paused:       p.paused,
		CapRemaining: make(map[string]string),
	}
	for _, state := range p.processed {
		switch {
		case state.aborted:
			status.Aborted++
		case state.completed:
			status.Processed++
		case state.inFlight:
			status.InFlight++
		}
	}
	if p.policies != nil {
		snapshot := p.policies.Snapshot(p.now())
		for asset, remaining := range snapshot {
			status.CapRemaining[asset] = remaining.String()
		}
	}
	if reporter, ok := p.wallet.(wallet.StatusProvider); ok {
		walletStatus := reporter.Status()
		status.Wallet = &walletStatus
	}
	if p.treasuryStore != nil {
		if items, err := p.treasuryStore.List(); err == nil {
			for _, item := range items {
				switch item.Status {
				case TreasuryInstructionPending:
					status.TreasuryPendingActions++
				case TreasuryInstructionApproved:
					status.TreasuryApprovedActions++
				}
			}
		}
	}
	if p.executionStore != nil {
		if items, err := p.executionStore.List(); err == nil {
			status.PayoutExecutionCount = len(items)
		}
	}
	if p.holdStore != nil {
		if items, err := p.holdStore.List(); err == nil {
			for _, item := range items {
				if item.Active {
					status.ActiveHolds++
				}
			}
		}
	}
	return status
}

func (p *Processor) recordExecution(record PayoutExecution) {
	if p == nil || p.executionStore == nil {
		return
	}
	existing, ok, err := p.executionStore.Get(record.IntentID)
	if err == nil && ok {
		if record.CreatedAt.IsZero() {
			record.CreatedAt = existing.CreatedAt
		}
		if strings.TrimSpace(record.StableAsset) == "" {
			record.StableAsset = existing.StableAsset
		}
		if strings.TrimSpace(record.Account) == "" {
			record.Account = existing.Account
		}
		if strings.TrimSpace(record.PartnerID) == "" {
			record.PartnerID = existing.PartnerID
		}
		if strings.TrimSpace(record.Region) == "" {
			record.Region = existing.Region
		}
		if strings.TrimSpace(record.RequestedBy) == "" {
			record.RequestedBy = existing.RequestedBy
		}
		if strings.TrimSpace(record.ApprovedBy) == "" {
			record.ApprovedBy = existing.ApprovedBy
		}
		if strings.TrimSpace(record.ApprovalRef) == "" {
			record.ApprovalRef = existing.ApprovalRef
		}
		if strings.TrimSpace(record.StableAmount) == "" {
			record.StableAmount = existing.StableAmount
		}
		if strings.TrimSpace(record.NhbAmount) == "" {
			record.NhbAmount = existing.NhbAmount
		}
		if strings.TrimSpace(record.Destination) == "" {
			record.Destination = existing.Destination
		}
		if strings.TrimSpace(record.EvidenceURI) == "" {
			record.EvidenceURI = existing.EvidenceURI
		}
		if strings.TrimSpace(record.TxHash) == "" {
			record.TxHash = existing.TxHash
		}
		if strings.TrimSpace(record.Error) == "" {
			record.Error = existing.Error
		}
		if record.SettledAt == nil {
			record.SettledAt = existing.SettledAt
		}
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = p.now()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = p.now()
	}
	_ = p.executionStore.Put(record)
}

func (p *Processor) recordFailure(record PayoutExecution, err error) {
	record.Status = PayoutExecutionFailed
	record.Error = strings.TrimSpace(err.Error())
	record.UpdatedAt = p.now()
	p.recordExecution(record)
}

// ListPayoutExecutions returns payout execution records filtered by status and asset.
func (p *Processor) ListPayoutExecutions(status, asset string, limit int) ([]PayoutExecution, error) {
	if p == nil || p.executionStore == nil {
		return nil, fmt.Errorf("payoutd: execution store not configured")
	}
	items, err := p.executionStore.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]PayoutExecution, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(status); trimmed != "" && !strings.EqualFold(string(item.Status), trimmed) {
			continue
		}
		if trimmed := strings.TrimSpace(asset); trimmed != "" && !strings.EqualFold(item.StableAsset, trimmed) {
			continue
		}
		filtered = append(filtered, item)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

func cloneBigInt(in *big.Int) *big.Int {
	if in == nil {
		return nil
	}
	return new(big.Int).Set(in)
}

func (p *Processor) validateHold(record PayoutExecution) error {
	if p == nil || p.holdStore == nil {
		return nil
	}
	checks := []struct {
		scope HoldScope
		value string
	}{
		{scope: HoldScopeAccount, value: record.Account},
		{scope: HoldScopeDestination, value: record.Destination},
		{scope: HoldScopePartner, value: record.PartnerID},
		{scope: HoldScopeRegion, value: record.Region},
	}
	items, err := p.holdStore.List()
	if err != nil {
		return fmt.Errorf("payoutd: hold store unavailable: %w", err)
	}
	for _, hold := range items {
		if !hold.Active {
			continue
		}
		for _, candidate := range checks {
			if candidate.value == "" {
				continue
			}
			if hold.Scope == candidate.scope && strings.EqualFold(strings.TrimSpace(hold.Value), strings.TrimSpace(candidate.value)) {
				return fmt.Errorf("payoutd: active %s hold for %s (%s)", hold.Scope, candidate.value, strings.TrimSpace(hold.Reason))
			}
		}
	}
	return nil
}

// ListHolds returns the configured payout holds.
func (p *Processor) ListHolds(scope string, activeOnly bool) ([]HoldRecord, error) {
	if p == nil || p.holdStore == nil {
		return nil, fmt.Errorf("payoutd: hold store not configured")
	}
	items, err := p.holdStore.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]HoldRecord, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(scope); trimmed != "" && !strings.EqualFold(string(item.Scope), trimmed) {
			continue
		}
		if activeOnly && !item.Active {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

// CreateHold creates a new active hold in the payout hold store.
func (p *Processor) CreateHold(scope HoldScope, value, reason, createdBy string) (HoldRecord, error) {
	if p == nil || p.holdStore == nil {
		return HoldRecord{}, fmt.Errorf("payoutd: hold store not configured")
	}
	record := HoldRecord{
		ID:        fmt.Sprintf("hold-%d", p.now().UnixNano()),
		Scope:     HoldScope(strings.ToLower(strings.TrimSpace(string(scope)))),
		Value:     strings.TrimSpace(value),
		Reason:    strings.TrimSpace(reason),
		CreatedBy: strings.TrimSpace(createdBy),
		CreatedAt: p.now(),
		UpdatedAt: p.now(),
		Active:    true,
	}
	switch record.Scope {
	case HoldScopeAccount, HoldScopeDestination, HoldScopePartner, HoldScopeRegion:
	default:
		return HoldRecord{}, fmt.Errorf("payoutd: invalid hold scope")
	}
	if record.Value == "" {
		return HoldRecord{}, fmt.Errorf("payoutd: hold value required")
	}
	if record.CreatedBy == "" {
		return HoldRecord{}, fmt.Errorf("payoutd: hold created_by required")
	}
	if err := p.holdStore.Put(record); err != nil {
		return HoldRecord{}, err
	}
	return record, nil
}

// ReleaseHold clears an active hold while preserving the audit trail.
func (p *Processor) ReleaseHold(id, actor, notes string) (HoldRecord, error) {
	if p == nil || p.holdStore == nil {
		return HoldRecord{}, fmt.Errorf("payoutd: hold store not configured")
	}
	record, ok, err := p.holdStore.Get(id)
	if err != nil {
		return HoldRecord{}, err
	}
	if !ok {
		return HoldRecord{}, fmt.Errorf("payoutd: hold not found")
	}
	releasedAt := p.now()
	record.Active = false
	record.ReleasedBy = strings.TrimSpace(actor)
	record.ReleaseNotes = strings.TrimSpace(notes)
	record.ReleasedAt = &releasedAt
	record.UpdatedAt = releasedAt
	if record.ReleasedBy == "" {
		return HoldRecord{}, fmt.Errorf("payoutd: release actor required")
	}
	if err := p.holdStore.Put(record); err != nil {
		return HoldRecord{}, err
	}
	return record, nil
}

func safeBigIntString(in *big.Int) string {
	if in == nil {
		return "0"
	}
	return in.String()
}
