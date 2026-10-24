package main

// End-to-end proof of RewardsByStakeFromEpoch (core/rewards_logic.go, distributeStakeWeighted) on a local network of real nhb processes.
//
// What it shows, with a validator that holds 12,000 ZNHB of its own and one wallet (alice) that delegated 6,000 ZNHB to it, so alice holds 6,000 of the 28,000 ZNHB of stake behind it (the
// genesis validator already holds 10,000):
//
//  1. SWITCHED OFF IS THE EARLIER RELEASE. A validator built from the release this one follows (NHB_E2E_BASELINE_REPO) produces the chain, with the earlier
//     split: each epoch the validator's own account is paid the whole validators' pool, and alice only her share of the stakers' pool. A
//     follower of THIS release with the setting at 0, started from a snapshot taken before those epochs, executes every one of them to the same block hash
//     and state root as the producer: nothing changed for a network that leaves the setting alone.
//  2. SWITCHED ON, ALL THREE POOLS FOLLOW STAKE. A validator of this release with RewardsByStakeFromEpoch = E pays, from epoch E on, alice's share of every pool to
//     her wallet (her wallet receives the validators' share by itself) and the rest to the validator's account; before E the earlier split.
//     A follower with the same E, from a snapshot taken before E, reaches the same hashes and roots through the switch-over.
//  3. THE VALUE IS CONSENSUS. A follower with another value (0) from the same snapshot stops at epoch E with a state root mismatch: why every validator and
//     every node that replays blocks must carry the same value, and why nhb-snapshot check-config pins it.
//
// It runs real nodes for many minutes and only runs when asked:
//
//	NHB_SNAPSHOT_E2E_REWARDS=1 NHB_TEST_BASH="C:/Program Files/Git/bin/bash.exe" NHB_E2E_BASELINE_REPO=C:/wt/r6/public \
//	    go test ./cmd/nhb-snapshot -run TestRewardsByStakeEndToEnd -v -timeout 90m
//
// NHB_E2E_ACTIVATION_EPOCH sets E (default 24).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

var (
	zn = func(whole int64) *big.Int { return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)) }
)

