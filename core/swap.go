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
)
