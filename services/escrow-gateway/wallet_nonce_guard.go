package main

import (
	"errors"
	"sync"
	"time"
)

// walletNonceTTL is how long a wallet signature's nonce is remembered. The API-key
// authenticator in front of every wallet-signed request accepts a timestamp at most
// two minutes old, so a captured request is replayable for about that long; twice
// that is kept so the two clocks can never disagree about a nonce still being fresh.
const walletNonceTTL = 5 * time.Minute

// walletNonceCapacity bounds the memory the guard may use. Only signatures that
// verified against an allowed signer are recorded, so filling it needs that many
// valid wallet signatures within the TTL.
const walletNonceCapacity = 200_000

var (
	errWalletNonceReused = errors.New("wallet signature nonce was already used")
	errWalletNonceFull   = errors.New("too many recent wallet signatures; retry shortly")
)

// walletNonceGuard remembers the nonce of every verified wallet signature for
// walletNonceTTL and refuses a second use of the same (signer, nonce).
//
// verifyWalletSignature signs a timestamp and a nonce into every wallet
// signature, but nothing checked the nonce: the API-key authenticator keeps its
// own nonce memory per API key, so a request captured on the wire could be sent
// again under a different valid API key with the victim's wallet signature
// unchanged (ledger PL-EXT-90).
type walletNonceGuard struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newWalletNonceGuard() *walletNonceGuard {
	return &walletNonceGuard{entries: make(map[string]time.Time)}
}

// use records the nonce of signer at now. It returns errWalletNonceReused when the
// same pair is still remembered and errWalletNonceFull when the table is full of
// fresh entries.
func (g *walletNonceGuard) use(signer, nonce string, now time.Time) error {
	if g == nil {
		return nil
	}
	key := signer + "|" + nonce
	g.mu.Lock()
	defer g.mu.Unlock()
	if seen, ok := g.entries[key]; ok && now.Sub(seen) < walletNonceTTL {
		return errWalletNonceReused
	}
	if len(g.entries) >= walletNonceCapacity {
		for k, seen := range g.entries {
			if now.Sub(seen) >= walletNonceTTL {
				delete(g.entries, k)
			}
		}
		if len(g.entries) >= walletNonceCapacity {
			return errWalletNonceFull
		}
	}
	g.entries[key] = now
	return nil
}
