package rpc

// Chains for the tests of the public reads: blocks added straight to the store,
// carrying every shape of transaction the explorer treats differently, on top of
// a store that counts the reads that reach it. A test can then say how much
// work a query did in reads rather than in time, which does not depend on the
// speed of the machine.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/modules"
	"nhbchain/storage"
)

// countingDB counts the reads that reach the store, block reads included, so a
// test can say how much work a query did instead of how long it took.
type countingDB struct {
	storage.Database
	gets atomic.Int64
}

func (c *countingDB) Get(key []byte) ([]byte, error) {
	c.gets.Add(1)
	return c.Database.Get(key)
}

type randChain struct {
	Node    *core.Node
	DB      storage.Database
	Reads   *countingDB
	Dir     string
	CloseDB func()
	Grow    func(n int)
	Key     *crypto.PrivateKey
	Keys    []*crypto.PrivateKey
	Addrs   [][]byte
	Admin   []byte
	Height  uint64
	LastTS  int64
	Hashes  []string          // every transaction hash, hex, no prefix
	HashAt  map[string]uint64 // hash -> the last height it is in
	Storage string
}

// openRandStore opens the block store (in memory, or on disk in dir when level
// is set) behind a read counter.
func openRandStore(tb testing.TB, level bool, dir string) (*countingDB, func()) {
	tb.Helper()
	var inner storage.Database
	if level {
		ldb, err := storage.NewLevelDB(dir)
		if err != nil {
			tb.Fatalf("open leveldb: %v", err)
		}
		inner = ldb
	} else {
		inner = storage.NewMemDB()
	}
	var once sync.Once
	closeDB := func() { once.Do(inner.Close) }
	tb.Cleanup(closeDB)
	return &countingDB{Database: inner}, closeDB
}

func newRandNode(tb testing.TB, level bool) (*core.Node, *countingDB, string, func(), *crypto.PrivateKey) {
	tb.Helper()
	dir := ""
	if level {
		dir = tb.TempDir()
	}
	db, closeDB := openRandStore(tb, level, dir)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		tb.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, false)
	if err != nil {
		tb.Fatalf("new node: %v", err)
	}
	return node, db, dir, closeDB, key
}