// prepareRewardsDir writes this test's keys and a genesis that funds the validator and the two test wallets with enough ZNHB to stake and delegate, in the
// directory newE2EEnv will use (which keeps a genesis that is already there).
func prepareRewardsDir(t *testing.T, dir, repo string) {
	t.Helper()
	keys := map[string]string{}
	for _, name := range []string{"validator", "follower", "alice", "bob", "second"} {
		path := filepath.Join(dir, "keys", name+".key")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, key.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		keys[name] = key.PubKey().Address().String()
	}
	raw, err := os.ReadFile(filepath.Join(repo, "config", "genesis.relaunch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	g["validators"].([]any)[0].(map[string]any)["address"] = keys["validator"]
	alloc := g["alloc"].(map[string]any)
	for _, name := range []string{"validator", "alice", "bob"} {
		alloc[keys[name]] = map[string]any{"NHB": zn(5000).String(), "ZNHB": zn(40000).String()}
	}
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "genesis.devnet.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// configWithActivation is the repository's config.toml with RewardsByStakeFromEpoch set to epoch.
func configWithActivation(t *testing.T, dir, name string, epoch uint64) string {
	t.Helper()
	raw := string(mustRead(t, filepath.Join(repoRoot(t), "config.toml")))
	raw = setLine(t, raw, `(?m)^RewardsByStakeFromEpoch = .*`, fmt.Sprintf("RewardsByStakeFromEpoch = %d", epoch))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// startWith starts a node with a given nhb binary (the environment's own is restored afterwards).
func (e *e2eEnv) startWith(bin string, n *e2eNode) {
	old := e.nhb
	e.nhb = bin
	defer func() { e.nhb = old }()
	n.start()
}

type accountView struct {
	Nonce              uint64 `json:"nonce"`
	Stake              json.Number `json:"stake"`
	LockedZNHB         json.Number `json:"lockedZNHB"`
	BalanceZNHB        json.Number `json:"balanceZNHB"`
	DelegatedValidator string `json:"delegatedValidator"`
	EngagementScore    uint64 `json:"engagementScore"`
}

func (n *e2eNode) account(addr string) (*accountView, error) {
	raw, err := n.call("nhb_getBalance", addr)
	if err != nil {
		return nil, err
	}
	var a accountView
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func bigOf(s string) *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

// epochNow returns the newest settled epoch and the length of an epoch in blocks.
func (n *e2eNode) epochNow() (epoch, length uint64) {
	raw, err := n.call("nhb_getEpochSummary")
	if err != nil {
		return 0, 0
	}
	var s struct {
		Epoch  uint64 `json:"epoch"`
		Height uint64 `json:"height"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Epoch == 0 {
		return 0, 0
	}
	return s.Epoch, s.Height / s.Epoch
}

type payoutView struct {
	Total, Validators, Stakers, Engagement *big.Int
}

// settlement returns what an epoch paid, by account (0x-prefixed lower-case hex), and what it paid in all.
func (n *e2eNode) settlement(epoch uint64) (map[string]payoutView, *big.Int, error) {
	raw, err := n.call("nhb_getRewardEpoch", epoch)
	if err != nil {
		return nil, nil, err
	}
	var s struct {
		PaidTotal string `json:"paidTotal"`
		Payouts   []struct {
			Account    string `json:"account"`
			Total      string `json:"total"`
			Validators string `json:"validators"`
			Stakers    string `json:"stakers"`
			Engagement string `json:"engagement"`
		} `json:"payouts"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, err
	}
	out := map[string]payoutView{}
	for _, p := range s.Payouts {
		out[strings.ToLower(p.Account)] = payoutView{bigOf(p.Total), bigOf(p.Validators), bigOf(p.Stakers), bigOf(p.Engagement)}
	}
	return out, bigOf(s.PaidTotal), nil
}

// sendSigned sends a signed transaction to a node with the privileged token.
func (e *e2eEnv) sendSigned(n *e2eNode, tx *types.Transaction) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "nhb_sendTransaction", "params": []any{tx}})
	req, err := http.NewRequest(http.MethodPost, n.rpcURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct {
		Error *struct {
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return fmt.Errorf("%s %s", reply.Error.Message, string(reply.Error.Data))
	}
	return nil
}

// delegate has wallet from delegate amount ZNHB to the validator, the way any wallet does it: a stake transaction whose data is the RLP of
// the validator's address.
func (e *e2eEnv) delegate(n *e2eNode, from, to *e2eKey, amount *big.Int) {
	t := e.t
	t.Helper()
	key, err := crypto.PrivateKeyFromBytes(from.raw)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := n.account(from.addr)
	if err != nil {
		t.Fatalf("read %s: %v", from.addr, err)
	}
	dest, err := crypto.DecodeAddress(to.addr)
	if err != nil {
		t.Fatal(err)
	}
	data, err := rlp.EncodeToBytes(&struct {
		Validator         []byte
		RegisterValidator bool `rlp:"optional"`
	}{Validator: dest.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	tx := types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeStake, Nonce: acct.Nonce, Value: amount, Data: data, GasLimit: 21000, GasPrice: big.NewInt(1)}
	tx.Sign(key.PrivateKey)
	if err := e.sendSigned(n, &tx); err != nil {
		t.Fatalf("delegate: %v", err)
	}
}

// rewardsScenario sets a validator up as the proof needs it (12,000 ZNHB of its own, 6,000 delegated by alice), keeps it alive, and returns once the chain has
// settled a number of epochs. It returns the node, the stop function of the background drivers, and the epoch the setup was complete in.
func (e *e2eEnv) rewardsScenario(name, bin, template string, untilEpoch uint64) (*e2eNode, func(), uint64) {
	t := e.t
	srcData := filepath.Join(e.dir, name, "data")
	src := e.newNode(name, e.keys["validator"], srcData, nil, e2eTimeout(), template)
	e.startWith(bin, src)
	// net_info is operator-only on this release, so the node's RPC is judged up by its tip, and wait-synced is given the operator token the way the install
	// script gives it (NHB_RPC_TOKEN, sent to this host only).
	e.waitUntil("the "+name+"'s RPC", 90*time.Second, src, func() bool { return src.height() > 0 })
	e.mintToken()
	op := exec.Command(e.cli, "rpc-token", "--secret-stdin", "--ttl", "12h", "--role", "operator")
	op.Stdin = strings.NewReader(e.secret)
	opOut, err := op.Output()
	if err != nil {
		t.Fatalf("mint operator token: %v", err)
	}
	t.Setenv("NHB_RPC_TOKEN", strings.TrimSpace(string(opOut)))
	val, alice := e.keys["validator"], e.keys["alice"]

	// The setup comes first and alone: the background traffic uses the same wallets, and two transactions of one wallet at once race for a nonce.
	out, err := e.runCLI(src, "register-validator", zn(12000).String(), val.path)
	if err != nil || strings.Contains(out, "Error") {
		t.Fatalf("register-validator: %v\n%s", err, out)
	}
	e.waitUntil("the validator's own stake", 90*time.Second, src, func() bool {
		a, err := src.account(val.addr)
		return err == nil && bigOf(a.Stake.String()).Cmp(zn(12000)) >= 0
	})
	e.delegate(src, alice, val, zn(6000))
	e.waitUntil("alice's delegation", 90*time.Second, src, func() bool {
		a, err := src.account(alice.addr)
		return err == nil && bigOf(a.LockedZNHB.String()).Cmp(zn(6000)) == 0 && strings.EqualFold(a.DelegatedValidator, val.addr)
	})
	if a, err := src.account(val.addr); err != nil || bigOf(a.Stake.String()).Cmp(zn(28000)) != 0 {
		t.Fatalf("the validator's stake is %v (%v), the expected shares assume 28,000: 10,000 from the genesis, 12,000 of its own and 6,000 delegated by alice", a, err)
	}
	setupEpoch, length := src.epochNow()
	t.Logf("%s: validator staked 12,000 and alice delegated 6,000 by epoch %d (an epoch is %d blocks)", name, setupEpoch, length)

	traffic := e.startTraffic(src)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the validator takes part: a heartbeat every few seconds
		defer wg.Done()
		beats := 0
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			out, err := e.runCLI(src, "heartbeat", val.path)
			if beats++; beats <= 3 || beats%60 == 0 {
				t.Logf("heartbeat %d of the validator: err=%v out=%q", beats, err, strings.TrimSpace(out))
			}
		}
	}()
	stopAll := func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		wg.Wait()
		traffic.stop()
	}

	// The engagement score is credited when a day rolls over, so on a network minutes old no validator has one and the engagement pool is unpaid (the unit
	// tests in core/rewards_by_stake_test.go cover that pool). Everything below is therefore about the other two pools: 40 + 100 of the 200 ZNHB.
	setupEpoch, _ = src.epochNow()
	e.waitUntil(fmt.Sprintf("the %s to settle epoch %d", name, untilEpoch), 25*time.Minute, src, func() bool {
		ep, _ := src.epochNow()
		return ep >= untilEpoch
	})
	return src, stopAll, setupEpoch
}

// alice delegated 6,000 of the 28,000 ZNHB of stake behind the validator (10,000 from the genesis, 12,000 the validator staked itself, 6,000 hers).
func aliceOf(pool *big.Int) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(pool, big.NewInt(6000)), big.NewInt(28000))
}

