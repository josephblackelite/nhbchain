package core

import (
	"bytes"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
)

// applyBuybackAsk handles TxTypeBuybackAsk: a ZNHB holder's market ask into
// the current epoch's treasury buyback. The seller's ZNHB is escrowed
// immediately (moved into the buyback accrual/escrow module account) rather
// than left in the seller's own balance until settlement -- this locks the
// commitment up front, the same way this codebase's escrow and
// swap-voucher flows lock funds before their eventual settlement, and rules
// out a seller submitting more asks than they can actually cover.
func (sp *StateProcessor) applyBuybackAsk(tx *types.Transaction, sender []byte, senderAccount *types.Account) error {
	if !sp.hasBuybackConfig || sp.buybackAccrualAddr.Bytes() == nil {
		return fmt.Errorf("buybackAsk: treasury buyback engine is not configured for this network")
	}
	var payload struct {
		ZNHBAmount *big.Int `json:"znhbAmount"`
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("buybackAsk: decode payload: %w", err)
	}
	if payload.ZNHBAmount == nil || payload.ZNHBAmount.Sign() <= 0 {
		return fmt.Errorf("buybackAsk: znhbAmount must be positive")
	}

	epochNumber, ok := sp.currentBuybackEpoch()
	if !ok {
		return fmt.Errorf("buybackAsk: epoch scheduling is not enabled on this network")
	}

	if sp.hasAdminWallet && bytes.Equal(sender, sp.adminWallet[:]) {
		return fmt.Errorf("buybackAsk: the treasury admin wallet may not sell into its own buyback")
	}
	if bytes.Equal(sender, sp.buybackAccrualAddr.Bytes()) {
		return fmt.Errorf("buybackAsk: the buyback escrow account may not submit asks")
	}
	// Insider-seller protection: any address currently holding bonded
	// validator stake is barred from selling into a buyback whose
	// settlement its own liveness helps finalize. Matches the barred-seller
	// protection carried into this engine's design (see the buyback
	// package's Config doc comment for the reference-price signer quorum's
	// equivalent isolation rationale).
	if senderAccount.Stake != nil && senderAccount.Stake.Sign() > 0 {
		return fmt.Errorf("buybackAsk: validator-bonded addresses may not sell into the treasury buyback")
	}

	if senderAccount.BalanceZNHB == nil || senderAccount.BalanceZNHB.Cmp(payload.ZNHBAmount) < 0 {
		return fmt.Errorf("buybackAsk: insufficient ZNHB balance")
	}

	escrowAcc, err := sp.getAccount(sp.buybackAccrualAddr.Bytes())
	if err != nil {
		return fmt.Errorf("buybackAsk: load escrow account: %w", err)
	}
	if escrowAcc.BalanceZNHB == nil {
		escrowAcc.BalanceZNHB = big.NewInt(0)
	}

	senderAccount.BalanceZNHB = new(big.Int).Sub(senderAccount.BalanceZNHB, payload.ZNHBAmount)
	senderAccount.Nonce++
	escrowAcc.BalanceZNHB = new(big.Int).Add(escrowAcc.BalanceZNHB, payload.ZNHBAmount)

	if err := sp.setAccount(sender, senderAccount); err != nil {
		return fmt.Errorf("buybackAsk: persist seller: %w", err)
	}
	if err := sp.setAccount(sp.buybackAccrualAddr.Bytes(), escrowAcc); err != nil {
		return fmt.Errorf("buybackAsk: persist escrow account: %w", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var sellerAddr [20]byte
	copy(sellerAddr[:], sender)
	if err := manager.BuybackAppendAsk(epochNumber, nhbstate.BuybackAskRecord{Seller: sellerAddr, AmountWei: payload.ZNHBAmount}); err != nil {
		return fmt.Errorf("buybackAsk: record ask: %w", err)
	}

	if evt := (events.BuybackAskSubmitted{Seller: sellerAddr, Epoch: epochNumber, AmountWei: payload.ZNHBAmount}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

// applyBuybackRefPrice handles TxTypeBuybackRefPrice: a senderless,
// envelope-unsigned transaction (like TxTypeMint) whose payload carries its
// own M-of-N signature bundle from the genesis-declared reference-price
// signer quorum instead of a single envelope signature. Verified and stored
// once per epoch -- the treasury buyback settlement (settleBuybackEpoch)
// reads it back at that epoch's finalization.
func (sp *StateProcessor) applyBuybackRefPrice(tx *types.Transaction) error {
	if !sp.hasBuybackConfig {
		return fmt.Errorf("buybackRefPrice: treasury buyback engine is not configured for this network")
	}
	var payload struct {
		RateNum    *big.Int
		RateDenom  *big.Int
		Epoch      uint64
		Timestamp  uint64
		Signatures [][]byte
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("buybackRefPrice: decode payload: %w", err)
	}
	if payload.RateNum == nil || payload.RateNum.Sign() <= 0 || payload.RateDenom == nil || payload.RateDenom.Sign() <= 0 {
		return fmt.Errorf("buybackRefPrice: rate must be a positive fraction")
	}

	epochNumber, ok := sp.currentBuybackEpoch()
	if !ok {
		return fmt.Errorf("buybackRefPrice: epoch scheduling is not enabled on this network")
	}
	if payload.Epoch != epochNumber {
		return fmt.Errorf("buybackRefPrice: submitted epoch %d does not match the current open epoch %d", payload.Epoch, epochNumber)
	}

	manager := nhbstate.NewManager(sp.Trie)
	if _, exists, err := manager.BuybackRefPriceForEpoch(epochNumber); err != nil {
		return fmt.Errorf("buybackRefPrice: check existing record: %w", err)
	} else if exists {
		return fmt.Errorf("buybackRefPrice: a reference price has already been recorded for epoch %d", epochNumber)
	}

	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(payload.RateNum, payload.RateDenom),
		Epoch:     payload.Epoch,
		Timestamp: time.Unix(int64(payload.Timestamp), 0).UTC(),
	}
	signers, err := buyback.VerifyReferencePrice(sp.buybackConfig, rp, payload.Signatures)
	if err != nil {
		return fmt.Errorf("buybackRefPrice: %w", err)
	}

	rec := nhbstate.BuybackRefPriceRecord{
		Epoch:       payload.Epoch,
		RateNum:     new(big.Int).Set(payload.RateNum),
		RateDenom:   new(big.Int).Set(payload.RateDenom),
		TimestampAt: payload.Timestamp,
		Signers:     signers,
	}
	if err := manager.BuybackSetRefPriceForEpoch(epochNumber, rec); err != nil {
		return fmt.Errorf("buybackRefPrice: persist record: %w", err)
	}

	if evt := (events.BuybackRefPriceRecorded{
		Epoch:       epochNumber,
		RateNum:     payload.RateNum,
		RateDenom:   payload.RateDenom,
		SignerCount: len(signers),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}
