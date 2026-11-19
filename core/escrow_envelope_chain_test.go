package core

import (
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strconv"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/config"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

// Ledger PL-EXT-07: from EscrowEnvelopeChainBindingActivationHeight the signed payload
// of a delegated escrow create, release, refund or dispute and the decision of an
// arbitration quorum must carry the network's own chain id as "chainId". Before it,
// and with the rule off, they are accepted as they always were.

const (
	envelopeChainActivation = uint64(300)
	envelopeNetworkChainID  = uint64(99070)
)

func envelopeProcessor(t *testing.T, activation uint64) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherChainID(envelopeNetworkChainID)
	sp.SetValidatorRules(ValidatorRules{EscrowEnvelopeChainBindingActivationHeight: activation})
	return sp
}

func atEnvelopeHeight(sp *StateProcessor, height uint64) {
	sp.BeginBlock(height, time.Now())
}

// chainField is how a payload names its chain: absent, a decimal string, a number, null.
type chainField struct {
	present bool
	raw     string // the JSON value
}

var (
	noChain     = chainField{}
	rightChain  = chainField{true, strconv.Quote(strconv.FormatUint(envelopeNetworkChainID, 10))}
	otherChain  = chainField{true, `"1337"`}
	numberChain = chainField{true, strconv.FormatUint(envelopeNetworkChainID, 10)}
	nullChain   = chainField{true, `null`}
	paddedChain = chainField{true, `"099070"`}
	spacedChain = chainField{true, `" 99070"`}
	legacyChain = chainField{true, `"5130306"`} // the legacy constant 0x4e4842
	emptyChain  = chainField{true, `""`}
	zeroChain   = chainField{true, `"0"`} // what an unset network id would print
)

// payload renders fields (already JSON-encoded values) plus the chain field as one JSON object.
func (c chainField) payload(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if !c.present {
		return body
	}
	// Add the chain field by hand so its JSON type is exactly what the case names.
	return append(body[:len(body)-1], []byte(`,"chainId":`+c.raw+`}`)...)
}

func TestEscrowEnvelopeChainCheck(t *testing.T) {
	type tc struct {
		name    string
		chain   chainField
		wantErr bool
	}
	cases := []tc{
		{"the network's id", rightChain, false},
		{"no chain id", noChain, true},
		{"another network's id", otherChain, true},
		{"the legacy constant", legacyChain, true},
		{"a number", numberChain, true},
		{"null", nullChain, true},
		{"leading zero", paddedChain, true},
		{"a space", spacedChain, true},
		{"empty", emptyChain, true},
		{"zero", zeroChain, true},
	}
	body := map[string]any{"escrowId": "0x01", "action": "release"}

	t.Run("from the height", func(t *testing.T) {
		sp := envelopeProcessor(t, envelopeChainActivation)
		for _, height := range []uint64{envelopeChainActivation, envelopeChainActivation + 1, 9_000_000} {
			atEnvelopeHeight(sp, height)
			for _, c := range cases {
				err := sp.requireEscrowEnvelopeChain(c.chain.payload(t, body))
				if (err != nil) != c.wantErr {
					t.Fatalf("height %d, %s: error %v, want error %v", height, c.name, err, c.wantErr)
				}
			}
			for _, bad := range []string{``, `not json`, `[]`, `"x"`} {
				if err := sp.requireEscrowEnvelopeChain([]byte(bad)); err == nil {
					t.Fatalf("height %d: %q was accepted", height, bad)
				}
			}
		}
	})

	t.Run("below the height and with the rule off every payload is accepted", func(t *testing.T) {
		for name, activation := range map[string]uint64{"below": envelopeChainActivation, "off": 0} {
			sp := envelopeProcessor(t, activation)
			height := envelopeChainActivation - 1
			if activation == 0 {
				height = 9_000_000
			}
			atEnvelopeHeight(sp, height)
			for _, c := range cases {
				if err := sp.requireEscrowEnvelopeChain(c.chain.payload(t, body)); err != nil {
					t.Fatalf("%s, %s: %v", name, c.name, err)
				}
			}
			if err := sp.requireEscrowEnvelopeChain([]byte(`not json`)); err != nil {
				t.Fatalf("%s: the engine, not this check, judges a payload before the height: %v", name, err)
			}
		}
	})

	t.Run("a network id that was never configured accepts nothing from the height", func(t *testing.T) {
		sp := envelopeProcessor(t, envelopeChainActivation)
		sp.SetSwapVoucherChainID(0)
		atEnvelopeHeight(sp, envelopeChainActivation)
		for _, c := range cases {
			if err := sp.requireEscrowEnvelopeChain(c.chain.payload(t, body)); err == nil {
				t.Fatalf("%s was accepted with no network id configured", c.name)
			}
		}
	})
}

