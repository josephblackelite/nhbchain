package core

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/config"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// Stage 1 of the bridge trust plan (ledger PL-BC-C): threshold signers, caps, waiting, guardian, rotations, and the
// escrow-coverage view. Every test runs the real apply path and checks the supply and bridge invariants.

func stage1Fixture(t *testing.T, mutate func(*BridgeParams)) *bcFixture {
	t.Helper()
	f := newBridgeFixture(t)
	f.fund("alice", wei(10_000), wei(0))
	if mutate != nil {
		f.setParams(mutate)
	}
	if err := f.send("alice", types.TxTypeBridgeLock, wei(8_000), lockPayload()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	return f
}

// ---- the attestation digest

func TestReleaseDigestIsTheDocumentedLayoutAndCommitsToEveryField(t *testing.T) {
	burn := []byte("burn-digest")
	to := bytes.Repeat([]byte{0xAB}, 20)
	amount := wei(5)
	got, err := BridgeReleaseDigest(7, burn, to, amount)
	if err != nil {
		t.Fatal(err)
	}
	// The layout written out by hand: domain, network id (8 bytes), escrow (20), burn id length (2 bytes), burn id,
	// recipient (20), amount (32 bytes).
	var want bytes.Buffer
	want.WriteString("NHB_BRIDGE_RELEASE_V1")
	var u64 [8]byte
	binary.BigEndian.PutUint64(u64[:], 7)
	want.Write(u64[:])
	want.Write(bridgeEscrowAddress())
	want.Write([]byte{0, byte(len(burn))})
	want.Write(burn)
	want.Write(to)
	var amt [32]byte
	amount.FillBytes(amt[:])
	want.Write(amt[:])
	if w := ethcrypto.Keccak256(want.Bytes()); !bytes.Equal(w, got[:]) {
		t.Fatalf("digest %x is not keccak256 of the documented layout %x", got, w)
	}
	// Changing any one field changes the digest.
	variants := map[string]func() ([32]byte, error){
		"network":   func() ([32]byte, error) { return BridgeReleaseDigest(8, burn, to, amount) },
		"burn id":   func() ([32]byte, error) { return BridgeReleaseDigest(7, []byte("burn-digesu"), to, amount) },
		"recipient": func() ([32]byte, error) { return BridgeReleaseDigest(7, burn, bytes.Repeat([]byte{0xAC}, 20), amount) },
		"amount":    func() ([32]byte, error) { return BridgeReleaseDigest(7, burn, to, wei(6)) },
	}
	for name, fn := range variants {
		d, err := fn()
		if err != nil || d == got {
			t.Fatalf("a different %s did not change the digest (%v)", name, err)
		}
	}
	// Bad inputs are refused, not hashed.
	for name, fn := range map[string]func() error{
		"empty burn id": func() error { _, err := BridgeReleaseDigest(7, nil, to, amount); return err },
		"long burn id":  func() error { _, err := BridgeReleaseDigest(7, bytes.Repeat([]byte{1}, 65), to, amount); return err },
		"short to":      func() error { _, err := BridgeReleaseDigest(7, burn, to[:19], amount); return err },
		"zero amount":   func() error { _, err := BridgeReleaseDigest(7, burn, to, big.NewInt(0)); return err },
		"nil amount":    func() error { _, err := BridgeReleaseDigest(7, burn, to, nil); return err },
		"amount too big": func() error {
			_, err := BridgeReleaseDigest(7, burn, to, new(big.Int).Lsh(big.NewInt(1), 256))
			return err
		},
	} {
		if err := fn(); !errors.Is(err, ErrBridgeInvalidPayload) {
			t.Fatalf("%s: %v, want ErrBridgeInvalidPayload", name, err)
		}
	}
}

// Two values are pinned so that an off-chain signer, which builds the same digest in its own code, can check itself
// against them: the escrow account's 20 bytes and the digest for fixed inputs. Changing the layout fails this test.
const (
	pinnedEscrowHex = "ffabf86b2c1adf2eb9a94806008f1566ac1a258a"
	pinnedDigestHex = "bdff957133f383058fc2ffd49b7f61d66e9be7dae4de81cb93e51195a9fe0c51"
)

func TestReleaseDigestPinnedVector(t *testing.T) {
	if got := hex.EncodeToString(bridgeEscrowAddress()); got != pinnedEscrowHex {
		t.Fatalf("escrow %s, pinned %s", got, pinnedEscrowHex)
	}
	to := bytes.Repeat([]byte{0xAB}, 20)
	d, err := BridgeReleaseDigest(7, []byte("burn-vector"), to, wei(5))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(d[:]); got != pinnedDigestHex {
		t.Fatalf("digest %s, pinned %s", got, pinnedDigestHex)
	}
}

// ---- threshold signers

func TestReleaseNeedsExactlyThresholdDistinctSigners(t *testing.T) {
	f := stage1Fixture(t, nil)
	// One signature: refused. The same signer twice is still one signer. Two distinct signers: paid.
	if err := f.release("t-1", "bob", wei(10), "signer1"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("one signature: %v", err)
	}
	if err := f.release("t-1", "bob", wei(10), "signer1", "signer1"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("one signer twice: %v", err)
	}
	if err := f.release("t-1", "bob", wei(10), "signer2", "signer3"); err != nil {
		t.Fatalf("two distinct signers: %v", err)
	}
	// All three is fine too (more than the threshold, not more than the signers).
	if err := f.release("t-2", "bob", wei(10), "signer1", "signer2", "signer3"); err != nil {
		t.Fatalf("three signers: %v", err)
	}
	// More signatures than there are signers is refused, whatever they are.
	if err := f.release("t-3", "bob", wei(10), "signer1", "signer2", "signer3", "signer1"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("four signatures for three signers: %v", err)
	}
	// A signature from an address that is not a signer fails the whole release, even beside two good ones.
	if err := f.release("t-4", "bob", wei(10), "signer1", "signer2", "guardian"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a guardian's signature among signers: %v", err)
	}
	if err := f.release("t-5", "bob", wei(10), "alice", "bob"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("two outsiders: %v", err)
	}
	// No signatures at all.
	if err := f.send("relayer", types.TxTypeBridgeRelease, big.NewInt(0), bridgeReleasePayload{Op: BridgeOpRelease, BurnID: []byte("t-6"), Recipient: f.addr["bob"], Amount: wei(1)}); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("no signatures: %v", err)
	}
	f.checkInvariants("after signature tests")
}

