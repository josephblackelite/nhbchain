package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/genesis"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// The devnet replay drives a fixed devnet of several thousand blocks through the
// real block path (AddTransaction, CreateBlock, CommitBlock) and records what a
// node exposes about it: every block's header hash and state root, the number of
// transactions of each type it included, and the events its readers can see, at
// fixed heights and at the end. It uses only fixed keys and a fixed clock, so the
// same code always produces the same record.
//
// The record checked in below was taken from release/hardening-r4, before the
// node stopped carrying its event log inside the state it copies for every block.
// A build that replays history has to reach the same block hashes and state
// roots, and the same readable events, so TestDevnetReplayIsUnchanged fails if
// any of them moves. To compare two trees line by line instead, set
// NHB_DEVNET_TRANSCRIPT to a file name, run the test on each tree and diff the
// files.
//
// The transactions are the kinds the live chain has included: NHB transfers
// (0x01), staking (0x06), signed mints (0x0E), ZNHB transfers (0x10) and the
// buyback and lending reference prices (0x25, 0x26); the per-block lifecycle
// (rewards, epochs, loyalty, POTSO) runs on every block.
const devnetBlocks = 3_000

const (
	devnetEpochLength = 20
	devnetCheckEvery  = 250

	// Recorded from release/hardening-r4.
	devnetBlocksDigest      = "037825ff6917dd32d22f25aff1f44fc8a4a61bdd81ae6dda1e325f45856891eb"
	devnetFinalEventCount   = 13281
	devnetFinalEventsDigest = "14b2b83b19842fb9bd33e3db4ffde169d34b2bac5fbd5a523b0b2ced3f90ced4"
	devnetIncludedByType    = "0x01=4000 0x06=18 0x0e=60 0x10=750 0x25=50 0x26=40"
)

// devnetCheckpointDigests are the digests of every event readable at heights 250,
// 500, ..., 3000, recorded from release/hardening-r4.
var devnetCheckpointDigests = []string{
	"7e78f8957f99e40c5942ba426769629b432f6d3069be32372b3d1ef91ed72dfb",
	"0cea96011e1ccdd8f7b059f9f07a9646229e00175f3669d1af34bbde253bccc0",
	"6c7dcf5863c39bba1e9ce0e31d6e1ff1fefcf90ec9dfd5ce9f3ad552e53ece7e",
	"1e17167703d78e6491dd5d02267b46f7e2a3e153a82f800e8fb5fa07fce1478b",
	"ddd00b6c251d053bc4f5bf1c8fac55890f1e06e7e0bab285c576fa758ce017e3",
	"20d8f827263e9cd939ba39467556a71cb0df07d98e79782bdf9d87961dd89ab1",
	"6a42dcbd5ccf10b54442248e45044861522390c7b0a7c7bc4f147cecec291064",
	"99ef18a55c693da68c3ea29067682c31398a60c7082b2415e9522a27b35cfe36",
	"039b1c94d4fea2f7b35099a1b73cb4705c6ba172682714813f3c98fa32bc4713",
	"26181f636d824e0aa9f7c69cff2e2bb8fbe29d3ab1795c5162e602c86c13e212",
	"9fa59b8a1b71323e81e4a44e1005bb67c8a3aea464341d7fbe826e18aa769f86",
	"14b2b83b19842fb9bd33e3db4ffde169d34b2bac5fbd5a523b0b2ced3f90ced4",
}

type devnetResult struct {
	node             *Node
	blockLines       []string
	included         map[types.TxType]int
	checkpointHeight []int
	// checkpointEvents[i] is every event Node.Events returned at
	// checkpointHeight[i], one line each, oldest first.
	checkpointEvents [][]string
}

func devnetKey(seed byte) *crypto.PrivateKey {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	key, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		panic(err)
	}
	return key
}

func devnetAddr(key *crypto.PrivateKey) [20]byte {
	var out [20]byte
	copy(out[:], key.PubKey().Address().Bytes())
	return out
}

// devnetDescribeEvents renders an event log one line per event, attributes
// sorted, so two logs can be compared as text.
func devnetDescribeEvents(log []types.Event) []string {
	lines := make([]string, 0, len(log))
	for _, evt := range log {
		keys := make([]string, 0, len(evt.Attributes))
		for k := range evt.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+evt.Attributes[k])
		}
		lines = append(lines, evt.Type+" "+strings.Join(parts, " "))
	}
	return lines
}

func devnetDigest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func (r *devnetResult) includedByType() string {
	types_ := make([]int, 0, len(r.included))
	for typ := range r.included {
		types_ = append(types_, int(typ))
	}
	sort.Ints(types_)
	parts := make([]string, 0, len(types_))
	for _, typ := range types_ {
		parts = append(parts, fmt.Sprintf("0x%02x=%d", typ, r.included[types.TxType(typ)]))
	}
	return strings.Join(parts, " ")
}

// runDevnet replays the devnet for the given number of blocks.
func runDevnet(t *testing.T, blocks int) *devnetResult {
	t.Helper()
	return runDevnetObserved(t, blocks, nil)
}

