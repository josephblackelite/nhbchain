package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// swapVoucherMintPriceProofPayload is the wire encoding for the price proof
// embedded in a TxTypeSwapVoucherMint transaction's Data payload.
type swapVoucherMintPriceProofPayload struct {
	Domain    string `json:"domain"`
	Provider  string `json:"provider"`
	Pair      string `json:"pair"`
	Rate      string `json:"rate"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature,omitempty"`
}

// swapVoucherMintPayload is the canonical on-chain payload for a
// TxTypeSwapVoucherMint transaction. It mirrors swap.VoucherSubmission (the
// RPC-layer type historically consumed by the now-retired direct-write
// Node.SwapSubmitVoucher path) so the same submission the fiat gateway
// already builds can be carried unmodified inside a signed, gossiped,
// consensus-ordered transaction.
type swapVoucherMintPayload struct {
	Voucher      swap.VoucherV1                    `json:"voucher"`
	Signature    string                            `json:"signature"`
	Provider     string                            `json:"provider"`
	ProviderTxID string                            `json:"providerTxId"`
	Username     string                            `json:"username,omitempty"`
	Address      string                            `json:"address,omitempty"`
	USDAmount    string                            `json:"usdAmount,omitempty"`
	PriceProof   *swapVoucherMintPriceProofPayload `json:"priceProof,omitempty"`
}

// encodeSwapVoucherMintTransaction serialises a voucher submission into the
// canonical Data payload for a TxTypeSwapVoucherMint transaction.
func encodeSwapVoucherMintTransaction(submission *swap.VoucherSubmission) ([]byte, error) {
	if submission == nil || submission.Voucher == nil {
		return nil, fmt.Errorf("swap: voucher required")
	}
	if len(submission.Signature) == 0 {
		return nil, fmt.Errorf("swap: signature required")
	}
	payload := swapVoucherMintPayload{
		Voucher:      *submission.Voucher,
		Signature:    "0x" + strings.ToLower(hex.EncodeToString(submission.Signature)),
		Provider:     strings.TrimSpace(submission.Provider),
		ProviderTxID: strings.TrimSpace(submission.ProviderTxID),
		Username:     strings.TrimSpace(submission.Username),
		Address:      strings.TrimSpace(submission.Address),
		USDAmount:    strings.TrimSpace(submission.USDAmount),
	}
	if submission.PriceProof != nil {
		proof := submission.PriceProof
		rate := ""
		if proof.Rate != nil {
			rate = proof.Rate.FloatString(18)
		}
		pair := strings.TrimSpace(proof.Base) + "/" + strings.TrimSpace(proof.Quote)
		payload.PriceProof = &swapVoucherMintPriceProofPayload{
			Domain:    strings.TrimSpace(proof.Domain),
			Provider:  strings.TrimSpace(proof.Provider),
			Pair:      pair,
			Rate:      rate,
			Timestamp: proof.Timestamp.UTC().Unix(),
		}
		if len(proof.Signature) > 0 {
			payload.PriceProof.Signature = "0x" + strings.ToLower(hex.EncodeToString(proof.Signature))
		}
	}
	return json.Marshal(payload)
}

