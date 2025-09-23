package escrow

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

var (
	errNilState       = errors.New("escrow engine: state not configured")
	errNilTreasury    = errors.New("escrow engine: fee treasury not configured")
	errEscrowNotFound = errors.New("escrow engine: escrow not found")
)

type engineState interface {
	EscrowPut(*Escrow) error
	EscrowGet(id [32]byte) (*Escrow, bool)
	EscrowCredit(id [32]byte, token string, amt *big.Int) error
	EscrowDebit(id [32]byte, token string, amt *big.Int) error
	EscrowVaultAddress(token string) ([20]byte, error)
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
}

type escrowEvent struct {
	evt *types.Event
}

func (e escrowEvent) EventType() string {
	if e.evt == nil {
		return ""
	}
	return e.evt.Type
}

func (e escrowEvent) Event() *types.Event { return e.evt }

// Engine wires the escrow business logic with external state and event
// emitters. The full transition logic will arrive in CODEx 1.2; for now the
// engine provides deterministic event emission helpers.
type Engine struct {
	state       engineState
	emitter     events.Emitter
	feeTreasury [20]byte
	nowFn       func() int64
}

// NewEngine creates an escrow engine with a no-op emitter. Callers can override
// the emitter via SetEmitter.
func NewEngine() *Engine {
	return &Engine{
		emitter: events.NoopEmitter{},
		nowFn:   func() int64 { return time.Now().Unix() },
	}
}

// SetState configures the state backend used by the engine.
func (e *Engine) SetState(state engineState) { e.state = state }

// SetFeeTreasury configures the address that should receive escrow fees.
func (e *Engine) SetFeeTreasury(addr [20]byte) { e.feeTreasury = addr }

// SetNowFunc overrides the time source used by the engine. Primarily intended
// for tests to provide deterministic timestamps.
func (e *Engine) SetNowFunc(now func() int64) {
	if now == nil {
		e.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	e.nowFn = now
}

// SetEmitter configures the event emitter used by the engine. Passing nil resets
// the emitter to a no-op implementation.
func (e *Engine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

func (e *Engine) emit(event *types.Event) {
	if e == nil || e.emitter == nil || event == nil {
		return
	}
	e.emitter.Emit(escrowEvent{evt: event})
}

func (e *Engine) now() int64 {
	if e == nil || e.nowFn == nil {
		return time.Now().Unix()
	}
	return e.nowFn()
}

func cloneBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func ensureAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	if acc.Stake == nil {
		acc.Stake = big.NewInt(0)
	}
	return acc
}

func (e *Engine) loadEscrow(id [32]byte) (*Escrow, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	escrow, ok := e.state.EscrowGet(id)
	if !ok {
		return nil, errEscrowNotFound
	}
	return escrow, nil
}

func (e *Engine) storeEscrow(esc *Escrow) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	return e.state.EscrowPut(esc)
}

func (e *Engine) transferToken(from, to [20]byte, token string, amount *big.Int) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	amt := cloneBigInt(amount)
	if amt.Sign() == 0 {
		return nil
	}
	normalized, err := NormalizeToken(token)
	if err != nil {
		return err
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative transfer amount")
	}
	fromAcc, err := e.state.GetAccount(from[:])
	if err != nil {
		return err
	}
	toAcc, err := e.state.GetAccount(to[:])
	if err != nil {
		return err
	}
	fromAcc = ensureAccount(fromAcc)
	toAcc = ensureAccount(toAcc)
	switch normalized {
	case "NHB":
		if fromAcc.BalanceNHB.Cmp(amt) < 0 {
			return fmt.Errorf("escrow: insufficient balance")
		}
		fromAcc.BalanceNHB = new(big.Int).Sub(fromAcc.BalanceNHB, amt)
		toAcc.BalanceNHB = new(big.Int).Add(toAcc.BalanceNHB, amt)
	case "ZNHB":
		if fromAcc.BalanceZNHB.Cmp(amt) < 0 {
			return fmt.Errorf("escrow: insufficient balance")
		}
		fromAcc.BalanceZNHB = new(big.Int).Sub(fromAcc.BalanceZNHB, amt)
		toAcc.BalanceZNHB = new(big.Int).Add(toAcc.BalanceZNHB, amt)
	default:
		return fmt.Errorf("escrow: unsupported token %s", token)
	}
	if err := e.state.PutAccount(from[:], fromAcc); err != nil {
		return err
	}
	if err := e.state.PutAccount(to[:], toAcc); err != nil {
		return err
	}
	return nil
}

