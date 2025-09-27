package loyalty

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"nhbchain/core/events"
	nativecommon "nhbchain/native/common"
)

const (
	roleLoyaltyAdmin = "ROLE_LOYALTY_ADMIN"
	moduleName       = "loyalty"
)

type registryState interface {
	TokenExists(symbol string) bool
	HasRole(role string, addr []byte) bool
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVAppend(key []byte, value []byte) error
	KVGetList(key []byte, out interface{}) error
}

// Registry manages persistence and retrieval of loyalty programs.
type Registry struct {
	st      registryState
	emitter events.Emitter
	pauses  nativecommon.PauseView
}

// NewRegistry creates a registry backed by the provided state manager.
func NewRegistry(st registryState) *Registry {
	return &Registry{st: st, emitter: events.NoopEmitter{}}
}

// SetEmitter configures the event emitter used to broadcast registry updates.
// Passing nil resets the emitter to a no-op implementation.
func (r *Registry) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		r.emitter = events.NoopEmitter{}
		return
	}
	r.emitter = emitter
}

func (r *Registry) SetPauses(p nativecommon.PauseView) {
	if r == nil {
		return
	}
	r.pauses = p
}

// CreateProgram persists a new loyalty program. Only the owner or a caller with
// the ROLE_LOYALTY_ADMIN role may create a program on behalf of the owner.
func (r *Registry) CreateProgram(caller [20]byte, p *Program) error {
	if p == nil {
		return ErrNilProgram
	}
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
	sanitized, err := sanitizeProgram(p)
	if err != nil {
		return err
	}
	if caller != sanitized.Owner && !r.st.HasRole(roleLoyaltyAdmin, caller[:]) {
		return ErrUnauthorized
	}
	if !r.st.TokenExists(sanitized.TokenSymbol) {
		return fmt.Errorf("%w: %s", ErrTokenNotRegistered, sanitized.TokenSymbol)
	}
	if sanitized.AccrualBps > 100_000 {
		return fmt.Errorf("%w: %d", ErrAccrualBpsTooHigh, sanitized.AccrualBps)
	}
	exists, err := r.st.KVGet(programKey(sanitized.ID), new(Program))
	if err != nil {
		return err
	}
	if exists {
		return ErrProgramExists
	}
	if err := r.st.KVPut(programKey(sanitized.ID), sanitized); err != nil {
		return err
	}
	if err := r.st.KVAppend(merchantIdxKey(sanitized.Owner), sanitized.ID[:]); err != nil {
		return err
	}
	r.emit(events.LoyaltyProgramCreated{
		ID:          sanitized.ID,
		Owner:       sanitized.Owner,
		Pool:        sanitized.Pool,
		TokenSymbol: sanitized.TokenSymbol,
		AccrualBps:  sanitized.AccrualBps,
	})
	return nil
}

// UpdateProgram updates the mutable fields of an existing program. The owner or
// a caller with ROLE_LOYALTY_ADMIN must authorise the update.
func (r *Registry) UpdateProgram(caller [20]byte, p *Program) error {
	if p == nil {
		return ErrNilProgram
	}
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
	existing := new(Program)
	found, err := r.st.KVGet(programKey(p.ID), existing)
	if err != nil {
		return err
	}
	if !found {
		return ErrProgramNotFound
	}
	if caller != existing.Owner && !r.st.HasRole(roleLoyaltyAdmin, caller[:]) {
		return ErrUnauthorized
	}
	sanitized, err := sanitizeProgram(p)
	if err != nil {
		return err
	}
	if sanitized.ID != existing.ID {
		return ErrImmutableField
	}
	if sanitized.Owner != existing.Owner {
		return ErrImmutableField
	}
	if !r.st.TokenExists(sanitized.TokenSymbol) {
		return fmt.Errorf("%w: %s", ErrTokenNotRegistered, sanitized.TokenSymbol)
	}
	if sanitized.AccrualBps > 100_000 {
		return fmt.Errorf("%w: %d", ErrAccrualBpsTooHigh, sanitized.AccrualBps)
	}

	existing.AccrualBps = sanitized.AccrualBps
	existing.MinSpendWei = sanitized.MinSpendWei
	existing.CapPerTx = sanitized.CapPerTx
	existing.DailyCapUser = sanitized.DailyCapUser
	existing.StartTime = sanitized.StartTime
	existing.EndTime = sanitized.EndTime
	existing.Active = sanitized.Active
	existing.TokenSymbol = sanitized.TokenSymbol
	existing.Pool = sanitized.Pool

	if err := r.st.KVPut(programKey(existing.ID), existing); err != nil {
		return err
	}
	r.emit(events.LoyaltyProgramUpdated{
		ID:           existing.ID,
		Active:       existing.Active,
		AccrualBps:   existing.AccrualBps,
		MinSpendWei:  cloneBigInt(existing.MinSpendWei),
		CapPerTx:     cloneBigInt(existing.CapPerTx),
		DailyCapUser: cloneBigInt(existing.DailyCapUser),
		StartTime:    existing.StartTime,
		EndTime:      existing.EndTime,
		Pool:         existing.Pool,
		TokenSymbol:  existing.TokenSymbol,
	})
	return nil
}

// GetProgram retrieves a program by its identifier.
func (r *Registry) GetProgram(id ProgramID) (*Program, bool) {
	out := new(Program)
	ok, err := r.st.KVGet(programKey(id), out)
	if err != nil || !ok {
		return nil, false
	}
	return out, true
}

// ListProgramsByOwner returns all program IDs owned by the provided address in
// deterministic order.
func (r *Registry) ListProgramsByOwner(owner [20]byte) ([]ProgramID, error) {
	var raw [][]byte
	if err := r.st.KVGetList(merchantIdxKey(owner), &raw); err != nil {
		return nil, err
	}
	ids := make([]ProgramID, 0, len(raw))
	seen := make(map[[32]byte]struct{}, len(raw))
	for _, b := range raw {
		var id ProgramID
		copy(id[:], b)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids, nil
}

func (r *Registry) emit(event events.Event) {
	if r.emitter == nil {
		return
	}
	r.emitter.Emit(event)
}

func sanitizeProgram(p *Program) (*Program, error) {
	copyProgram := *p
	copyProgram.TokenSymbol = strings.ToUpper(strings.TrimSpace(copyProgram.TokenSymbol))
	if copyProgram.TokenSymbol == "" {
		return nil, fmt.Errorf("%w: token symbol required", ErrInvalidProgram)
	}
	if copyProgram.MinSpendWei != nil && copyProgram.MinSpendWei.Sign() < 0 {
		return nil, fmt.Errorf("%w: min spend must be non-negative", ErrInvalidProgram)
	}
	if copyProgram.CapPerTx != nil && copyProgram.CapPerTx.Sign() < 0 {
		return nil, fmt.Errorf("%w: cap per tx must be non-negative", ErrInvalidProgram)
	}
	if copyProgram.DailyCapUser != nil && copyProgram.DailyCapUser.Sign() < 0 {
		return nil, fmt.Errorf("%w: daily cap must be non-negative", ErrInvalidProgram)
	}
	if copyProgram.EndTime != 0 && copyProgram.EndTime < copyProgram.StartTime {
		return nil, fmt.Errorf("%w: end time before start time", ErrInvalidProgram)
	}
	copyProgram.MinSpendWei = cloneBigInt(copyProgram.MinSpendWei)
	copyProgram.CapPerTx = cloneBigInt(copyProgram.CapPerTx)
	copyProgram.DailyCapUser = cloneBigInt(copyProgram.DailyCapUser)
	return &copyProgram, nil
}

func cloneBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}
