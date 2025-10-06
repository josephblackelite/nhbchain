package pos

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

// lifecycleState abstracts the subset of state manager functionality required by
// the POS payment lifecycle engine.
type lifecycleState interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVDelete(key []byte) error
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
}

var (
	authorizationPrefix    = []byte("pos/auth/")
	authorizationNoncePref = []byte("pos/auth/nonce/")
)

// AuthorizationStatus captures the lifecycle state for a payment authorization.
type AuthorizationStatus uint8

const (
	// AuthorizationStatusPending marks an authorization that still holds a
	// locked balance and may be captured or voided.
	AuthorizationStatusPending AuthorizationStatus = iota
	// AuthorizationStatusCaptured indicates the authorization was captured
	// and the locked balance transferred to the merchant.
	AuthorizationStatusCaptured
	// AuthorizationStatusVoided marks authorizations that were manually
	// voided before capture, returning the locked balance to the payer.
	AuthorizationStatusVoided
	// AuthorizationStatusExpired marks authorizations that expired before
	// capture. The locked balance has been returned to the payer.
	AuthorizationStatusExpired
)

// Authorization represents a locked POS payment authorization tracked on-chain.
type Authorization struct {
	ID             [32]byte
	Payer          [20]byte
	Merchant       [20]byte
	Amount         *big.Int
	CapturedAmount *big.Int
	RefundedAmount *big.Int
	Expiry         uint64
	IntentRef      []byte
	Status         AuthorizationStatus
	CreatedAt      uint64
	UpdatedAt      uint64
	VoidReason     string
}

// Clone returns a deep copy of the authorization to avoid mutating shared
// pointers.
func (a *Authorization) Clone() *Authorization {
	if a == nil {
		return nil
	}
	clone := *a
	if a.Amount != nil {
		clone.Amount = new(big.Int).Set(a.Amount)
	}
	if a.CapturedAmount != nil {
		clone.CapturedAmount = new(big.Int).Set(a.CapturedAmount)
	}
	if a.RefundedAmount != nil {
		clone.RefundedAmount = new(big.Int).Set(a.RefundedAmount)
	}
	clone.IntentRef = append([]byte(nil), a.IntentRef...)
	return &clone
}

// Lifecycle orchestrates the authorization/capture/void flow for card-like POS
// transactions.
type Lifecycle struct {
	state   lifecycleState
	emitter events.Emitter
	nowFn   func() time.Time
}

// NewLifecycle constructs a lifecycle engine bound to the provided state
// backend.
func NewLifecycle(state lifecycleState) *Lifecycle {
	return &Lifecycle{
		state:   state,
		emitter: events.NoopEmitter{},
		nowFn:   func() time.Time { return time.Now().UTC() },
	}
}

// SetEmitter overrides the event emitter used by the lifecycle engine.
func (l *Lifecycle) SetEmitter(emitter events.Emitter) {
	if l == nil {
		return
	}
	if emitter == nil {
		l.emitter = events.NoopEmitter{}
		return
	}
	l.emitter = emitter
}

// SetNowFunc overrides the time source used for expiry checks. Passing nil
// restores the default UTC clock.
func (l *Lifecycle) SetNowFunc(now func() time.Time) {
	if l == nil {
		return
	}
	if now == nil {
		l.nowFn = func() time.Time { return time.Now().UTC() }
		return
	}
	l.nowFn = now
}

var (
	errLifecycleUninitialised    = errors.New("pos: lifecycle not initialised")
	errAuthorizationNotFound     = errors.New("pos: authorization not found")
	errAuthorizationConsumed     = errors.New("pos: authorization already captured")
	errAuthorizationVoided       = errors.New("pos: authorization voided")
	errAuthorizationExpired      = errors.New("pos: authorization expired")
	errAuthorizationInvalidAddr  = errors.New("pos: address required")
	errAuthorizationInvalidAmt   = errors.New("pos: amount must be positive")
	errAuthorizationInsufficient = errors.New("pos: insufficient balance")
)

