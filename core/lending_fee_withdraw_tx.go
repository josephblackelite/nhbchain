package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// LendingProtocolFeeWithdrawDomainV1 and LendingDeveloperFeeWithdrawDomainV1
// domain-separate the embedded signatures TxTypeLendingWithdrawProtocolFees/
// TxTypeLendingWithdrawDeveloperFees carry from every other signed payload
// this codebase produces, so a signature produced for one purpose can never
// be replayed as authorization for another -- mirrors
// core/swap_admin_tx.go's SwapVoucherReverseDomainV1/
// SwapMarkReconciledDomainV1 precedent exactly.
const (
	LendingProtocolFeeWithdrawDomainV1  = "NHB_LENDING_PROTOCOL_FEE_WITHDRAW_V1"
	LendingDeveloperFeeWithdrawDomainV1 = "NHB_LENDING_DEVELOPER_FEE_WITHDRAW_V1"
)

var (
	// ErrLendingFeeWithdrawInvalidPayload indicates a
	// TxTypeLendingWithdrawProtocolFees/TxTypeLendingWithdrawDeveloperFees
	// transaction's Data payload was missing, malformed, or carried a
	// malformed/undecodable embedded signature.
	ErrLendingFeeWithdrawInvalidPayload = errors.New("lending: invalid fee withdrawal payload")
	// ErrLendingFeeWithdrawUnauthorized indicates the recovered embedded
	// signer did not hold the required authority: RoleLendingProtocolAdmin
	// for a protocol-fee withdrawal, or the target pool's own
	// Market.DeveloperOwner for a developer-fee withdrawal.
	ErrLendingFeeWithdrawUnauthorized = errors.New("lending: unauthorized fee withdrawal")
	// ErrLendingFeeWithdrawPoolNotFound indicates a
	// TxTypeLendingWithdrawDeveloperFees transaction named a pool that has
	// never been created -- unlike protocol-fee withdrawal (which can
	// lazily materialise the implicit default pool), a developer-fee
	// withdrawal's authorization depends on an existing Market's
	// DeveloperOwner, so there is nothing to check authorization against.
	ErrLendingFeeWithdrawPoolNotFound = errors.New("lending: pool not found")
)

// lendingFeeWithdrawPayload is the canonical on-chain payload for both
// TxTypeLendingWithdrawProtocolFees and TxTypeLendingWithdrawDeveloperFees
// transactions -- mirrors core/swap_admin_tx.go's
// swapVoucherReversePayload/swapMarkReconciledPayload shape (JSON, not RLP,
// matching every other senderless admin tx payload in this codebase).
type lendingFeeWithdrawPayload struct {
	PoolID    string `json:"poolId,omitempty"`
	Recipient string `json:"recipient"`
	AmountWei string `json:"amountWei"`
	Signature string `json:"signature"`
}

// LendingProtocolFeeWithdrawSigningHash returns the deterministic digest a
// RoleLendingProtocolAdmin-holding operator key must sign to authorize
// withdrawing a pool's accrued protocol fees -- exported because the
// signature is produced off-chain, by whichever tool submits a raw
// TxTypeLendingWithdrawProtocolFees transaction on that operator's behalf; it
// must reproduce this exact hash byte-for-byte. Mirrors
// core/swap_admin_tx.go's SwapVoucherReverseSigningHash's plain-keccak256,
// pipe-delimited canonical-string convention rather than inventing a new
// signing scheme. The three fields are hashed as the exact trimmed strings
// the submitted payload carries (not a round-tripped/re-serialised amount),
// so the caller must pass the same poolID/recipient/amountWei strings it
// intends to submit.
func LendingProtocolFeeWithdrawSigningHash(poolID, recipient, amountWei string) []byte {
	payload := fmt.Sprintf("%s|poolId=%s|recipient=%s|amountWei=%s", LendingProtocolFeeWithdrawDomainV1, strings.TrimSpace(poolID), strings.TrimSpace(recipient), strings.TrimSpace(amountWei))
	return ethcrypto.Keccak256([]byte(payload))
}

// LendingDeveloperFeeWithdrawSigningHash is
// LendingProtocolFeeWithdrawSigningHash's counterpart for developer-fee
// withdrawal, domain-separated so a developer's signature can never be
// replayed as a protocol-admin withdrawal authorization or vice versa.
func LendingDeveloperFeeWithdrawSigningHash(poolID, recipient, amountWei string) []byte {
	payload := fmt.Sprintf("%s|poolId=%s|recipient=%s|amountWei=%s", LendingDeveloperFeeWithdrawDomainV1, strings.TrimSpace(poolID), strings.TrimSpace(recipient), strings.TrimSpace(amountWei))
	return ethcrypto.Keccak256([]byte(payload))
}