func TestEscrowEnvelopeChainRuleIsReadFromTheConfig(t *testing.T) {
	if got := ValidatorRulesFromConfig(&config.Config{}).EscrowEnvelopeChainBindingActivationHeight; got != 0 {
		t.Fatalf("rule with no setting = %d, want 0 (off)", got)
	}
	cfg := &config.Config{EscrowEnvelopeChainBindingActivationHeight: 12345}
	if got := ValidatorRulesFromConfig(cfg).EscrowEnvelopeChainBindingActivationHeight; got != 12345 {
		t.Fatalf("rule read from the config = %d, want 12345", got)
	}
	sp := envelopeProcessor(t, 12345)
	if got := sp.ValidatorRulesInForce().EscrowEnvelopeChainBindingActivationHeight; got != 12345 {
		t.Fatalf("rule in force = %d", got)
	}
}

// envelopeWorld is a payer, a payee and a relayer on a processor with the rule set.
type envelopeWorld struct {
	t                            *testing.T
	sp                           *StateProcessor
	payerKey, payeeKey, relayKey *crypto.PrivateKey
	payer, payee                 [20]byte
	nextEscrow                   uint64
}

func newEnvelopeWorld(t *testing.T, activation uint64) *envelopeWorld {
	t.Helper()
	w := &envelopeWorld{t: t, sp: envelopeProcessor(t, activation), nextEscrow: 4_000}
	var err error
	if w.payerKey, err = crypto.GeneratePrivateKey(); err != nil {
		t.Fatalf("key: %v", err)
	}
	if w.payeeKey, err = crypto.GeneratePrivateKey(); err != nil {
		t.Fatalf("key: %v", err)
	}
	if w.relayKey, err = crypto.GeneratePrivateKey(); err != nil {
		t.Fatalf("key: %v", err)
	}
	copy(w.payer[:], w.payerKey.PubKey().Address().Bytes())
	copy(w.payee[:], w.payeeKey.PubKey().Address().Bytes())
	var relay, treasury [20]byte
	copy(relay[:], w.relayKey.PubKey().Address().Bytes())
	treasury[0] = 0xCE
	w.sp.SetEscrowFeeTreasury(treasury)
	acct := func(nhb int64) *types.Account {
		return &types.Account{BalanceNHB: big.NewInt(nhb), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	writeAccount(t, w.sp, w.payer, acct(100_000))
	writeAccount(t, w.sp, w.payee, acct(0))
	writeAccount(t, w.sp, relay, acct(0))
	writeAccount(t, w.sp, treasury, acct(0))
	return w
}

func (w *envelopeWorld) nonceOf(addr [20]byte) uint64 {
	w.t.Helper()
	acc, err := w.sp.getAccount(addr[:])
	if err != nil {
		w.t.Fatalf("account: %v", err)
	}
	return acc.Nonce
}

func (w *envelopeWorld) relayerAddr() [20]byte {
	var a [20]byte
	copy(a[:], w.relayKey.PubKey().Address().Bytes())
	return a
}

func (w *envelopeWorld) escrowID(nonce uint64) [32]byte {
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)
	return ethcrypto.Keccak256Hash(w.payer[:], w.payee[:], make([]byte, 32), nonceBytes[:])
}

func (w *envelopeWorld) sign(key *crypto.PrivateKey, payload []byte) []byte {
	w.t.Helper()
	sig, err := ethcrypto.Sign(ethcrypto.Keccak256Hash(payload).Bytes(), key.PrivateKey)
	if err != nil {
		w.t.Fatalf("sign: %v", err)
	}
	return sig
}

func (w *envelopeWorld) relay(txType types.TxType, data []byte) error {
	w.t.Helper()
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: txType, Nonce: w.nonceOf(w.relayerAddr()), Data: data, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(w.relayKey.PrivateKey); err != nil {
		w.t.Fatalf("sign relay tx: %v", err)
	}
	return w.sp.ApplyTransaction(tx)
}

// delegatedCreate submits a create signed by the payer with the chain field c.
func (w *envelopeWorld) delegatedCreate(c chainField) (escrowNonce uint64, err error) {
	w.t.Helper()
	w.nextEscrow++
	payload := c.payload(w.t, map[string]any{
		"action":   "create",
		"payer":    hexEncode(w.payer[:]),
		"payee":    hexEncode(w.payee[:]),
		"token":    "NHB",
		"amount":   "100",
		"feeBps":   100,
		"deadline": time.Now().Add(2 * time.Hour).Unix(),
		"nonce":    w.nextEscrow,
	})
	data, encErr := rlp.EncodeToBytes(struct {
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{payload, w.sign(w.payerKey, payload)})
	if encErr != nil {
		w.t.Fatalf("rlp: %v", encErr)
	}
	return w.nextEscrow, w.relay(types.TxTypeDelegatedCreateEscrow, data)
}

func (w *envelopeWorld) exists(nonce uint64) bool {
	_, ok := nhbstate.NewManager(w.sp.Trie).EscrowGet(w.escrowID(nonce))
	return ok
}

func TestEscrowEnvelopeChainDelegatedCreate(t *testing.T) {
	const height = envelopeChainActivation
	for _, tc := range []struct {
		name   string
		height uint64
		chain  chainField
		want   bool // the escrow is created
	}{
		{"before the height, no chain id", height - 1, noChain, true},
		{"before the height, another network's", height - 1, otherChain, true},
		{"at the height, no chain id", height, noChain, false},
		{"at the height, another network's", height, otherChain, false},
		{"at the height, a number", height, numberChain, false},
		{"at the height, the network's", height, rightChain, true},
		{"after the height, no chain id", height + 50, noChain, false},
		{"after the height, the network's", height + 50, rightChain, true},
	} {
		w := newEnvelopeWorld(t, height)
		atEnvelopeHeight(w.sp, tc.height)
		before := w.nonceOf(w.relayerAddr())
		nonce, err := w.delegatedCreate(tc.chain)
		if (err == nil) != tc.want || w.exists(nonce) != tc.want {
			t.Fatalf("%s: error %v, created %v, want created %v", tc.name, err, w.exists(nonce), tc.want)
		}
		if !tc.want && w.nonceOf(w.relayerAddr()) != before {
			t.Fatalf("%s: a refused create advanced the relayer's nonce", tc.name)
		}
	}
}

// openFundedEscrow creates and funds an escrow directly (the payer's own transactions,
// which carry no signed envelope).
func (w *envelopeWorld) openFundedEscrow() uint64 {
	w.t.Helper()
	w.nextEscrow++
	createData, err := jsonMarshal(struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}{w.payee[:], "NHB", big.NewInt(100), 100, time.Now().Add(2 * time.Hour).Unix(), w.nextEscrow})
	if err != nil {
		w.t.Fatalf("marshal: %v", err)
	}
	id := w.escrowID(w.nextEscrow)
	for _, step := range []struct {
		txType types.TxType
		data   []byte
	}{{types.TxTypeCreateEscrow, createData}, {types.TxTypeLockEscrow, id[:]}} {
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: step.txType, Nonce: w.nonceOf(w.payer), Data: step.data, GasLimit: 21000, GasPrice: big.NewInt(1)}
		if err := tx.Sign(w.payerKey.PrivateKey); err != nil {
			w.t.Fatalf("sign: %v", err)
		}
		if err := w.sp.ApplyTransaction(tx); err != nil {
			w.t.Fatalf("apply %v: %v", step.txType, err)
		}
	}
	return w.nextEscrow
}