var aliceShare = struct{ validators, stakers, total *big.Int }{
	validators: aliceOf(zn(40)),
	stakers:    aliceOf(zn(100)),
	total:      aliceOf(zn(140)),
}

func near(a, b *big.Int) bool { // within a few wei: the remainders of the divisions
	d := new(big.Int).Sub(a, b)
	return d.CmpAbs(big.NewInt(10)) <= 0
}

func (e *e2eEnv) hexOf(name string) string { return mustHexAddress(e.t, e.keys[name].addr) }

// followFrom starts a follower of the given build and config from a snapshot and reports whether it kept up with src.
func (e *e2eEnv) followFrom(snap *sourceSnapshot, name, bin, template string, src *e2eNode) (*e2eNode, bool, string) {
	t := e.t
	data := filepath.Join(e.dir, name, "data")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := e.runTool(append([]string{"extract", "--manifest", snap.manifest, "--archive", snap.archive, "--target", data}, e.pinArgs()...)...); code != 0 {
		t.Fatalf("extract for %s: %s", name, out)
	}
	f := e.newNode(name, e.keys["follower"], data, src, e2eTimeout(), template)
	e.startWith(bin, f)
	// wait-synced would ask the source for net_info, which is operator-only on this release; the follower is judged by its own tip against the source's
	// instead: caught up when it is past the snapshot and within a few blocks of the source, stalled when its tip has not moved for a while.
	ok, out := e.syncedWith(f, src, snap.m.Height, 60*time.Second, 6*time.Minute)
	return f, ok, out
}

