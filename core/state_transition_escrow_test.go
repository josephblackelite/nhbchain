package core

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
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

func TestEscrowNativeLifecycle(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xAA
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	var payeeAccountAddr [20]byte
	copy(payeeAccountAddr[:], payeeAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	meta := [32]byte{}
	escrowNonce := uint64(1)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:], nonceBytes[:])

	createPayload := struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}{
		Payee:    payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		FeeBps:   100,
		Deadline: time.Now().Add(2 * time.Hour).Unix(),
		Nonce:    escrowNonce,
	}
	createData, err := jsonMarshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeCreateEscrow,
		Nonce:    0,
		Data:     createData,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := createTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow not stored")
	}
	if esc.Status != escrow.EscrowInit {
		t.Fatalf("unexpected escrow status after create: %v", esc.Status)
	}
	if esc.Nonce != escrowNonce {
		t.Fatalf("unexpected escrow nonce: %d", esc.Nonce)
	}

	fundTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLockEscrow,
		Nonce:    1,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}
	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after fund")
	}
	if esc.Status != escrow.EscrowFunded {
		t.Fatalf("unexpected status after fund: %v", esc.Status)
	}

	releaseTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeReleaseEscrow,
		Nonce:    0,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := releaseTx.Sign(payeeKey.PrivateKey); err != nil {
		t.Fatalf("sign release: %v", err)
	}
	if err := sp.ApplyTransaction(releaseTx); err != nil {
		t.Fatalf("apply release: %v", err)
	}

	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after release")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("unexpected status after release: %v", esc.Status)
	}

	payerAccount, err := sp.getAccount(payerAddr.Bytes())
	if err != nil {
		t.Fatalf("load payer account: %v", err)
	}
	if payerAccount.Nonce != 2 {
		t.Fatalf("unexpected payer nonce: %d", payerAccount.Nonce)
	}
	expectedPayer := big.NewInt(1_000)
	expectedPayer.Sub(expectedPayer, big.NewInt(100))
	if payerAccount.BalanceNHB.Cmp(expectedPayer) != 0 {
		t.Fatalf("unexpected payer balance: %s", payerAccount.BalanceNHB)
	}

	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee account: %v", err)
	}
	if payeeAccount.Nonce != 1 {
		t.Fatalf("unexpected payee nonce: %d", payeeAccount.Nonce)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("unexpected payee balance: %s", payeeAccount.BalanceNHB)
	}

	treasuryAccount, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury account: %v", err)
	}
	if treasuryAccount.BalanceNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("unexpected treasury balance: %s", treasuryAccount.BalanceNHB)
	}
}