func TestSignaturesBindTheBurnTheRecipientTheAmountAndTheNetwork(t *testing.T) {
	f := stage1Fixture(t, nil)
	good := func(burn string, to []byte, amount *big.Int) [][]byte {
		return [][]byte{f.signRelease("signer1", burn, to, amount), f.signRelease("signer2", burn, to, amount)}
	}
	send := func(burn string, to []byte, amount *big.Int, sigs [][]byte) error {
		return f.send("relayer", types.TxTypeBridgeRelease, big.NewInt(0), bridgeReleasePayload{Op: BridgeOpRelease, BurnID: []byte(burn), Recipient: to, Amount: amount, Signatures: sigs})
	}
	bob, alice := f.addr["bob"], f.addr["alice"]
	// Signed for 10, submitted for 11; signed for bob, submitted for alice; signed for another burn id.
	if err := send("b-1", bob, wei(11), good("b-1", bob, wei(10))); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a changed amount: %v", err)
	}
	if err := send("b-1", alice, wei(10), good("b-1", bob, wei(10))); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a changed recipient: %v", err)
	}
	if err := send("b-2", bob, wei(10), good("b-1", bob, wei(10))); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a changed burn id: %v", err)
	}
	// Signed for another network.
	other := func(signer string) []byte {
		d, _ := BridgeReleaseDigest(bridgeNetworkID(f.sp)+1, []byte("b-3"), bob, wei(10))
		sig, _ := ethcrypto.Sign(d[:], f.keys[signer].PrivateKey)
		return sig
	}
	if err := send("b-3", bob, wei(10), [][]byte{other("signer1"), other("signer2")}); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("signatures for another network: %v", err)
	}
	// Malformed signatures: wrong length, an impossible recovery id, garbage that does not recover.
	sigs := good("b-4", bob, wei(10))
	short := [][]byte{sigs[0][:64], sigs[1]}
	if err := send("b-4", bob, wei(10), short); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a short signature: %v", err)
	}
	badV := append([]byte(nil), sigs[0]...)
	badV[64] = 5
	if err := send("b-4", bob, wei(10), [][]byte{badV, sigs[1]}); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a recovery id of 5: %v", err)
	}
	zero := make([]byte, 65)
	if err := send("b-4", bob, wei(10), [][]byte{zero, sigs[1]}); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("an all-zero signature: %v", err)
	}
	// The same signatures with v as 27 or 28 are accepted (the usual Ethereum form).
	legacy := [][]byte{append([]byte(nil), sigs[0]...), append([]byte(nil), sigs[1]...)}
	legacy[0][64] += 27
	legacy[1][64] += 27
	if err := send("b-4", bob, wei(10), legacy); err != nil {
		t.Fatalf("signatures with v 27/28: %v", err)
	}
	f.checkInvariants("after binding tests")
}

// ---- caps