// Authorize locks the supplied ZapNHB amount on the payer account and records a
// payment authorization that can later be captured or voided.
func (l *Lifecycle) Authorize(payer, merchant [20]byte, amount *big.Int, expiry uint64, intentRef []byte) (*Authorization, error) {
	if l == nil || l.state == nil {
		return nil, errLifecycleUninitialised
	}
	if isZeroAddress(payer[:]) {
		return nil, errAuthorizationInvalidAddr
	}
	if isZeroAddress(merchant[:]) {
		return nil, errAuthorizationInvalidAddr
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errAuthorizationInvalidAmt
	}
	now := l.nowFn().UTC()
	if expiry == 0 || uint64(now.Unix()) >= expiry {
		return nil, errAuthorizationExpired
	}
	payerAcc, err := l.state.GetAccount(payer[:])
	if err != nil {
		return nil, err
	}
	payerAcc = cloneAccount(payerAcc)
	if payerAcc.BalanceZNHB.Cmp(amount) < 0 {
		return nil, errAuthorizationInsufficient
	}
	payerAcc.BalanceZNHB = new(big.Int).Sub(payerAcc.BalanceZNHB, amount)
	payerAcc.LockedZNHB = new(big.Int).Add(payerAcc.LockedZNHB, amount)
	if err := l.state.PutAccount(payer[:], payerAcc); err != nil {
		return nil, err
	}
	authID, nonce, err := l.nextAuthorizationID(payer)
	if err != nil {
		// restore balance changes before returning
		payerAcc.BalanceZNHB = new(big.Int).Add(payerAcc.BalanceZNHB, amount)
		payerAcc.LockedZNHB = new(big.Int).Sub(payerAcc.LockedZNHB, amount)
		_ = l.state.PutAccount(payer[:], payerAcc)
		return nil, err
	}
	rollback := func() {
		l.revertAuthorizationNonce(payer, nonce)
		payerAcc.BalanceZNHB = new(big.Int).Add(payerAcc.BalanceZNHB, amount)
		payerAcc.LockedZNHB = new(big.Int).Sub(payerAcc.LockedZNHB, amount)
		_ = l.state.PutAccount(payer[:], payerAcc)
	}
	record := &Authorization{
		ID:             authID,
		Payer:          payer,
		Merchant:       merchant,
		Amount:         new(big.Int).Set(amount),
		CapturedAmount: big.NewInt(0),
		RefundedAmount: big.NewInt(0),
		Expiry:         expiry,
		IntentRef:      append([]byte(nil), intentRef...),
		Status:         AuthorizationStatusPending,
		CreatedAt:      uint64(now.Unix()),
		UpdatedAt:      uint64(now.Unix()),
	}
	if err := l.persistAuthorization(record); err != nil {
		rollback()
		return nil, err
	}
	l.emitter.Emit(events.PaymentAuthorized{
		AuthorizationID: authID,
		Payer:           payer,
		Merchant:        merchant,
		Amount:          new(big.Int).Set(amount),
		Expiry:          expiry,
		IntentRef:       append([]byte(nil), intentRef...),
	})
	return record.Clone(), nil
}

// Capture transfers the locked funds to the merchant, optionally releasing any
// remaining authorization amount back to the payer when capturing less than the
// authorized total.
func (l *Lifecycle) Capture(id [32]byte, amount *big.Int) (*Authorization, error) {
	if l == nil || l.state == nil {
		return nil, errLifecycleUninitialised
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errAuthorizationInvalidAmt
	}
	auth, err := l.loadAuthorization(id)
	if err != nil {
		return nil, err
	}
	if auth.Status == AuthorizationStatusCaptured {
		return nil, errAuthorizationConsumed
	}
	if auth.Status == AuthorizationStatusVoided {
		return nil, errAuthorizationVoided
	}
	if auth.Status == AuthorizationStatusExpired {
		return nil, errAuthorizationExpired
	}
	now := l.nowFn().UTC()
	if uint64(now.Unix()) >= auth.Expiry {
		updated, err := l.autoVoid(auth, AuthorizationStatusExpired, "expired")
		if err != nil {
			return nil, err
		}
		return updated, errAuthorizationExpired
	}
	if amount.Cmp(auth.Amount) > 0 {
		return nil, fmt.Errorf("pos: capture exceeds authorization")
	}
	payerAcc, err := l.state.GetAccount(auth.Payer[:])
	if err != nil {
		return nil, err
	}
	merchantAcc, err := l.state.GetAccount(auth.Merchant[:])
	if err != nil {
		return nil, err
	}
	payerAcc = cloneAccount(payerAcc)
	merchantAcc = cloneAccount(merchantAcc)
	if payerAcc.LockedZNHB.Cmp(auth.Amount) < 0 {
		return nil, fmt.Errorf("pos: locked balance inconsistent")
	}
	refund := new(big.Int).Sub(auth.Amount, amount)
	payerAcc.LockedZNHB = new(big.Int).Sub(payerAcc.LockedZNHB, auth.Amount)
	if refund.Sign() > 0 {
		payerAcc.BalanceZNHB = new(big.Int).Add(payerAcc.BalanceZNHB, refund)
	}
	merchantAcc.BalanceZNHB = new(big.Int).Add(merchantAcc.BalanceZNHB, amount)
	if err := l.state.PutAccount(auth.Payer[:], payerAcc); err != nil {
		return nil, err
	}
	if err := l.state.PutAccount(auth.Merchant[:], merchantAcc); err != nil {
		return nil, err
	}
	auth.Status = AuthorizationStatusCaptured
	auth.CapturedAmount = new(big.Int).Set(amount)
	auth.RefundedAmount = new(big.Int).Set(refund)
	auth.UpdatedAt = uint64(now.Unix())
	auth.VoidReason = ""
	if err := l.persistAuthorization(auth); err != nil {
		// best-effort rollback; balances restored to previous state
		payerAcc.LockedZNHB = new(big.Int).Add(payerAcc.LockedZNHB, auth.Amount)
		if refund.Sign() > 0 {
			payerAcc.BalanceZNHB = new(big.Int).Sub(payerAcc.BalanceZNHB, refund)
		}
		_ = l.state.PutAccount(auth.Payer[:], payerAcc)
		merchantAcc.BalanceZNHB = new(big.Int).Sub(merchantAcc.BalanceZNHB, amount)
		_ = l.state.PutAccount(auth.Merchant[:], merchantAcc)
		return nil, err
	}
	l.emitter.Emit(events.PaymentCaptured{
		AuthorizationID: auth.ID,
		Payer:           auth.Payer,
		Merchant:        auth.Merchant,
		CapturedAmount:  new(big.Int).Set(amount),
		RefundedAmount:  new(big.Int).Set(refund),
	})
	return auth.Clone(), nil
}