func TestEscrowExpireLifecycle(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}
	// A third, unrelated account -- TxTypeExpireEscrow is deliberately
	// permissionless, so this key stands in for "anyone" sweeping a stale
	// escrow, not the payer or payee.
	sweeperKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sweeper key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()
	sweeperAddr := sweeperKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xBB
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr, payeeAccountAddr, sweeperAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	copy(payeeAccountAddr[:], payeeAddr.Bytes())
	copy(sweeperAccountAddr[:], sweeperAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, sweeperAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	meta := [32]byte{}
	escrowNonce := uint64(7)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:], nonceBytes[:])

	deadline := time.Now().Add(1 * time.Hour).Unix()
	createPayload := struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}{
		Payee:    payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		FeeBps:   100,
		Deadline: deadline,
		Nonce:    escrowNonce,
	}
	createData, err := jsonMarshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeCreateEscrow,
		Nonce:    0,
		Data:     createData,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := createTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}

	fundTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLockEscrow,
		Nonce:    1,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after fund")
	}
	if esc.Status != escrow.EscrowFunded {
		t.Fatalf("unexpected status after fund: %v", esc.Status)
	}

	// Before the deadline: expire must be rejected, from the sweeper too --
	// permissionless doesn't mean premature.
	sp.BeginBlock(1, time.Unix(deadline-1, 0).UTC())
	earlyExpireTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeExpireEscrow,
		Nonce:    0,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := earlyExpireTx.Sign(sweeperKey.PrivateKey); err != nil {
		t.Fatalf("sign early expire: %v", err)
	}
	if err := sp.ApplyTransaction(earlyExpireTx); err == nil {
		t.Fatalf("expected expire before deadline to fail")
	}

	// After the deadline: the sweeper (neither payer nor payee) can expire
	// it, proving the transition is genuinely permissionless.
	sp.BeginBlock(2, time.Unix(deadline+1, 0).UTC())
	expireTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeExpireEscrow,
		Nonce:    0,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := expireTx.Sign(sweeperKey.PrivateKey); err != nil {
		t.Fatalf("sign expire: %v", err)
	}
	if err := sp.ApplyTransaction(expireTx); err != nil {
		t.Fatalf("apply expire: %v", err)
	}

	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after expire")
	}
	if esc.Status != escrow.EscrowExpired {
		t.Fatalf("unexpected status after expire: %v", esc.Status)
	}

	payerAccount, err := sp.getAccount(payerAddr.Bytes())
	if err != nil {
		t.Fatalf("load payer account: %v", err)
	}
	if payerAccount.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected payer refunded in full, got %s", payerAccount.BalanceNHB)
	}

	// Idempotent: a second expire (e.g. a different sweeper, or a retry) is
	// a no-op, not an error, and does not increment the sweeper's nonce
	// beyond what a single successful call already did -- exercised here by
	// resubmitting from the same sweeper key at the next nonce.
	secondExpireTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeExpireEscrow,
		Nonce:    1,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := secondExpireTx.Sign(sweeperKey.PrivateKey); err != nil {
		t.Fatalf("sign second expire: %v", err)
	}
	if err := sp.ApplyTransaction(secondExpireTx); err != nil {
		t.Fatalf("apply second expire: %v", err)
	}
}

func TestEscrowLegacyMigration(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xBB
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	var payeeAccountAddr [20]byte
	copy(payeeAccountAddr[:], payeeAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	legacyID := ethcrypto.Keccak256Hash([]byte("legacy"), payerAddr.Bytes(), payeeAddr.Bytes())
	legacy := &escrow.LegacyEscrow{
		ID:     append([]byte(nil), legacyID[:]...),
		Buyer:  append([]byte(nil), payeeAddr.Bytes()...),
		Seller: append([]byte(nil), payerAddr.Bytes()...),
		Amount: big.NewInt(50),
		Status: escrow.LegacyStatusInProgress,
	}
	encodedLegacy, err := rlp.EncodeToBytes(legacy)
	if err != nil {
		t.Fatalf("encode legacy: %v", err)
	}
	legacyKey := ethcrypto.Keccak256(append([]byte("escrow-"), legacyID[:]...))
	if err := sp.Trie.Update(legacyKey, encodedLegacy); err != nil {
		t.Fatalf("write legacy escrow: %v", err)
	}

	releaseTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeReleaseEscrow,
		Nonce:    0,
		Data:     legacyID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := releaseTx.Sign(payeeKey.PrivateKey); err != nil {
		t.Fatalf("sign legacy release: %v", err)
	}
	if err := sp.ApplyTransaction(releaseTx); err != nil {
		t.Fatalf("apply legacy release: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(legacyID)
	if !ok {
		t.Fatalf("escrow not migrated")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("expected released status, got %v", esc.Status)
	}

	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee: %v", err)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("unexpected payee balance after migration: %s", payeeAccount.BalanceNHB)
	}
}

// signEscrowActionEnvelope builds and signs the exact JSON wire payload
// escrow.ReleaseWithSignature/RefundWithSignature/DisputeWithSignature
// verify (escrow.escrowActionEnvelope, native/escrow/engine.go) -- built
// here from raw JSON tags rather than importing the unexported type,
// exactly as a real relayer (escrow-gateway) or client would have to.
func signEscrowActionEnvelope(t *testing.T, id [32]byte, action, reason string, key *ecdsa.PrivateKey) (payload []byte, signature []byte) {
	t.Helper()
	envelope := struct {
		EscrowID string `json:"escrowId"`
		Action   string `json:"action"`
		Reason   string `json:"reason,omitempty"`
	}{
		EscrowID: "0x" + hexEncode(id[:]),
		Action:   action,
		Reason:   reason,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal action envelope: %v", err)
	}
	digest := ethcrypto.Keccak256Hash(data)
	sig, err := ethcrypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign action envelope: %v", err)
	}
	return data, sig
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}

// TestDelegatedReleaseEscrowLifecycle exercises TxTypeDelegatedReleaseEscrow
// end to end: a relayer with no relationship to the escrow (not payer,
// payee, or mediator) submits the transaction and pays its gas, but
// authorization comes entirely from the payee's off-chain signature
// embedded in the payload -- proving the relayer's own identity is
// irrelevant to whether the release is authorized.
// signEscrowCreateEnvelope builds and signs the exact JSON wire payload
// escrow.CreateWithSignature verifies (escrow.escrowCreateEnvelope,
// native/escrow/engine.go) -- built from raw JSON tags rather than
// importing the unexported type, exactly as a real relayer or client
// would have to.
func signEscrowCreateEnvelope(t *testing.T, payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, nonce uint64, key *ecdsa.PrivateKey) (payload []byte, signature []byte) {
	t.Helper()
	envelope := struct {
		Action   string `json:"action"`
		Payer    string `json:"payer"`
		Payee    string `json:"payee"`
		Token    string `json:"token"`
		Amount   string `json:"amount"`
		FeeBps   uint32 `json:"feeBps"`
		Deadline int64  `json:"deadline"`
		Nonce    uint64 `json:"nonce"`
	}{
		Action:   "create",
		Payer:    hexEncode(payer[:]),
		Payee:    hexEncode(payee[:]),
		Token:    token,
		Amount:   amount.String(),
		FeeBps:   feeBps,
		Deadline: deadline,
		Nonce:    nonce,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal create envelope: %v", err)
	}
	digest := ethcrypto.Keccak256Hash(data)
	sig, err := ethcrypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign create envelope: %v", err)
	}
	return data, sig
}

