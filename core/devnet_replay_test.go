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
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
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
// (0x01), staking (0x06), heartbeats (0x08), signed mints (0x0E), ZNHB transfers
// (0x10), redemption requests and their attestations (0x1B, 0x1C) and the buyback
// and lending reference prices (0x25, 0x26); the per-block lifecycle (rewards,
// epochs, loyalty, POTSO) runs on every block. Governance and loyalty
// transactions are not driven by it.
const devnetBlocks = 3_000

const (
	devnetEpochLength = 20
	devnetCheckEvery  = 250

	// Recorded from release/hardening-r4.
	devnetBlocksDigest      = "3c11e4fdf071cf5fbefa0ff168caabff60f869d9cfca6b497e8cc0487673ceda"
	devnetFinalEventCount   = 13612
	devnetFinalEventsDigest = "c665cd85a40c1bea2dd66b49dc698dfbf4b6a5584b5d533976ca859b6ece0f5e"
	devnetIncludedByType    = "0x01=4000 0x06=18 0x08=251 0x0e=60 0x10=750 0x1b=22 0x1c=22 0x25=50 0x26=40"
)

// devnetCheckpointDigests are the digests of every event readable at heights 250,
// 500, ..., 3000, recorded from release/hardening-r4.
var devnetCheckpointDigests = []string{
	"4149f0db1a8bcf3aa4b1d0991c267936d425fc365959ca3627e01f820dcdfeb3",
	"d4767712238e636be799f63cbb27bce43f76cb49b2471f103283b500d6567e99",
	"6325dee870ca96dc3cadc9e0040d4d4d1d5b6d53ca814598b6d2bb0bfd091ba6",
	"6c434a3ac9f6d89c8225fad1d76f021d6d50d48935bd099c4cfa0caac5b1b74c",
	"23f4b2802a604d93bbbd296e57cb94d02f27716414c67da8c60aaf7db39b4d27",
	"98d38a85146edcfd693ae018dd76c64363cd11306dedea31c1257e48e382631b",
	"135da4c2e10ecc42e646ac5dfa4464a402511c160c2b9efb027ee48549f1849f",
	"5eb74c9a4d22509f9ec7ea77c32b6b741ec9d2ce74df4f8cddd6890148f0fbf6",
	"844f9b67f877607edd96b9b8d88850ec63b25383a21c5e906bef1b42eafc79ff",
	"b74385fa53ec4a91fdef0748f46b30b49435a0e6b191e77db2401dac7fd92e20",
	"a36b9941cb297549c43e1d35fd705f213a794f2152fade2bd397afc9d0231d46",
	"c665cd85a40c1bea2dd66b49dc698dfbf4b6a5584b5d533976ca859b6ece0f5e",
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
	attestorKey := devnetKey(0x28)
	signerKeys := []*crypto.PrivateKey{devnetKey(0x22), devnetKey(0x23), devnetKey(0x24)}
	var signerAddrs [][20]byte
	for _, k := range signerKeys {
		signerAddrs = append(signerAddrs, devnetAddr(k))
	}

	alloc := map[string]map[string]string{
		adminKey.PubKey().Address().String():    {"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()},
		attestorKey.PubKey().Address().String(): {"NHB": "1000000000000000000000000", "ZNHB": "0"},
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
		Roles: map[string][]string{
			"MINTER_NHB":           {minterKey.PubKey().Address().String()},
			RoleSwapPayoutAttestor: {attestorKey.PubKey().Address().String()},
		},
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
	submit := func(key *crypto.PrivateKey, typ types.TxType, to []byte, value *big.Int, payload []byte) (*types.Transaction, bool) {
		addr := devnetAddr(key)
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: typ, Nonce: nonces[addr], To: to, Value: value, Data: payload, GasLimit: 25_000, GasPrice: big.NewInt(1)}
		if err := tx.Sign(key.PrivateKey); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := node.AddTransaction(tx); err != nil {
			return tx, false
		}
		nonces[addr]++
		return tx, true
	}
	var pendingRedemptions []string
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
	senders := append(append([]*crypto.PrivateKey{}, userKeys...), adminKey, attestorKey)
	for height := 1; height <= blocks; height++ {
		current = start.Add(time.Duration(height) * 2 * time.Second)
		// A transaction accepted into the pool may still be left out of the block,
		// so start each block from the nonces the chain holds.
		for _, key := range senders {
			addr := devnetAddr(key)
			account, err := node.GetAccount(addr[:])
			if err != nil {
				t.Fatalf("load account: %v", err)
			}
			nonces[addr] = account.Nonce
		}
		sender := userKeys[height%users]
		receiver := userKeys[(height*7+3)%users]
		to := receiver.PubKey().Address().Bytes()

		// NHB transfers between the users.
		submit(sender, types.TxTypeTransfer, to, big.NewInt(int64(1+height%5)), nil)
		if height%3 == 0 {
			submit(userKeys[(height+5)%users], types.TxTypeTransfer, userKeys[(height*3+1)%users].PubKey().Address().Bytes(), big.NewInt(int64(2+height%7)), nil)
		}
		// ZNHB transfers, from the admin wallet to a user and between users.
		if height%4 == 0 {
			submit(adminKey, types.TxTypeTransferZNHB, to, new(big.Int).Mul(big.NewInt(int64(1+height%9)), weiPerToken), nil)
		}
		if height%6 == 1 {
			submit(sender, types.TxTypeTransferZNHB, to, new(big.Int).Mul(big.NewInt(int64(1+height%3)), weiPerToken), nil)
		}
		// Staking.
		if height%40 == 5 {
			submit(userKeys[(height/40)%users], types.TxTypeStake, nil, new(big.Int).Mul(big.NewInt(3), weiPerToken), nil)
		}
		// Heartbeats from a few of the users.
		if height%45 == 7 {
			for i := 0; i < 4; i++ {
				submit(userKeys[i], types.TxTypeHeartbeat, nil, nil, nil)
			}
		}
		// Redemption requests, and the attestor's answer to the earlier ones:
		// paid for one, failed (which returns the burned NHB) for the next.
		if height%100 == 33 {
			requester := userKeys[(height/100)%users]
			request, err := rlp.EncodeToBytes(struct {
				DestinationAsset   string
				DestinationAddress string
			}{"asset-a", "destination-" + strconv.Itoa(height)})
			if err != nil {
				t.Fatalf("encode redemption request: %v", err)
			}
			if tx, ok := submit(requester, types.TxTypeRedeemNHB, nil, new(big.Int).Mul(big.NewInt(6), weiPerToken), request); ok {
				hash, err := tx.Hash()
				if err != nil {
					t.Fatalf("hash redemption request: %v", err)
				}
				requester := devnetAddr(requester)
				pendingRedemptions = append(pendingRedemptions, nhbstate.RedemptionRequestID(requester[:], hash))
			}
		}
		if height%100 == 39 {
			for i, id := range pendingRedemptions {
				answer := struct {
					RequestID       string
					Status          string
					PayoutReference string
					FailureReason   string
				}{RequestID: id, Status: "paid", PayoutReference: "reference-" + strconv.Itoa(height)}
				if (height/100+i)%2 == 1 {
					answer.Status, answer.PayoutReference, answer.FailureReason = "failed", "", "not completed"
				}
				payload, err := rlp.EncodeToBytes(answer)
				if err != nil {
					t.Fatalf("encode attestation: %v", err)
				}
				submit(attestorKey, types.TxTypeAttestRedemption, nil, nil, payload)
			}
			pendingRedemptions = nil
		}
		// Signed mints. The first is large enough for the redemptions to burn
		// from, since the recorded NHB supply starts at what was minted.
		if height%50 == 10 {
			mintAmount := strconv.Itoa(1_000 + height)
			if height == 10 {
				mintAmount = "1000000000000000000000"
			}
			voucher := MintVoucher{
				InvoiceID: "devnet-inv-" + strconv.Itoa(height),
				Recipient: receiver.PubKey().Address().String(),
				Token:     "NHB",
				Amount:    mintAmount,
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
