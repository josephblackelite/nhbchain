package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// SwapVoucherReverseDomainV1 and SwapMarkReconciledDomainV1 domain-separate
// the embedded signatures TxTypeSwapVoucherReverse/TxTypeSwapMarkReconciled
// carry from every other signed payload this codebase produces (voucher
// mints, price proofs, escrow action envelopes, ...), so a signature
// produced for one purpose can never be replayed as authorization for
// another.
const (
	SwapVoucherReverseDomainV1 = "NHB_SWAP_VOUCHER_REVERSE_V1"
	SwapMarkReconciledDomainV1 = "NHB_SWAP_MARK_RECONCILED_V1"
)

// swapVoucherReversePayload is the canonical on-chain payload for a
// TxTypeSwapVoucherReverse transaction.
type swapVoucherReversePayload struct {
	ProviderTxID string `json:"providerTxId"`
	Signature    string `json:"signature"`
}

// swapMarkReconciledPayload is the canonical on-chain payload for a
// TxTypeSwapMarkReconciled transaction.
type swapMarkReconciledPayload struct {
	ProviderTxIDs []string `json:"providerTxIds"`
	Signature     string   `json:"signature"`
}

// SwapVoucherReverseSigningHash returns the deterministic digest a
// RoleSwapAdmin-holding operator key must sign to authorize reversing a
// minted voucher -- exported because the signature is produced off-chain,
// by whichever tool submits swap_voucher_reverse (or a raw
// TxTypeSwapVoucherReverse transaction) on that operator's behalf; it must
// reproduce this exact hash byte-for-byte. Mirrors swap.VoucherV1.Hash()'s
// own plain-keccak256, pipe-delimited canonical-string convention
// (native/swap/voucher.go) rather than inventing a new signing scheme.
func SwapVoucherReverseSigningHash(providerTxID string) []byte {
	payload := fmt.Sprintf("%s|providerTxId=%s", SwapVoucherReverseDomainV1, strings.TrimSpace(providerTxID))
	return ethcrypto.Keccak256([]byte(payload))
}

// SwapMarkReconciledSigningHash is SwapVoucherReverseSigningHash's
// counterpart for a batch of providerTxIds. The caller must pass IDs
// already trimmed, blank-filtered, and in the exact order they will be
// persisted -- this hash is order-sensitive, matching the deterministic
// canonical-string convention every other signed payload in this codebase
// already follows.
func SwapMarkReconciledSigningHash(providerTxIDs []string) []byte {
	payload := fmt.Sprintf("%s|providerTxIds=%s", SwapMarkReconciledDomainV1, strings.Join(providerTxIDs, ","))
	return ethcrypto.Keccak256([]byte(payload))
}

// encodeSwapVoucherReverseTransaction serialises a providerTxId and its
// operator signature into the canonical Data payload for a
// TxTypeSwapVoucherReverse transaction.
func encodeSwapVoucherReverseTransaction(providerTxID string, signature []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(providerTxID)
	if trimmed == "" {
		return nil, fmt.Errorf("swap: providerTxId required")
	}
	if len(signature) == 0 {
		return nil, fmt.Errorf("swap: signature required")
	}
	payload := swapVoucherReversePayload{
		ProviderTxID: trimmed,
		Signature:    "0x" + strings.ToLower(hex.EncodeToString(signature)),
	}
	return json.Marshal(payload)
}

// decodeSwapVoucherReverseTransaction reconstructs the providerTxId and
// signature from a TxTypeSwapVoucherReverse transaction's Data payload.
func decodeSwapVoucherReverseTransaction(data []byte) (string, []byte, error) {
	if len(data) == 0 {
		return "", nil, fmt.Errorf("%w: payload required", ErrSwapAdminInvalidPayload)
	}
	var payload swapVoucherReversePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrSwapAdminInvalidPayload, err)
	}
	providerTxID := strings.TrimSpace(payload.ProviderTxID)
	if providerTxID == "" {
		return "", nil, fmt.Errorf("%w: providerTxId required", ErrSwapAdminInvalidPayload)
	}
	signature, err := decodeSwapAdminSignature(payload.Signature)
	if err != nil {
		return "", nil, err
	}
	return providerTxID, signature, nil
}

// encodeSwapMarkReconciledTransaction serialises a batch of providerTxIds
// and the operator signature covering them into the canonical Data payload
// for a TxTypeSwapMarkReconciled transaction.
func encodeSwapMarkReconciledTransaction(providerTxIDs []string, signature []byte) ([]byte, error) {
	trimmed := make([]string, 0, len(providerTxIDs))
	for _, id := range providerTxIDs {
		if t := strings.TrimSpace(id); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("swap: at least one providerTxId required")
	}
	if len(signature) == 0 {
		return nil, fmt.Errorf("swap: signature required")
	}
	payload := swapMarkReconciledPayload{
		ProviderTxIDs: trimmed,
		Signature:     "0x" + strings.ToLower(hex.EncodeToString(signature)),
	}
	return json.Marshal(payload)
}

