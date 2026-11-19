package types

import (
	"bytes"
	"crypto/ecdsa"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// PL-R5T-31: a vote signed on one network must not verify on another, and two honest
// votes by one key on two networks must not look like an equivocation.

var (
	genesisNet1 = bytes.Repeat([]byte{0x11}, 32)
	genesisNet2 = bytes.Repeat([]byte{0x22}, 32)
)

func TestVoteBindingDigest_Table(t *testing.T) {
	vote := PrecommitVote{BlockHash: []byte{1}, Round: 1, Type: VoteTypePrecommit, Height: 50}
	payload := vote.Bytes()
	cases := []struct {
		name      string
		binding   VoteBinding
		height    uint64
		wantBound bool
		wantErr   bool
	}{
		{"off: legacy digest at any height", VoteBinding{Genesis: genesisNet1}, 1_000_000, false, false},
		{"off with no genesis: legacy digest", VoteBinding{}, 5, false, false},
		{"below the height: legacy digest", VoteBinding{Genesis: genesisNet1, FromHeight: 100}, 99, false, false},
		{"at the height: bound digest", VoteBinding{Genesis: genesisNet1, FromHeight: 100}, 100, true, false},
		{"above the height: bound digest", VoteBinding{Genesis: genesisNet1, FromHeight: 100}, 101, true, false},
		{"active with no genesis fails rather than falling back", VoteBinding{FromHeight: 100}, 100, false, true},
		{"below the height with no genesis is fine", VoteBinding{FromHeight: 100}, 99, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.binding.Digest(tc.height, payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, want error=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.wantBound {
				if got != BoundVoteDigest(tc.binding.Genesis, payload) {
					t.Fatalf("expected the bound digest")
				}
				return
			}
			if got != vote.Digest() {
				t.Fatalf("expected the legacy digest (PrecommitVote.Digest)")
			}
		})
	}
}

func TestBoundVoteDigest_DependsOnTheGenesisAndTheDomain(t *testing.T) {
	payload := []byte(`{"height":1}`)
	a := BoundVoteDigest(genesisNet1, payload)
	if a == BoundVoteDigest(genesisNet2, payload) {
		t.Fatalf("two networks produce one digest")
	}
	if a == BoundVoteDigest(genesisNet1, []byte(`{"height":2}`)) {
		t.Fatalf("the payload is not covered")
	}
	// The genesis length is covered: moving a byte from the genesis into the payload changes it.
	if BoundVoteDigest(genesisNet1[:31], append([]byte{genesisNet1[31]}, payload...)) == a {
		t.Fatalf("the boundary between genesis and payload is not covered")
	}
}

type qcKey struct {
	priv *ecdsa.PrivateKey
	addr []byte
}

func TestQuorumCertVerifyBound(t *testing.T) {
	var keys []qcKey
	power := map[string]*big.Int{}
	for i := 0; i < 3; i++ {
		k, err := ethcrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		addr := ethcrypto.PubkeyToAddress(k.PublicKey).Bytes()
		keys = append(keys, qcKey{priv: k, addr: addr})
		power[string(addr)] = big.NewInt(1)
	}
	header := bytes.Repeat([]byte{0xAB}, 32)
	signWith := func(digest [32]byte, height uint64) *QuorumCert {
		qc := &QuorumCert{Height: height, Round: 0, BlockHash: header}
		for _, k := range keys {
			sig, err := ethcrypto.Sign(digest[:], k.priv)
			if err != nil {
				t.Fatal(err)
			}
			qc.Signatures = append(qc.Signatures, QuorumSignature{Validator: k.addr, Signature: sig})
		}
		return qc
	}
	sign := func(binding VoteBinding, height uint64) *QuorumCert {
		vote := &PrecommitVote{BlockHash: header, Round: 0, Type: VoteTypePrecommit, Height: height}
		digest, err := vote.DigestBound(binding)
		if err != nil {
			t.Fatal(err)
		}
		return signWith(digest, height)
	}
	// signForcedBound signs over the genesis hash whatever the height, as a node that runs with
	// the binding switched on earlier than the verifier would.
	signForcedBound := func(genesis []byte, height uint64) *QuorumCert {
		vote := &PrecommitVote{BlockHash: header, Round: 0, Type: VoteTypePrecommit, Height: height}
		return signWith(BoundVoteDigest(genesis, vote.Bytes()), height)
	}
	on := VoteBinding{Genesis: genesisNet1, FromHeight: 100}
	other := VoteBinding{Genesis: genesisNet2, FromHeight: 100}

	t.Run("legacy certificate below the height verifies under the binding", func(t *testing.T) {
		if err := sign(VoteBinding{}, 99).VerifyBound(header, power, on); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("bound certificate below the height is refused", func(t *testing.T) {
		if err := signForcedBound(genesisNet1, 99).VerifyBound(header, power, on); err == nil {
			t.Fatal("a certificate signed over the genesis hash below the binding height was accepted")
		}
	})
	t.Run("bound certificate at the height verifies", func(t *testing.T) {
		if err := sign(on, 100).VerifyBound(header, power, on); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("legacy certificate at the height is refused", func(t *testing.T) {
		if err := sign(VoteBinding{}, 100).VerifyBound(header, power, on); err == nil {
			t.Fatal("a legacy certificate at the binding height was accepted")
		}
	})
	t.Run("a certificate of another network is refused", func(t *testing.T) {
		if err := sign(other, 150).VerifyBound(header, power, on); err == nil {
			t.Fatal("a certificate signed over another network's genesis hash was accepted")
		}
	})
	t.Run("Verify keeps the legacy rule", func(t *testing.T) {
		if err := sign(VoteBinding{}, 150).Verify(header, power); err != nil {
			t.Fatal(err)
		}
		if err := sign(on, 150).Verify(header, power); err == nil {
			t.Fatal("Verify accepted a bound certificate")
		}
	})
	t.Run("active binding with no genesis fails closed", func(t *testing.T) {
		if err := sign(VoteBinding{}, 150).VerifyBound(header, power, VoteBinding{FromHeight: 100}); err == nil {
			t.Fatal("a certificate was accepted with the binding active and no genesis hash")
		}
	})
}
