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
)
