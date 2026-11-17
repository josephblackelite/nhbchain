package evidence

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// signer is a key that can double-sign (as an offender) or report (as a reporter).
type signer struct {
	priv *ecdsa.PrivateKey
	addr [20]byte
}

func newSigner(t *testing.T) signer {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := signer{priv: priv}
	copy(s.addr[:], ethcrypto.PubkeyToAddress(priv.PublicKey).Bytes())
	return s
}

// vote signs the vote a validator would sign for blockHash at the decision point,
// exactly as a consensus round does.
func (s signer) vote(t *testing.T, blockHash []byte, height uint64, round int, voteType EquivocationVoteType) EquivocationSignedVote {
	t.Helper()
	payload := equivocationVotePayload{BlockHash: blockHash, Round: round, Type: voteType, Height: height}
	digest := payload.digest()
	sig, err := ethcrypto.Sign(digest[:], s.priv)
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	return EquivocationSignedVote{BlockHash: "0x" + hex.EncodeToString(blockHash), Signature: "0x" + hex.EncodeToString(sig)}
}

// proofAt is the offender's genuine double-sign at the decision point: a vote for
// block 0xAA.. and another for 0xBB...
func (s signer) proofAt(t *testing.T, height uint64, round int, voteType EquivocationVoteType) EquivocationProof {
	t.Helper()
	return EquivocationProof{
		Height:   height,
		Round:    round,
		VoteType: voteType,
		VoteA:    s.vote(t, []byte{0xAA, 0x01}, height, round, voteType),
		VoteB:    s.vote(t, []byte{0xBB, 0x02}, height, round, voteType),
	}
}

// report is a signed EQUIVOCATION report by reporter against offender.
func report(t *testing.T, reporter signer, offender [20]byte, heights []uint64, details []byte, timestamp int64) (*Evidence, [32]byte) {
	t.Helper()
	e := &Evidence{
		Type:      TypeEquivocation,
		Offender:  offender,
		Heights:   heights,
		Details:   details,
		Reporter:  reporter.addr,
		Timestamp: timestamp,
	}
	hash, err := e.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	sig, err := ethcrypto.Sign(e.SigningDigest(hash), reporter.priv)
	if err != nil {
		t.Fatalf("sign report: %v", err)
	}
	e.ReporterSig = sig
	return e, hash
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// malleated returns the same signature in its other valid encoding: (r, n-s) with
// the recovery id flipped recovers the same key from the same digest.
func malleated(t *testing.T, sigHex string) string {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(sigHex, "0x"))
	if err != nil || len(raw) != 65 {
		t.Fatalf("bad signature %q: %v", sigHex, err)
	}
	s := new(big.Int).SetBytes(raw[32:64])
	s.Sub(ethcrypto.S256().Params().N, s)
	out := make([]byte, 65)
	copy(out[:32], raw[:32])
	s.FillBytes(out[32:64])
	out[64] = raw[64] ^ 1
	return "0x" + hex.EncodeToString(out)
}