// encodeLendingFeeWithdrawTransaction serialises a poolID/recipient/amount
// and the operator's signature over them into the canonical Data payload
// shared by TxTypeLendingWithdrawProtocolFees and
// TxTypeLendingWithdrawDeveloperFees transactions.
func encodeLendingFeeWithdrawTransaction(poolID, recipient string, amountWei *big.Int, signature []byte) ([]byte, error) {
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		trimmedPool = defaultLendingPoolID
	}
	trimmedRecipient := strings.TrimSpace(recipient)
	if trimmedRecipient == "" {
		return nil, fmt.Errorf("lending: recipient required")
	}
	if amountWei == nil || amountWei.Sign() <= 0 {
		return nil, fmt.Errorf("lending: amount must be positive")
	}
	if len(signature) == 0 {
		return nil, fmt.Errorf("lending: signature required")
	}
	payload := lendingFeeWithdrawPayload{
		PoolID:    trimmedPool,
		Recipient: trimmedRecipient,
		AmountWei: amountWei.String(),
		Signature: "0x" + strings.ToLower(hex.EncodeToString(signature)),
	}
	return json.Marshal(payload)
}

// decodeLendingFeeWithdrawTransaction reconstructs the poolID/recipient/
// amount strings and signature from a TxTypeLendingWithdrawProtocolFees/
// TxTypeLendingWithdrawDeveloperFees transaction's Data payload. It
// deliberately returns the amount as the exact trimmed string the payload
// carried (not a parsed *big.Int) so callers can recompute the signing hash
// byte-for-byte from the same bytes the operator actually signed.
func decodeLendingFeeWithdrawTransaction(data []byte) (poolID, recipient, amountWei string, signature []byte, err error) {
	if len(data) == 0 {
		return "", "", "", nil, fmt.Errorf("%w: payload required", ErrLendingFeeWithdrawInvalidPayload)
	}
	var payload lendingFeeWithdrawPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", "", nil, fmt.Errorf("%w: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	poolID = strings.TrimSpace(payload.PoolID)
	if poolID == "" {
		poolID = defaultLendingPoolID
	}
	recipient = strings.TrimSpace(payload.Recipient)
	if recipient == "" {
		return "", "", "", nil, fmt.Errorf("%w: recipient required", ErrLendingFeeWithdrawInvalidPayload)
	}
	amountWei = strings.TrimSpace(payload.AmountWei)
	amount, ok := new(big.Int).SetString(amountWei, 10)
	if !ok || amount.Sign() <= 0 {
		return "", "", "", nil, fmt.Errorf("%w: invalid amount", ErrLendingFeeWithdrawInvalidPayload)
	}
	sig, sigErr := decodeLendingFeeWithdrawSignature(payload.Signature)
	if sigErr != nil {
		return "", "", "", nil, sigErr
	}
	return poolID, recipient, amountWei, sig, nil
}

func decodeLendingFeeWithdrawSignature(raw string) ([]byte, error) {
	sigHex := strings.TrimSpace(raw)
	if sigHex == "" {
		return nil, fmt.Errorf("%w: signature required", ErrLendingFeeWithdrawInvalidPayload)
	}
	sigHex = strings.TrimPrefix(strings.ToLower(sigHex), "0x")
	signature, err := hex.DecodeString(sigHex)
	if err != nil || len(signature) != 65 {
		return nil, fmt.Errorf("%w: invalid signature", ErrLendingFeeWithdrawInvalidPayload)
	}
	return signature, nil
}