func (e *Engine) ensureTreasuryConfigured() error {
	if e == nil {
		return errNilTreasury
	}
	if e.feeTreasury == ([20]byte{}) {
		return errNilTreasury
	}
	return nil
}

// Create initialises and persists a new escrow definition.
func (e *Engine) Create(payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, mediatorOpt *[20]byte, metaHash [32]byte) (*Escrow, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	normalizedToken, err := NormalizeToken(token)
	if err != nil {
		return nil, err
	}
	amt := cloneBigInt(amount)
	if amt.Sign() <= 0 {
		return nil, fmt.Errorf("escrow: amount must be positive")
	}
	if feeBps > 10_000 {
		return nil, fmt.Errorf("escrow: fee bps out of range")
	}
	now := e.now()
	if deadline < now {
		return nil, fmt.Errorf("escrow: deadline before creation time")
	}
	mediator := [20]byte{}
	if mediatorOpt != nil {
		mediator = *mediatorOpt
	}
	id := ethcrypto.Keccak256Hash(payer[:], payee[:], metaHash[:])
	existing, ok := e.state.EscrowGet(id)
	if ok {
		// Ensure idempotent behaviour: definitions must match
		if existing.Payer != payer || existing.Payee != payee || existing.Token != normalizedToken || existing.Amount.Cmp(amt) != 0 || existing.FeeBps != feeBps || existing.Deadline != deadline || existing.MetaHash != metaHash || existing.Mediator != mediator {
			return nil, fmt.Errorf("escrow: identifier already exists with different definition")
		}
		return existing, nil
	}
	esc := &Escrow{
		ID:        id,
		Payer:     payer,
		Payee:     payee,
		Mediator:  mediator,
		Token:     normalizedToken,
		Amount:    amt,
		FeeBps:    feeBps,
		Deadline:  deadline,
		CreatedAt: now,
		MetaHash:  metaHash,
		Status:    EscrowInit,
	}
	if err := e.storeEscrow(esc); err != nil {
		return nil, err
	}
	e.emit(NewCreatedEvent(esc))
	return esc.Clone(), nil
}

// Fund moves the escrow amount from the payer to the module vault and marks the
// escrow as funded. The operation is idempotent.
func (e *Engine) Fund(id [32]byte, from [20]byte) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowFunded {
		return nil
	}
	if esc.Status != EscrowInit {
		return fmt.Errorf("escrow: cannot fund in status %d", esc.Status)
	}
	if esc.Payer != from {
		return fmt.Errorf("escrow: unauthorized fund caller")
	}
	if esc.Amount == nil || esc.Amount.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	if err := e.transferToken(esc.Payer, vault, esc.Token, esc.Amount); err != nil {
		return err
	}
	if err := e.state.EscrowCredit(id, esc.Token, esc.Amount); err != nil {
		return err
	}
	esc.Status = EscrowFunded
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewFundedEvent(esc))
	return nil
}

// Release settles the escrow in favour of the payee, distributing any fees to
// the configured treasury. The operation is idempotent.
func (e *Engine) Release(id [32]byte, caller [20]byte) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowReleased {
		return nil
	}
	if esc.Status != EscrowFunded && esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot release in status %d", esc.Status)
	}
	if caller != esc.Payee {
		if esc.Mediator == ([20]byte{}) || caller != esc.Mediator {
			return fmt.Errorf("escrow: unauthorized release caller")
		}
	}
	if esc.Status == EscrowDisputed && caller != esc.Mediator {
		return fmt.Errorf("escrow: dispute requires mediator release")
	}
	if err := e.ensureTreasuryConfigured(); err != nil {
		return err
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	total := cloneBigInt(esc.Amount)
	if total.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	fee := new(big.Int).Mul(total, new(big.Int).SetUint64(uint64(esc.FeeBps)))
	fee.Div(fee, big.NewInt(10_000))
	payout := new(big.Int).Sub(total, fee)
	if payout.Sign() > 0 {
		if err := e.transferToken(vault, esc.Payee, esc.Token, payout); err != nil {
			return err
		}
	}
	if fee.Sign() > 0 {
		if err := e.transferToken(vault, e.feeTreasury, esc.Token, fee); err != nil {
			return err
		}
	}
	if err := e.state.EscrowDebit(id, esc.Token, total); err != nil {
		return err
	}
	esc.Status = EscrowReleased
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewReleasedEvent(esc))
	return nil
}