func TestReleaseCapsPerReleaseAndPerWindow(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.MaxReleaseWei = wei(1_000)
		p.WindowBlocks = 100
		p.WindowCapWei = wei(2_500)
	})
	f.atHeight(100)
	if err := f.release("c-1", "bob", wei(1_001)); !errors.Is(err, ErrBridgeAboveMaxRelease) {
		t.Fatalf("one unit above the per-release cap: %v", err)
	}
	if err := f.release("c-1", "bob", wei(1_000)); err != nil {
		t.Fatalf("exactly the per-release cap: %v", err)
	}
	if err := f.release("c-2", "bob", wei(1_000)); err != nil {
		t.Fatalf("second release: %v", err)
	}
	// 2,000 used of 2,500: 501 does not fit, 500 does (exactly the cap), then nothing does.
	if err := f.release("c-3", "bob", wei(501)); !errors.Is(err, ErrBridgeWindowCap) {
		t.Fatalf("a release that passes the window cap by one: %v", err)
	}
	if err := f.release("c-3", "bob", wei(500)); err != nil {
		t.Fatalf("a release that fills the window exactly: %v", err)
	}
	if err := f.release("c-4", "bob", wei(1)); !errors.Is(err, ErrBridgeWindowCap) {
		t.Fatalf("a release into a full window: %v", err)
	}
	view, _ := f.sp.BridgeStatus()
	if view.WindowUsedWei.Cmp(wei(2_500)) != 0 || view.WindowCapWei.Cmp(wei(2_500)) != 0 {
		t.Fatalf("window used %s of %s, want 2,500 of 2,500", view.WindowUsedWei, view.WindowCapWei)
	}
	// The window is height / 100: block 199 is the same window, block 200 the next.
	f.atHeight(199)
	if err := f.release("c-4", "bob", wei(1)); !errors.Is(err, ErrBridgeWindowCap) {
		t.Fatalf("the last block of the window: %v", err)
	}
	f.atHeight(200)
	if err := f.release("c-4", "bob", wei(1_000)); err != nil {
		t.Fatalf("the first block of the next window: %v", err)
	}
	if view, _ := f.sp.BridgeStatus(); view.WindowUsedWei.Cmp(wei(1_000)) != 0 {
		t.Fatalf("the new window shows %s used, want 1,000", view.WindowUsedWei)
	}
	// A release in the LAST block of a window counts against that window and not the next one: window 2 (blocks
	// 200 to 299) holds 2,000 after this, and window 3 starts empty and can take exactly its cap of 2,500.
	f.atHeight(299)
	if err := f.release("c-5", "bob", wei(1_000)); err != nil {
		t.Fatalf("a release in the last block of a window: %v", err)
	}
	f.atHeight(300)
	for i, id := range []string{"c-6", "c-7"} {
		if err := f.release(id, "bob", wei(1_000)); err != nil {
			t.Fatalf("release %d of the new window: %v", i+1, err)
		}
	}
	if err := f.release("c-8", "bob", wei(500)); err != nil {
		t.Fatalf("the rest of a window that the last block of the one before must not have touched: %v", err)
	}
	f.checkInvariants("after cap tests")
}

// ---- waiting

