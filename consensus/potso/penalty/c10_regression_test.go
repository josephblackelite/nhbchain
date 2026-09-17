package penalty

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	statepotso "nhbchain/state/potso"
)

// NHB-AUDIT-C10: joint regression test for both sub-issues together. Unlike
// newLedgerForTest above (which seeds a nonzero base via ledger.Set and so
// never exercised the real production zero-weight bug), this test uses a
// nil-floor ledger -- exactly core/node.go's real
// statepotso.NewLedger(nil, nil) -- and never calls Set at all, only
// EnsureBaseline, exactly as processPendingEvidenceForState now does.

type c10TestKey struct {
	priv *ecdsa.PrivateKey
}

func newC10TestKey(t *testing.T) *c10TestKey {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &c10TestKey{priv: priv}
}

func (k *c10TestKey) address() [20]byte {
	addr := ethcrypto.PubkeyToAddress(k.priv.PublicKey)
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}

// signVote reconstructs and signs the exact payload
// consensus/bft.Vote.bytes() produces (sha256 of the JSON-encoded
// blockHash/round/type/height), matching consensus/potso/evidence's
// equivocationVotePayload convention.
func (k *c10TestKey) signVote(blockHash []byte, round int, voteType evidence.EquivocationVoteType, height uint64) (string, string) {
	payload := struct {
		BlockHash []byte                          `json:"blockHash"`
		Round     int                             `json:"round"`
		Type      evidence.EquivocationVoteType   `json:"type"`
		Height    uint64                          `json:"height"`
	}{BlockHash: blockHash, Round: round, Type: voteType, Height: height}
	b, _ := json.Marshal(&payload)
	digest := sha256.Sum256(b)
	sig, err := ethcrypto.Sign(digest[:], k.priv)
	if err != nil {
		panic(err)
	}
	return "0x" + hexEncodeC10(blockHash), "0x" + hexEncodeC10(sig)
}

func hexEncodeC10(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hextable[c>>4]
		out[i*2+1] = hextable[c&0x0f]
	}
	return string(out)
}

func signedVoteC10(key *c10TestKey, blockHash []byte, round int, voteType evidence.EquivocationVoteType, height uint64) evidence.EquivocationSignedVote {
	hash, sig := key.signVote(blockHash, round, voteType, height)
	return evidence.EquivocationSignedVote{BlockHash: hash, Signature: sig}
}

func buildAndSignEvidence(t *testing.T, offender [20]byte, reporter *c10TestKey, proof *evidence.EquivocationProof) (evidence.Evidence, [32]byte) {
	t.Helper()
	details, err := json.Marshal(proof)
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}
	ev := evidence.Evidence{
		Type:      evidence.TypeEquivocation,
		Offender:  offender,
		Heights:   []uint64{proof.Height},
		Details:   details,
		Reporter:  reporter.address(),
		Timestamp: 1,
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporter.priv)
	if err != nil {
		t.Fatalf("sign evidence: %v", err)
	}
	ev.ReporterSig = sig
	return ev, hash
}

func TestEnginePenalizesRealStakeAfterEnsureBaselineWithGenuineEquivocationProof(t *testing.T) {
	ledger, err := statepotso.NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	offenderKey := newC10TestKey(t)
	offender := offenderKey.address()

	// Real, current on-chain stake -- what core/node.go's fix reads from
	// the account trie and feeds into EnsureBaseline.
	if _, err := ledger.EnsureBaseline(offender, big.NewInt(800)); err != nil {
		t.Fatalf("ensure baseline: %v", err)
	}

	proof := &evidence.EquivocationProof{
		Height:   99,
		Round:    2,
		VoteType: evidence.EquivocationVotePrevote,
		VoteA:    signedVoteC10(offenderKey, []byte{0x01}, 2, evidence.EquivocationVotePrevote, 99),
		VoteB:    signedVoteC10(offenderKey, []byte{0x02}, 2, evidence.EquivocationVotePrevote, 99),
	}
	reporterKey := newC10TestKey(t)
	ev, hash := buildAndSignEvidence(t, offender, reporterKey, proof)

	// The real submission gate -- this must pass before the evidence is
	// ever persisted or handed to the penalty engine.
	if verr := evidence.ValidateEvidence(&ev, hash, 99, 0, nil); verr != nil {
		t.Fatalf("expected genuine equivocation proof to pass validation, got: %s", verr.Message)
	}

	cfg := DefaultConfig()
	cfg.EquivocationThetaBps = 5000
	cfg.EquivocationMinDecay = big.NewInt(50)
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	engine := NewEngine(catalog, ledger, nil)
	record := &evidence.Record{Hash: hash, Evidence: ev}
	result, err := engine.Apply(record, Context{BlockHeight: 99})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.WeightUpdate == nil || result.WeightUpdate.Applied == nil || result.WeightUpdate.Applied.Sign() <= 0 {
		t.Fatalf("expected a real, non-zero slash against the offender's actual stake, got %+v", result.WeightUpdate)
	}
}

func TestEvidenceGateRejectsForgedEquivocationBeforeItEverReachesThePenaltyEngine(t *testing.T) {
	offenderKey := newC10TestKey(t)
	offender := offenderKey.address()
	reporterKey := newC10TestKey(t)

	// The reporter fabricates both "votes" with their OWN key -- never
	// touching the offender's key at all.
	proof := &evidence.EquivocationProof{
		Height:   10,
		Round:    0,
		VoteType: evidence.EquivocationVotePrevote,
		VoteA:    signedVoteC10(reporterKey, []byte{0x01}, 0, evidence.EquivocationVotePrevote, 10),
		VoteB:    signedVoteC10(reporterKey, []byte{0x02}, 0, evidence.EquivocationVotePrevote, 10),
	}
	ev, hash := buildAndSignEvidence(t, offender, reporterKey, proof)

	verr := evidence.ValidateEvidence(&ev, hash, 10, 0, nil)
	if verr == nil {
		t.Fatalf("expected a fabricated accusation (reporter-signed, not offender-signed) to be rejected before ever reaching the penalty engine")
	}
}
