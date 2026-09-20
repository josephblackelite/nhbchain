package penalty

import (
	"errors"
	"fmt"
	"math/big"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/events"
	"nhbchain/core/types"
	statebank "nhbchain/state/bank"
	statepotso "nhbchain/state/potso"
)

type Engine struct {
	catalog *Catalog
	weights *statepotso.Ledger
	slasher statebank.Slasher
	records Records
}

// Records is the durable record of which offenses have already had their
// penalty applied, keyed by the offense (evidence.Record.Offense) and the
// offender. Anything that executes as part of a block must supply one backed by
// that block's own state (core/state.Manager satisfies it), so a provisional
// execution whose state is discarded leaves nothing behind and every node reaches
// the same answer for the same block. Without one the engine falls back to the
// weight ledger's own in-memory record, which belongs to a single process and is
// only suitable for tools and tests.
type Records interface {
	PotsoPenaltyApplied(offense [32]byte, offender [20]byte) (bool, error)
	PotsoPenaltyMarkApplied(offense [32]byte, offender [20]byte) error
}

type Context struct {
	BlockHeight        uint64
	MissedEpochs       uint64
	BaseWeightOverride *big.Int
}

type Result struct {
	Penalty      Penalty
	WeightUpdate *statepotso.WeightUpdate
	SlashApplied *big.Int
	Event        *types.Event
	Idempotent   bool
}

func NewEngine(catalog *Catalog, weights *statepotso.Ledger, slasher statebank.Slasher) *Engine {
	return &Engine{catalog: catalog, weights: weights, slasher: slasher}
}

// WithRecords makes the engine read and write its applied-penalty record
// through records instead of the weight ledger's in-memory one.
func (e *Engine) WithRecords(records Records) *Engine {
	e.records = records
	return e
}

func (e *Engine) penaltyApplied(offense [32]byte, offender [20]byte) (bool, error) {
	if e.records != nil {
		return e.records.PotsoPenaltyApplied(offense, offender)
	}
	return e.weights.WasPenaltyApplied(offense, offender), nil
}

func (e *Engine) markPenaltyApplied(offense [32]byte, offender [20]byte) error {
	if e.records != nil {
		return e.records.PotsoPenaltyMarkApplied(offense, offender)
	}
	e.weights.MarkPenaltyApplied(offense, offender)
	return nil
}

func (e *Engine) Apply(record *evidence.Record, ctx Context) (*Result, error) {
	if record == nil {
		return nil, errors.New("penalty: nil record")
	}
	if e.catalog == nil {
		return nil, errors.New("penalty: catalog unavailable")
	}
	if e.weights == nil {
		return nil, errors.New("penalty: weight ledger unavailable")
	}
	// The penalty is recorded against the offense, not against this report's own
	// hash: a second report of the same offense, written another way, must find
	// the penalty already applied rather than be penalised again.
	offense := record.Offense().Key
	applied, err := e.penaltyApplied(offense, record.Evidence.Offender)
	if err != nil {
		return nil, fmt.Errorf("penalty: load applied record: %w", err)
	}
	if applied {
		entry := e.weights.Entry(record.Evidence.Offender)
		evt := events.PotsoPenaltyApplied{
			Hash:       record.Hash,
			Type:       string(record.Evidence.Type),
			Offender:   record.Evidence.Offender,
			DecayBps:   0,
			SlashAmt:   big.NewInt(0),
			NewWeight:  entry.Value,
			Block:      ctx.BlockHeight,
			Idempotent: true,
		}
		zero := big.NewInt(0)
		return &Result{
			Event:      evt.Event(),
			Idempotent: true,
			Penalty:    Penalty{},
			WeightUpdate: &statepotso.WeightUpdate{
				Offender: record.Evidence.Offender,
				Previous: new(big.Int).Set(entry.Value),
				Current:  new(big.Int).Set(entry.Value),
				Applied:  zero,
			},
			SlashApplied: zero,
		}, nil
	}
	rule, ok := e.catalog.Rule(record.Evidence.Type)
	if !ok {
		return nil, fmt.Errorf("penalty: no rule for evidence type %s", record.Evidence.Type)
	}
	entry := e.weights.Entry(record.Evidence.Offender)
	base := entry.Base
	if ctx.BaseWeightOverride != nil {
		base = ctx.BaseWeightOverride
	}
	meta := Metadata{
		MissedEpochs:  ctx.MissedEpochs,
		BaseWeight:    base,
		CurrentWeight: entry.Value,
	}
	penalty, err := rule.Compute(meta)
	if err != nil {
		return nil, fmt.Errorf("penalty: compute: %w", err)
	}
	update, err := e.weights.ApplyDecay(record.Evidence.Offender, penalty.DecayAmount)
	if err != nil {
		return nil, fmt.Errorf("penalty: apply decay: %w", err)
	}
	var slashApplied *big.Int
	if penalty.SlashAmount != nil && penalty.SlashAmount.Sign() > 0 && e.slasher != nil {
		if err := e.slasher.Slash(record.Evidence.Offender, penalty.SlashAmount); err != nil {
			return nil, fmt.Errorf("penalty: slash: %w", err)
		}
		slashApplied = new(big.Int).Set(penalty.SlashAmount)
	} else {
		slashApplied = big.NewInt(0)
	}
	if err := e.markPenaltyApplied(offense, record.Evidence.Offender); err != nil {
		return nil, fmt.Errorf("penalty: record applied: %w", err)
	}
	evt := events.PotsoPenaltyApplied{
		Hash:       record.Hash,
		Type:       string(record.Evidence.Type),
		Offender:   record.Evidence.Offender,
		DecayBps:   penalty.DecayBps,
		SlashAmt:   slashApplied,
		NewWeight:  update.Current,
		Block:      ctx.BlockHeight,
		Idempotent: false,
	}
	return &Result{Penalty: penalty, WeightUpdate: update, SlashApplied: slashApplied, Event: evt.Event()}, nil
}
