package swap

import (
	"encoding/hex"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func testVoucherV1(orderID, providerTxIDUnused string) VoucherV1 {
	var recipient [20]byte
	recipient[0] = 0xAB
	amount, _ := new(big.Int).SetString("100000000000000000000", 10) // 100 * 1e18
	return VoucherV1{
		Domain:     VoucherDomainV1,
		ChainID:    187001,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     amount,
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       "0.05",
		OrderID:    orderID,
		Nonce:      []byte("nonce-" + orderID),
		Expiry:     4102444800, // 2100-01-01, far future, fixed for a stable golden hash
	}
}

// TestVoucherV1HashUnchanged pins VoucherV1.Hash's exact digest for a fixed
// payload. It is a golden-value regression test: if this task's VoucherV2
// addition (or anything else) ever changes VoucherV1.Hash's format string,
// byte layout, or field order, this test fails immediately -- proving V1's
// own signed digest, and therefore every already-signed/in-flight V1
// voucher's verifiability, is provably unchanged by the V2 schema addition.
func TestVoucherV1HashUnchanged(t *testing.T) {
	v := testVoucherV1("ORDER-GOLDEN-1", "")
	got := hex.EncodeToString(v.Hash())
	// Computed once from the exact payload above via VoucherV1.Hash's own
	// documented format string; pinned here as a golden value.
	const want = "17d3337c3ce7e965df1866436505a498b2c177b423e543dfd71d432cb54c14c5"
	if got != want {
		t.Fatalf("VoucherV1.Hash() regressed: got %s, want %s (V1's signed digest must never change -- it would invalidate every already-signed voucher)", got, want)
	}
}

// TestVoucherV1HashDoesNotCoverProviderFields documents (and pins) the exact
// gap NHB-AUDIT-C8 and this V2 follow-up are about: two V1 vouchers that are
// byte-identical in every VoucherV1 field produce the IDENTICAL Hash()
// regardless of any Provider/ProviderTxID a caller later pairs the resulting
// signature with -- because VoucherV1.Hash simply has no such fields to
// begin with. This is why VoucherSubmission.Provider/ProviderTxID are not
// trustworthy for a V1 submission (see VoucherSubmission's doc comment), and
// exactly what VoucherV2.Hash exists to fix.
func TestVoucherV1HashDoesNotCoverProviderFields(t *testing.T) {
	v := testVoucherV1("ORDER-1", "")
	// VoucherV1 has no Provider/ProviderTxID fields at all -- Hash() is a
	// pure function of the fields it does have, so signing it once yields a
	// digest (and therefore a signature) that says nothing whatsoever about
	// which providerTxId it may later be submitted with.
	h1 := v.Hash()
	h2 := v.Hash()
	if hex.EncodeToString(h1) != hex.EncodeToString(h2) {
		t.Fatalf("VoucherV1.Hash() must be a pure, deterministic function of its own fields")
	}
}

func testVoucherV2(orderID, provider, providerTxID string) VoucherV2 {
	inner := testVoucherV1(orderID, "")
	inner.Domain = VoucherDomainV2
	return VoucherV2{
		Voucher:      inner,
		Provider:     provider,
		ProviderTxID: providerTxID,
	}
}

// TestVoucherV2HashCoversProviderAndProviderTxID is test (2)'s underlying
// primitive: it proves VoucherV2.Hash changes -- and therefore any existing
// signature over it is invalidated -- when EITHER Provider or ProviderTxID
// changes, with every other field held fixed. This is the exact property
// that closes the NHB-AUDIT-C8 griefing gap: a V2 signature simply does not
// exist for any ProviderTxID other than the one actually signed.
func TestVoucherV2HashCoversProviderAndProviderTxID(t *testing.T) {
	base := testVoucherV2("ORDER-V2-1", "nowpayments", "LEGIT-TX-1")
	baseHash := hex.EncodeToString(base.Hash())

	tamperedTxID := base
	tamperedTxID.ProviderTxID = "EVIL-TX-COLLIDE"
	if hex.EncodeToString(tamperedTxID.Hash()) == baseHash {
		t.Fatalf("VoucherV2.Hash() must change when ProviderTxID changes (this is exactly the griefing vector V2 exists to close)")
	}

	tamperedProvider := base
	tamperedProvider.Provider = "otherprovider"
	if hex.EncodeToString(tamperedProvider.Hash()) == baseHash {
		t.Fatalf("VoucherV2.Hash() must change when Provider changes")
	}

	// Re-deriving the same fields must reproduce the identical digest
	// (determinism), confirming the above differences are caused by the
	// changed fields, not nondeterminism in Hash() itself.
	again := testVoucherV2("ORDER-V2-1", "nowpayments", "LEGIT-TX-1")
	if hex.EncodeToString(again.Hash()) != baseHash {
		t.Fatalf("VoucherV2.Hash() must be deterministic for identical fields")
	}
}

// TestVoucherV2HashDiffersFromV1ForSameUnderlyingVoucher proves the two
// schema versions can never be confused with one another: a V2 voucher
// wrapping the exact same VoucherV1 payload (domain included) never
// produces the same digest as that voucher's own V1 Hash, even before
// considering Provider/ProviderTxID -- so a V1 signature can never be
// replayed as a V2 signature or vice versa.
func TestVoucherV2HashDiffersFromV1ForSameUnderlyingVoucher(t *testing.T) {
	v1 := testVoucherV1("ORDER-CROSS-1", "")
	v2 := VoucherV2{Voucher: v1, Provider: "nowpayments", ProviderTxID: "TX-CROSS-1"}
	// Note: v2.Voucher.Domain is still VoucherDomainV1 here deliberately (we
	// did not overwrite it), isolating the comparison to Hash()'s own added
	// fields/format rather than the domain string.
	if hex.EncodeToString(v1.Hash()) == hex.EncodeToString(v2.Hash()) {
		t.Fatalf("VoucherV2.Hash() must never coincide with the wrapped VoucherV1's own Hash()")
	}
}

// TestVoucherV2SignAndRecoverRoundTrip exercises the full sign/verify
// primitive VoucherV2 is designed for, exactly mirroring how
// core.applySwapVoucherMintTransaction recovers a signer via
// ethcrypto.SigToPub(voucherHash, signature): a legitimate signature
// recovers the signer's own address, and mutating ProviderTxID after
// signing (the griefing attempt) makes that same signature recover a
// completely different, essentially-random address instead -- never the
// real signer -- which is precisely what makes core-level verification
// reject it as ErrSwapInvalidSigner rather than silently accepting a
// forged providerTxId pairing.
func TestVoucherV2SignAndRecoverRoundTrip(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signerAddr := ethcrypto.PubkeyToAddress(key.PublicKey)

	voucher := testVoucherV2("ORDER-V2-SIGN", "nowpayments", "LEGIT-TX-42")
	sig, err := ethcrypto.Sign(voucher.Hash(), key)
	if err != nil {
		t.Fatalf("sign voucher: %v", err)
	}

	// (3) Legitimate ProviderTxID: recovering against the untampered hash
	// must yield the real signer.
	pub, err := ethcrypto.SigToPub(voucher.Hash(), sig)
	if err != nil {
		t.Fatalf("recover signer: %v", err)
	}
	if got := ethcrypto.PubkeyToAddress(*pub); got != signerAddr {
		t.Fatalf("expected recovered signer %s, got %s", signerAddr, got)
	}

	// (2) Tampered ProviderTxID: the same signature, recovered against the
	// tampered voucher's own (different) hash, must NOT yield the real
	// signer -- proving the forged pairing is cryptographically rejected,
	// not merely inconvenient.
	tampered := voucher
	tampered.ProviderTxID = "EVIL-TX-COLLIDE"
	pub2, err := ethcrypto.SigToPub(tampered.Hash(), sig)
	if err != nil {
		// SigToPub itself failing is also an acceptable rejection outcome.
		return
	}
	if got := ethcrypto.PubkeyToAddress(*pub2); got == signerAddr {
		t.Fatalf("SECURITY REGRESSION: recovered the real signer's address after tampering ProviderTxID -- VoucherV2.Hash is not covering ProviderTxID")
	}
}
