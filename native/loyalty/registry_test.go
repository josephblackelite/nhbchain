package loyalty_test

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/events"
	"nhbchain/core/state"
	loyalty "nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

type capturingEmitter struct {
	events []events.Event
}

func (c *capturingEmitter) Emit(e events.Event) {
	c.events = append(c.events, e)
}

const roleLoyaltyAdmin = "ROLE_LOYALTY_ADMIN"

func newTestRegistry(t *testing.T) (*loyalty.Registry, *state.Manager) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	manager := state.NewManager(tr)
	if err := manager.RegisterToken("ZNHB", "ZapNHB", 18); err != nil {
		t.Fatalf("register token: %v", err)
	}
	registry := loyalty.NewRegistry(manager)
	return registry, manager
}

func TestRegistryCreateAndListPrograms(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[19] = 0x11
	var pool [20]byte
	pool[18] = 0x22
	var id loyalty.ProgramID
	id[31] = 0xAA

	emitter := &capturingEmitter{}
	registry.SetEmitter(emitter)

	program := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		Pool:         pool,
		TokenSymbol:  "znhb",
		AccrualBps:   150,
		MinSpendWei:  big.NewInt(10),
		CapPerTx:     big.NewInt(5),
		DailyCapUser: big.NewInt(20),
		StartTime:    100,
		EndTime:      0,
		Active:       true,
	}
	if err := registry.CreateProgram(owner, program); err != nil {
		t.Fatalf("create program: %v", err)
	}

	stored, ok := registry.GetProgram(id)
	if !ok {
		t.Fatalf("expected program to exist")
	}
	if stored.TokenSymbol != "ZNHB" {
		t.Fatalf("expected token symbol uppercased, got %q", stored.TokenSymbol)
	}
	if stored.AccrualBps != program.AccrualBps {
		t.Fatalf("unexpected accrual bps: got %d", stored.AccrualBps)
	}

	ids, err := registry.ListProgramsByOwner(owner)
	if err != nil {
		t.Fatalf("list programs: %v", err)
	}
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("expected one program id, got %v", ids)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	if emitter.events[0].EventType() != events.TypeLoyaltyProgramCreated {
		t.Fatalf("unexpected event type %q", emitter.events[0].EventType())
	}
}

func TestRegistryCreateProgramUnauthorized(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x01
	var caller [20]byte
	caller[0] = 0x02
	var id loyalty.ProgramID
	id[0] = 0x01

	program := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		TokenSymbol:  "ZNHB",
		AccrualBps:   100,
		MinSpendWei:  big.NewInt(1),
		CapPerTx:     big.NewInt(1),
		DailyCapUser: big.NewInt(1),
		Active:       true,
	}
	err := registry.CreateProgram(caller, program)
	if !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestRegistryPauseProgramUnauthorized(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x01
	var outsider [20]byte
	outsider[0] = 0x02
	var id loyalty.ProgramID
	id[0] = 0x03

	program := &loyalty.Program{
		ID:          id,
		Owner:       owner,
		TokenSymbol: "ZNHB",
		AccrualBps:  100,
		Active:      true,
	}
	if err := registry.CreateProgram(owner, program); err != nil {
		t.Fatalf("create program: %v", err)
	}
	if err := registry.PauseProgram(outsider, id); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
}

func TestRegistryPauseAndResumeProgram(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x11
	var admin [20]byte
	admin[0] = 0x12
	var id loyalty.ProgramID
	id[0] = 0x13

	base := &loyalty.Program{
		ID:          id,
		Owner:       owner,
		TokenSymbol: "ZNHB",
		AccrualBps:  100,
		Active:      true,
	}
	if err := registry.CreateProgram(owner, base); err != nil {
		t.Fatalf("create program: %v", err)
	}
	emitter := &capturingEmitter{}
	registry.SetEmitter(emitter)

	if err := registry.PauseProgram(owner, id); err != nil {
		t.Fatalf("pause program: %v", err)
	}
	stored, ok := registry.GetProgram(id)
	if !ok || stored.Active {
		t.Fatalf("expected program to be inactive after pause")
	}
	if len(emitter.events) != 1 || emitter.events[0].EventType() != events.TypeLoyaltyProgramPaused {
		t.Fatalf("expected pause event, got %#v", emitter.events)
	}

	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	if err := registry.ResumeProgram(admin, id); err != nil {
		t.Fatalf("resume program: %v", err)
	}
	stored, ok = registry.GetProgram(id)
	if !ok || !stored.Active {
		t.Fatalf("expected program to be active after resume")
	}
	if len(emitter.events) != 2 || emitter.events[1].EventType() != events.TypeLoyaltyProgramResumed {
		t.Fatalf("expected resume event, got %#v", emitter.events)
	}
}