// syncedWith reports whether f catches up with src (past minHeight and within 8 blocks of it) before it stalls (its tip does not move for stall) or total passes.
func (e *e2eEnv) syncedWith(f, src *e2eNode, minHeight uint64, stall, total time.Duration) (bool, string) {
	deadline := time.Now().Add(total)
	last, lastMove := f.height(), time.Now()
	for time.Now().Before(deadline) {
		h := f.height()
		if h != last {
			last, lastMove = h, time.Now()
		}
		if h >= minHeight+1 && h+8 >= src.height() {
			return true, fmt.Sprintf("caught up: follower at %d, source at %d, snapshot at %d", h, src.height(), minHeight)
		}
		if time.Since(lastMove) > stall {
			return false, fmt.Sprintf("stalled: follower stuck at %d for %s, source at %d, snapshot at %d", h, stall, src.height(), minHeight)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false, fmt.Sprintf("not caught up after %s: follower at %d, source at %d", total, f.height(), src.height())
}

func TestRewardsByStakeEndToEnd(t *testing.T) {
	e2eEnabledFor(t, "NHB_SNAPSHOT_E2E_REWARDS")
	baseRepo := strings.TrimSpace(os.Getenv("NHB_E2E_BASELINE_REPO"))
	if baseRepo == "" {
		t.Skip("set NHB_E2E_BASELINE_REPO to a checkout of the release this one follows (the earlier split)")
	}
	activation := uint64(24)
	if v := strings.TrimSpace(os.Getenv("NHB_E2E_ACTIVATION_EPOCH")); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil || parsed < 8 {
			t.Fatalf("NHB_E2E_ACTIVATION_EPOCH %q", v)
		}
		activation = parsed
	}
	dir := filepath.Join(t.TempDir(), "rewards")
	if keep := strings.TrimSpace(os.Getenv("NHB_SNAPSHOT_E2E_DIR")); keep != "" {
		dir = keep
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHB_SNAPSHOT_E2E_DIR", dir)
	prepareRewardsDir(t, dir, repoRoot(t))
	e := newE2EEnv(t)

	baseBin := filepath.Join(e.dir, "bin", "nhb-baseline")
	if runtime.GOOS == "windows" {
		baseBin += ".exe"
	}
	goBuild(t, baseRepo, baseBin, "./cmd/nhb")
	newBin := e.nhb
	cfgOff := configWithActivation(t, e.dir, "config.rewards-off.toml", 0)
	cfgOn := configWithActivation(t, e.dir, fmt.Sprintf("config.rewards-%d.toml", activation), activation)
	baseCfg := filepath.Join(baseRepo, "config.toml")
	valHex, aliceHex := e.hexOf("validator"), e.hexOf("alice")

	noMismatch := func(f *e2eNode) {
		t.Helper()
		for _, bad := range []string{"state root mismatch", "quorum certificate verification failed", "tx root mismatch", "block height mismatch: got"} {
			if n := f.logCount(bad); n > 0 {
				t.Fatalf("%s's log holds %d lines with %q\n%s", f.name, n, bad, f.logTail(20))
			}
		}
	}

	// ---- 1. the earlier release produces, this release follows with the setting at 0 ----
	t.Log("=== 1. earlier release as the producer, this release with the setting at 0 as the follower")
	src, stopA, setupEpochA := e.rewardsScenario("baseline-source", baseBin, baseCfg, 0) // returns after setup; settle epochs below
	snapA := e.makeSnapshot(src, "baseline")
	targetA := setupEpochA + 6
	e.waitUntil(fmt.Sprintf("the baseline source to settle epoch %d", targetA), 25*time.Minute, src, func() bool { ep, _ := src.epochNow(); return ep >= targetA })
	for epoch := setupEpochA + 2; epoch <= targetA; epoch++ {
		pay, paid, err := src.settlement(epoch)
		if err != nil {
			t.Fatalf("epoch %d: %v", epoch, err)
		}
		a, ok := pay[aliceHex]
		if !ok {
			t.Fatalf("epoch %d (baseline): alice was paid nothing: %v", epoch, pay)
		}
		v := pay[valHex]
		if a.Validators.Sign() != 0 || a.Engagement.Sign() != 0 || !near(a.Stakers, aliceShare.stakers) {
			t.Fatalf("epoch %d (baseline): alice got validators %s engagement %s stakers %s; the earlier split pays her only her share of the stakers' pool (%s)", epoch, a.Validators, a.Engagement, a.Stakers, aliceShare.stakers)
		}
		if v.Validators.Cmp(zn(40)) != 0 {
			t.Fatalf("epoch %d (baseline): the validator was paid %s of the 40 ZNHB validators' pool", epoch, v.Validators)
		}
		if paid.Cmp(zn(140)) != 0 {
			t.Fatalf("epoch %d (baseline): %s paid of 140 (validators 40 + stakers 100; the engagement pool is unpaid on a network with no engagement score yet)", epoch, paid)
		}
	}
	t.Logf("baseline epochs %d..%d: validator got the whole validators' pool (40), alice only her share of the stakers' pool", setupEpochA+2, targetA)

	fA, okA, outA := e.followFrom(snapA, "follower-new-off", newBin, cfgOff, src)
	t.Logf("wait-synced of the new build (setting 0) from the earlier release's chain:\n%s", outA)
	if !okA {
		t.Fatalf("the new build with the setting at 0 did not keep up with the earlier release's chain\n%s", fA.logTail(30))
	}
	e.waitUntil("the follower to be past the settled epochs", 4*time.Minute, fA, func() bool { ep, _ := fA.epochNow(); return ep >= targetA })
	noMismatch(fA)
	stopA()
	src.kill()
	fA.kill()
	tipS, tipF := compareChains(t, src.dataDir, fA.dataDir, snapA.m.Height)
	t.Logf("earlier release vs this release (setting 0): identical block hashes and state roots at every height to %d (source tip %d, follower tip %d, snapshot at %d, epochs through %d)", min(tipS, tipF), tipS, tipF, snapA.m.Height, targetA)

	// ---- 2. this release with the setting on: pays by stake from epoch E ----
	t.Logf("=== 2. this release with RewardsByStakeFromEpoch = %d", activation)
	srcB, stopB, setupEpochB := e.rewardsScenario("source-on", newBin, cfgOn, 0)
	if setupEpochB+3 >= activation {
		stopB()
		t.Fatalf("setup finished in epoch %d, too close to the switch-over epoch %d: raise NHB_E2E_ACTIVATION_EPOCH", setupEpochB, activation)
	}
	snapB := e.makeSnapshot(srcB, "on")
	if ep, _ := srcB.epochNow(); ep >= activation-1 {
		stopB()
		t.Fatalf("the snapshot was taken in epoch %d, too close to the switch-over epoch %d", ep, activation)
	}
	targetB := activation + 3
	e.waitUntil(fmt.Sprintf("the source to settle epoch %d", targetB), 40*time.Minute, srcB, func() bool { ep, _ := srcB.epochNow(); return ep >= targetB })
	before, after := 0, 0
	for epoch := setupEpochB + 2; epoch <= targetB; epoch++ {
		pay, paid, err := srcB.settlement(epoch)
		if err != nil {
			t.Fatalf("epoch %d: %v", epoch, err)
		}
		a, v := pay[aliceHex], pay[valHex]
		if a.Total == nil || v.Total == nil {
			t.Fatalf("epoch %d: alice or the validator was paid nothing: %v", epoch, pay)
		}
		if paid.Cmp(zn(140)) != 0 {
			t.Fatalf("epoch %d: %s paid of 140 (validators 40 + stakers 100; the engagement pool is unpaid on a network with no engagement score yet)", epoch, paid)
		}
		sum := new(big.Int).Add(a.Total, v.Total)
		if sum.Cmp(paid) != 0 {
			t.Fatalf("epoch %d: alice %s + validator %s != paid %s", epoch, a.Total, v.Total, paid)
		}
		if epoch < activation {
			before++
			if a.Validators.Sign() != 0 || a.Engagement.Sign() != 0 || v.Validators.Cmp(zn(40)) != 0 {
				t.Fatalf("epoch %d (before %d): alice validators %s engagement %s, validator validators %s: the earlier split was expected", epoch, activation, a.Validators, a.Engagement, v.Validators)
			}
		} else {
			after++
			if !near(a.Validators, aliceShare.validators) || a.Engagement.Sign() != 0 || !near(a.Stakers, aliceShare.stakers) || !near(a.Total, aliceShare.total) {
				t.Fatalf("epoch %d (from %d): alice got validators %s engagement %s stakers %s total %s, want her share of every pool (%s %s %s %s)",
					epoch, activation, a.Validators, a.Engagement, a.Stakers, a.Total, aliceShare.validators, "0", aliceShare.stakers, aliceShare.total)
			}
			rest := new(big.Int).Sub(zn(140), aliceShare.total)
			if !near(v.Total, rest) {
				t.Fatalf("epoch %d (from %d): the validator's account was paid %s, want the rest (%s)", epoch, activation, v.Total, rest)
			}
		}
	}
	if before < 2 || after < 3 {
		t.Fatalf("only %d epochs before and %d after the switch-over were checked", before, after)
	}
	t.Logf("epochs %d..%d checked: %d before epoch %d (earlier split) and %d from it (alice receives her share of every pool by herself, the validator's account the rest)", setupEpochB+2, targetB, before, activation, after)

	// a follower with the same setting goes through the switch-over to the same blocks
	fB, okB, outB := e.followFrom(snapB, "follower-on", newBin, cfgOn, srcB)
	t.Logf("wait-synced of the follower with the same setting:\n%s", outB)
	if !okB {
		t.Fatalf("the follower with the same setting did not keep up\n%s", fB.logTail(30))
	}
	e.waitUntil("the follower to be past the switch-over", 4*time.Minute, fB, func() bool { ep, _ := fB.epochNow(); return ep >= targetB })
	noMismatch(fB)

	// ---- 3. a follower with another value stops at epoch E ----
	fC, okC, outC := e.followFrom(snapB, "follower-wrong-setting", newBin, cfgOff, srcB)
	t.Logf("wait-synced of the follower with the setting at 0 (the value differs):\n%s", outC)
	if okC {
		t.Fatalf("a follower with another RewardsByStakeFromEpoch kept up with the network")
	}
	mismatches := fC.logCount("state root mismatch")
	if mismatches == 0 {
		t.Fatalf("the follower with the other value stopped without a state root mismatch:\n%s", fC.logTail(30))
	}
	failedAt := uint64(0)
	for _, l := range strings.Split(string(mustRead(t, fC.logPath)), "\n") {
		if strings.Contains(l, "commit state root mismatch") {
			if m := regexp.MustCompile(`"height":(\d+)`).FindStringSubmatch(l); m != nil {
				failedAt, _ = strconv.ParseUint(m[1], 10, 64)
			}
			break
		}
	}
	_, length := srcB.epochNow()
	t.Logf("the follower with the setting at 0 rejected block %d (%d state root mismatches); epoch %d closes at height %d", failedAt, mismatches, activation, activation*length)
	if failedAt != activation*length {
		t.Fatalf("it should stop at the first block of epoch %d (height %d), it stopped at %d", activation, activation*length, failedAt)
	}

	stopB()
	srcB.kill()
	fB.kill()
	fC.kill()
	tipS, tipF = compareChains(t, srcB.dataDir, fB.dataDir, snapB.m.Height)
	t.Logf("this release (setting %d) source vs follower: identical block hashes and state roots at every height to %d, through epoch %d", activation, min(tipS, tipF), targetB)
}