// decodeSwapVoucherMintTransaction reconstructs the voucher submission from a
// TxTypeSwapVoucherMint transaction's Data payload.
func decodeSwapVoucherMintTransaction(data []byte) (*swap.VoucherSubmission, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: payload required", ErrSwapVoucherInvalidPayload)
	}
	var payload swapVoucherMintPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSwapVoucherInvalidPayload, err)
	}
	sigHex := strings.TrimSpace(payload.Signature)
	if sigHex == "" {
		return nil, fmt.Errorf("%w: signature required", ErrSwapVoucherInvalidPayload)
	}
	sigHex = strings.TrimPrefix(strings.ToLower(sigHex), "0x")
	signature, err := hex.DecodeString(sigHex)
	if err != nil || len(signature) == 0 {
		return nil, fmt.Errorf("%w: invalid signature", ErrSwapVoucherInvalidPayload)
	}
	voucher := payload.Voucher
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signature,
		Provider:     strings.TrimSpace(payload.Provider),
		ProviderTxID: strings.TrimSpace(payload.ProviderTxID),
		Username:     strings.TrimSpace(payload.Username),
		Address:      strings.TrimSpace(payload.Address),
		USDAmount:    strings.TrimSpace(payload.USDAmount),
	}
	if payload.PriceProof != nil {
		p := payload.PriceProof
		var proofSig []byte
		if trimmed := strings.TrimSpace(p.Signature); trimmed != "" {
			trimmed = strings.TrimPrefix(strings.ToLower(trimmed), "0x")
			decoded, err := hex.DecodeString(trimmed)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid priceProof signature", ErrSwapVoucherInvalidPayload)
			}
			proofSig = decoded
		}
		proof, err := swap.NewPriceProof(p.Domain, p.Provider, p.Pair, p.Rate, p.Timestamp, proofSig)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid priceProof: %v", ErrSwapVoucherInvalidPayload, err)
		}
		submission.PriceProof = proof
	}
	return submission, nil
}

