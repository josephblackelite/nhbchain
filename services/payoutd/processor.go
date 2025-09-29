package payoutd

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"nhbchain/native/swap"
	"nhbchain/services/payoutd/wallet"
)

// ErrProcessorPaused is returned when a payout is attempted while the processor is paused.
var ErrProcessorPaused = errors.New("payoutd: processor paused")

// ErrIntentAborted indicates that the intent has been explicitly aborted by an operator.
var ErrIntentAborted = errors.New("payoutd: intent aborted")

// CashOutRequest bundles a cash-out intent with payout metadata.
type CashOutRequest struct {
	Intent      *swap.CashOutIntent
	Destination string
	EvidenceURI string
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
	wallet       wallet.ERC20Wallet
	attestor     Attestor
	policies     *PolicyEnforcer
	metrics      *Metrics
	waitInterval time.Duration
	now          func() time.Time

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

// NewProcessor constructs a payout processor enforcing the supplied policies.
func NewProcessor(policies *PolicyEnforcer, opts ...ProcessorOption) *Processor {
	proc := &Processor{
		policies:     policies,
		metrics:      NewMetrics(),
		processed:    make(map[string]processState),
		waitInterval: 5 * time.Second,
		now:          time.Now,
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
	if err := p.policies.Validate(asset, amount, p.now()); err != nil {
		p.mu.Unlock()
		switch {
		case errors.Is(err, ErrDailyCapExceeded):
			p.metrics.RecordError(asset, "daily_cap")
		case errors.Is(err, ErrSoftBalanceExceeded):
			p.metrics.RecordError(asset, "inventory")
		default:
			p.metrics.RecordError(asset, "policy")
		}
		return err
	}
	p.processed[intentID] = processState{inFlight: true, updatedAt: p.now()}
	p.mu.Unlock()

	start := p.now()
	if p.wallet == nil {
		p.finishFailure(intentID)
		return fmt.Errorf("payoutd: wallet not configured")
	}
	txHash, err := p.wallet.Transfer(ctx, asset, strings.TrimSpace(req.Destination), amount)
	if err != nil {
		p.finishFailure(intentID)
		p.metrics.RecordError(asset, "transfer")
		return err
	}
	confirmations := p.policies.Confirmations(asset)
	interval := p.waitInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if err := p.wallet.WaitForConfirmations(ctx, txHash, confirmations, interval); err != nil {
		p.finishFailure(intentID)
		p.metrics.RecordError(asset, "confirmations")
		return err
	}
	if p.attestor == nil {
		p.finishFailure(intentID)
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
	if err := p.attestor.SubmitReceipt(ctx, receipt); err != nil {
		p.finishFailure(intentID)
		p.metrics.RecordError(asset, "attest")
		return err
	}

	p.mu.Lock()
	p.policies.Record(asset, amount, p.now())
	remaining := p.policies.RemainingCap(asset, p.now())
	p.metrics.RecordCap(asset, remaining)
	p.processed[intentID] = processState{
		completed: true,
		txHash:    txHash,
		updatedAt: p.now(),
	}
	p.mu.Unlock()

	p.metrics.ObserveLatency(asset, p.now().Sub(start))
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
	return nil
}

// Pause halts new payout processing.
func (p *Processor) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
}

// Resume re-enables payout processing.
func (p *Processor) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
}

// UpdateInventory sets the available soft balance for the provided asset.
func (p *Processor) UpdateInventory(asset string, balance *big.Int) {
	if p.policies == nil {
		return
	}
	p.policies.SetInventory(asset, balance)
	p.metrics.RecordCap(strings.ToUpper(strings.TrimSpace(asset)), p.policies.RemainingCap(asset, p.now()))
}

// Status summarises processor state for administrative endpoints.
type Status struct {
	Paused       bool              `json:"paused"`
	Processed    int               `json:"processed"`
	Aborted      int               `json:"aborted"`
	InFlight     int               `json:"in_flight"`
	CapRemaining map[string]string `json:"cap_remaining"`
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
	return status
}

func cloneBigInt(in *big.Int) *big.Int {
	if in == nil {
		return nil
	}
	return new(big.Int).Set(in)
}
