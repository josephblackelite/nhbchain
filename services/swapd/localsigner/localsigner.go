// Package localsigner implements services/swapd/priceproof.Signer against a
// local, encrypted keystore file instead of an HSM. It exists so an
// operator without HSM infrastructure can still run swapd's price-proof
// signing endpoint using a wallet they already control, held on disk as a
// standard Ethereum V3 keystore (the exact same encryption this repo
// already uses for validator keys -- see crypto.SaveToKeystore /
// crypto.LoadFromKeystore in crypto/keystore.go). No new encryption scheme
// is introduced here; this package only decides when to load the key and
// how to turn a digest into a signature once it has.
//
// The private key is decrypted exactly once, at construction time
// (NewClient), and held in memory for the lifetime of the process. There is
// deliberately no lazy/retry/fallback path: if the keystore cannot be
// decrypted, NewClient returns an error and the caller (services/swapd/main.go)
// is expected to treat that as fatal, refusing to start rather than serving
// price-proof requests with no signer -- an unsigned or unauthenticated
// price-proof endpoint would let anyone move the swap module's mint
// gate, so failing loudly here is the safe default, not a rough edge to
// smooth over later.
package localsigner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// Signer abstracts the capability to sign an arbitrary 32-byte digest. This
// mirrors services/swapd/priceproof.Signer and services/otc-gateway/hsm.Signer
// exactly (same three-value Sign(ctx, digest) ([]byte, string, error)
// shape) but is declared locally, the same way those two packages declare
// their own copies, so none of these packages need to import one another
// just to satisfy a constructor signature.
type Signer interface {
	Sign(ctx context.Context, digest []byte) ([]byte, string, error)
}

// Config configures a Client backed by a local encrypted keystore file.
type Config struct {
	// KeystorePath is the path to the encrypted keystore JSON file. Create
	// one with `nhb-cli keystore import` (see cmd/nhb-cli/keystore_cmd.go) --
	// never hand-author or copy a raw private key into this path.
	KeystorePath string
	// PassphraseEnv is the NAME of an environment variable holding the
	// keystore's decryption passphrase -- never the passphrase itself. This
	// package refuses to accept a passphrase through any other channel (a
	// config field, a CLI flag, a bare file path with no indirection) so the
	// secret can never end up committed to git or captured in a config dump
	// or log line.
	PassphraseEnv string
}

// Client signs digests with a private key decrypted once, at construction
// time, from a local encrypted keystore file.
type Client struct {
	key     *crypto.PrivateKey
	address string
}

// NewClient decrypts the configured keystore file exactly once and returns
// a Client ready to sign, or an error if anything about that fails --
// missing configuration, an unset/empty passphrase environment variable, or
// a keystore that fails to decrypt (wrong passphrase or corrupted file).
// There is no partially-usable return value: either NewClient succeeds and
// the returned Client can sign immediately, or it returns (nil, err) and
// the caller must not proceed as if a signer were configured.
func NewClient(cfg Config) (*Client, error) {
	path := strings.TrimSpace(cfg.KeystorePath)
	if path == "" {
		return nil, fmt.Errorf("localsigner: keystore path required")
	}
	envVar := strings.TrimSpace(cfg.PassphraseEnv)
	if envVar == "" {
		return nil, fmt.Errorf("localsigner: passphrase environment variable name required")
	}
	passphrase, ok := os.LookupEnv(envVar)
	if !ok {
		return nil, fmt.Errorf("localsigner: environment variable %s is not set", envVar)
	}
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("localsigner: environment variable %s is set but empty", envVar)
	}
	key, err := crypto.LoadFromKeystore(path, passphrase)
	if err != nil {
		return nil, fmt.Errorf("localsigner: decrypt keystore %s: %w", path, err)
	}
	return &Client{key: key, address: key.PubKey().Address().String()}, nil
}

// Address returns the bech32 NHB address of the loaded key, so the caller
// can log it at startup -- letting an operator visually confirm the running
// process actually loaded the wallet they expect, the same confirmation
// step `nhb-cli keystore import` performs when the file is first written.
func (c *Client) Address() string {
	if c == nil {
		return ""
	}
	return c.address
}

// Sign signs digest with the loaded key using go-ethereum's standard
// secp256k1 recoverable-signature scheme: a 65-byte [R || S || V] signature
// with the recovery id V in {0, 1}. This is the exact scheme
// core/types.Transaction.Sign already uses elsewhere in this repo
// (crypto.Sign(hash, privKey)) and the exact scheme
// native/swap.PriceProofEngine.Verify expects when it recovers the signer
// via ethcrypto.SigToPub(hash, proof.Signature) -- so a proof signed here is
// genuinely verifiable through that unmodified production code path, not
// merely shaped like a signature. The returned string carries this signer's
// address in the slot services/otc-gateway/hsm.Client uses for a
// certificate's distinguished name; a local keystore signer has no
// certificate, so its address is the closest equivalent "who signed this"
// identifier.
func (c *Client) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	if c == nil || c.key == nil {
		return nil, "", fmt.Errorf("localsigner: client not configured")
	}
	sig, err := ethcrypto.Sign(digest, c.key.PrivateKey)
	if err != nil {
		return nil, "", fmt.Errorf("localsigner: sign: %w", err)
	}
	return sig, c.address, nil
}

var _ Signer = (*Client)(nil)
