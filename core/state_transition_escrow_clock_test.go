package core

import (
	"encoding/binary"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

// The escrow and trade engines persist CreatedAt/UpdatedAt (and evaluate
// deadlines) from the time source configureTradeEngine gives them. That has
// to be the block timestamp: two validators (or one validator and a later
// replay) executing the same transaction at different wall-clock instants
// must reach the same state root. These tests run the same transactions on
// independent processors whose wall clocks disagree.

const (
	escrowClockBlockTime = int64(1_800_000_000)
	escrowClockWallA     = int64(1_900_000_000)
	escrowClockWallB     = int64(1_900_000_777)
)

type escrowClockFixture struct {
	admin, payer, payee, arbitrator *crypto.PrivateKey
	treasury                        [20]byte
}

func newEscrowClockFixture(t *testing.T) *escrowClockFixture {
	t.Helper()
	fx := &escrowClockFixture{}
	for _, dst := range []**crypto.PrivateKey{&fx.admin, &fx.payer, &fx.payee, &fx.arbitrator} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		*dst = key
	}
	fx.treasury[0] = 0xAB
	return fx
}

func escrowClockAddr(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}

// newProcessor builds an identically seeded processor whose wall clock is
// fixed at wall and which is executing a block stamped escrowClockBlockTime.
func (fx *escrowClockFixture) newProcessor(t *testing.T, wall int64) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.nowFunc = func() time.Time { return time.Unix(wall, 0).UTC() }
	sp.SetEscrowFeeTreasury(fx.treasury)
	for _, key := range []*crypto.PrivateKey{fx.admin, fx.payer, fx.payee, fx.arbitrator} {
		writeAccount(t, sp, escrowClockAddr(key), &types.Account{BalanceNHB: big.NewInt(10_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}
	writeAccount(t, sp, fx.treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	manager := nhbstate.NewManager(sp.Trie)
	admin := escrowClockAddr(fx.admin)
	if err := manager.SetRole(RoleEscrowRealmAdmin, admin[:]); err != nil {
		t.Fatalf("grant realm admin role: %v", err)
	}
	sp.BeginBlock(1, time.Unix(escrowClockBlockTime, 0).UTC())
	return sp
}

func escrowClockTx(t *testing.T, key *crypto.PrivateKey, txType types.TxType, nonce uint64, data []byte) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}

func escrowClockCreateData(t *testing.T, payee [20]byte, deadline int64, nonce uint64) []byte {
	t.Helper()
	data, err := jsonMarshal(struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}{Payee: payee[:], Token: "NHB", Amount: big.NewInt(100), FeeBps: 100, Deadline: deadline, Nonce: nonce})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	return data
}

func escrowClockID(payer, payee [20]byte, nonce uint64) [32]byte {
	var meta [32]byte
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)
	return ethcrypto.Keccak256Hash(payer[:], payee[:], meta[:], nonceBytes[:])
}

// applyOnBoth applies the same signed transaction to both processors and
// requires the same outcome and the same pending state root.
func applyOnBoth(t *testing.T, step string, a, b *StateProcessor, tx *types.Transaction) {
	t.Helper()
	errA := a.ApplyTransaction(tx)
	errB := b.ApplyTransaction(tx)
	if (errA == nil) != (errB == nil) {
		t.Fatalf("%s: outcome diverged: a=%v b=%v", step, errA, errB)
	}
	if errA != nil {
		t.Fatalf("%s: apply failed: %v", step, errA)
	}
	if rootA, rootB := a.PendingRoot(), b.PendingRoot(); rootA != rootB {
		t.Fatalf("%s: state root depends on the wall clock: %s != %s", step, rootA.Hex(), rootB.Hex())
	}
}

