package core

import (
	"encoding/binary"
	"errors"
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

// arbitrationFixture is an escrow processor with one realm whose only
// arbitrator is arbitratorKey, a payer that opens escrows in that realm and a
// relayer that submits the arbitrators' signed decisions.
type arbitrationFixture struct {
	sp             *StateProcessor
	payerKey       *crypto.PrivateKey
	payeeAddr      crypto.Address
	arbitratorKey  *crypto.PrivateKey
	relayerKey     *crypto.PrivateKey
	relayerAddr    [20]byte
	realmID        string
	payerNextNonce uint64
	nextEscrowSeed uint64
}

func newArbitrationFixture(t *testing.T) *arbitrationFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	newKey := func() *crypto.PrivateKey {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		return key
	}
	adminKey, payerKey, payeeKey, arbitratorKey, relayerKey := newKey(), newKey(), newKey(), newKey(), newKey()
	fx := &arbitrationFixture{
		sp:             sp,
		payerKey:       payerKey,
		payeeAddr:      payeeKey.PubKey().Address(),
		arbitratorKey:  arbitratorKey,
		relayerKey:     relayerKey,
		realmID:        "realm-arbitrate-nonce",
		nextEscrowSeed: 700,
	}
	copy(fx.relayerAddr[:], relayerKey.PubKey().Address().Bytes())

	var treasury [20]byte
	treasury[0] = 0xDD
	sp.SetEscrowFeeTreasury(treasury)
	empty := func() *types.Account {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	var adminRaw, payerRaw, payeeRaw [20]byte
	copy(adminRaw[:], adminKey.PubKey().Address().Bytes())
	copy(payerRaw[:], payerKey.PubKey().Address().Bytes())
	copy(payeeRaw[:], fx.payeeAddr.Bytes())
	writeAccount(t, sp, adminRaw, empty())
	writeAccount(t, sp, fx.relayerAddr, empty())
	writeAccount(t, sp, treasury, empty())
	writeAccount(t, sp, payeeRaw, empty())
	payer := empty()
	payer.BalanceNHB = big.NewInt(10_000)
	writeAccount(t, sp, payerRaw, payer)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleEscrowRealmAdmin, adminKey.PubKey().Address().Bytes()); err != nil {
		t.Fatalf("grant realm admin role: %v", err)
	}
	realmData, err := jsonMarshal(struct {
		ID              string   `json:"id"`
		Threshold       uint32   `json:"threshold"`
		Scheme          uint8    `json:"scheme"`
		Members         []string `json:"members"`
		Scope           uint8    `json:"scope"`
		ProviderProfile string   `json:"providerProfile"`
	}{
		ID:              fx.realmID,
		Threshold:       1,
		Scheme:          uint8(escrow.ArbitrationSchemeSingle),
		Members:         []string{arbitratorKey.PubKey().Address().String()},
		Scope:           uint8(escrow.EscrowRealmScopePlatform),
		ProviderProfile: "core-team",
	})
	if err != nil {
		t.Fatalf("marshal realm payload: %v", err)
	}
	realmTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeEscrowCreateRealm, Nonce: 0, Data: realmData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := realmTx.Sign(adminKey.PrivateKey); err != nil {
		t.Fatalf("sign create realm: %v", err)
	}
	if err := sp.ApplyTransaction(realmTx); err != nil {
		t.Fatalf("apply create realm: %v", err)
	}
	return fx
}

// disputedEscrow opens a funded escrow in the realm and raises a dispute, the
// only state an arbitration can resolve. It returns the escrow id and the
// policy nonce the arbitrators must sign over.
func (fx *arbitrationFixture) disputedEscrow(t *testing.T) ([32]byte, uint64) {
	t.Helper()
	fx.nextEscrowSeed++
	escrowNonce := fx.nextEscrowSeed
	payerAddr := fx.payerKey.PubKey().Address()
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	meta := [32]byte{}
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), fx.payeeAddr.Bytes(), meta[:], nonceBytes[:])

	createData, err := jsonMarshal(struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
		Realm    string   `json:"realm"`
	}{
		Payee:    fx.payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		Deadline: time.Now().Add(2 * time.Hour).Unix(),
		Nonce:    escrowNonce,
		Realm:    fx.realmID,
	})
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	steps := []struct {
		txType types.TxType
		data   []byte
	}{
		{types.TxTypeCreateEscrow, createData},
		{types.TxTypeLockEscrow, escrowID[:]},
		{types.TxTypeDisputeEscrow, escrowID[:]},
	}
	for _, step := range steps {
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: step.txType, Nonce: fx.payerNextNonce, Data: step.data, GasLimit: 21000, GasPrice: big.NewInt(1)}
		if err := tx.Sign(fx.payerKey.PrivateKey); err != nil {
			t.Fatalf("sign escrow step %v: %v", step.txType, err)
		}
		if err := fx.sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("apply escrow step %v: %v", step.txType, err)
		}
		fx.payerNextNonce++
	}
	esc, ok := nhbstate.NewManager(fx.sp.Trie).EscrowGet(escrowID)
	if !ok || esc.FrozenArb == nil {
		t.Fatalf("expected a disputed escrow carrying the realm's frozen arbitrator policy")
	}
	return escrowID, esc.FrozenArb.PolicyNonce
}