func TestLargeReleasesWaitAndAreCountedAgainstTheWindowWhenQueued(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(300)
		p.DelayBlocks = 50
		p.WindowCapWei = wei(1_000)
	})
	f.atHeight(100)
	bobBefore, _ := f.balances(f.addr["bob"])
	// 300 is not above 300: it pays at once. 301 waits.
	if err := f.release("w-1", "bob", wei(300)); err != nil {
		t.Fatal(err)
	}
	if err := f.release("w-2", "bob", wei(301)); err != nil {
		t.Fatalf("a release above the waiting threshold: %v", err)
	}
	bobAfter, _ := f.balances(f.addr["bob"])
	if new(big.Int).Sub(bobAfter, bobBefore).Cmp(wei(300)) != 0 {
		t.Fatalf("bob received %s, want only the immediate 300", new(big.Int).Sub(bobAfter, bobBefore))
	}
	view, _ := f.sp.BridgeStatus()
	if view.PendingWei.Cmp(wei(301)) != 0 || view.ReleasedWei.Cmp(wei(300)) != 0 || view.OutstandingWei.Cmp(wei(7_700)) != 0 {
		t.Fatalf("status after a paid and a queued release: pending %s released %s outstanding %s", view.PendingWei, view.ReleasedWei, view.OutstandingWei)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.release.queued"), map[string]string{
		"burnId": "772d32", "recipient": ammAddrString(f.addr["bob"]), "amountWei": wei(301).String(), "executableAt": "150",
	})
	// The queued release already uses the window: 300 + 301 + 400 fits 1,000, but 400 + 400 more does not.
	if err := f.release("w-3", "bob", wei(399)); err != nil {
		t.Fatalf("the rest of the window: %v", err)
	}
	if err := f.release("w-4", "bob", wei(1)); !errors.Is(err, ErrBridgeWindowCap) {
		t.Fatalf("a queued release must use the window at once: %v", err)
	}
	// Execute: unknown id, too early, then due (the boundary block itself is allowed).
	if err := f.op("relayer", BridgeOpExecute, "nope"); !errors.Is(err, ErrBridgeNotPending) {
		t.Fatalf("executing an unknown id: %v", err)
	}
	f.atHeight(149)
	if err := f.op("relayer", BridgeOpExecute, "w-2"); !errors.Is(err, ErrBridgeNotYet) {
		t.Fatalf("one block early: %v", err)
	}
	f.atHeight(150)
	if err := f.op("relayer", BridgeOpExecute, "w-2"); err != nil {
		t.Fatalf("on the due block: %v", err)
	}
	// w-3 (399) is above the waiting threshold too, so it is still queued: only w-2 was paid.
	if got, _ := f.balances(f.addr["bob"]); new(big.Int).Sub(got, bobAfter).Cmp(wei(301)) != 0 {
		t.Fatalf("after the execution bob gained %s, want 301", new(big.Int).Sub(got, bobAfter))
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.released"), map[string]string{
		"burnId": "772d32", "recipient": ammAddrString(f.addr["bob"]), "amountWei": wei(301).String(), "executed": "true",
	})
	if err := f.op("relayer", BridgeOpExecute, "w-2"); !errors.Is(err, ErrBridgeNotPending) {
		t.Fatalf("executing twice: %v", err)
	}
	if err := f.release("w-2", "bob", wei(301)); !errors.Is(err, ErrBridgeAlreadyReleased) {
		t.Fatalf("releasing an executed id again: %v", err)
	}
	if view, _ := f.sp.BridgeStatus(); view.PendingWei.Cmp(wei(399)) != 0 {
		t.Fatalf("pending %s after the execution, want only w-3's 399", view.PendingWei)
	}
	f.checkInvariants("after waiting releases")
}

func TestAQueuedReleaseIsSpokenForAndCannotBeDoubleBooked(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(0) // every release waits
		p.DelayBlocks = 10
		p.MaxReleaseWei = wei(8_000)
		p.WindowCapWei = wei(20_000)
	})
	// 8,000 is outstanding: queue 5,000, then only 3,000 more can be queued, however the escrow looks.
	if err := f.release("q-1", "bob", wei(5_000)); err != nil {
		t.Fatal(err)
	}
	if err := f.release("q-2", "bob", wei(3_001)); !errors.Is(err, ErrBridgeEscrowShort) {
		t.Fatalf("queuing more than is free: %v", err)
	}
	if err := f.release("q-2", "bob", wei(3_000)); err != nil {
		t.Fatalf("queuing exactly what is free: %v", err)
	}
	f.atHeight(110)
	for _, id := range []string{"q-1", "q-2"} {
		if err := f.op("relayer", BridgeOpExecute, id); err != nil {
			t.Fatalf("executing %s: %v", id, err)
		}
	}
	if view, _ := f.sp.BridgeStatus(); view.OutstandingWei.Sign() != 0 || view.EscrowBalanceWei.Sign() != 0 || view.PendingWei.Sign() != 0 {
		t.Fatalf("after executing everything: %+v", view)
	}
	f.checkInvariants("after double-booking test")
}

// ---- guardian

