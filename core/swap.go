package core

import "errors"

var (
	// ErrSwapInvalidDomain indicates the voucher domain does not match the expected identifier.
	ErrSwapInvalidDomain = errors.New("swap: invalid domain")
	// ErrSwapInvalidChainID indicates the voucher targets a different chain.
	ErrSwapInvalidChainID = errors.New("swap: invalid chain id")
	// ErrSwapExpired indicates the voucher expiry timestamp has elapsed.
	ErrSwapExpired = errors.New("swap: voucher expired")
	// ErrSwapInvalidToken indicates the voucher requested an unsupported token.
	ErrSwapInvalidToken = errors.New("swap: invalid token")
	// ErrSwapInvalidSignature indicates the signature is malformed or cannot be recovered.
	ErrSwapInvalidSignature = errors.New("swap: invalid signature")
	// ErrSwapInvalidSigner indicates the recovered signer does not match the configured mint authority.
	ErrSwapInvalidSigner = errors.New("swap: invalid signer")
	// ErrSwapNonceUsed indicates the order identifier has already been processed.
	ErrSwapNonceUsed = errors.New("swap: order already processed")
	// ErrSwapMintPaused indicates the token mint has been paused by governance.
	ErrSwapMintPaused = errors.New("swap: mint paused")
	// ErrSwapUnsupportedFiat indicates the voucher references a fiat currency outside the allow-list.
	ErrSwapUnsupportedFiat = errors.New("swap: fiat currency not allowed")
	// ErrSwapOracleUnavailable indicates the price oracle has not been configured.
	ErrSwapOracleUnavailable = errors.New("swap: price oracle unavailable")
	// ErrSwapQuoteStale indicates the oracle quote exceeded the configured freshness window.
	ErrSwapQuoteStale = errors.New("swap: oracle quote stale")
	// ErrSwapSlippageExceeded indicates the submitted mint amount deviates beyond the allowed slippage threshold.
	ErrSwapSlippageExceeded = errors.New("swap: slippage exceeds maximum")
	// ErrSwapDuplicateProviderTx indicates the provider transaction identifier has already been recorded.
	ErrSwapDuplicateProviderTx = errors.New("swap: provider transaction already processed")
	// ErrSwapProviderTxIDCollision indicates the submitted providerTxId
	// already exists in the ledger, but under a DIFFERENT, already-signed
	// OrderID than this submission's own OrderID. NHB-AUDIT-C8:
	// ProviderTxID is never covered by the mint authority's signature
	// (unlike OrderID, the real anti-double-mint control -- see
	// VoucherV1.Hash), so anyone holding any validly-signed voucher can
	// choose an arbitrary providerTxId, including one matching a
	// different, unrelated, not-yet-processed order -- permanently
	// blocking that legitimate order under the ledger's current
	// ProviderTxID-keyed storage. Kept distinct from
	// ErrSwapDuplicateProviderTx (a genuine, harmless retry of the exact
	// same order) so this specific, actionable symptom is never
	// misdiagnosed as an ordinary duplicate.
	//
	// NHB-AUDIT-C8 follow-up: this collision can still occur for a V1
	// voucher (see swap.VoucherV1.Hash's doc comment). A V2 voucher (see
	// swap.VoucherV2.Hash / swap.VoucherDomainV2) folds Provider and
	// ProviderTxID into the signed digest, so its signature is only ever
	// valid for the exact ProviderTxID the mint authority signed -- nobody
	// downstream of the signer can pair a valid V2 signature with a
	// different, self-chosen ProviderTxID, closing this specific griefing
	// vector for V2 vouchers. See applySwapVoucherMintTransaction in
	// swap_voucher_tx.go for the version-aware verification.
	ErrSwapProviderTxIDCollision = errors.New("swap: providerTxId collides with a different order")
	// ErrSwapProviderNotAllowed indicates the mint originated from a non-whitelisted provider.
	ErrSwapProviderNotAllowed = errors.New("swap: provider not allowed")
	// ErrSwapAmountBelowMinimum indicates the mint fell below the configured minimum threshold.
	ErrSwapAmountBelowMinimum = errors.New("swap: amount below minimum")
	// ErrSwapAmountAboveMaximum indicates the mint exceeded the configured per-transaction ceiling.
	ErrSwapAmountAboveMaximum = errors.New("swap: amount exceeds maximum")
	// ErrSwapDailyCapExceeded indicates the address exhausted its daily mint allowance.
	ErrSwapDailyCapExceeded = errors.New("swap: daily limit exceeded")
	// ErrSwapMonthlyCapExceeded indicates the address exhausted its monthly mint allowance.
	ErrSwapMonthlyCapExceeded = errors.New("swap: monthly limit exceeded")
	// ErrSwapVelocityExceeded indicates the mint frequency surpassed the configured burst threshold.
	ErrSwapVelocityExceeded = errors.New("swap: velocity limit exceeded")
	// ErrSwapSanctioned indicates the sanctions hook rejected the address.
	ErrSwapSanctioned = errors.New("swap: address failed sanctions check")
	// ErrSwapVoucherNotMinted indicates the voucher is not in a reversible state.
	ErrSwapVoucherNotMinted = errors.New("swap: voucher not in minted state")
	// ErrSwapVoucherAlreadyReversed indicates the voucher reversal has already been processed.
	ErrSwapVoucherAlreadyReversed = errors.New("swap: voucher already reversed")
	// ErrSwapReversalInsufficientBalance indicates the treasury or custody account cannot fund the reversal sink.
	ErrSwapReversalInsufficientBalance = errors.New("swap: insufficient balance to reverse voucher")
	// ErrSwapPriceProofRequired indicates the submission did not include a price proof payload.
	ErrSwapPriceProofRequired = errors.New("swap: price proof required")
	// ErrSwapPriceProofInvalid indicates the supplied proof failed validation.
	ErrSwapPriceProofInvalid = errors.New("swap: invalid price proof")
	// ErrSwapPriceProofSignerUnknown indicates the signer has not been registered on-chain.
	ErrSwapPriceProofSignerUnknown = errors.New("swap: price proof signer unknown")
	// ErrSwapPriceProofStale indicates the proof exceeded the freshness window.
	ErrSwapPriceProofStale = errors.New("swap: price proof stale")
	// ErrSwapPriceProofDeviation indicates the proof deviated beyond the allowed tolerance.
	ErrSwapPriceProofDeviation = errors.New("swap: price proof deviation too large")
	// ErrSwapVoucherInvalidPayload indicates a TxTypeSwapVoucherMint transaction's
	// Data payload could not be decoded into a voucher submission. Since this is a
	// pure function of the transaction's own immutable bytes, it can never become
	// valid later -- safe to treat as a prunable proposal error.
	ErrSwapVoucherInvalidPayload = errors.New("swap: invalid voucher transaction payload")
	// ErrRedeemInsufficientBalance indicates a TxTypeRedeemNHB transaction's
	// sender does not currently hold enough NHB to cover the burn. Transient:
	// a same-block ordering effect (another transaction crediting this
	// address first) or a later resubmission could make this succeed, so
	// this is a skippable, not prunable, proposal error.
	ErrRedeemInsufficientBalance = errors.New("redeemNHB: insufficient NHB balance")
	// ErrRedeemRequestExists indicates a TxTypeRedeemNHB transaction's derived
	// request ID (from its own transaction hash) already has a stored
	// redemption request -- i.e. this exact transaction already succeeded in
	// an earlier block. Since the ID is a pure function of the transaction's
	// own immutable bytes, this can never become valid later -- prunable.
	ErrRedeemRequestExists = errors.New("redeemNHB: request already exists")
	// ErrRedeemRequestNotPending indicates a TxTypeAttestRedemption
	// transaction targets a redemption request that has already been
	// settled (paid or failed) by an earlier attestation. Since a request's
	// terminal status never reverts, a resubmitted attestation for the same
	// requestId can never become valid later -- prunable.
	ErrRedeemRequestNotPending = errors.New("redeemNHB: request not pending")
	// ErrRedeemUnauthorizedAttestor indicates a TxTypeAttestRedemption
	// transaction's sender does not currently hold RoleSwapPayoutAttestor.
	// Skippable, not prunable, mirroring ErrSwapInvalidSigner/
	// ErrMintInvalidSigner above: the role could be granted to this signer
	// by a later governance action, so a same-attempt exclusion (not a
	// permanent mempool removal) is the correct disposition.
	ErrRedeemUnauthorizedAttestor = errors.New("attestRedemption: unauthorized attestor")
	// ErrRedeemInvalidPayload indicates a TxTypeRedeemNHB or
	// TxTypeAttestRedemption transaction's Data payload failed to decode or
	// is missing/malformed required fields. Since this is a pure function of
	// the transaction's own immutable bytes, it can never become valid
	// later -- prunable, mirroring ErrSwapVoucherInvalidPayload above.
	ErrRedeemInvalidPayload = errors.New("redeem: invalid transaction payload")
	// ErrSwapAdminInvalidPayload indicates a TxTypeSwapVoucherReverse or
	// TxTypeSwapMarkReconciled transaction's Data payload failed to decode,
	// or its embedded signature is malformed/unrecoverable. Since this is a
	// pure function of the transaction's own immutable bytes, it can never
	// become valid later -- prunable, mirroring ErrSwapVoucherInvalidPayload
	// above.
	ErrSwapAdminInvalidPayload = errors.New("swap: invalid admin transaction payload")
	// ErrSwapAdminUnauthorized indicates a TxTypeSwapVoucherReverse or
	// TxTypeSwapMarkReconciled transaction's embedded signature recovers to
	// an address that does not currently hold RoleSwapAdmin. Skippable, not
	// prunable, mirroring ErrRedeemUnauthorizedAttestor above: the role
	// could be granted to this signer by a later governance action, so a
	// same-attempt exclusion (not a permanent mempool removal) is the
	// correct disposition.
	ErrSwapAdminUnauthorized = errors.New("swap: unauthorized admin")
	// ErrSwapVoucherReversalNotFound indicates a TxTypeSwapVoucherReverse
	// transaction names a providerTxId with no ledger record at all yet.
	// Skippable, not prunable: unlike a status that has already moved past
	// "minted" (a one-way, permanent transition -- see
	// ErrSwapVoucherNotMinted/ErrSwapVoucherAlreadyReversed), a voucher that
	// does not exist YET could still be minted by a later transaction (e.g.
	// still sitting in the same mempool), so this providerTxId could
	// legitimately become reversible on a later attempt.
	ErrSwapVoucherReversalNotFound = errors.New("swap: voucher not found")
)