// arbitrationTx builds the relayer's transaction that submits the arbitrator's
// signed decision for escrowID.
func (fx *arbitrationFixture) arbitrationTx(t *testing.T, txType types.TxType, relayerNonce uint64, escrowID [32]byte, policyNonce uint64, outcome string) *types.Transaction {
	t.Helper()
	escrowIDHex := "0x" + hexEncode(escrowID[:])
	decision, err := jsonMarshal(struct {
		EscrowID    string `json:"escrowId"`
		Outcome     string `json:"outcome"`
		PolicyNonce uint64 `json:"policyNonce"`
	}{EscrowID: escrowIDHex, Outcome: outcome, PolicyNonce: policyNonce})
	if err != nil {
		t.Fatalf("marshal decision: %v", err)
	}
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256Hash(decision).Bytes(), fx.arbitratorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign decision: %v", err)
	}
	data, err := rlp.EncodeToBytes(struct {
		EscrowID   string   `json:"escrowId"`
		Decision   []byte   `json:"decision"`
		Signatures []string `json:"signatures"`
	}{EscrowID: escrowIDHex, Decision: decision, Signatures: []string{"0x" + hexEncode(sig)}})
	if err != nil {
		t.Fatalf("rlp encode arbitration payload: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: txType, Nonce: relayerNonce, Data: data, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(fx.relayerKey.PrivateKey); err != nil {
		t.Fatalf("sign arbitration tx: %v", err)
	}
	return tx
}

func (fx *arbitrationFixture) relayerNonce(t *testing.T) uint64 {
	t.Helper()
	account, err := fx.sp.getAccount(fx.relayerAddr[:])
	if err != nil {
		t.Fatalf("load relayer account: %v", err)
	}
	return account.Nonce
}

// TestArbitrationAdvancesTheRelayersNonce covers a relayer that submits
// several arbitration decisions in a row, as the escrow gateway does: each
// successful arbitration, release or refund, must consume the nonce it
// carried, so the next one is accepted at the next nonce and a resubmission
// of a spent nonce is refused as too low.
func TestArbitrationAdvancesTheRelayersNonce(t *testing.T) {
	fx := newArbitrationFixture(t)

	firstID, firstPolicy := fx.disputedEscrow(t)
	first := fx.arbitrationTx(t, types.TxTypeArbitrateRelease, 0, firstID, firstPolicy, "release")
	if err := fx.sp.ApplyTransaction(first); err != nil {
		t.Fatalf("apply first arbitration: %v", err)
	}
	if got := fx.relayerNonce(t); got != 1 {
		t.Fatalf("relayer nonce after one successful arbitration = %d, want 1", got)
	}
	if esc, ok := nhbstate.NewManager(fx.sp.Trie).EscrowGet(firstID); !ok || esc.Status != escrow.EscrowReleased {
		t.Fatalf("first escrow not released after arbitration: ok=%v", ok)
	}

	secondID, secondPolicy := fx.disputedEscrow(t)
	second := fx.arbitrationTx(t, types.TxTypeArbitrateRefund, 1, secondID, secondPolicy, "refund")
	if err := fx.sp.ApplyTransaction(second); err != nil {
		t.Fatalf("apply second arbitration at the next nonce: %v", err)
	}
	if got := fx.relayerNonce(t); got != 2 {
		t.Fatalf("relayer nonce after two successful arbitrations = %d, want 2", got)
	}
	if esc, ok := nhbstate.NewManager(fx.sp.Trie).EscrowGet(secondID); !ok || esc.Status != escrow.EscrowRefunded {
		t.Fatalf("second escrow not refunded after arbitration: ok=%v", ok)
	}

	thirdID, thirdPolicy := fx.disputedEscrow(t)
	stale := fx.arbitrationTx(t, types.TxTypeArbitrateRelease, 0, thirdID, thirdPolicy, "release")
	if err := fx.sp.ApplyTransaction(stale); !errors.Is(err, ErrNonceTooLow) {
		t.Fatalf("arbitration reusing a spent nonce: got %v, want ErrNonceTooLow", err)
	}
	if got := fx.relayerNonce(t); got != 2 {
		t.Fatalf("relayer nonce after a refused arbitration = %d, want 2", got)
	}
}

// TestFailedArbitrationLeavesTheRelayersNonce keeps the other half of the
// rule: an arbitration that is refused must not consume the nonce, so the
// relayer can resubmit a corrected decision at the same nonce.
func TestFailedArbitrationLeavesTheRelayersNonce(t *testing.T) {
	fx := newArbitrationFixture(t)
	escrowID, policyNonce := fx.disputedEscrow(t)

	// A decision signed over a policy nonce the realm never issued is refused.
	bad := fx.arbitrationTx(t, types.TxTypeArbitrateRelease, 0, escrowID, policyNonce+99, "release")
	if err := fx.sp.ApplyTransaction(bad); err == nil {
		t.Fatalf("expected an arbitration signed over the wrong policy nonce to be refused")
	}
	if got := fx.relayerNonce(t); got != 0 {
		t.Fatalf("relayer nonce after a refused arbitration = %d, want 0", got)
	}

	good := fx.arbitrationTx(t, types.TxTypeArbitrateRelease, 0, escrowID, policyNonce, "release")
	if err := fx.sp.ApplyTransaction(good); err != nil {
		t.Fatalf("apply corrected arbitration at the unchanged nonce: %v", err)
	}
	if got := fx.relayerNonce(t); got != 1 {
		t.Fatalf("relayer nonce after the corrected arbitration = %d, want 1", got)
	}
}