// buildRandomChain adds `blocks` blocks straight to the block store. Roughly a
// third of them carry transactions, drawn from every shape the explorer treats
// differently: user transfers of both assets, staking, senderless mints, ZNHB
// purchases (which touch the admin wallet without naming it), a lending borrow
// (whose direction is overridden), and the heartbeat and reference-price
// submissions that are not user-facing. A few blocks carry several so that
// the order within a block matters.
func buildRandomChain(tb testing.TB, seed int64, blocks int, level bool) *randChain {
	tb.Helper()
	node, db, dir, closeDB, key := newRandNode(tb, level)
	rc := &randChain{Node: node, DB: db, Reads: db, Dir: dir, CloseDB: closeDB, Key: key, HashAt: map[string]uint64{}}
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < 14; i++ {
		k := costSeededKey(seed, 50+i)
		rc.Keys = append(rc.Keys, k)
		rc.Addrs = append(rc.Addrs, costAddr(k))
	}
	rc.Admin = costAddr(costSeededKey(seed, 99))
	chain := node.Chain()
	chain.SetAdminWalletForTests(toArray20(rc.Admin))

	// Accounts, so balances, usernames and segments are exercised.
	if err := node.WithState(func(m *nhbstate.Manager) error {
		for i, addr := range rc.Addrs {
			account := &types.Account{BalanceNHB: big.NewInt(int64(1000 * (i + 1))), BalanceZNHB: big.NewInt(int64(50 * i)), Stake: big.NewInt(0)}
			if i%2 == 0 {
				account.Username = fmt.Sprintf("user%d", i)
			}
			if i == 3 {
				account.Stake = big.NewInt(7)
			}
			if err := m.PutAccount(addr, account); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		tb.Fatalf("seed accounts: %v", err)
	}

	genesis := chain.CurrentHeader()
	prev := chain.Tip()
	stateRoot := append([]byte(nil), genesis.StateRoot...)
	base := time.Now().Unix() - int64(blocks+5)*2
	nonce := uint64(0)
	pick := func() int { return rng.Intn(len(rc.Addrs)) }
	makeTx := func() *types.Transaction {
		nonce++
		a, b := pick(), pick()
		switch rng.Intn(10) {
		case 0, 1, 2:
			return costSignedTx(tb, rc.Keys[a], types.TxTypeTransfer, nonce, rc.Addrs[b], big.NewInt(int64(1+rng.Intn(1000))), nil)
		case 3:
			return costSignedTx(tb, rc.Keys[a], types.TxTypeTransferZNHB, nonce, rc.Addrs[b], big.NewInt(int64(1+rng.Intn(1000))), nil)
		case 4:
			return costSignedTx(tb, rc.Keys[a], types.TxTypeStake, nonce, nil, big.NewInt(int64(1+rng.Intn(50))), nil)
		case 5:
			recipient := crypto.MustNewAddress(crypto.NHBPrefix, rc.Addrs[b]).String()
			voucher, _ := json.Marshal(map[string]any{"voucher": map[string]string{"recipient": recipient, "token": "NHB", "amount": fmt.Sprint(1 + rng.Intn(9999))}})
			return &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeMint, Nonce: nonce, Data: voucher, GasPrice: big.NewInt(0)}
		case 6:
			payload, _ := rlp.EncodeToBytes(struct {
				ZNHBAmount   *big.Int
				MaxNHBAmount *big.Int
				QuoteID      string
			}{big.NewInt(int64(10 + rng.Intn(90))), big.NewInt(1000), ""})
			return costSignedTx(tb, rc.Keys[a], types.TxTypeBuyZNHB, nonce, nil, nil, payload)
		case 7:
			return costSignedTx(tb, rc.Keys[a], types.TxTypeLendingBorrowNHB, nonce, nil, big.NewInt(int64(1+rng.Intn(500))), []byte(`{"poolId":"default"}`))
		case 8:
			payload, _ := json.Marshal(types.HeartbeatPayload{DeviceID: "d", Timestamp: int64(nonce)})
			return costSignedTx(tb, rc.Key, types.TxTypeHeartbeat, nonce, nil, nil, payload)
		default:
			return &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeBuybackRefPrice, Nonce: 0, Data: []byte(fmt.Sprintf("ref-%d", nonce)), GasPrice: big.NewInt(0)}
		}
	}
	nextHeight := 1
	// Grow adds n more blocks of the same kind on top of the chain, so that a
	// test can look at it again after it has grown.
	rc.Grow = func(n int) {
		restore := silenceStdout(tb)
		defer restore()
		end := nextHeight + n
		for h := nextHeight; h < end; h++ {
			var txs []*types.Transaction
			if rng.Intn(3) == 0 {
				count := 1
				switch rng.Intn(6) {
				case 0:
					count = 3
				case 1:
					count = 2
				}
				for i := 0; i < count; i++ {
					txs = append(txs, makeTx())
				}
			}
			root, err := core.ComputeTxRoot(txs)
			if err != nil {
				tb.Fatalf("tx root: %v", err)
			}
			header := &types.BlockHeader{
				Height:             uint64(h),
				Timestamp:          base + int64(h)*2,
				PrevHash:           prev,
				StateRoot:          stateRoot,
				TxRoot:             root,
				ExecutionGraphRoot: bytes.Repeat([]byte{0xE1}, 32),
				Validator:          costAddr(key),
			}
			hash, err := header.Hash()
			if err != nil {
				tb.Fatalf("hash header: %v", err)
			}
			if err := chain.AddBlock(&types.Block{Header: header, Transactions: txs}); err != nil {
				tb.Fatalf("add block %d: %v", h, err)
			}
			for _, tx := range txs {
				th, err := tx.Hash()
				if err != nil {
					tb.Fatalf("hash tx: %v", err)
				}
				hx := hex.EncodeToString(th)
				if _, seen := rc.HashAt[hx]; !seen {
					rc.Hashes = append(rc.Hashes, hx)
				}
				rc.HashAt[hx] = uint64(h)
				// Half of the ZNHB purchases have their NHB cost on record.
				if tx.Type == types.TxTypeBuyZNHB && h%2 == 0 {
					_ = chain.PutExplorerMeta(core.BuyZNHBCostMetaKey(hx), []byte(fmt.Sprint(100+h)))
				}
			}
			prev = hash
			rc.LastTS = header.Timestamp
		}
		nextHeight = end
		rc.Height = chain.GetHeight()
	}
	rc.Grow(blocks)
	return rc
}