func (w *envelopeWorld) status(nonce uint64) escrow.EscrowStatus {
	w.t.Helper()
	esc, ok := nhbstate.NewManager(w.sp.Trie).EscrowGet(w.escrowID(nonce))
	if !ok {
		w.t.Fatalf("escrow missing")
	}
	return esc.Status
}

// delegatedAction submits the payee's (or payer's) signed action with the chain field c.
func (w *envelopeWorld) delegatedAction(txType types.TxType, action string, signer *crypto.PrivateKey, nonce uint64, c chainField) error {
	w.t.Helper()
	id := w.escrowID(nonce)
	fields := map[string]any{"escrowId": "0x" + hexEncode(id[:]), "action": action}
	if action == escrow.ActionDispute {
		fields["reason"] = "late"
	}
	payload := c.payload(w.t, fields)
	data, err := rlp.EncodeToBytes(struct {
		EscrowID  string `json:"escrowId"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{"0x" + hexEncode(id[:]), payload, w.sign(signer, payload)})
	if err != nil {
		w.t.Fatalf("rlp: %v", err)
	}
	return w.relay(txType, data)
}

func TestEscrowEnvelopeChainDelegatedActions(t *testing.T) {
	const height = envelopeChainActivation
	type act struct {
		name   string
		txType types.TxType
		action string
		payee  bool // signed by the payee, otherwise by the payer
		done   escrow.EscrowStatus
	}
	for _, a := range []act{
		{"release", types.TxTypeDelegatedReleaseEscrow, escrow.ActionRelease, true, escrow.EscrowReleased},
		{"refund", types.TxTypeDelegatedRefundEscrow, escrow.ActionRefund, false, escrow.EscrowRefunded},
		{"dispute", types.TxTypeDelegatedDisputeEscrow, escrow.ActionDispute, false, escrow.EscrowDisputed},
	} {
		for _, tc := range []struct {
			name   string
			height uint64
			chain  chainField
			want   bool
		}{
			{"before the height, no chain id", height - 1, noChain, true},
			{"at the height, no chain id", height, noChain, false},
			{"at the height, another network's", height, otherChain, false},
			{"at the height, a number", height, numberChain, false},
			{"at the height, the network's", height, rightChain, true},
			{"after the height, no chain id", height + 9, noChain, false},
			{"after the height, the network's", height + 9, rightChain, true},
		} {
			w := newEnvelopeWorld(t, height)
			atEnvelopeHeight(w.sp, height-1)
			nonce := w.openFundedEscrow()
			signer := w.payerKey
			if a.payee {
				signer = w.payeeKey
			}
			atEnvelopeHeight(w.sp, tc.height)
			before := w.status(nonce)
			relayerBefore := w.nonceOf(w.relayerAddr())
			err := w.delegatedAction(a.txType, a.action, signer, nonce, tc.chain)
			if (err == nil) != tc.want {
				t.Fatalf("%s, %s: error %v, want accepted %v", a.name, tc.name, err, tc.want)
			}
			after := w.status(nonce)
			if tc.want && after != a.done {
				t.Fatalf("%s, %s: status %v, want %v", a.name, tc.name, after, a.done)
			}
			if !tc.want && (after != before || w.nonceOf(w.relayerAddr()) != relayerBefore) {
				t.Fatalf("%s, %s: a refused action changed the escrow (%v to %v) or the relayer's nonce", a.name, tc.name, before, after)
			}
		}
	}
}

// The arbitration quorum's decision is bound the same way.
func TestEscrowEnvelopeChainArbitrationDecision(t *testing.T) {
	const height = envelopeChainActivation
	for _, tc := range []struct {
		name   string
		height uint64
		chain  chainField
		want   bool
	}{
		{"before the height, no chain id", height - 1, noChain, true},
		{"at the height, no chain id", height, noChain, false},
		{"at the height, another network's", height, otherChain, false},
		{"at the height, the network's", height, rightChain, true},
		{"after the height, no chain id", height + 9, noChain, false},
		{"after the height, the network's", height + 9, rightChain, true},
	} {
		fx := newArbitrationFixture(t)
		fx.sp.SetSwapVoucherChainID(envelopeNetworkChainID)
		fx.sp.SetValidatorRules(ValidatorRules{EscrowEnvelopeChainBindingActivationHeight: height})
		atEnvelopeHeight(fx.sp, height-1)
		escrowID, policyNonce := fx.disputedEscrow(t)
		atEnvelopeHeight(fx.sp, tc.height)

		idHex := "0x" + hexEncode(escrowID[:])
		decision := tc.chain.payload(t, map[string]any{"escrowId": idHex, "outcome": "release", "policyNonce": policyNonce})
		sig, err := ethcrypto.Sign(ethcrypto.Keccak256Hash(decision).Bytes(), fx.arbitratorKey.PrivateKey)
		if err != nil {
			t.Fatalf("sign decision: %v", err)
		}
		data, err := rlp.EncodeToBytes(struct {
			EscrowID   string   `json:"escrowId"`
			Decision   []byte   `json:"decision"`
			Signatures []string `json:"signatures"`
		}{idHex, decision, []string{"0x" + hexEncode(sig)}})
		if err != nil {
			t.Fatalf("rlp: %v", err)
		}
		before := fx.relayerNonce(t)
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeArbitrateRelease, Nonce: before, Data: data, GasLimit: 21000, GasPrice: big.NewInt(1)}
		if err := tx.Sign(fx.relayerKey.PrivateKey); err != nil {
			t.Fatalf("sign: %v", err)
		}
		applyErr := fx.sp.ApplyTransaction(tx)
		esc, _ := nhbstate.NewManager(fx.sp.Trie).EscrowGet(escrowID)
		if (applyErr == nil) != tc.want {
			t.Fatalf("%s: error %v, want accepted %v", tc.name, applyErr, tc.want)
		}
		if tc.want && esc.Status != escrow.EscrowReleased {
			t.Fatalf("%s: status %v, want released", tc.name, esc.Status)
		}
		if !tc.want && (esc.Status != escrow.EscrowDisputed || fx.relayerNonce(t) != before) {
			t.Fatalf("%s: a refused decision changed the escrow (%v) or the relayer's nonce", tc.name, esc.Status)
		}
	}
}