// decodeSwapMarkReconciledTransaction reconstructs the providerTxId batch
// and signature from a TxTypeSwapMarkReconciled transaction's Data payload.
func decodeSwapMarkReconciledTransaction(data []byte) ([]string, []byte, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("%w: payload required", ErrSwapAdminInvalidPayload)
	}
	var payload swapMarkReconciledPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrSwapAdminInvalidPayload, err)
	}
	trimmed := make([]string, 0, len(payload.ProviderTxIDs))
	for _, id := range payload.ProviderTxIDs {
		if t := strings.TrimSpace(id); t != "" {
			trimmed = append(trimmed, t)
		}
	}
	if len(trimmed) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one providerTxId required", ErrSwapAdminInvalidPayload)
	}
	signature, err := decodeSwapAdminSignature(payload.Signature)
	if err != nil {
		return nil, nil, err
	}
	return trimmed, signature, nil
}

func decodeSwapAdminSignature(raw string) ([]byte, error) {
	sigHex := strings.TrimSpace(raw)
	if sigHex == "" {
		return nil, fmt.Errorf("%w: signature required", ErrSwapAdminInvalidPayload)
	}
	sigHex = strings.TrimPrefix(strings.ToLower(sigHex), "0x")
	signature, err := hex.DecodeString(sigHex)
	if err != nil || len(signature) != 65 {
		return nil, fmt.Errorf("%w: invalid signature", ErrSwapAdminInvalidPayload)
	}
	return signature, nil
}