func TestOnlyTheGuardianPausesUnpausesAndCancels(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(100)
		p.DelayBlocks = 20
	})
	f.atHeight(100)
	if err := f.release("g-1", "bob", wei(200)); err != nil { // waits
		t.Fatal(err)
	}
	for _, who := range []string{"relayer", "alice", "signer1", "signer2", "signer3"} {
		for _, op := range []uint8{BridgeOpCancel, BridgeOpPause, BridgeOpUnpause} {
			if err := f.op(who, op, "g-1"); !errors.Is(err, ErrBridgeNotGuardian) {
				t.Fatalf("%s doing op %d: %v, want ErrBridgeNotGuardian", who, op, err)
			}
		}
	}
	// The guardian can stop things, never release: it has no signatures.
	if err := f.send("guardian", types.TxTypeBridgeRelease, big.NewInt(0), bridgeReleasePayload{Op: BridgeOpRelease, BurnID: []byte("g-2"), Recipient: f.addr["guardian"], Amount: wei(1)}); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a guardian release without signers: %v", err)
	}
	// Pause: locks, releases and executions stop; the guardian may still cancel.
	if err := f.op("guardian", BridgeOpPause, ""); err != nil {
		t.Fatalf("pause: %v", err)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.paused"), map[string]string{"height": "100"})
	if view, _ := f.sp.BridgeStatus(); !view.Paused {
		t.Fatalf("status does not say paused")
	}
	if err := f.send("alice", types.TxTypeBridgeLock, wei(1), lockPayload()); !errors.Is(err, ErrBridgePaused) {
		t.Fatalf("a lock while paused: %v", err)
	}
	if err := f.release("g-3", "bob", wei(50)); !errors.Is(err, ErrBridgePaused) {
		t.Fatalf("a release while paused: %v", err)
	}
	f.atHeight(130)
	if err := f.op("relayer", BridgeOpExecute, "g-1"); !errors.Is(err, ErrBridgePaused) {
		t.Fatalf("an execution while paused: %v", err)
	}
	// Pausing twice is harmless; unpausing brings everything back.
	if err := f.op("guardian", BridgeOpPause, ""); err != nil {
		t.Fatalf("a second pause: %v", err)
	}
	if err := f.op("guardian", BridgeOpUnpause, ""); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.unpaused"), map[string]string{"height": "130"})
	if err := f.send("alice", types.TxTypeBridgeLock, wei(1), lockPayload()); err != nil {
		t.Fatalf("a lock after unpausing: %v", err)
	}
	if err := f.op("relayer", BridgeOpExecute, "g-1"); err != nil {
		t.Fatalf("an execution after unpausing: %v", err)
	}
	f.checkInvariants("after pause tests")
}

func TestCancelDropsAWaitingReleaseAndKeepsTheBurnIdSpent(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(100)
		p.DelayBlocks = 20
	})
	f.atHeight(100)
	if err := f.release("x-1", "bob", wei(250)); err != nil {
		t.Fatal(err)
	}
	escrowBefore, _ := f.balances(bridgeEscrowAddress())
	if err := f.op("guardian", BridgeOpCancel, "unknown"); !errors.Is(err, ErrBridgeNotPending) {
		t.Fatalf("cancelling an unknown id: %v", err)
	}
	if err := f.op("guardian", BridgeOpCancel, "x-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.release.cancelled"), map[string]string{
		"burnId": "782d31", "recipient": ammAddrString(f.addr["bob"]), "amountWei": wei(250).String(),
	})
	// The ZNHB stays in the escrow, nothing is pending, nothing was paid, and the id is spent for good.
	escrowAfter, _ := f.balances(bridgeEscrowAddress())
	bobZ, _ := f.balances(f.addr["bob"])
	if escrowAfter.Cmp(escrowBefore) != 0 || bobZ.Sign() != 0 {
		t.Fatalf("escrow %s -> %s, bob %s: a cancel must move nothing", escrowBefore, escrowAfter, bobZ)
	}
	view, _ := f.sp.BridgeStatus()
	if view.PendingWei.Sign() != 0 || view.ReleasedWei.Sign() != 0 || view.OutstandingWei.Cmp(wei(8_000)) != 0 {
		t.Fatalf("status after a cancel: %+v", view)
	}
	f.atHeight(200)
	if err := f.op("relayer", BridgeOpExecute, "x-1"); !errors.Is(err, ErrBridgeNotPending) {
		t.Fatalf("executing a cancelled release: %v", err)
	}
	if err := f.release("x-1", "bob", wei(250)); !errors.Is(err, ErrBridgeAlreadyReleased) {
		t.Fatalf("releasing a cancelled burn id again: %v", err)
	}
	var rec bridgeReleaseRecord
	if found, err := nhbstate.NewManager(f.sp.Trie).KVGet(bridgeReleaseKey([]byte("x-1")), &rec); err != nil || !found || rec.Status != bridgeStatusCancelled {
		t.Fatalf("the cancelled record: %v %v %+v", found, err, rec)
	}
	f.checkInvariants("after a cancel")
}

// ---- rotations