// applySwapVoucherMintTransaction deterministically executes a
// TxTypeSwapVoucherMint transaction. It is the consensus-side counterpart of
// the retired Node.SwapSubmitVoucher direct-write RPC handler: every check
// below runs identically on every validator against the same block-execution
// state, so the duplicate-submission guards (ledger.Exists /
// HasSeenSwapNonce) are real, network-wide-agreed checks instead of a single
// validator's local view.
//
// Unlike the retired RPC path, this function never calls a live price
// oracle -- oracle.GetRate() is a non-deterministic network call and cannot
// run inside consensus execution, where every validator must compute the
// identical result from the same inputs. Instead the mint amount and
// slippage gate are derived from the transaction's embedded price proof,
// and -- critically -- that price proof's signature is ALWAYS verified
// against a registered swap.SwapPriceSigner for this transaction type,
// regardless of the general swap.risk.PriceProofSignatureRequired toggle.
// See the SwapSubmitVoucher / applySwapVoucherMintTransaction commit message
// for why: priceProof is not covered by the voucher's own MintAuthority
// signature, and unlike other transaction types this one carries no
// envelope signature either, so a mandatory, deterministically-verified
// price proof signature is the only authenticity anchor available for the
// rate used in the slippage check.
func (sp *StateProcessor) applySwapVoucherMintTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("swap: transaction required")
	}
	submission, err := decodeSwapVoucherMintTransaction(tx.Data)
	if err != nil {
		return err
	}
	voucher := submission.Voucher
	if voucher == nil {
		return fmt.Errorf("%w: voucher required", ErrSwapVoucherInvalidPayload)
	}
	if err := nativecommon.Guard(sp.pauses, moduleSwap); err != nil {
		return err
	}
	if strings.TrimSpace(voucher.Domain) != swap.VoucherDomainV1 {
		return ErrSwapInvalidDomain
	}
	if voucher.ChainID != sp.swapVoucherChainID {
		return ErrSwapInvalidChainID
	}
	blockTime := sp.blockTimestamp()
	if voucher.Expiry <= blockTime.Unix() {
		return ErrSwapExpired
	}
	if voucher.Amount == nil || voucher.Amount.Sign() <= 0 {
		return fmt.Errorf("%w: invalid amount", ErrSwapVoucherInvalidPayload)
	}
	if len(voucher.Nonce) == 0 {
		return fmt.Errorf("%w: nonce required", ErrSwapVoucherInvalidPayload)
	}
	if voucher.Recipient == ([20]byte{}) {
		return fmt.Errorf("%w: recipient required", ErrSwapVoucherInvalidPayload)
	}
	orderID := strings.TrimSpace(voucher.OrderID)
	if orderID == "" {
		return fmt.Errorf("%w: orderId required", ErrSwapVoucherInvalidPayload)
	}
	provider := strings.TrimSpace(submission.Provider)
	if provider == "" {
		return fmt.Errorf("%w: provider required", ErrSwapVoucherInvalidPayload)
	}
	providerTxID := strings.TrimSpace(submission.ProviderTxID)
	if providerTxID == "" {
		return fmt.Errorf("%w: providerTxId required", ErrSwapVoucherInvalidPayload)
	}
	signature := append([]byte(nil), submission.Signature...)
	if len(signature) != 65 {
		return ErrSwapInvalidSignature
	}
	token := strings.ToUpper(strings.TrimSpace(voucher.Token))
	if token != "ZNHB" {
		return ErrSwapInvalidToken
	}
	hash := voucher.Hash()
	if len(hash) == 0 {
		return ErrSwapInvalidSignature
	}
	pubKey, err := ethcrypto.SigToPub(hash, signature)
	if err != nil {
		return fmt.Errorf("%w: recover signer: %v", ErrSwapVoucherInvalidPayload, err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)

	cfg := sp.swapConfig
	if !cfg.IsFiatAllowed(voucher.Fiat) {
		return ErrSwapUnsupportedFiat
	}
	riskParams, err := cfg.Risk.Parameters()
	if err != nil {
		return err
	}

	manager := nhbstate.NewManager(sp.Trie)
	riskEngine := swap.NewRiskEngine(manager)
	riskEngine.SetClock(func() time.Time { return sp.blockTimestamp() })
	sanctionsLog := swap.NewSanctionsLog(manager)
	sanctionsLog.SetClock(func() time.Time { return sp.blockTimestamp() })
	priceEngine := swap.NewPriceProofEngine(manager, cfg.MaxQuoteAge(), cfg.PriceProofMaxDeviationBps)
	priceEngine.SetClock(func() time.Time { return sp.blockTimestamp() })
	// FIX (silently-weakened security control): this transaction type has no
	// other authenticity anchor for its embedded rate, so a valid, registered
	// signer's signature over the price proof is always required here --
	// regardless of the general swap.risk.PriceProofSignatureRequired
	// toggle, which remains an operator-facing knob for the legacy RPC-era
	// behaviour only. Verify() below runs pure, deterministic secp256k1
	// signature recovery against on-chain-registered signer state
	// (SwapPriceSigner) -- no live network call, fully consensus-safe.
	priceEngine.RequireSignature(true)

	priceProof := submission.PriceProof
	if priceProof == nil {
		return ErrSwapPriceProofRequired
	}
	if err := priceEngine.Verify(priceProof, provider, token); err != nil {
		switch {
		case errors.Is(err, swap.ErrPriceProofNil), errors.Is(err, swap.ErrPriceProofSignatureMissing):
			return ErrSwapPriceProofRequired
		case errors.Is(err, swap.ErrPriceProofDomain),
			errors.Is(err, swap.ErrPriceProofPair),
			errors.Is(err, swap.ErrPriceProofProviderMismatch),
			errors.Is(err, swap.ErrPriceProofSignatureInvalid):
			return ErrSwapPriceProofInvalid
		case errors.Is(err, swap.ErrPriceProofSignerUnknown):
			return ErrSwapPriceProofSignerUnknown
		case errors.Is(err, swap.ErrPriceProofStale):
			return ErrSwapPriceProofStale
		case errors.Is(err, swap.ErrPriceProofDeviation):
			return ErrSwapPriceProofDeviation
		default:
			return fmt.Errorf("swap: price proof verify: %w", err)
		}
	}
	proofID, err := priceProof.ID()
	if err != nil {
		return fmt.Errorf("swap: price proof id: %w", err)
	}

	if len(cfg.Providers.Allow) > 0 && !cfg.Providers.IsAllowed(provider) {
		if evt := (events.SwapLimitAlert{
			Address:      voucher.Recipient,
			Provider:     provider,
			ProviderTxID: providerTxID,
			Limit:        "provider",
			Amount:       new(big.Int).Set(voucher.Amount),
		}).Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapProviderNotAllowed
	}

	if riskParams.SanctionsCheckEnabled {
		sanctionsParams, err := cfg.Sanctions.Parameters()
		if err != nil {
			return err
		}
		checker := sanctionsParams.Checker()
		if checker != nil && !checker(voucher.Recipient) {
			if err := sanctionsLog.RecordFailure(voucher.Recipient, provider, providerTxID); err != nil {
				return fmt.Errorf("swap: record sanctions failure: %w", err)
			}
			if evt := (events.SwapSanctionAlert{
				Address:      voucher.Recipient,
				Provider:     provider,
				ProviderTxID: providerTxID,
			}).Event(); evt != nil {
				sp.AppendEvent(evt)
			}
			return ErrSwapSanctioned
		}
	}

	violation, err := riskEngine.CheckLimits(voucher.Recipient, voucher.Amount, riskParams)
	if err != nil {
		return err
	}
	if violation != nil {
		return sp.emitSwapRiskViolation(violation, provider, providerTxID, voucher)
	}

	tokenMeta, err := manager.Token(token)
	if err != nil {
		return err
	}
	if tokenMeta == nil {
		return ErrSwapInvalidToken
	}
	if tokenMeta.MintPaused {
		return ErrSwapMintPaused
	}
	if len(tokenMeta.MintAuthority) != 20 {
		// Not yet configured, not "wrong" -- an admin could set MintAuthority
		// after this transaction was submitted, so this must SKIP (retry
		// later), not PRUNE (permanent), matching ErrSwapInvalidSigner's own
		// classification for the same tokenMeta.MintAuthority-mutable-state
		// reasoning just below.
		return fmt.Errorf("%w: mint authority not configured", ErrSwapInvalidSigner)
	}
	if !bytes.Equal(tokenMeta.MintAuthority, recovered.Bytes()) {
		return ErrSwapInvalidSigner
	}

	if priceProof.Rate == nil || priceProof.Rate.Sign() <= 0 {
		return ErrSwapPriceProofInvalid
	}
	mintAmount, err := swap.ComputeMintAmount(voucher.FiatAmount, priceProof.Rate, tokenMeta.Decimals)
	if err != nil {
		// voucher.FiatAmount and priceProof.Rate are both fixed values from
		// this transaction's own immutable payload (tokenMeta.Decimals is
		// static genesis config) -- a deterministic function of tx bytes, so
		// this can never succeed on retry either.
		return fmt.Errorf("%w: compute mint amount: %v", ErrSwapVoucherInvalidPayload, err)
	}
	if mintAmount == nil || mintAmount.Sign() == 0 {
		return fmt.Errorf("%w: computed mint amount zero", ErrSwapVoucherInvalidPayload)
	}
	diff := new(big.Int).Sub(mintAmount, voucher.Amount)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	allowance := new(big.Int).SetUint64(cfg.SlippageBps)
	slippage := new(big.Int).Mul(diff, big.NewInt(10000))
	slippage.Div(slippage, mintAmount)
	if slippage.Cmp(allowance) == 1 {
		return ErrSwapSlippageExceeded
	}

	ledger := swap.NewLedger(manager)
	exists, err := ledger.Exists(providerTxID)
	if err != nil {
		return err
	}
	if exists {
		return ErrSwapDuplicateProviderTx
	}
	if manager.HasSeenSwapNonce(orderID) {
		return ErrSwapNonceUsed
	}

	usdAmount := strings.TrimSpace(submission.USDAmount)
	if usdAmount == "" && strings.EqualFold(voucher.Fiat, "USD") {
		usdAmount = strings.TrimSpace(voucher.FiatAmount)
	}

	// The Genesis Treasury Distribution Curve (core/tokenomics/curve), not
	// the price-proof rate above, is ZNHB's authoritative treasury price.
	// The price-proof check just above still matters (it validates the
	// OTC gateway's own submitted rate is internally consistent with what
	// it requested), but the curve independently prices exactly how much
	// this voucher's ZNHB amount actually costs against the treasury's
	// own Sale Pool schedule, and the transfer below draws from that same
	// pool -- mirroring applyBuyZNHB, never sp.MintToken.
	curveManager := manager
	c0, err := curveManager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		return fmt.Errorf("swap: load cumulative sale distributed: %w", err)
	}
	c1 := new(big.Int).Add(c0, voucher.Amount)
	curveParams := curve.Default()
	curveCostRat, err := curveParams.Cost(c0, c1)
	if err != nil {
		if errors.Is(err, curve.ErrExceedsSalePool) {
			// Depends on the CURRENT cumulative_sale_distributed, which is
			// mutable (buybacks can lower it) -- a voucher that exceeds
			// capacity right now could become valid later, so this must
			// SKIP (retry), not PRUNE (permanent), matching this file's
			// existing reasoning for other mutable-state-dependent checks
			// (see the "mint authority not configured" comment above).
			return fmt.Errorf("%w: mint would exceed the treasury Sale Pool's remaining inventory", ErrSwapAmountAboveMaximum)
		}
		return fmt.Errorf("swap: compute curve cost: %w", err)
	}
	budgetRat, ok := new(big.Rat).SetString(usdAmount)
	if !ok || budgetRat.Sign() <= 0 {
		return fmt.Errorf("%w: invalid usd/fiat budget", ErrSwapVoucherInvalidPayload)
	}
	curveDiff := new(big.Rat).Sub(curveCostRat, budgetRat)
	curveDiff.Abs(curveDiff)
	curveAllowance := new(big.Rat).SetFrac64(int64(cfg.SlippageBps), 10000)
	curveTolerance := new(big.Rat).Mul(curveCostRat, curveAllowance)
	if curveDiff.Cmp(curveTolerance) > 0 {
		return ErrSwapSlippageExceeded
	}

	if !sp.hasAdminWallet {
		return fmt.Errorf("%w: no admin wallet configured for this network", ErrSwapInvalidSigner)
	}
	salePoolBalance, err := curveManager.ZNHBSalePoolBalance()
	if err != nil {
		return fmt.Errorf("swap: load sale pool balance: %w", err)
	}
	if salePoolBalance.Cmp(voucher.Amount) < 0 {
		return fmt.Errorf("%w: treasury sale pool has insufficient ZNHB", ErrSwapInvalidSigner)
	}
	adminAccount, err := sp.getAccount(sp.adminWallet[:])
	if err != nil {
		return fmt.Errorf("swap: load admin wallet: %w", err)
	}
	if adminAccount.BalanceZNHB == nil || adminAccount.BalanceZNHB.Cmp(voucher.Amount) < 0 {
		return fmt.Errorf("%w: admin wallet has insufficient ZNHB", ErrSwapInvalidSigner)
	}

	if err := priceEngine.Record(priceProof); err != nil {
		return fmt.Errorf("swap: record price proof: %w", err)
	}

	recipientAccount, err := sp.getAccount(voucher.Recipient[:])
	if err != nil {
		return fmt.Errorf("swap: load recipient: %w", err)
	}
	if recipientAccount.BalanceZNHB == nil {
		recipientAccount.BalanceZNHB = big.NewInt(0)
	}
	recipientAccount.BalanceZNHB = new(big.Int).Add(recipientAccount.BalanceZNHB, voucher.Amount)
	adminAccount.BalanceZNHB = new(big.Int).Sub(adminAccount.BalanceZNHB, voucher.Amount)
	if err := sp.setAccount(voucher.Recipient[:], recipientAccount); err != nil {
		return fmt.Errorf("swap: persist recipient: %w", err)
	}
	if err := sp.setAccount(sp.adminWallet[:], adminAccount); err != nil {
		return fmt.Errorf("swap: persist admin wallet: %w", err)
	}
	if err := curveManager.ZNHBSetSalePoolBalance(new(big.Int).Sub(salePoolBalance, voucher.Amount)); err != nil {
		return fmt.Errorf("swap: update sale pool balance: %w", err)
	}
	if err := curveManager.ZNHBSetCumulativeSaleDistributed(c1); err != nil {
		return fmt.Errorf("swap: advance cumulative sale distributed: %w", err)
	}

	if err := manager.MarkSwapNonce(orderID); err != nil {
		return err
	}
	record := &swap.VoucherRecord{
		Provider:        provider,
		ProviderTxID:    providerTxID,
		FiatCurrency:    strings.ToUpper(strings.TrimSpace(voucher.Fiat)),
		FiatAmount:      strings.TrimSpace(voucher.FiatAmount),
		USD:             usdAmount,
		Rate:            priceProof.Rate.FloatString(18),
		Token:           token,
		MintAmountWei:   new(big.Int).Set(voucher.Amount),
		Recipient:       voucher.Recipient,
		Username:        strings.TrimSpace(submission.Username),
		Address:         strings.TrimSpace(submission.Address),
		QuoteTimestamp:  priceProof.Timestamp.UTC().Unix(),
		OracleSource:    strings.ToLower(strings.TrimSpace(priceProof.Provider)),
		PriceProofID:    proofID,
		MinterSignature: "0x" + hex.EncodeToString(signature),
		Status:          swap.VoucherStatusMinted,
	}
	if err := ledger.Put(record); err != nil {
		return err
	}

	if err := riskEngine.RecordMint(voucher.Recipient, voucher.Amount, riskParams.VelocityWindowSeconds); err != nil {
		return err
	}

	if evt := (events.SwapMinted{
		OrderID:    orderID,
		Recipient:  voucher.Recipient,
		Amount:     new(big.Int).Set(voucher.Amount),
		Fiat:       voucher.Fiat,
		FiatAmount: voucher.FiatAmount,
		Rate:       voucher.Rate,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	if evt := (events.SwapMintProof{
		ProviderTxID:   providerTxID,
		OrderID:        orderID,
		Token:          token,
		PriceProofID:   proofID,
		OracleSource:   strings.ToLower(strings.TrimSpace(priceProof.Provider)),
		QuoteTimestamp: priceProof.Timestamp.UTC().Unix(),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	return nil
}

func (sp *StateProcessor) emitSwapRiskViolation(violation *swap.RiskViolation, provider, providerTxID string, voucher *swap.VoucherV1) error {
	alert := events.SwapLimitAlert{
		Address:      voucher.Recipient,
		Provider:     provider,
		ProviderTxID: providerTxID,
		Limit:        string(violation.Code),
		Amount:       new(big.Int).Set(voucher.Amount),
		LimitValue:   cloneBigInt(violation.Limit),
		CurrentValue: cloneBigInt(violation.Current),
	}
	switch violation.Code {
	case swap.RiskCodeVelocity:
		if evt := (events.SwapVelocityAlert{
			Address:       voucher.Recipient,
			Provider:      provider,
			ProviderTxID:  providerTxID,
			WindowSeconds: violation.WindowSeconds,
			ObservedCount: violation.Count,
		}).Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapVelocityExceeded
	case swap.RiskCodePerTxMin:
		if evt := alert.Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapAmountBelowMinimum
	case swap.RiskCodePerTxMax:
		if evt := alert.Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapAmountAboveMaximum
	case swap.RiskCodeDailyCap:
		if evt := alert.Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapDailyCapExceeded
	case swap.RiskCodeMonthlyCap:
		if evt := alert.Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return ErrSwapMonthlyCapExceeded
	default:
		if evt := alert.Event(); evt != nil {
			sp.AppendEvent(evt)
		}
		return fmt.Errorf("swap: risk violation %s", violation.Code)
	}
}