func TestRegistryUpdateProgramByOwner(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x01
	var pool [20]byte
	pool[0] = 0x02
	var id loyalty.ProgramID
	id[0] = 0xFF

	base := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		Pool:         pool,
		TokenSymbol:  "ZNHB",
		AccrualBps:   100,
		MinSpendWei:  big.NewInt(10),
		CapPerTx:     big.NewInt(5),
		DailyCapUser: big.NewInt(20),
		StartTime:    10,
		Active:       true,
	}
	if err := registry.CreateProgram(owner, base); err != nil {
		t.Fatalf("create program: %v", err)
	}

	update := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		Pool:         pool,
		TokenSymbol:  "ZNHB",
		AccrualBps:   450,
		MinSpendWei:  big.NewInt(15),
		CapPerTx:     big.NewInt(6),
		DailyCapUser: big.NewInt(25),
		StartTime:    20,
		EndTime:      1000,
		Active:       false,
	}
	emitter := &capturingEmitter{}
	registry.SetEmitter(emitter)
	if err := registry.UpdateProgram(owner, update); err != nil {
		t.Fatalf("update program: %v", err)
	}
	stored, ok := registry.GetProgram(id)
	if !ok {
		t.Fatalf("program missing after update")
	}
	if stored.AccrualBps != update.AccrualBps {
		t.Fatalf("accrual not updated: got %d", stored.AccrualBps)
	}
	if stored.StartTime != update.StartTime || stored.EndTime != update.EndTime {
		t.Fatalf("unexpected time window: got (%d,%d)", stored.StartTime, stored.EndTime)
	}
	if stored.Active != update.Active {
		t.Fatalf("active flag mismatch")
	}
	if len(emitter.events) != 1 || emitter.events[0].EventType() != events.TypeLoyaltyProgramUpdated {
		t.Fatalf("expected update event, got %v", emitter.events)
	}
}

func TestRegistryUpdateProgramByAdmin(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x03
	var admin [20]byte
	admin[0] = 0x04
	var id loyalty.ProgramID
	id[0] = 0x05

	base := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		TokenSymbol:  "ZNHB",
		AccrualBps:   100,
		MinSpendWei:  big.NewInt(10),
		CapPerTx:     big.NewInt(5),
		DailyCapUser: big.NewInt(20),
		Active:       true,
	}
	if err := registry.CreateProgram(owner, base); err != nil {
		t.Fatalf("create program: %v", err)
	}
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("set role: %v", err)
	}

	update := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		TokenSymbol:  "ZNHB",
		AccrualBps:   250,
		MinSpendWei:  big.NewInt(15),
		CapPerTx:     big.NewInt(30),
		DailyCapUser: big.NewInt(45),
		Active:       true,
	}
	if err := registry.UpdateProgram(admin, update); err != nil {
		t.Fatalf("admin update: %v", err)
	}
}

func TestRegistryRejectsImmutableChanges(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x07
	var other [20]byte
	other[0] = 0x08
	var id loyalty.ProgramID
	id[0] = 0x09

	base := &loyalty.Program{
		ID:           id,
		Owner:        owner,
		TokenSymbol:  "ZNHB",
		AccrualBps:   100,
		MinSpendWei:  big.NewInt(1),
		CapPerTx:     big.NewInt(1),
		DailyCapUser: big.NewInt(1),
	}
	if err := registry.CreateProgram(owner, base); err != nil {
		t.Fatalf("create program: %v", err)
	}

	update := &loyalty.Program{
		ID:           id,
		Owner:        other,
		TokenSymbol:  "ZNHB",
		AccrualBps:   100,
		MinSpendWei:  big.NewInt(1),
		CapPerTx:     big.NewInt(1),
		DailyCapUser: big.NewInt(1),
	}
	err := registry.UpdateProgram(owner, update)
	if !errors.Is(err, loyalty.ErrImmutableField) {
		t.Fatalf("expected immutable field error, got %v", err)
	}
}