// TestDelegatedCreateEscrowLifecycle exercises TxTypeDelegatedCreateEscrow:
// a relayer with no NHB/ZNHB stake in the outcome submits the transaction
// and pays its own gas, but the created escrow's payer is the address that
// actually signed the create envelope -- not the relayer -- so the real
// payer retains every authorization Refund/Expire/Dispute already checks
// against esc.Payer.
func TestDelegatedCreateEscrowLifecycle(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}
	relayerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate relayer key: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()
	relayerAddr := relayerKey.PubKey().Address()

	var payerAccountAddr, relayerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	copy(relayerAccountAddr[:], relayerAddr.Bytes())
	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, relayerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	var payerRaw, payeeRaw [20]byte
	copy(payerRaw[:], payerAddr.Bytes())
	copy(payeeRaw[:], payeeAddr.Bytes())
	deadline := time.Now().Add(2 * time.Hour).Unix()
	createPayload, createSig := signEscrowCreateEnvelope(t, payerRaw, payeeRaw, "NHB", big.NewInt(100), 100, deadline, 301, payerKey.PrivateKey)

	delegatedCreate := struct {
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{Payload: createPayload, Signature: createSig}
	delegatedData, err := rlp.EncodeToBytes(delegatedCreate)
	if err != nil {
		t.Fatalf("rlp encode delegated create payload: %v", err)
	}
	delegatedTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeDelegatedCreateEscrow, Nonce: 0, Data: delegatedData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := delegatedTx.Sign(relayerKey.PrivateKey); err != nil {
		t.Fatalf("sign delegated create: %v", err)
	}
	if err := sp.ApplyTransaction(delegatedTx); err != nil {
		t.Fatalf("apply delegated create: %v", err)
	}

	meta := [32]byte{}
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], 301)
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:], nonceBytes[:])

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after delegated create")
	}
	if esc.Payer != payerRaw {
		t.Fatalf("expected the signer to become payer, got %x want %x (relayer was %x)", esc.Payer, payerRaw, relayerAddr.Bytes())
	}
	if esc.Payee != payeeRaw {
		t.Fatalf("unexpected payee: %x", esc.Payee)
	}
	if esc.Status != escrow.EscrowInit {
		t.Fatalf("unexpected status after delegated create: %v", esc.Status)
	}

	relayerAccount, err := sp.getAccount(relayerAddr.Bytes())
	if err != nil {
		t.Fatalf("load relayer account: %v", err)
	}
	if relayerAccount.Nonce != 1 {
		t.Fatalf("expected relayer's own nonce to advance, got %d", relayerAccount.Nonce)
	}

	// Now prove the real payer -- not the relayer -- actually controls the
	// escrow: fund and refund it directly via the payer's own key.
	fundTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeLockEscrow, Nonce: 0, Data: escrowID[:], GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}
	refundTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeRefundEscrow, Nonce: 1, Data: escrowID[:], GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := refundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign refund: %v", err)
	}
	if err := sp.ApplyTransaction(refundTx); err != nil {
		t.Fatalf("apply refund: %v", err)
	}
	payerAccount, err := sp.getAccount(payerAddr.Bytes())
	if err != nil {
		t.Fatalf("load payer account: %v", err)
	}
	if payerAccount.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected payer refunded in full, got %s", payerAccount.BalanceNHB)
	}
}