// TestEquivocationOffenseIsTheSameForEveryWayOfWritingOneDoubleSign: whatever a
// reporter changes about how one double-sign is written down -- and every change
// below gives the report another canonical hash -- the offense it accuses the
// offender of is the same one, at the same height.
func TestEquivocationOffenseIsTheSameForEveryWayOfWritingOneDoubleSign(t *testing.T) {
	offender, reporter := newSigner(t), newSigner(t)
	const height, round = 100, 3
	proof := offender.proofAt(t, height, round, EquivocationVotePrevote)
	base, baseHash := report(t, reporter, offender.addr, []uint64{height}, mustJSON(t, proof), 1)
	want, ok := base.equivocationOffense()
	if !ok {
		t.Fatalf("the genuine report does not read as an offense")
	}
	if want.Height != height {
		t.Fatalf("expected the offense at height %d, got %d", height, want.Height)
	}

	swapped := proof
	swapped.VoteA, swapped.VoteB = proof.VoteB, proof.VoteA

	upper := proof
	upper.VoteA.BlockHash = strings.ToUpper(strings.TrimPrefix(proof.VoteA.BlockHash, "0x"))
	upper.VoteA.Signature = "0X" + strings.ToUpper(strings.TrimPrefix(proof.VoteA.Signature, "0x"))

	spaced := proof
	spaced.VoteA.BlockHash = "  " + proof.VoteA.BlockHash + " "

	thirdBlock := proof
	thirdBlock.VoteB = offender.vote(t, []byte{0xCC, 0x03}, height, round, EquivocationVotePrevote)

	highS := proof
	highS.VoteA.Signature = malleated(t, proof.VoteA.Signature)

	withNote, err := json.Marshal(map[string]any{"note": "another reporter's layout", "voteB": proof.VoteB, "voteA": proof.VoteA, "voteType": proof.VoteType, "round": proof.Round, "height": proof.Height})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	variants := []struct {
		name      string
		heights   []uint64
		details   []byte
		timestamp int64
		reporter  signer
	}{
		{"votes swapped", []uint64{height}, mustJSON(t, swapped), 1, reporter},
		{"hex in another case and with another prefix", []uint64{height}, mustJSON(t, upper), 1, reporter},
		{"whitespace around a block hash", []uint64{height}, mustJSON(t, spaced), 1, reporter},
		{"json indented", []uint64{height}, []byte(`  ` + strings.ReplaceAll(string(mustJSON(t, proof)), `,`, ",\n  ")), 1, reporter},
		{"json with other key order and an extra field", []uint64{height}, withNote, 1, reporter},
		{"a third conflicting block in place of one vote", []uint64{height}, mustJSON(t, thirdBlock), 1, reporter},
		{"a signature in its other valid encoding", []uint64{height}, mustJSON(t, highS), 1, reporter},
		{"an older height listed as well", []uint64{height - 1, height}, mustJSON(t, proof), 1, reporter},
		{"a newer height listed as well", []uint64{height, height + 1}, mustJSON(t, proof), 1, reporter},
		{"many heights listed", []uint64{height - 10, height - 1, height, height + 5}, mustJSON(t, proof), 1, reporter},
		{"another timestamp", []uint64{height}, mustJSON(t, proof), 999, reporter},
		{"another reporter", []uint64{height}, mustJSON(t, proof), 1, newSigner(t)},
	}
	for _, v := range variants {
		e, hash := report(t, v.reporter, offender.addr, v.heights, v.details, v.timestamp)
		if hash == baseHash && v.timestamp == 1 && v.reporter.addr == reporter.addr {
			t.Errorf("%s: test premise: the report should have another canonical hash", v.name)
		}
		got, ok := e.equivocationOffense()
		if !ok {
			t.Errorf("%s: the report does not read as an offense", v.name)
			continue
		}
		if got != want {
			t.Errorf("%s: offense %x at %d, want %x at %d", v.name, got.Key[:6], got.Height, want.Key[:6], want.Height)
		}
		// Each variant is a report the chain would accept as a first report.
		if verr := ValidateEvidence(e, hash, height+5, DefaultMaxAgeBlocks, nil); verr != nil {
			t.Errorf("%s: expected the variant to validate as a report on its own, got %v", v.name, verr)
		}
	}
}

// TestEquivocationOffenseDiffersForEachDecisionPointAndOffender: a double-sign at
// another height, round or vote type -- or by another offender -- is another
// offense, which is what keeps a validator's separate misdeeds separately
// answerable.
func TestEquivocationOffenseDiffersForEachDecisionPointAndOffender(t *testing.T) {
	offender, other, reporter := newSigner(t), newSigner(t), newSigner(t)
	offenseOf := func(who signer, height uint64, round int, voteType EquivocationVoteType) Offense {
		t.Helper()
		proof := who.proofAt(t, height, round, voteType)
		e, _ := report(t, reporter, who.addr, []uint64{height}, mustJSON(t, proof), 1)
		offense, ok := e.equivocationOffense()
		if !ok {
			t.Fatalf("does not read as an offense")
		}
		return offense
	}
	seen := map[[32]byte]string{}
	for name, offense := range map[string]Offense{
		"base":         offenseOf(offender, 100, 3, EquivocationVotePrevote),
		"height":       offenseOf(offender, 101, 3, EquivocationVotePrevote),
		"round":        offenseOf(offender, 100, 4, EquivocationVotePrevote),
		"round zero":   offenseOf(offender, 100, 0, EquivocationVotePrevote),
		"vote type":    offenseOf(offender, 100, 3, EquivocationVotePrecommit),
		"the offender": offenseOf(other, 100, 3, EquivocationVotePrevote),
	} {
		if earlier, dup := seen[offense.Key]; dup {
			t.Errorf("%s and %s are one offense", earlier, name)
		}
		seen[offense.Key] = name
	}
}

// TestOtherReportsAreTheirOwnOffense: only an equivocation carries a proof to
// identify a misdeed by. A report of any other type -- even one whose details
// happen to look like a proof -- is its own offense, recorded, penalised and
// expired by its own hash and the oldest height it lists, as before.
func TestOtherReportsAreTheirOwnOffense(t *testing.T) {
	offender := newSigner(t)
	proof := offender.proofAt(t, 100, 3, EquivocationVotePrevote)
	for _, typ := range []Type{TypeDowntime, TypeInvalidBlockProposal} {
		e := Evidence{Type: typ, Offender: offender.addr, Heights: []uint64{120, 110}, Details: mustJSON(t, proof)}
		hash, err := e.CanonicalHash()
		if err != nil {
			t.Fatalf("canonical hash: %v", err)
		}
		record := &Record{Hash: hash, Evidence: e}
		if got := record.Offense(); got.Key != hash || got.Height != 110 {
			t.Errorf("%s: offense %x at %d, want the report's hash at its oldest height 110", typ, got.Key[:6], got.Height)
		}
	}

	// An equivocation report that does not carry a readable proof cannot be
	// identified by one: it falls back to being its own offense (such a report is
	// refused before it is ever recorded).
	for name, details := range map[string][]byte{"no details": nil, "not json": []byte("proof"), "no vote type": []byte(`{"height":5}`)} {
		e := Evidence{Type: TypeEquivocation, Offender: offender.addr, Heights: []uint64{5}, Details: details}
		record := &Record{Hash: [32]byte{0x42}, Evidence: e}
		if got := record.Offense(); got.Key != record.Hash || got.Height != 5 {
			t.Errorf("%s: offense %x at %d, want the record's hash at height 5", name, got.Key[:6], got.Height)
		}
	}

	var none *Record
	if got := none.Offense(); got != (Offense{}) {
		t.Errorf("a nil record has no offense, got %+v", got)
	}
}