func TestARotationReplacesSignersGuardianAndLimitsFromItsHeight(t *testing.T) {
	f := stage1Fixture(t, nil)
	first := f.defaultParams(100)
	second := f.defaultParams(500)
	second.Signers = [][20]byte{{}, {}}
	copy(second.Signers[0][:], f.addr["signer3"])
	copy(second.Signers[1][:], f.addr["relayer"]) // a new signer
	second.Threshold = 2
	copy(second.Guardian[:], f.addr["provider"]) // a new guardian
	second.MaxReleaseWei = wei(100)
	second.DelayAboveWei = wei(50)
	second.DelayBlocks = 5
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, Params: []BridgeParams{first, second}})

	f.atHeight(499)
	if view, _ := f.sp.BridgeStatus(); view.NextRotation != 500 || len(view.Signers) != 3 {
		t.Fatalf("before the rotation the view shows %d signers and next rotation %d", len(view.Signers), view.NextRotation)
	}
	if err := f.release("r-1", "bob", wei(900), "signer1", "signer2"); err != nil {
		t.Fatalf("a release under the first rules: %v", err)
	}
	// Under the first rules this large release waits for no one; from block 500 the new rules cap it and delay it.
	f.atHeight(500)
	if view, _ := f.sp.BridgeStatus(); view.NextRotation != 0 || len(view.Signers) != 2 || view.MaxReleaseWei.Cmp(wei(100)) != 0 {
		t.Fatalf("from the rotation the view shows %d signers, next %d, max %v", len(view.Signers), view.NextRotation, view.MaxReleaseWei)
	}
	if err := f.release("r-2", "bob", wei(10), "signer1", "signer2"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("the old signers after the rotation: %v", err)
	}
	if err := f.release("r-2", "bob", wei(101), "signer3", "relayer"); !errors.Is(err, ErrBridgeAboveMaxRelease) {
		t.Fatalf("the new cap: %v", err)
	}
	if err := f.release("r-2", "bob", wei(80), "signer3", "relayer"); err != nil {
		t.Fatalf("the new signers: %v", err)
	}
	if view, _ := f.sp.BridgeStatus(); view.PendingWei.Cmp(wei(80)) != 0 {
		t.Fatalf("a release above the new waiting threshold was not queued: %+v", view)
	}
	// The old guardian no longer has the power, the new one does.
	if err := f.op("guardian", BridgeOpPause, ""); !errors.Is(err, ErrBridgeNotGuardian) {
		t.Fatalf("the old guardian after the rotation: %v", err)
	}
	if err := f.op("provider", BridgeOpCancel, "r-2"); err != nil {
		t.Fatalf("the new guardian: %v", err)
	}
	f.checkInvariants("after a rotation")
}

func TestAWaitingReleaseSurvivesARotationOfTheSigners(t *testing.T) {
	f := stage1Fixture(t, nil)
	first := f.defaultParams(100)
	first.DelayAboveWei = wei(100)
	first.DelayBlocks = 400
	second := f.defaultParams(300)
	second.Signers = [][20]byte{{1}, {2}}
	second.Threshold = 2
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, Params: []BridgeParams{first, second}})
	f.atHeight(100)
	if err := f.release("s-1", "bob", wei(500)); err != nil {
		t.Fatal(err)
	}
	f.atHeight(500)
	if err := f.op("relayer", BridgeOpExecute, "s-1"); err != nil {
		t.Fatalf("executing a release queued under signers that were rotated out: %v", err)
	}
	if got, _ := f.balances(f.addr["bob"]); got.Cmp(wei(500)) != 0 {
		t.Fatalf("bob holds %s, want 500", got)
	}
	f.checkInvariants("after a rotation with a waiting release")
}

func TestWindowKeysDoNotCollideWhenARotationChangesTheWindowLength(t *testing.T) {
	f := stage1Fixture(t, nil)
	first := f.defaultParams(100)
	first.WindowBlocks = 100
	first.WindowCapWei = wei(1_000)
	second := f.defaultParams(200)
	second.WindowBlocks = 50 // block 200 is window 2 of length 100 and window 4 of length 50
	second.WindowCapWei = wei(1_000)
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, Params: []BridgeParams{first, second}})
	f.atHeight(150)
	if err := f.release("k-1", "bob", wei(1_000)); err != nil { // window 1 of 100
		t.Fatal(err)
	}
	f.atHeight(200)
	if err := f.release("k-2", "bob", wei(1_000)); err != nil {
		t.Fatalf("a new window length starts with an empty window: %v", err)
	}
	f.checkInvariants("after changing the window length")
}

// ---- the escrow view and the bookkeeping