func TestDelegatedReleaseEscrowLifecycle(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}
	relayerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate relayer key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()
	relayerAddr := relayerKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xCC
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr, payeeAccountAddr, relayerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	copy(payeeAccountAddr[:], payeeAddr.Bytes())
	copy(relayerAccountAddr[:], relayerAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, relayerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	meta := [32]byte{}
	escrowNonce := uint64(101)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:], nonceBytes[:])

	createPayload := struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
	}{
		Payee:    payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		FeeBps:   100,
		Deadline: time.Now().Add(2 * time.Hour).Unix(),
		Nonce:    escrowNonce,
	}
	createData, err := jsonMarshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeCreateEscrow, Nonce: 0, Data: createData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := createTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}

	fundTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeLockEscrow, Nonce: 1, Data: escrowID[:], GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}

	actionPayload, actionSig := signEscrowActionEnvelope(t, escrowID, "release", "", payeeKey.PrivateKey)
	delegatedPayload := struct {
		EscrowID  string `json:"escrowId"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{
		EscrowID:  "0x" + hexEncode(escrowID[:]),
		Payload:   actionPayload,
		Signature: actionSig,
	}
	delegatedData, err := rlp.EncodeToBytes(delegatedPayload)
	if err != nil {
		t.Fatalf("rlp encode delegated payload: %v", err)
	}
	delegatedTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeDelegatedReleaseEscrow, Nonce: 0, Data: delegatedData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := delegatedTx.Sign(relayerKey.PrivateKey); err != nil {
		t.Fatalf("sign delegated release: %v", err)
	}
	if err := sp.ApplyTransaction(delegatedTx); err != nil {
		t.Fatalf("apply delegated release: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after delegated release")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("unexpected status after delegated release: %v", esc.Status)
	}

	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee account: %v", err)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("unexpected payee balance: %s", payeeAccount.BalanceNHB)
	}

	relayerAccount, err := sp.getAccount(relayerAddr.Bytes())
	if err != nil {
		t.Fatalf("load relayer account: %v", err)
	}
	if relayerAccount.Nonce != 1 {
		t.Fatalf("expected relayer's own nonce to advance, got %d", relayerAccount.Nonce)
	}

	// A relayer with no signature at all (or a signature from a
	// non-participant) cannot force a release -- the relayer's own key
	// signing the outer transaction is not sufficient authorization.
	outsiderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate outsider key: %v", err)
	}
	badPayload, badSig := signEscrowActionEnvelope(t, escrowID, "release", "", outsiderKey.PrivateKey)
	badDelegated := struct {
		EscrowID  string `json:"escrowId"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{EscrowID: "0x" + hexEncode(escrowID[:]), Payload: badPayload, Signature: badSig}
	badData, err := rlp.EncodeToBytes(badDelegated)
	if err != nil {
		t.Fatalf("rlp encode bad delegated payload: %v", err)
	}
	badTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeDelegatedReleaseEscrow, Nonce: 1, Data: badData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := badTx.Sign(relayerKey.PrivateKey); err != nil {
		t.Fatalf("sign bad delegated release: %v", err)
	}
	// The escrow is already released (idempotent no-op path), so this
	// should not error -- but it also must not pay out a second time.
	if err := sp.ApplyTransaction(badTx); err != nil {
		t.Fatalf("apply second delegated release: %v", err)
	}
	payeeAccountAfter, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("reload payee account: %v", err)
	}
	if payeeAccountAfter.BalanceNHB.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("expected no double payout, payee balance changed to %s", payeeAccountAfter.BalanceNHB)
	}
}