// Void releases the locked funds back to the payer. The reason string is
// recorded for analytics and defaults to "manual" when empty.
func (l *Lifecycle) Void(id [32]byte, reason string) (*Authorization, error) {
	if l == nil || l.state == nil {
		return nil, errLifecycleUninitialised
	}
	auth, err := l.loadAuthorization(id)
	if err != nil {
		return nil, err
	}
	if auth.Status == AuthorizationStatusCaptured {
		return nil, errAuthorizationConsumed
	}
	if auth.Status == AuthorizationStatusVoided {
		return auth.Clone(), nil
	}
	if auth.Status == AuthorizationStatusExpired {
		return auth.Clone(), nil
	}
	status := AuthorizationStatusVoided
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	updated, err := l.autoVoid(auth, status, reason)
	if err != nil {
		return nil, err
	}
	return updated.Clone(), nil
}

// autoVoid releases the locked balance and persists the voided authorization
// with the provided status and reason.
func (l *Lifecycle) autoVoid(auth *Authorization, status AuthorizationStatus, reason string) (*Authorization, error) {
	if auth == nil {
		return nil, errAuthorizationNotFound
	}
	payerAcc, err := l.state.GetAccount(auth.Payer[:])
	if err != nil {
		return nil, err
	}
	payerAcc = cloneAccount(payerAcc)
	if payerAcc.LockedZNHB.Cmp(auth.Amount) < 0 {
		return nil, fmt.Errorf("pos: locked balance inconsistent")
	}
	payerAcc.LockedZNHB = new(big.Int).Sub(payerAcc.LockedZNHB, auth.Amount)
	payerAcc.BalanceZNHB = new(big.Int).Add(payerAcc.BalanceZNHB, auth.Amount)
	if err := l.state.PutAccount(auth.Payer[:], payerAcc); err != nil {
		return nil, err
	}
	now := l.nowFn().UTC()
	auth.Status = status
	auth.CapturedAmount = big.NewInt(0)
	auth.RefundedAmount = new(big.Int).Set(auth.Amount)
	auth.UpdatedAt = uint64(now.Unix())
	auth.VoidReason = strings.TrimSpace(reason)
	if err := l.persistAuthorization(auth); err != nil {
		payerAcc.LockedZNHB = new(big.Int).Add(payerAcc.LockedZNHB, auth.Amount)
		payerAcc.BalanceZNHB = new(big.Int).Sub(payerAcc.BalanceZNHB, auth.Amount)
		_ = l.state.PutAccount(auth.Payer[:], payerAcc)
		return nil, err
	}
	l.emitter.Emit(events.PaymentVoided{
		AuthorizationID: auth.ID,
		Payer:           auth.Payer,
		Merchant:        auth.Merchant,
		RefundedAmount:  new(big.Int).Set(auth.Amount),
		Reason:          auth.VoidReason,
		Expired:         status == AuthorizationStatusExpired,
	})
	return auth, nil
}

func (l *Lifecycle) loadAuthorization(id [32]byte) (*Authorization, error) {
	if l == nil || l.state == nil {
		return nil, errLifecycleUninitialised
	}
	var stored storedAuthorization
	ok, err := l.state.KVGet(authorizationKey(id), &stored)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errAuthorizationNotFound
	}
	record := stored.toAuthorization()
	return record, nil
}