// runDevnetObserved is runDevnet with a hook: observe is called with the node
// before the first block and returns a function that is called after the last.
func runDevnetObserved(t *testing.T, blocks int, observe func(*Node) func()) *devnetResult {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")

	const users = 12
	userKeys := make([]*crypto.PrivateKey, users)
	for i := range userKeys {
		userKeys[i] = devnetKey(byte(0x30 + i))
	}
	adminKey := devnetKey(0x20)
	minterKey := devnetKey(0x21)
	signerKeys := []*crypto.PrivateKey{devnetKey(0x22), devnetKey(0x23), devnetKey(0x24)}
	var signerAddrs [][20]byte
	for _, k := range signerKeys {
		signerAddrs = append(signerAddrs, devnetAddr(k))
	}

	alloc := map[string]map[string]string{
		adminKey.PubKey().Address().String(): {"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()},
	}
	for _, k := range userKeys {
		alloc[k.PubKey().Address().String()] = map[string]string{"NHB": "1000000000000000000000000", "ZNHB": "0"}
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		// One validator: the first block registers each genesis validator by
		// walking a map, so with two the order of their two registration events
		// changes from run to run (the state they write does not).
		Validators: []genesis.ValidatorSpec{
			{Address: devnetKey(0x25).PubKey().Address().String(), Power: 11440},
		},
		Alloc:       alloc,
		AdminWallet: adminKey.PubKey().Address().String(),
		Roles:       map[string][]string{"MINTER_NHB": {minterKey.PubKey().Address().String()}},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}

	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	node, err := NewNode(db, devnetKey(0x27), genesisPath, false, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	if err := node.ConfigureEpochLengthForTests(devnetEpochLength); err != nil {
		t.Fatalf("configure epoch length: %v", err)
	}
	if err := node.ConfigureBuybackForTests(buyback.Config{FeeShareBps: 2000, SignerThreshold: 2, Signers: signerAddrs}); err != nil {
		t.Fatalf("configure buyback: %v", err)
	}
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	current := start
	node.SetTimeSource(func() time.Time { return current })

	nonces := map[[20]byte]uint64{}
	// submit signs and submits a transaction from key. A refused submission is
	// part of the record, not a failure: every build must refuse the same ones.
	submit := func(key *crypto.PrivateKey, typ types.TxType, to []byte, value *big.Int) {
		addr := devnetAddr(key)
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: typ, Nonce: nonces[addr], To: to, Value: value, GasLimit: 21_000, GasPrice: big.NewInt(1)}
		if err := tx.Sign(key.PrivateKey); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := node.AddTransaction(tx); err == nil {
			nonces[addr]++
		}
	}
	weiPerToken := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	signers := func(digest []byte) [][]byte {
		var sigs [][]byte
		for _, k := range signerKeys[:2] {
			sig, err := ethcrypto.Sign(digest, k.PrivateKey)
			if err != nil {
				t.Fatalf("sign price: %v", err)
			}
			sigs = append(sigs, sig)
		}
		return sigs
	}

	result := &devnetResult{node: node, included: map[types.TxType]int{}}
	if observe != nil {
		if done := observe(node); done != nil {
			defer done()
		}
	}
	for height := 1; height <= blocks; height++ {
		current = start.Add(time.Duration(height) * 2 * time.Second)
		sender := userKeys[height%users]
		receiver := userKeys[(height*7+3)%users]
		to := receiver.PubKey().Address().Bytes()

		// NHB transfers between the users.
		submit(sender, types.TxTypeTransfer, to, big.NewInt(int64(1+height%5)))
		if height%3 == 0 {
			submit(userKeys[(height+5)%users], types.TxTypeTransfer, userKeys[(height*3+1)%users].PubKey().Address().Bytes(), big.NewInt(int64(2+height%7)))
		}
		// ZNHB transfers, from the admin wallet to a user and between users.
		if height%4 == 0 {
			submit(adminKey, types.TxTypeTransferZNHB, to, new(big.Int).Mul(big.NewInt(int64(1+height%9)), weiPerToken))
		}
		if height%6 == 1 {
			submit(sender, types.TxTypeTransferZNHB, to, new(big.Int).Mul(big.NewInt(int64(1+height%3)), weiPerToken))
		}
		// Staking.
		if height%40 == 5 {
			submit(userKeys[(height/40)%users], types.TxTypeStake, nil, new(big.Int).Mul(big.NewInt(3), weiPerToken))
		}
		// Signed mints.
		if height%50 == 10 {
			voucher := MintVoucher{
				InvoiceID: "devnet-inv-" + strconv.Itoa(height),
				Recipient: receiver.PubKey().Address().String(),
				Token:     "NHB",
				Amount:    strconv.Itoa(1_000 + height),
				ChainID:   MintChainID,
				Expiry:    start.Add(24 * time.Hour).Unix(),
			}
			payload, err := voucher.CanonicalJSON()
			if err != nil {
				t.Fatalf("canonical voucher: %v", err)
			}
			sig, err := ethcrypto.Sign(ethcrypto.Keccak256(payload), minterKey.PrivateKey)
			if err != nil {
				t.Fatalf("sign voucher: %v", err)
			}
			_, _ = node.MintWithSignature(&voucher, sig)
		}
		// Reference prices signed by two of the three signers.
		if height%60 == 20 {
			epoch, ok := node.CurrentBuybackEpoch()
			if !ok {
				t.Fatalf("epoch scheduling is not enabled")
			}
			rate := big.NewRat(int64(5+height%7), 100)
			digest, err := (&buyback.ReferencePrice{Rate: rate, Epoch: epoch, Timestamp: current}).Hash()
			if err != nil {
				t.Fatalf("hash buyback price: %v", err)
			}
			_, _ = node.SubmitBuybackRefPrice(rate.Num(), rate.Denom(), epoch, uint64(current.Unix()), signers(digest[:]))
		}
		if height%75 == 30 {
			rate := big.NewRat(int64(4+height%5), 100)
			digest, err := (&lendingoracle.ReferencePrice{Rate: rate, Timestamp: current}).Hash()
			if err != nil {
				t.Fatalf("hash lending price: %v", err)
			}
			_, _ = node.SubmitLendingRefPrice(rate.Num(), rate.Denom(), uint64(current.Unix()), signers(digest[:]))
		}

		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("create block %d: %v", height, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit block %d: %v", height, err)
		}
		hash, err := block.Header.Hash()
		if err != nil {
			t.Fatalf("hash block header %d: %v", height, err)
		}
		for _, tx := range block.Transactions {
			result.included[tx.Type]++
		}
		result.blockLines = append(result.blockLines, fmt.Sprintf("block %d ts=%d txs=%d hash=%x root=%x", block.Header.Height, block.Header.Timestamp, len(block.Transactions), hash, block.Header.StateRoot))
		if height%devnetCheckEvery == 0 || height == blocks {
			result.checkpointHeight = append(result.checkpointHeight, height)
			result.checkpointEvents = append(result.checkpointEvents, devnetDescribeEvents(node.Events()))
		}
	}
	return result
}

// write saves the record as text, for comparing two trees with diff.
func (r *devnetResult) write(t *testing.T, path string) {
	t.Helper()
	lines := append([]string{}, r.blockLines...)
	lines = append(lines, "included "+r.includedByType())
	for i, height := range r.checkpointHeight {
		lines = append(lines, fmt.Sprintf("events at %d count=%d digest=%s", height, len(r.checkpointEvents[i]), devnetDigest(r.checkpointEvents[i])))
	}
	last := r.checkpointEvents[len(r.checkpointEvents)-1]
	for _, line := range last[maxDevnetInt(0, len(last)-500):] {
		lines = append(lines, "event "+line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func maxDevnetInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestDevnetReplayIsUnchanged(t *testing.T) {
	run := runDevnet(t, devnetBlocks)
	if path := os.Getenv("NHB_DEVNET_TRANSCRIPT"); path != "" {
		run.write(t, path)
	}
	final := run.checkpointEvents[len(run.checkpointEvents)-1]
	var checkpointDigests []string
	for _, events := range run.checkpointEvents {
		checkpointDigests = append(checkpointDigests, devnetDigest(events))
	}
	recorded := func() {
		t.Logf("RECORDED devnetBlocksDigest = %q", devnetDigest(run.blockLines))
		t.Logf("RECORDED devnetFinalEventCount = %d", len(final))
		t.Logf("RECORDED devnetFinalEventsDigest = %q", devnetDigest(final))
		t.Logf("RECORDED devnetIncludedByType = %q", run.includedByType())
		for _, d := range checkpointDigests {
			t.Logf("RECORDED checkpoint %q", d)
		}
	}
	if got := run.includedByType(); got != devnetIncludedByType {
		recorded()
		t.Fatalf("transactions included by type = %s, recorded %s", got, devnetIncludedByType)
	}
	if got := devnetDigest(run.blockLines); got != devnetBlocksDigest {
		recorded()
		t.Fatalf("the block hashes and state roots of the devnet changed: digest %s, recorded %s", got, devnetBlocksDigest)
	}
	if len(final) != devnetFinalEventCount || devnetDigest(final) != devnetFinalEventsDigest {
		recorded()
		t.Fatalf("the events readable after the devnet changed: %d events with digest %s, recorded %d with %s", len(final), devnetDigest(final), devnetFinalEventCount, devnetFinalEventsDigest)
	}
	if len(checkpointDigests) != len(devnetCheckpointDigests) {
		recorded()
		t.Fatalf("%d checkpoints, recorded %d", len(checkpointDigests), len(devnetCheckpointDigests))
	}
	for i, want := range devnetCheckpointDigests {
		if checkpointDigests[i] != want {
			recorded()
			t.Fatalf("the events readable at height %d changed: digest %s, recorded %s", run.checkpointHeight[i], checkpointDigests[i], want)
		}
	}
}