// Refund returns escrowed funds to the payer when invoked by the payer before
// the deadline. The operation is idempotent.
func (e *Engine) Refund(id [32]byte, caller [20]byte) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowRefunded {
		return nil
	}
	if esc.Status != EscrowFunded {
		return fmt.Errorf("escrow: cannot refund in status %d", esc.Status)
	}
	if caller != esc.Payer {
		return fmt.Errorf("escrow: unauthorized refund caller")
	}
	if e.now() >= esc.Deadline {
		return fmt.Errorf("escrow: refund deadline passed")
	}
	return e.refundEscrow(esc, esc.Payer, EscrowRefunded, NewRefundedEvent)
}

// Expire refunds the escrow to the payer once the deadline has elapsed. Anyone
// may invoke the expiry transition. The operation is idempotent.
func (e *Engine) Expire(id [32]byte, now int64) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowExpired {
		return nil
	}
	if now < esc.Deadline {
		return fmt.Errorf("escrow: deadline not reached")
	}
	if esc.Status != EscrowFunded {
		return nil
	}
	return e.refundEscrow(esc, esc.Payer, EscrowExpired, NewExpiredEvent)
}

// Dispute flags the escrow as disputed. Only the payer or payee may invoke the
// transition. The operation is idempotent.
func (e *Engine) Dispute(id [32]byte, caller [20]byte) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowDisputed {
		return nil
	}
	if esc.Status != EscrowFunded {
		return fmt.Errorf("escrow: cannot dispute in status %d", esc.Status)
	}
	if caller != esc.Payer && caller != esc.Payee {
		return fmt.Errorf("escrow: unauthorized dispute caller")
	}
	esc.Status = EscrowDisputed
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewDisputedEvent(esc))
	return nil
}

// Resolve settles a disputed escrow according to the mediator-determined
// outcome. Valid outcomes are "release" and "refund".
func (e *Engine) Resolve(id [32]byte, caller [20]byte, outcome string) error {
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowReleased || esc.Status == EscrowRefunded || esc.Status == EscrowExpired {
		return nil
	}
	if esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot resolve in status %d", esc.Status)
	}
	if esc.Mediator == ([20]byte{}) || caller != esc.Mediator {
		return fmt.Errorf("escrow: unauthorized resolver")
	}
	normalized := strings.ToLower(strings.TrimSpace(outcome))
	switch normalized {
	case "release":
		if err := e.Release(id, caller); err != nil {
			return err
		}
	case "refund":
		if err := e.refundEscrow(esc, esc.Payer, EscrowRefunded, NewRefundedEvent); err != nil {
			return err
		}
	default:
		return fmt.Errorf("escrow: invalid resolution outcome %s", outcome)
	}
	esc, err = e.loadEscrow(id)
	if err != nil {
		return err
	}
	e.emit(NewResolvedEvent(esc))
	return nil
}

func (e *Engine) refundEscrow(esc *Escrow, recipient [20]byte, status EscrowStatus, eventFn func(*Escrow) *types.Event) error {
	if esc == nil {
		return fmt.Errorf("escrow: nil escrow")
	}
	if esc.Status == status {
		return nil
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	amount := cloneBigInt(esc.Amount)
	if amount.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	if err := e.transferToken(vault, recipient, esc.Token, amount); err != nil {
		return err
	}
	if err := e.state.EscrowDebit(esc.ID, esc.Token, amount); err != nil {
		return err
	}
	esc.Status = status
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(eventFn(esc))
	return nil
}