// recoverLendingProtocolFeeAdminSigner recovers the secp256k1 signer of
// hash/signature and confirms it currently holds RoleLendingProtocolAdmin.
// applyLendingWithdrawProtocolFeesTransaction calls this before touching any
// other state, so an unauthorized submission never has any observable side
// effect -- mirrors core/swap_admin_tx.go's recoverSwapAdminSigner.
func recoverLendingProtocolFeeAdminSigner(manager *nhbstate.Manager, hash, signature []byte) ([20]byte, error) {
	var addr [20]byte
	pubKey, err := ethcrypto.SigToPub(hash, signature)
	if err != nil {
		return addr, fmt.Errorf("%w: recover signer: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	copy(addr[:], recovered.Bytes())
	if !manager.HasRole(RoleLendingProtocolAdmin, addr[:]) {
		return addr, ErrLendingFeeWithdrawUnauthorized
	}
	return addr, nil
}

// recoverLendingDeveloperFeeSigner recovers the secp256k1 signer of
// hash/signature and confirms it matches market.DeveloperOwner --
// developer-fee entitlement is inherently per-pool (see
// Market.DeveloperOwner's own doc comment), so this checks the target pool's
// own owner field rather than a generic registered role.
// applyLendingWithdrawDeveloperFeesTransaction calls this (after reading the
// market read-only, before mutating anything) so an unauthorized submission
// never has any observable side effect.
func recoverLendingDeveloperFeeSigner(market *lending.Market, hash, signature []byte) ([20]byte, error) {
	var addr [20]byte
	pubKey, err := ethcrypto.SigToPub(hash, signature)
	if err != nil {
		return addr, fmt.Errorf("%w: recover signer: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	copy(addr[:], recovered.Bytes())
	if market == nil || len(market.DeveloperOwner.Bytes()) == 0 || !bytes.Equal(market.DeveloperOwner.Bytes(), addr[:]) {
		return addr, ErrLendingFeeWithdrawUnauthorized
	}
	return addr, nil
}

// applyLendingWithdrawProtocolFeesTransaction handles
// TxTypeLendingWithdrawProtocolFees -- the first and only caller of
// native/lending's Engine.WithdrawProtocolFees (ledger PL-DC-19: the method
// was fully implemented but completely unreachable before this). See the
// TxType's doc comment (core/types/transaction.go) for why this is
// senderless and role-gated the same way TxTypeSwapVoucherReverse is.
func (sp *StateProcessor) applyLendingWithdrawProtocolFeesTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("lending: transaction required")
	}
	poolID, recipientStr, amountStr, signature, err := decodeLendingFeeWithdrawTransaction(tx.Data)
	if err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	hash := LendingProtocolFeeWithdrawSigningHash(poolID, recipientStr, amountStr)
	adminAddr, err := recoverLendingProtocolFeeAdminSigner(manager, hash, signature)
	if err != nil {
		return err
	}

	recipientAddr, err := crypto.DecodeAddress(recipientStr)
	if err != nil {
		return fmt.Errorf("%w: invalid recipient: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		return fmt.Errorf("%w: invalid amount", ErrLendingFeeWithdrawInvalidPayload)
	}

	engine, _, err := sp.lendingEngine(poolID)
	if err != nil {
		return err
	}
	withdrawn, err := engine.WithdrawProtocolFees(recipientAddr, amount)
	if err != nil {
		return err
	}

	if evt := (events.LendingProtocolFeesWithdrawn{
		PoolID:    poolID,
		Admin:     adminAddr,
		Recipient: recipientAddr,
		Amount:    withdrawn,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

// applyLendingWithdrawDeveloperFeesTransaction handles
// TxTypeLendingWithdrawDeveloperFees -- the first and only caller of
// native/lending's Engine.WithdrawDeveloperFees (ledger PL-DC-19). Unlike the
// protocol-fee withdrawal above, authorization is checked against the
// target pool's own Market.DeveloperOwner (read read-only, via
// nhbstate.Manager directly) BEFORE sp.lendingEngine is ever called -- that
// call can lazily create/migrate pool state as a side effect of ordinary
// use, so checking authorization first keeps an unauthorized submission's
// side effects at zero, matching every other admin tx in this family.
func (sp *StateProcessor) applyLendingWithdrawDeveloperFeesTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("lending: transaction required")
	}
	poolID, recipientStr, amountStr, signature, err := decodeLendingFeeWithdrawTransaction(tx.Data)
	if err != nil {
		return err
	}

	manager := nhbstate.NewManager(sp.Trie)
	market, ok, err := manager.LendingGetMarket(poolID)
	if err != nil {
		return err
	}
	if !ok || market == nil {
		return ErrLendingFeeWithdrawPoolNotFound
	}

	hash := LendingDeveloperFeeWithdrawSigningHash(poolID, recipientStr, amountStr)
	adminAddr, err := recoverLendingDeveloperFeeSigner(market, hash, signature)
	if err != nil {
		return err
	}

	recipientAddr, err := crypto.DecodeAddress(recipientStr)
	if err != nil {
		return fmt.Errorf("%w: invalid recipient: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	amount, ok2 := new(big.Int).SetString(amountStr, 10)
	if !ok2 {
		return fmt.Errorf("%w: invalid amount", ErrLendingFeeWithdrawInvalidPayload)
	}

	engine, _, err := sp.lendingEngine(poolID)
	if err != nil {
		return err
	}
	withdrawn, err := engine.WithdrawDeveloperFees(recipientAddr, amount)
	if err != nil {
		return err
	}

	if evt := (events.LendingDeveloperFeesWithdrawn{
		PoolID:    poolID,
		Admin:     adminAddr,
		Recipient: recipientAddr,
		Amount:    withdrawn,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}