// recoverSwapAdminSigner recovers the secp256k1 signer of hash/signature and
// confirms it currently holds RoleSwapAdmin. Both
// applySwapVoucherReverseTransaction and applySwapMarkReconciledTransaction
// call this before touching any other state, so an unauthorized submission
// never has any observable side effect.
func recoverSwapAdminSigner(manager *nhbstate.Manager, hash, signature []byte) ([20]byte, error) {
	var addr [20]byte
	pubKey, err := ethcrypto.SigToPub(hash, signature)
	if err != nil {
		return addr, fmt.Errorf("%w: recover signer: %v", ErrSwapAdminInvalidPayload, err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	copy(addr[:], recovered.Bytes())
	if !manager.HasRole(RoleSwapAdmin, addr[:]) {
		return addr, ErrSwapAdminUnauthorized
	}
	return addr, nil
}

// applySwapVoucherReverseTransaction deterministically executes a
// TxTypeSwapVoucherReverse transaction. It is the consensus-side
// counterpart of the retired Node.SwapReverseVoucher direct-write RPC
// handler (NHB-AUDIT-C4 follow-up): every check below, including the
// RoleSwapAdmin authorization check the old RPC handler left entirely to
// the HTTP layer's bearer-auth middleware, now runs identically on every
// validator against the same block-execution state.
func (sp *StateProcessor) applySwapVoucherReverseTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("swap: transaction required")
	}
	if err := nativecommon.Guard(sp.pauses, moduleSwap); err != nil {
		return err
	}
	providerTxID, signature, err := decodeSwapVoucherReverseTransaction(tx.Data)
	if err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	adminAddr, err := recoverSwapAdminSigner(manager, SwapVoucherReverseSigningHash(providerTxID), signature)
	if err != nil {
		return err
	}

	ledger := swap.NewLedger(manager)
	// See applySwapVoucherMintTransaction's identical clock-wiring comment
	// (core/swap_voucher_tx.go): Ledger.Put/MarkReversed must never observe
	// wall-clock time during consensus execution.
	ledger.SetClock(func() time.Time { return sp.blockTimestamp() })
	record, ok, err := ledger.Get(providerTxID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSwapVoucherReversalNotFound
	}
	switch strings.ToLower(strings.TrimSpace(record.Status)) {
	case swap.VoucherStatusReversed:
		return ErrSwapVoucherAlreadyReversed
	case swap.VoucherStatusMinted:
		// proceed
	default:
		return ErrSwapVoucherNotMinted
	}
	if record.MintAmountWei == nil || record.MintAmountWei.Sign() <= 0 {
		return fmt.Errorf("%w: voucher amount invalid", ErrSwapAdminInvalidPayload)
	}
	if strings.ToUpper(strings.TrimSpace(record.Token)) != "ZNHB" {
		return ErrSwapInvalidToken
	}

	// Vouchers are minted onto Account.BalanceZNHB and drawn from the admin
	// wallet's Sale Pool (applySwapVoucherMintTransaction), so the reversal
	// runs on those same account balances and pool counters rather than the
	// separate per-token ledger. Each address is loaded once and persisted
	// once, and a recipient that is also the sink is rejected because the
	// reversal would move nothing.
	sink := sp.swapRefundSink
	if bytes.Equal(sink[:], record.Recipient[:]) {
		return fmt.Errorf("%w: voucher recipient is the refund sink", ErrSwapAdminInvalidPayload)
	}
	recipientAccount, err := sp.getAccount(record.Recipient[:])
	if err != nil {
		return fmt.Errorf("swap: load recipient: %w", err)
	}
	if recipientAccount.BalanceZNHB == nil || recipientAccount.BalanceZNHB.Cmp(record.MintAmountWei) < 0 {
		return ErrSwapReversalInsufficientBalance
	}
	sinkAccount, err := sp.getAccount(sink[:])
	if err != nil {
		return fmt.Errorf("swap: load refund sink: %w", err)
	}
	if sinkAccount.BalanceZNHB == nil {
		sinkAccount.BalanceZNHB = big.NewInt(0)
	}
	// Only a refund that lands back in the admin wallet rejoins the Sale Pool;
	// the Sale Pool and Reward Pool sub-ledgers always sum to the admin
	// wallet's own ZNHB (CheckZNHBSupplyInvariant), so a refund to any other
	// address must leave them untouched.
	restoresPool := sp.hasAdminWallet && bytes.Equal(sink[:], sp.adminWallet[:])
	var salePoolBalance, cumulative *big.Int
	if restoresPool {
		if salePoolBalance, err = manager.ZNHBSalePoolBalance(); err != nil {
			return fmt.Errorf("swap: load sale pool balance: %w", err)
		}
		if cumulative, err = manager.ZNHBCumulativeSaleDistributed(); err != nil {
			return fmt.Errorf("swap: load cumulative sale distributed: %w", err)
		}
	}

	recipientAccount.BalanceZNHB = new(big.Int).Sub(recipientAccount.BalanceZNHB, record.MintAmountWei)
	sinkAccount.BalanceZNHB = new(big.Int).Add(sinkAccount.BalanceZNHB, record.MintAmountWei)
	if err := sp.setAccount(record.Recipient[:], recipientAccount); err != nil {
		return fmt.Errorf("swap: persist recipient: %w", err)
	}
	if err := sp.setAccount(sink[:], sinkAccount); err != nil {
		return fmt.Errorf("swap: persist refund sink: %w", err)
	}
	if restoresPool {
		if err := manager.ZNHBSetSalePoolBalance(new(big.Int).Add(salePoolBalance, record.MintAmountWei)); err != nil {
			return fmt.Errorf("swap: update sale pool balance: %w", err)
		}
		newCumulative := new(big.Int).Sub(cumulative, record.MintAmountWei)
		if newCumulative.Sign() < 0 {
			newCumulative = big.NewInt(0)
		}
		if err := manager.ZNHBSetCumulativeSaleDistributed(newCumulative); err != nil {
			return fmt.Errorf("swap: rewind cumulative sale distributed: %w", err)
		}
	}
	if err := ledger.MarkReversed(providerTxID); err != nil {
		return err
	}

	if evt := (events.SwapVoucherReversed{
		ProviderTxID: providerTxID,
		Admin:        adminAddr,
		Recipient:    record.Recipient,
		Token:        record.Token,
		Amount:       new(big.Int).Set(record.MintAmountWei),
		ObservedAt:   sp.blockTimestamp().Unix(),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

// applySwapMarkReconciledTransaction deterministically executes a
// TxTypeSwapMarkReconciled transaction. It is the consensus-side
// counterpart of the retired Node.SwapMarkReconciled direct-write RPC
// handler -- see applySwapVoucherReverseTransaction's doc comment.
func (sp *StateProcessor) applySwapMarkReconciledTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("swap: transaction required")
	}
	if err := nativecommon.Guard(sp.pauses, moduleSwap); err != nil {
		return err
	}
	providerTxIDs, signature, err := decodeSwapMarkReconciledTransaction(tx.Data)
	if err != nil {
		return err
	}
	manager := nhbstate.NewManager(sp.Trie)
	if _, err := recoverSwapAdminSigner(manager, SwapMarkReconciledSigningHash(providerTxIDs), signature); err != nil {
		return err
	}

	ledger := swap.NewLedger(manager)
	if err := ledger.MarkReconciled(providerTxIDs); err != nil {
		return err
	}
	if evt := (events.SwapTreasuryReconciled{
		VoucherIDs: providerTxIDs,
		ObservedAt: sp.blockTimestamp().Unix(),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}