func TestEscrowViewAndNonceAndInvariantWithPendingReleases(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(100)
		p.DelayBlocks = 10
	})
	f.atHeight(100)
	nonce := f.nonceOf("relayer")
	if err := f.release("v-1", "bob", wei(5)); err != nil {
		t.Fatal(err)
	}
	if err := f.release("v-2", "bob", wei(200)); err != nil {
		t.Fatal(err)
	}
	if got := f.nonceOf("relayer"); got != nonce+2 {
		t.Fatalf("relayer nonce %d, want %d: every accepted operation consumes one", got, nonce+2)
	}
	// A refused operation consumes nothing.
	_ = f.release("v-3", "bob", wei(5), "signer1")
	if got := f.nonceOf("relayer"); got != nonce+2 {
		t.Fatalf("a refused release moved the nonce to %d", got)
	}
	view, _ := f.sp.BridgeStatus()
	if !view.EscrowCovered || view.EscrowBalanceWei.Cmp(wei(7_995)) != 0 || view.OutstandingWei.Cmp(wei(7_995)) != 0 || view.PendingWei.Cmp(wei(200)) != 0 {
		t.Fatalf("view %+v", view)
	}
	if view.Threshold != 2 || len(view.Signers) != 3 || view.Guardian == "" || view.DelayBlocks != 10 || view.DelayAboveWei.Cmp(wei(100)) != 0 {
		t.Fatalf("the limits in the view: %+v", view)
	}
	// The invariant notices a book that says more is waiting than is outstanding, and an escrow that is short.
	if err := nhbstate.NewManager(f.sp.Trie).KVPut([]byte("bridge/totals"), bridgeTotals{Locked: wei(8_000), Released: wei(5), Pending: wei(9_000)}); err != nil {
		t.Fatal(err)
	}
	if err := f.sp.CheckBridgeInvariant(); err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Fatalf("more waiting than outstanding: %v", err)
	}
	escrow, _ := f.sp.getAccount(bridgeEscrowAddress())
	escrow.BalanceZNHB = wei(1)
	if err := f.sp.setAccount(bridgeEscrowAddress(), escrow); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.sp.BridgeStatus(); v.EscrowCovered {
		t.Fatalf("the view says the escrow is covered with 1 ZNHB in the escrow against 7,995 outstanding")
	}
}

// Executing a waiting release when the escrow somehow holds less than it owes is refused, not paid short.
func TestExecuteRefusesWhenTheEscrowIsShort(t *testing.T) {
	f := stage1Fixture(t, func(p *BridgeParams) {
		p.DelayAboveWei = wei(100)
		p.DelayBlocks = 5
	})
	f.atHeight(100)
	if err := f.release("e-1", "bob", wei(300)); err != nil {
		t.Fatal(err)
	}
	escrow, _ := f.sp.getAccount(bridgeEscrowAddress())
	escrow.BalanceZNHB = wei(299)
	if err := f.sp.setAccount(bridgeEscrowAddress(), escrow); err != nil {
		t.Fatal(err)
	}
	f.atHeight(105)
	if err := f.op("relayer", BridgeOpExecute, "e-1"); !errors.Is(err, ErrBridgeEscrowShort) {
		t.Fatalf("executing against a short escrow: %v", err)
	}
	if bobZ, _ := f.balances(f.addr["bob"]); bobZ.Sign() != 0 {
		t.Fatalf("a refused execution paid %s", bobZ)
	}
}

// ---- the settings a main refuses to start with

