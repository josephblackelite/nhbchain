package bft

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A validator that starts with no vote record on a chain that already has blocks cannot know whether it voted in the height in flight (PL-R5T-76). It must
// say so; a node that is not a validator, a chain with no blocks yet, and a node with a record must stay quiet.

func logsDuring(t *testing.T, run func()) string {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	run()
	return buf.String()
}

func TestAValidatorWithNoVoteRecordOnAChainWithBlocksSaysSo(t *testing.T) {
	tv := newTestValidators(t, 2)
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")

	out := logsDuring(t, func() {
		node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5}
		NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))
	})

	if !strings.Contains(out, "sign_state_missing") {
		t.Fatalf("expected a warning with event sign_state_missing, log was:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("the warning must be at WARN level, log was:\n%s", out)
	}
}

func TestNoWarningWhenTheVoteRecordIsNotExpected(t *testing.T) {
	tv := newTestValidators(t, 2)
	outsider := newTestValidators(t, 1)

	for _, tc := range []struct {
		name   string
		node   *signGuardNode
		key    int
		record bool
		path   bool
	}{
		{"a chain with no blocks yet", &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 0}, 0, false, true},
		{"a record is present for the contested height", &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5}, 0, true, true},
		{"no record file is configured (memory only, as in tests and devnets)", &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5}, 0, false, false},
	} {
		path := ""
		if tc.path {
			path = filepath.Join(t.TempDir(), "bft_sign_state.json")
		}
		if tc.record {
			if err := writeSignState(path, signState{Height: 6, Round: 1, Prevote: &signedValue{BlockHash: []byte{1}}}); err != nil {
				t.Fatal(err)
			}
		}
		out := logsDuring(t, func() { NewEngine(tc.node, tv.keys[tc.key], &captureBroadcaster{}, WithSignStatePath(path)) })
		if strings.Contains(out, "sign_state_missing") {
			t.Fatalf("%s: no warning expected, log was:\n%s", tc.name, out)
		}
	}

	// A node that is not in the validator set never signs a vote, so it has nothing to remember.
	out := logsDuring(t, func() {
		node := &signGuardNode{validatorSet: tv.set, self: outsider.addrs[0], height: 5}
		NewEngine(node, outsider.keys[0], &captureBroadcaster{}, WithSignStatePath(filepath.Join(t.TempDir(), "bft_sign_state.json")))
	})
	if strings.Contains(out, "sign_state_missing") {
		t.Fatalf("a node outside the validator set must not warn, log was:\n%s", out)
	}
}

// The record is read by whichever build starts next, so its on-disk shape is a contract between builds. A record written by hand (the runbook's last resort
// for a start with no record) must be honoured exactly like one the node wrote.
func TestTheVoteRecordFormatIsPinnedAndAHandWrittenRecordIsHonoured(t *testing.T) {
	written, err := json.Marshal(signState{Height: 6, Round: 3, Prevote: &signedValue{BlockHash: []byte{1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	const pinned = `{"height":6,"round":3,"prevote":{"blockHash":"AQID"}}`
	if string(written) != pinned {
		t.Fatalf("the vote record format changed:\n got %s\nwant %s", written, pinned)
	}

	tv := newTestValidators(t, 2)
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	if err := os.WriteFile(path, []byte(pinned), 0o600); err != nil {
		t.Fatal(err)
	}
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5}
	engine := NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))

	if engine.signed.Height != 6 || engine.signed.Round != 3 || engine.signed.Prevote == nil || !bytes.Equal(engine.signed.Prevote.BlockHash, []byte{1, 2, 3}) {
		t.Fatalf("the hand-written record was not restored as written: %+v", engine.signed)
	}
	if err := engine.claimVote(Prevote, []byte{9, 9, 9}, 3, 6); err == nil {
		t.Fatal("a different prevote in the recorded round must be refused")
	}
	if err := engine.claimVote(Prevote, []byte{1, 2, 3}, 3, 6); err != nil {
		t.Fatalf("the recorded prevote may be repeated: %v", err)
	}
}

// The line the validator runbook tells an operator to write for a start with no record: a vote for nothing in the last round seen in the log.
func TestTheRunbooksHandWrittenRecordHoldsTheValidatorBackFromThatRound(t *testing.T) {
	tv := newTestValidators(t, 2)
	path := filepath.Join(t.TempDir(), "bft_sign_state.json")
	if err := os.WriteFile(path, []byte(`{"height":6,"round":2,"prevote":{},"precommit":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	node := &signGuardNode{validatorSet: tv.set, self: tv.addrs[0], height: 5}
	engine := NewEngine(node, tv.keys[0], &captureBroadcaster{}, WithSignStatePath(path))

	for _, kind := range []VoteType{Prevote, Precommit} {
		if err := engine.claimVote(kind, []byte{7}, 2, 6); err == nil {
			t.Fatalf("a %s for a block in the recorded round must be refused", kind)
		}
		if err := engine.claimVote(kind, []byte{7}, 1, 6); err == nil {
			t.Fatalf("a %s in an earlier round must be refused", kind)
		}
		if err := engine.claimVote(kind, []byte{7}, 3, 6); err != nil {
			t.Fatalf("a %s in the next round must be allowed: %v", kind, err)
		}
		engine.signed = signState{Height: 6, Round: 2, Prevote: &signedValue{}, Precommit: &signedValue{}}
	}
}
