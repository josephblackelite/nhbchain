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
)