func (l *Lifecycle) persistAuthorization(auth *Authorization) error {
	if l == nil || l.state == nil {
		return errLifecycleUninitialised
	}
	stored := newStoredAuthorization(auth)
	return l.state.KVPut(authorizationKey(auth.ID), stored)
}

func (l *Lifecycle) nextAuthorizationID(payer [20]byte) ([32]byte, uint64, error) {
	var counter storedAuthorizationNonce
	key := authorizationNonceKey(payer)
	ok, err := l.state.KVGet(key, &counter)
	if err != nil {
		return [32]byte{}, 0, err
	}
	nonce := counter.Counter
	if !ok {
		nonce = 0
	}
	if nonce == math.MaxUint64 {
		return [32]byte{}, 0, fmt.Errorf("pos: authorization nonce overflow")
	}
	buf := make([]byte, len(payer)+8)
	copy(buf, payer[:])
	binary.BigEndian.PutUint64(buf[len(payer):], nonce)
	hash := ethcrypto.Keccak256(buf)
	var id [32]byte
	copy(id[:], hash)
	counter.Counter = nonce + 1
	if err := l.state.KVPut(key, counter); err != nil {
		return [32]byte{}, 0, err
	}
	return id, nonce, nil
}

func (l *Lifecycle) revertAuthorizationNonce(payer [20]byte, nonce uint64) {
	key := authorizationNonceKey(payer)
	_ = l.state.KVPut(key, storedAuthorizationNonce{Counter: nonce})
}

func authorizationKey(id [32]byte) []byte {
	return append([]byte(nil), append(authorizationPrefix, fmt.Sprintf("%x", id[:])...)...)
}

func authorizationNonceKey(payer [20]byte) []byte {
	return append([]byte(nil), append(authorizationNoncePref, fmt.Sprintf("%x", payer[:])...)...)
}

func isZeroAddress(addr []byte) bool {
	for _, b := range addr {
		if b != 0 {
			return false
		}
	}
	return true
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	}
	cloned := *acc
	if acc.BalanceNHB != nil {
		cloned.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	} else {
		cloned.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB != nil {
		cloned.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	} else {
		cloned.BalanceZNHB = big.NewInt(0)
	}
	if acc.LockedZNHB != nil {
		cloned.LockedZNHB = new(big.Int).Set(acc.LockedZNHB)
	} else {
		cloned.LockedZNHB = big.NewInt(0)
	}
	return &cloned
}

type storedAuthorization struct {
	ID             [32]byte
	Payer          [20]byte
	Merchant       [20]byte
	Amount         *big.Int
	CapturedAmount *big.Int
	RefundedAmount *big.Int
	Expiry         uint64
	IntentRef      []byte
	Status         uint8
	CreatedAt      uint64
	UpdatedAt      uint64
	VoidReason     string
}

type storedAuthorizationNonce struct {
	Counter uint64
}

func newStoredAuthorization(a *Authorization) *storedAuthorization {
	if a == nil {
		return nil
	}
	stored := &storedAuthorization{
		ID:         a.ID,
		Payer:      a.Payer,
		Merchant:   a.Merchant,
		Expiry:     a.Expiry,
		IntentRef:  append([]byte(nil), a.IntentRef...),
		Status:     uint8(a.Status),
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
		VoidReason: strings.TrimSpace(a.VoidReason),
	}
	if a.Amount != nil {
		stored.Amount = new(big.Int).Set(a.Amount)
	}
	if a.CapturedAmount != nil {
		stored.CapturedAmount = new(big.Int).Set(a.CapturedAmount)
	}
	if a.RefundedAmount != nil {
		stored.RefundedAmount = new(big.Int).Set(a.RefundedAmount)
	}
	return stored
}

func (s *storedAuthorization) toAuthorization() *Authorization {
	if s == nil {
		return nil
	}
	record := &Authorization{
		ID:         s.ID,
		Payer:      s.Payer,
		Merchant:   s.Merchant,
		Amount:     big.NewInt(0),
		Expiry:     s.Expiry,
		IntentRef:  append([]byte(nil), s.IntentRef...),
		Status:     AuthorizationStatus(s.Status),
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
		VoidReason: strings.TrimSpace(s.VoidReason),
	}
	if s.Amount != nil {
		record.Amount = new(big.Int).Set(s.Amount)
	}
	if s.CapturedAmount != nil {
		record.CapturedAmount = new(big.Int).Set(s.CapturedAmount)
	} else {
		record.CapturedAmount = big.NewInt(0)
	}
	if s.RefundedAmount != nil {
		record.RefundedAmount = new(big.Int).Set(s.RefundedAmount)
	} else {
		record.RefundedAmount = big.NewInt(0)
	}
	return record
}