func TestBridgeRulesFromConfigRefuseEveryInconsistentValue(t *testing.T) {
	s1, _ := configAddress(t)
	s2, _ := configAddress(t)
	s3, _ := configAddress(t)
	g, _ := configAddress(t)
	good := func() *config.Config {
		cfg := newTestConfigForBridge()
		cfg.Bridge.BridgeActivationHeight = 100
		cfg.Bridge.Signers = []string{s1, s2, s3}
		cfg.Bridge.SignerThreshold = 2
		cfg.Bridge.Guardian = g
		cfg.Bridge.MaxReleaseZNHBWei = "1000"
		cfg.Bridge.WindowBlocks = 100
		cfg.Bridge.WindowCapZNHBWei = "1000"
		return cfg
	}
	if _, _, err := BridgeAmmSettingsFromConfig(good()); err != nil {
		t.Fatalf("the base config: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*config.Config)
		want string
	}{
		"one signer":                   {func(c *config.Config) { c.Bridge.Signers = []string{s1} }, "Signers"},
		"no signers":                   {func(c *config.Config) { c.Bridge.Signers = nil }, "Signers"},
		"a repeated signer":            {func(c *config.Config) { c.Bridge.Signers = []string{s1, s2, s1} }, "repeats"},
		"a bad signer":                 {func(c *config.Config) { c.Bridge.Signers = []string{s1, "nope"} }, "Signers[1]"},
		"threshold one":                {func(c *config.Config) { c.Bridge.SignerThreshold = 1 }, "SignerThreshold"},
		"threshold zero":               {func(c *config.Config) { c.Bridge.SignerThreshold = 0 }, "SignerThreshold"},
		"threshold above the signers":  {func(c *config.Config) { c.Bridge.SignerThreshold = 4 }, "SignerThreshold"},
		"no guardian":                  {func(c *config.Config) { c.Bridge.Guardian = "" }, "Guardian"},
		"a guardian that signs":        {func(c *config.Config) { c.Bridge.Guardian = s2 }, "also a signer"},
		"no release cap":               {func(c *config.Config) { c.Bridge.MaxReleaseZNHBWei = "" }, "MaxReleaseZNHBWei"},
		"a zero release cap":           {func(c *config.Config) { c.Bridge.MaxReleaseZNHBWei = "0" }, "MaxReleaseZNHBWei"},
		"a bad release cap":            {func(c *config.Config) { c.Bridge.MaxReleaseZNHBWei = "ten" }, "MaxReleaseZNHBWei"},
		"no window":                    {func(c *config.Config) { c.Bridge.WindowBlocks = 0 }, "WindowBlocks"},
		"no window cap":                {func(c *config.Config) { c.Bridge.WindowCapZNHBWei = "" }, "WindowCapZNHBWei"},
		"a window cap below a release": {func(c *config.Config) { c.Bridge.WindowCapZNHBWei = "999" }, "WindowCapZNHBWei"},
		"a wait threshold alone":       {func(c *config.Config) { c.Bridge.DelayAboveZNHBWei = "10" }, "DelayBlocks is zero"},
		"a wait length alone":          {func(c *config.Config) { c.Bridge.DelayBlocks = 10 }, "DelayBlocks is set"},
		"a wait threshold above the cap": {func(c *config.Config) {
			c.Bridge.DelayAboveZNHBWei = "1001"
			c.Bridge.DelayBlocks = 10
		}, "ever wait"},
		"a rotation not above the activation": {func(c *config.Config) {
			c.Bridge.Rotations = []config.BridgeRotationConfig{{FromHeight: 100}}
		}, "not above"},
		"an incomplete rotation": {func(c *config.Config) {
			c.Bridge.Rotations = []config.BridgeRotationConfig{{FromHeight: 200}}
		}, "Rotations[0]"},
	} {
		cfg := good()
		tc.edit(cfg)
		if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
	// A wait threshold of zero (with a length) means every release waits; it is valid.
	cfg := good()
	cfg.Bridge.DelayAboveZNHBWei = "0"
	cfg.Bridge.DelayBlocks = 5
	if b, _, err := BridgeAmmSettingsFromConfig(cfg); err != nil || b.Params[0].DelayAboveWei == nil || b.Params[0].DelayAboveWei.Sign() != 0 {
		t.Fatalf("a wait threshold of zero: %+v %v", b, err)
	}
	// A complete rotation, and a second one that is not above the first.
	cfg = good()
	rot := config.BridgeRotationConfig{FromHeight: 200, Signers: []string{s2, s3}, SignerThreshold: 2, Guardian: g, MaxReleaseZNHBWei: "10", WindowBlocks: 10, WindowCapZNHBWei: "10"}
	cfg.Bridge.Rotations = []config.BridgeRotationConfig{rot}
	if b, _, err := BridgeAmmSettingsFromConfig(cfg); err != nil || len(b.Params) != 2 || b.Params[1].FromHeight != 200 || len(b.Params[1].Signers) != 2 {
		t.Fatalf("a complete rotation: %+v %v", b, err)
	}
	cfg.Bridge.Rotations = []config.BridgeRotationConfig{rot, rot}
	if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "not above") {
		t.Fatalf("two rotations at the same height: %v", err)
	}
	// A bridge that is off ignores every bridge value, however wrong.
	off := newTestConfigForBridge()
	off.Bridge.Signers, off.Bridge.SignerThreshold, off.Bridge.Guardian = []string{"x"}, 9, "y"
	off.Bridge.Rotations = []config.BridgeRotationConfig{{FromHeight: 1}}
	if b, _, err := BridgeAmmSettingsFromConfig(off); err != nil || b.ActivationHeight != 0 || len(b.Params) != 0 {
		t.Fatalf("an unactivated bridge must ignore everything: %+v %v", b, err)
	}
}

func TestBridgeParamsAreAtHeightAndCopiedDeeply(t *testing.T) {
	a, b := testBridgeParams(10), testBridgeParams(50)
	s := BridgeSettings{ActivationHeight: 10, Params: []BridgeParams{a, b}}
	for h, want := range map[uint64]uint64{9: 0, 10: 10, 49: 10, 50: 50, 5_000: 50} {
		p := s.paramsAt(h)
		if want == 0 {
			if p != nil {
				t.Fatalf("height %d: rules %v before the first entry", h, p.FromHeight)
			}
			continue
		}
		if p == nil || p.FromHeight != want {
			t.Fatalf("height %d: got %v, want the entry from %d", h, p, want)
		}
	}
	c := s.clone()
	s.Params[0].Signers[0][0] = 0xEE
	s.Params[1].MaxReleaseWei.SetInt64(1)
	if c.Params[0].Signers[0][0] == 0xEE || c.Params[1].MaxReleaseWei.Int64() != 1_000 {
		t.Fatalf("a clone shares the rule sets with the original")
	}
}