func toArray20(b []byte) [20]byte {
	var out [20]byte
	copy(out[:], b)
	return out
}

func (rc *randChain) allAddresses(extra ...string) []string {
	out := []string{}
	for _, a := range rc.Addrs {
		out = append(out, crypto.MustNewAddress(crypto.NHBPrefix, a).String())
	}
	out = append(out, crypto.MustNewAddress(crypto.NHBPrefix, rc.Admin).String())
	out = append(out, crypto.MustNewAddress(crypto.NHBPrefix, costAddr(costSeededKey(1234, 1))).String())
	return append(out, extra...)
}

// settleActivityIndex lets the all-time payment totals catch up with the chain,
// so two snapshots built one after the other report the same totals.
func settleActivityIndex(srv *Server) {
	for {
		srv.advanceExplorerActivityIndex()
		if _, _, complete := srv.currentExplorerActivityTotals(); complete {
			return
		}
	}
}

func mustJSON(tb testing.TB, v any) string {
	tb.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		tb.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func unknownHash() string { return "0x" + strings.Repeat("ab", 32) }

// quietServer is a server for node without the background work NewServer starts
// (the snapshot refresh, the activity index, the lending index warm-up). Tests
// that count reads from the store, or close and reopen it under the server, need
// the only reads and the only users of the store to be their own.
func quietServer(tb testing.TB, node *core.Node, cfg ServerConfig) *Server {
	tb.Helper()
	srv := newTestServer(tb, nil, nil, cfg)
	srv.node = node
	srv.lending = modules.NewLendingModule(node)
	srv.transactions = modules.NewTransactionsModule(node)
	srv.escrow = modules.NewEscrowModule(node)
	return srv
}

// removeCompletenessMarker makes the store look like one whose index backfill
// never finished, which sends unknown hashes to the scan.
func removeCompletenessMarker(t *testing.T, rc *randChain) {
	t.Helper()
	deleteIndex(t, rc, false)
}

// deleteIndex removes the completeness marker and, when entries is set, every
// transaction entry, as on a store that never had the index.
func deleteIndex(t *testing.T, rc *randChain, entries bool) {
	t.Helper()
	batch := rc.DB.NewBatch()
	if err := batch.Delete([]byte("txHashIndexBackfillDone")); err != nil {
		t.Fatalf("delete marker: %v", err)
	}
	if entries {
		for hx := range rc.HashAt {
			raw, _ := hex.DecodeString(hx)
			if err := batch.Delete(append([]byte("txhash:"), raw...)); err != nil {
				t.Fatalf("delete entry: %v", err)
			}
		}
	}
	if err := batch.Write(); err != nil {
		t.Fatalf("write batch: %v", err)
	}
}

// addBlockOn appends one empty block to the chain.
func (rc *randChain) addBlockOn(t *testing.T) {
	t.Helper()
	restore := silenceStdout(t)
	defer restore()
	chain := rc.Node.Chain()
	head := chain.CurrentHeader()
	root, err := core.ComputeTxRoot(nil)
	if err != nil {
		t.Fatalf("tx root: %v", err)
	}
	header := &types.BlockHeader{
		Height:             head.Height + 1,
		Timestamp:          head.Timestamp + 2,
		PrevHash:           chain.Tip(),
		StateRoot:          head.StateRoot,
		TxRoot:             root,
		ExecutionGraphRoot: bytes.Repeat([]byte{0xE1}, 32),
		Validator:          costAddr(rc.Key),
	}
	if err := chain.AddBlock(&types.Block{Header: header}); err != nil {
		t.Fatalf("add block: %v", err)
	}
	rc.Height = chain.GetHeight()
}