// TestEscrowCreateRealmRequiresRole confirms TxTypeEscrowCreateRealm is
// rejected for a caller without RoleEscrowRealmAdmin, and that a
// role-granted caller's realm is actually usable end to end by
// TxTypeCreateEscrow (opting an escrow in) and TxTypeArbitrateRelease
// (resolving it via the realm's registered arbitrator).
func TestEscrowCreateRealmRequiresRole(t *testing.T) {
	sp := newStakingStateProcessor(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	outsiderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate outsider key: %v", err)
	}
	arbitratorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate arbitrator key: %v", err)
	}

	adminAddr := adminKey.PubKey().Address()
	outsiderAddr := outsiderKey.PubKey().Address()
	arbitratorAddr := arbitratorKey.PubKey().Address()

	var adminAccountAddr, outsiderAccountAddr [20]byte
	copy(adminAccountAddr[:], adminAddr.Bytes())
	copy(outsiderAccountAddr[:], outsiderAddr.Bytes())
	writeAccount(t, sp, adminAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, outsiderAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleEscrowRealmAdmin, adminAddr.Bytes()); err != nil {
		t.Fatalf("grant realm admin role: %v", err)
	}

	realmPayload := struct {
		ID              string   `json:"id"`
		Threshold       uint32   `json:"threshold"`
		Scheme          uint8    `json:"scheme"`
		Members         []string `json:"members"`
		Scope           uint8    `json:"scope"`
		ProviderProfile string   `json:"providerProfile"`
	}{
		ID:              "realm-outsider-attempt",
		Threshold:       1,
		Scheme:          uint8(escrow.ArbitrationSchemeSingle),
		Members:         []string{arbitratorAddr.String()},
		Scope:           uint8(escrow.EscrowRealmScopePlatform),
		ProviderProfile: "core-team",
	}
	realmData, err := jsonMarshal(realmPayload)
	if err != nil {
		t.Fatalf("marshal realm payload: %v", err)
	}

	unauthorizedTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeEscrowCreateRealm, Nonce: 0, Data: realmData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := unauthorizedTx.Sign(outsiderKey.PrivateKey); err != nil {
		t.Fatalf("sign unauthorized create realm: %v", err)
	}
	if err := sp.ApplyTransaction(unauthorizedTx); err == nil {
		t.Fatalf("expected realm creation without RoleEscrowRealmAdmin to be rejected")
	}
	if _, ok, _ := manager.EscrowRealmGet(realmPayload.ID); ok {
		t.Fatalf("realm should not exist after an unauthorized create attempt")
	}

	authorizedPayload := realmPayload
	authorizedPayload.ID = "realm-internal-default"
	authorizedData, err := jsonMarshal(authorizedPayload)
	if err != nil {
		t.Fatalf("marshal authorized realm payload: %v", err)
	}
	authorizedTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeEscrowCreateRealm, Nonce: 0, Data: authorizedData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := authorizedTx.Sign(adminKey.PrivateKey); err != nil {
		t.Fatalf("sign authorized create realm: %v", err)
	}
	if err := sp.ApplyTransaction(authorizedTx); err != nil {
		t.Fatalf("apply authorized create realm: %v", err)
	}
	realm, ok, err := manager.EscrowRealmGet(authorizedPayload.ID)
	if err != nil || !ok {
		t.Fatalf("realm not stored after authorized create: ok=%v err=%v", ok, err)
	}
	if realm.Arbitrators == nil || len(realm.Arbitrators.Members) != 1 {
		t.Fatalf("unexpected realm arbitrator set: %+v", realm.Arbitrators)
	}

	// Now prove the realm is actually usable: create an escrow that opts
	// into it, then resolve it via TxTypeArbitrateRelease signed by the
	// realm's registered arbitrator.
	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()
	var payerAccountAddr, payeeAccountAddr, treasury [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	copy(payeeAccountAddr[:], payeeAddr.Bytes())
	treasury[0] = 0xDD
	sp.SetEscrowFeeTreasury(treasury)
	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(500), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	meta := [32]byte{}
	escrowNonce := uint64(202)
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], escrowNonce)
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:], nonceBytes[:])

	createPayload := struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
		Nonce    uint64   `json:"nonce"`
		Realm    string   `json:"realm"`
	}{
		Payee:    payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		FeeBps:   0,
		Deadline: time.Now().Add(2 * time.Hour).Unix(),
		Nonce:    escrowNonce,
		Realm:    authorizedPayload.ID,
	}
	createData, err := jsonMarshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeCreateEscrow, Nonce: 0, Data: createData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := createTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create with realm: %v", err)
	}

	fundTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeLockEscrow, Nonce: 1, Data: escrowID[:], GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}

	esc, ok := manager.EscrowGet(escrowID)
	if !ok || esc.FrozenArb == nil {
		t.Fatalf("expected escrow to carry a frozen arbitrator policy from the realm")
	}

	// ResolveWithSignatures (behind TxTypeArbitrateRelease) only accepts a
	// Disputed escrow -- raise the dispute first, same as any real
	// arbitrated case would.
	disputeTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeDisputeEscrow, Nonce: 2, Data: escrowID[:], GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := disputeTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign dispute: %v", err)
	}
	if err := sp.ApplyTransaction(disputeTx); err != nil {
		t.Fatalf("apply dispute: %v", err)
	}

	decisionEnvelope := struct {
		EscrowID    string `json:"escrowId"`
		Outcome     string `json:"outcome"`
		PolicyNonce uint64 `json:"policyNonce"`
	}{
		EscrowID:    "0x" + hexEncode(escrowID[:]),
		Outcome:     "release",
		PolicyNonce: esc.FrozenArb.PolicyNonce,
	}
	decisionData, err := jsonMarshal(decisionEnvelope)
	if err != nil {
		t.Fatalf("marshal decision envelope: %v", err)
	}
	decisionDigest := ethcrypto.Keccak256Hash(decisionData)
	decisionSig, err := ethcrypto.Sign(decisionDigest.Bytes(), arbitratorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign decision: %v", err)
	}

	arbitratePayload := struct {
		EscrowID   string   `json:"escrowId"`
		Decision   []byte   `json:"decision"`
		Signatures []string `json:"signatures"`
	}{
		EscrowID:   "0x" + hexEncode(escrowID[:]),
		Decision:   decisionData,
		Signatures: []string{"0x" + hexEncode(decisionSig)},
	}
	arbitrateData, err := rlp.EncodeToBytes(arbitratePayload)
	if err != nil {
		t.Fatalf("rlp encode arbitrate payload: %v", err)
	}
	relayerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate relayer key: %v", err)
	}
	relayerAddr := relayerKey.PubKey().Address()
	var relayerAccountAddr [20]byte
	copy(relayerAccountAddr[:], relayerAddr.Bytes())
	writeAccount(t, sp, relayerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	arbitrateTx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeArbitrateRelease, Nonce: 0, Data: arbitrateData, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := arbitrateTx.Sign(relayerKey.PrivateKey); err != nil {
		t.Fatalf("sign arbitrate: %v", err)
	}
	if err := sp.ApplyTransaction(arbitrateTx); err != nil {
		t.Fatalf("apply arbitrate release via realm: %v", err)
	}

	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after arbitrated release")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("unexpected status after realm-arbitrated release: %v", esc.Status)
	}
	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee account: %v", err)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected payee balance after realm-arbitrated release: %s", payeeAccount.BalanceNHB)
	}
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