// TestValidateEvidenceBindsTheEquivocationProofToTheReportedHeights: the window
// checks look at the heights a report LISTS, which its reporter picks. A proof
// must be for one of them, and for a height the chain has reached; otherwise a
// proof for a double-sign of any age -- or of a height that never existed -- could
// be carried by a report that lists a recent height.
func TestValidateEvidenceBindsTheEquivocationProofToTheReportedHeights(t *testing.T) {
	offender, reporter := newSigner(t), newSigner(t)
	const current = 60
	cases := []struct {
		name        string
		proofHeight uint64
		heights     []uint64
		maxAge      uint64
		want        RejectReason
	}{
		{"the proof's height is listed", 40, []uint64{40}, 100, ""},
		{"listed among several", 40, []uint64{30, 40, 50}, 100, ""},
		{"the current height", current, []uint64{current}, 100, ""},
		{"not listed: another height in the window", 40, []uint64{current}, 100, RejectReasonInvalidEquivocationProof},
		{"not listed: a height the chain never reached", 1_000_000_000, []uint64{current}, 100, RejectReasonInvalidEquivocationProof},
		{"not listed although older heights are", 40, []uint64{39, 41}, 100, RejectReasonInvalidEquivocationProof},
		{"above the current height even though listed", 1_000_000_000, []uint64{1_000_000_000}, 100, RejectReasonInvalidEquivocationProof},
		{"one above the current height even though listed", current + 1, []uint64{current + 1}, 100, RejectReasonInvalidEquivocationProof},
		{"an old proof listed alone is expired", 3, []uint64{3}, 10, RejectReasonExpired},
		{"an old proof does not become valid by listing the current height too", 3, []uint64{3, current}, 10, RejectReasonExpired},
		{"the oldest height still in the window", 50, []uint64{50}, 10, ""},
		{"one older than the window", 49, []uint64{49}, 10, RejectReasonExpired},
	}
	for _, tc := range cases {
		proof := offender.proofAt(t, tc.proofHeight, 1, EquivocationVotePrevote)
		e, hash := report(t, reporter, offender.addr, tc.heights, mustJSON(t, proof), 1)
		verr := ValidateEvidence(e, hash, current, tc.maxAge, nil)
		switch {
		case tc.want == "" && verr != nil:
			t.Errorf("%s: expected acceptance, got %v", tc.name, verr)
		case tc.want != "" && (verr == nil || verr.Reason != tc.want):
			t.Errorf("%s: expected %q, got %v", tc.name, tc.want, verr)
		}
	}

	// Reports of the other types are not read as proofs, so the height rules
	// above do not apply to them.
	e := Evidence{Type: TypeDowntime, Offender: offender.addr, Heights: []uint64{current}, Details: []byte("anything"), Reporter: reporter.addr, Timestamp: 1}
	hash, err := e.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	if e.ReporterSig, err = ethcrypto.Sign(e.SigningDigest(hash), reporter.priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if verr := ValidateEvidence(&e, hash, current, 100, nil); verr != nil {
		t.Errorf("a downtime report must not be read as a proof, got %v", verr)
	}
}

// TestValidateEvidenceStillVerifiesTheVotesOfABoundProof: a proof for a listed
// height is still refused unless the offender's own key signed both votes and
// they conflict.
func TestValidateEvidenceStillVerifiesTheVotesOfABoundProof(t *testing.T) {
	offender, impostor, reporter := newSigner(t), newSigner(t), newSigner(t)
	const height = 40
	forged := impostor.proofAt(t, height, 1, EquivocationVotePrevote)
	same := offender.proofAt(t, height, 1, EquivocationVotePrevote)
	same.VoteB = same.VoteA
	wrongPoint := offender.proofAt(t, height, 1, EquivocationVotePrevote)
	wrongPoint.Round = 2 // the votes were signed for round 1
	for name, proof := range map[string]EquivocationProof{"signed by another key": forged, "not a conflict": same, "votes signed for another round": wrongPoint} {
		e, hash := report(t, reporter, offender.addr, []uint64{height}, mustJSON(t, proof), 1)
		if verr := ValidateEvidence(e, hash, 60, 100, nil); verr == nil || verr.Reason != RejectReasonInvalidEquivocationProof {
			t.Errorf("%s: expected %q, got %v", name, RejectReasonInvalidEquivocationProof, verr)
		}
	}
}