func TestEscrowTransactionsAreWallClockIndependent(t *testing.T) {
	fx := newEscrowClockFixture(t)
	a := fx.newProcessor(t, escrowClockWallA)
	b := fx.newProcessor(t, escrowClockWallB)
	if a.PendingRoot() != b.PendingRoot() {
		t.Fatalf("processors are not identically seeded")
	}

	payer, payee := escrowClockAddr(fx.payer), escrowClockAddr(fx.payee)
	deadline := escrowClockBlockTime + 7200

	realmData := func(profile string) []byte {
		data, err := jsonMarshal(struct {
			ID              string   `json:"id"`
			Threshold       uint32   `json:"threshold"`
			Scheme          uint8    `json:"scheme"`
			Members         []string `json:"members"`
			Scope           uint8    `json:"scope"`
			ProviderProfile string   `json:"providerProfile"`
		}{
			ID:              "clock-realm",
			Threshold:       1,
			Scheme:          uint8(escrow.ArbitrationSchemeSingle),
			Members:         []string{fx.arbitrator.PubKey().Address().String()},
			Scope:           uint8(escrow.EscrowRealmScopePlatform),
			ProviderProfile: profile,
		})
		if err != nil {
			t.Fatalf("marshal realm payload: %v", err)
		}
		return data
	}
	applyOnBoth(t, "create realm", a, b, escrowClockTx(t, fx.admin, types.TxTypeEscrowCreateRealm, 0, realmData("core-team")))
	applyOnBoth(t, "update realm", a, b, escrowClockTx(t, fx.admin, types.TxTypeEscrowUpdateRealm, 1, realmData("core-team-v2")))

	// Escrow that is created, locked and disputed.
	id1 := escrowClockID(payer, payee, 1)
	applyOnBoth(t, "create escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 0, escrowClockCreateData(t, payee, deadline, 1)))
	applyOnBoth(t, "lock escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeLockEscrow, 1, id1[:]))
	applyOnBoth(t, "dispute escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeDisputeEscrow, 2, id1[:]))

	// Escrow that is created, locked and refunded before its deadline.
	id2 := escrowClockID(payer, payee, 2)
	applyOnBoth(t, "create second escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 3, escrowClockCreateData(t, payee, deadline, 2)))
	applyOnBoth(t, "lock second escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeLockEscrow, 4, id2[:]))
	applyOnBoth(t, "refund second escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeRefundEscrow, 5, id2[:]))

	// The stored timestamps are the block timestamp, not either wall clock.
	for name, sp := range map[string]*StateProcessor{"a": a, "b": b} {
		manager := nhbstate.NewManager(sp.Trie)
		esc, ok := manager.EscrowGet(id1)
		if !ok {
			t.Fatalf("%s: escrow not stored", name)
		}
		if esc.CreatedAt != escrowClockBlockTime {
			t.Fatalf("%s: escrow CreatedAt = %d, want block time %d", name, esc.CreatedAt, escrowClockBlockTime)
		}
		realm, ok, err := manager.EscrowRealmGet("clock-realm")
		if err != nil || !ok {
			t.Fatalf("%s: realm not stored: ok=%v err=%v", name, ok, err)
		}
		if realm.CreatedAt != escrowClockBlockTime || realm.UpdatedAt != escrowClockBlockTime {
			t.Fatalf("%s: realm times = %d/%d, want block time %d", name, realm.CreatedAt, realm.UpdatedAt, escrowClockBlockTime)
		}
	}
}

// The engine's deadline checks must also read the block timestamp.
func TestEscrowDeadlineChecksUseBlockTimestamp(t *testing.T) {
	fx := newEscrowClockFixture(t)
	// The wall clock is far BEFORE the deadline; the block time is after it.
	sp := fx.newProcessor(t, escrowClockBlockTime-1_000_000)
	payer, payee := escrowClockAddr(fx.payer), escrowClockAddr(fx.payee)
	deadline := escrowClockBlockTime + 100

	id := escrowClockID(payer, payee, 1)
	for step, tx := range []*types.Transaction{
		escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 0, escrowClockCreateData(t, payee, deadline, 1)),
		escrowClockTx(t, fx.payer, types.TxTypeLockEscrow, 1, id[:]),
	} {
		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("setup step %d: %v", step, err)
		}
	}

	sp.BeginBlock(2, time.Unix(deadline+1, 0).UTC())
	if err := sp.ApplyTransaction(escrowClockTx(t, fx.payer, types.TxTypeRefundEscrow, 2, id[:])); err == nil {
		t.Fatalf("refund after the block-time deadline must be rejected even though the wall clock is before it")
	}

	// A creation whose deadline is before the block time is rejected even
	// though the wall clock is far earlier.
	if err := sp.ApplyTransaction(escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 2, escrowClockCreateData(t, payee, deadline, 2))); err == nil {
		t.Fatalf("creation with a deadline before the block time must be rejected")
	}
}

func TestLegacyEscrowMigrationIsWallClockIndependent(t *testing.T) {
	fx := newEscrowClockFixture(t)
	a := fx.newProcessor(t, escrowClockWallA)
	b := fx.newProcessor(t, escrowClockWallB)

	payer, payee := escrowClockAddr(fx.payer), escrowClockAddr(fx.payee)
	var legacyID [32]byte
	legacyID[0] = 0x77
	legacy := &escrow.LegacyEscrow{
		ID:     legacyID[:],
		Buyer:  payee[:],
		Seller: payer[:],
		Amount: big.NewInt(250),
		Status: escrow.LegacyStatusOpen,
	}
	encoded, err := rlp.EncodeToBytes(legacy)
	if err != nil {
		t.Fatalf("encode legacy escrow: %v", err)
	}
	legacyKey := ethcrypto.Keccak256(append([]byte("escrow-"), legacyID[:]...))
	for _, sp := range []*StateProcessor{a, b} {
		if err := sp.Trie.Update(legacyKey, encoded); err != nil {
			t.Fatalf("seed legacy escrow: %v", err)
		}
	}

	// Disputing forces the legacy record through ensureEscrowReady.
	applyOnBoth(t, "dispute migrated escrow", a, b, escrowClockTx(t, fx.payer, types.TxTypeDisputeEscrow, 0, legacyID[:]))

	for name, sp := range map[string]*StateProcessor{"a": a, "b": b} {
		esc, ok := nhbstate.NewManager(sp.Trie).EscrowGet(legacyID)
		if !ok {
			t.Fatalf("%s: migrated escrow not stored", name)
		}
		if esc.CreatedAt != escrowClockBlockTime {
			t.Fatalf("%s: migrated CreatedAt = %d, want block time %d", name, esc.CreatedAt, escrowClockBlockTime)
		}
		if want := escrowClockBlockTime + 30*24*3600; esc.Deadline != want {
			t.Fatalf("%s: migrated Deadline = %d, want %d", name, esc.Deadline, want)
		}
	}
}

func TestTradeEngineIsWallClockIndependent(t *testing.T) {
	fx := newEscrowClockFixture(t)
	a := fx.newProcessor(t, escrowClockWallA)
	b := fx.newProcessor(t, escrowClockWallB)

	buyer, seller := escrowClockAddr(fx.payer), escrowClockAddr(fx.payee)
	var nonce [32]byte
	nonce[31] = 9
	deadline := escrowClockBlockTime + 3600

	for _, sp := range []*StateProcessor{a, b} {
		engine, _ := sp.configureTradeEngine()
		trade, err := engine.CreateTrade("offer-clock", buyer, seller, "NHB", big.NewInt(100), "ZNHB", big.NewInt(50), deadline, 0, nonce)
		if err != nil {
			t.Fatalf("create trade: %v", err)
		}
		if trade.CreatedAt != escrowClockBlockTime {
			t.Fatalf("trade CreatedAt = %d, want block time %d", trade.CreatedAt, escrowClockBlockTime)
		}
	}
	if rootA, rootB := a.PendingRoot(), b.PendingRoot(); rootA != rootB {
		t.Fatalf("trade creation state root depends on the wall clock: %s != %s", rootA.Hex(), rootB.Hex())
	}
}
