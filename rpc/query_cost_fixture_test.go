package rpc

// Fixture for the public-query cost tests: a chain shaped like the live one --
// mostly blocks that carry nothing, a few percent with a heartbeat or a
// reference-price submission, and a few dozen user-facing transactions spread
// over the whole history -- built straight into the block store (no state
// execution), which is all the read paths under test look at.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// The live chain's composition, measured over its first 196,693 blocks. The
// fixture scales these counts to the number of blocks it is asked to build.
const (
	liveChainBlocks     = 196693
	liveChainHeartbeats = 13859
	liveChainBuyback    = 1443
	liveChainLending    = 2420
	liveChainUserFacing = 61
)

type costChainOpts struct {
	Blocks   int   // blocks added above genesis
	LevelDB  bool  // a real on-disk store instead of the in-memory one
	Seed     int64 // every random choice derives from this
	Interval int64 // seconds between block timestamps (default 2)
}

type costChain struct {
	Node   *core.Node
	DB     storage.Database
	Reads  *countingDB // counts every read that reaches DB
	Dir    string
	Key    *crypto.PrivateKey
	Height uint64

	// Probes for the read methods.
	RecentTx        string // 64 hex, no prefix: a transfer a few blocks below the tip
	MidTx           string // a transfer well inside the 50,000-block scan window
	OldTx           string // a transfer beyond the scan window (when the chain is long enough)
	UnknownTx       string // a hash that was never in any block
	RecentBlockHash string // 0x-prefixed hash of a block near the tip
	ActiveAddr      string // an address with transfers recent and old
	DormantAddr     string // an address that never appears in any transaction

	// Every user-facing transaction placed, hash -> height, and every
	// transaction of any type, for differential tests.
	UserTx map[string]uint64
	AllTx  map[string]uint64
}

func costSeededKey(seed int64, n int) *crypto.PrivateKey {
	raw := make([]byte, 32)
	rng := rand.New(rand.NewSource(seed*1_000_003 + int64(n)))
	for {
		for i := range raw {
			raw[i] = byte(rng.Intn(256))
		}
		if key, err := crypto.PrivateKeyFromBytes(raw); err == nil {
			return key
		}
	}
}

func costSignedTx(tb testing.TB, key *crypto.PrivateKey, typ types.TxType, nonce uint64, to []byte, value *big.Int, data []byte) *types.Transaction {
	tb.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     typ,
		Nonce:    nonce,
		To:       to,
		Value:    value,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		tb.Fatalf("sign fixture transaction: %v", err)
	}
	return tx
}

func costAddr(key *crypto.PrivateKey) []byte {
	return append([]byte(nil), key.PubKey().Address().Bytes()...)
}

// silenceStdout mutes the block store's per-block progress line while a large
// fixture is built.
func silenceStdout(tb testing.TB) func() {
	tb.Helper()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	prev := os.Stdout
	os.Stdout = null
	return func() {
		os.Stdout = prev
		_ = null.Close()
	}
}

func buildCostChain(tb testing.TB, opts costChainOpts) *costChain {
	tb.Helper()
	if opts.Interval <= 0 {
		opts.Interval = 2
	}
	n := opts.Blocks
	if n < 20 {
		tb.Fatalf("fixture needs at least 20 blocks, got %d", n)
	}

	fx := &costChain{UserTx: map[string]uint64{}, AllTx: map[string]uint64{}}
	if opts.LevelDB {
		fx.Dir = tb.TempDir()
		ldb, err := storage.NewLevelDB(fx.Dir)
		if err != nil {
			tb.Fatalf("open leveldb: %v", err)
		}
		fx.DB = ldb
	} else {
		fx.DB = storage.NewMemDB()
	}
	tb.Cleanup(func() { fx.DB.Close() })
	fx.Reads = &countingDB{Database: fx.DB}
	fx.DB = fx.Reads

	fx.Key = costSeededKey(opts.Seed, 0)
	node, err := core.NewNode(fx.DB, fx.Key, "", true, false)
	if err != nil {
		tb.Fatalf("new node: %v", err)
	}
	fx.Node = node
	chain := node.Chain()
	genesis := chain.CurrentHeader()
	if genesis == nil {
		tb.Fatalf("no genesis header")
	}

	rng := rand.New(rand.NewSource(opts.Seed))
	scaled := func(live int) int {
		v := int(int64(live) * int64(n) / liveChainBlocks)
		if v < 1 {
			v = 1
		}
		return v
	}
	byHeight := make(map[int][]*types.Transaction)
	place := func(h int, tx *types.Transaction) {
		if h < 1 {
			h = 1
		}
		if h > n {
			h = n
		}
		byHeight[h] = append(byHeight[h], tx)
	}
	record := func(h int, tx *types.Transaction, user bool) string {
		hash, err := tx.Hash()
		if err != nil {
			tb.Fatalf("hash fixture transaction: %v", err)
		}
		key := fmt.Sprintf("%x", hash)
		if _, dup := fx.AllTx[key]; dup {
			tb.Fatalf("fixture produced a duplicate transaction hash %s", key)
		}
		fx.AllTx[key] = uint64(h)
		if user {
			fx.UserTx[key] = uint64(h)
		}
		return key
	}

	// Heartbeats: signed by the validator, one small JSON payload each.
	for i := 0; i < scaled(liveChainHeartbeats); i++ {
		h := 1 + rng.Intn(n)
		payload, _ := json.Marshal(types.HeartbeatPayload{DeviceID: "validator-0a1b2c3d", Timestamp: int64(1_700_000_000 + i)})
		tx := costSignedTx(tb, fx.Key, types.TxTypeHeartbeat, uint64(i), nil, nil, payload)
		place(h, tx)
		record(h, tx, false)
	}
	// Reference-price submissions: senderless, an RLP payload with signatures.
	refPayload := func(epoch, ts uint64) []byte {
		raw, err := rlp.EncodeToBytes(struct {
			RateNum    *big.Int
			RateDenom  *big.Int
			Epoch      uint64
			Timestamp  uint64
			Signatures [][]byte
		}{big.NewInt(1_000_000), big.NewInt(1_000_000), epoch, ts, [][]byte{bytes.Repeat([]byte{7}, 65), bytes.Repeat([]byte{9}, 65)}})
		if err != nil {
			tb.Fatalf("encode reference price: %v", err)
		}
		return raw
	}
	for i := 0; i < scaled(liveChainBuyback); i++ {
		h := 1 + rng.Intn(n)
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeBuybackRefPrice, Data: refPayload(uint64(i+1), uint64(1_700_000_000+i)), GasPrice: big.NewInt(0)}
		place(h, tx)
		record(h, tx, false)
	}
	for i := 0; i < scaled(liveChainLending); i++ {
		h := 1 + rng.Intn(n)
		tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeLendingRefPrice, Data: refPayload(uint64(i+1), uint64(1_800_000_000+i)), GasPrice: big.NewInt(0)}
		place(h, tx)
		record(h, tx, false)
	}

	// User-facing transactions. The probes sit at fixed distances from the tip
	// (recent, inside the scan window, beyond it); the rest are scattered.
	recentH := n - 5
	midH := n - minInt(30000, n*3/10)
	oldH := n - 70000
	if oldH < 1 {
		oldH = 1
	}
	active := costSeededKey(opts.Seed, 1)
	activeAddr := costAddr(active)
	fx.ActiveAddr = crypto.MustNewAddress(crypto.NHBPrefix, activeAddr).String()
	fx.DormantAddr = crypto.MustNewAddress(crypto.NHBPrefix, costAddr(costSeededKey(opts.Seed, 2))).String()
	fx.UnknownTx = fmt.Sprintf("%064x", rng.Uint64())

	nonce := uint64(0)
	transfer := func(h int, from *crypto.PrivateKey, to []byte, value int64) string {
		nonce++
		tx := costSignedTx(tb, from, types.TxTypeTransfer, nonce, to, big.NewInt(value), nil)
		place(h, tx)
		return record(h, tx, true)
	}
	fx.RecentTx = transfer(recentH, active, costAddr(costSeededKey(opts.Seed, 3)), 1_000_000_000_000_000_000)
	fx.MidTx = transfer(midH, active, costAddr(costSeededKey(opts.Seed, 4)), 2_000_000_000_000_000_000)
	fx.OldTx = transfer(oldH, costSeededKey(opts.Seed, 5), activeAddr, 3_000_000_000_000_000_000)
	// A few more for the active address, near and far.
	for _, back := range []int{3, 40, 1500, 9000} {
		transfer(n-back, active, costAddr(costSeededKey(opts.Seed, 10+back)), 5)
	}
	// The rest of the live count, at random heights and random parties.
	for i := 0; i < scaled(liveChainUserFacing); i++ {
		h := 1 + rng.Intn(n)
		from := costSeededKey(opts.Seed, 100+i%7)
		to := costAddr(costSeededKey(opts.Seed, 200+i%11))
		switch i % 4 {
		case 3:
			nonce++
			tx := costSignedTx(tb, from, types.TxTypeTransferZNHB, nonce, to, big.NewInt(int64(7+i)), nil)
			place(h, tx)
			record(h, tx, true)
		default:
			transfer(h, from, to, int64(11+i))
		}
	}

	// Build the blocks.
	restore := silenceStdout(tb)
	defer restore()
	emptyRoot, err := core.ComputeTxRoot(nil)
	if err != nil {
		tb.Fatalf("empty tx root: %v", err)
	}
	validator := costAddr(fx.Key)
	base := time.Now().Unix() - int64(n+5)*opts.Interval
	prev := chain.Tip()
	stateRoot := append([]byte(nil), genesis.StateRoot...)
	execRoot := bytes.Repeat([]byte{0xE1}, 32)
	sigA, sigB := bytes.Repeat([]byte{0xA1}, 65), bytes.Repeat([]byte{0xB2}, 65)
	for h := 1; h <= n; h++ {
		txs := byHeight[h]
		root := emptyRoot
		if len(txs) > 0 {
			if root, err = core.ComputeTxRoot(txs); err != nil {
				tb.Fatalf("tx root at %d: %v", h, err)
			}
		}
		header := &types.BlockHeader{
			Height:             uint64(h),
			Timestamp:          base + int64(h)*opts.Interval,
			PrevHash:           prev,
			StateRoot:          stateRoot,
			TxRoot:             root,
			ExecutionGraphRoot: execRoot,
			Validator:          validator,
		}
		hash, err := header.Hash()
		if err != nil {
			tb.Fatalf("hash header %d: %v", h, err)
		}
		block := &types.Block{
			Header:       header,
			Transactions: txs,
			QuorumCert: &types.QuorumCert{
				Height:    uint64(h),
				BlockHash: hash,
				Signatures: []types.QuorumSignature{
					{Validator: validator, Signature: sigA},
					{Validator: activeAddr, Signature: sigB},
				},
			},
		}
		if err := chain.AddBlock(block); err != nil {
			tb.Fatalf("add block %d: %v", h, err)
		}
		prev = hash
		if h == n-2 {
			fx.RecentBlockHash = "0x" + fmt.Sprintf("%x", hash)
		}
	}
	fx.Height = chain.GetHeight()
	return fx
}

// costRPC issues one JSON-RPC call through the real dispatch and returns the
// HTTP status, the raw body and how long it took.
func costRPC(srv *Server, remote, method string, params ...any) (int, []byte, time.Duration) {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	out, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, out, elapsed
}

// costServerConfig lifts the per-source request quotas out of the way so a
// test measures the cost of the work and not the limiter.
func costServerConfig() ServerConfig {
	const lots = 1 << 30
	return ServerConfig{
		MaxTxPerWindow:        lots,
		MaxTxPerIP:            lots,
		MaxTxPerIdentity:      lots,
		MaxTxPerChain:         lots,
		MaxTxPerIdentityChain: lots,
	}
}

func decodeInto(tb testing.TB, raw []byte, v any) {
	tb.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		tb.Fatalf("decode %q: %v", raw, err)
	}
}

type rpcCall struct {
	Code     int
	Header   http.Header
	Body     []byte
	Duration time.Duration
	Resp     RPCResponse
}

func (c rpcCall) errCode() int {
	if c.Resp.Error == nil {
		return 0
	}
	return c.Resp.Error.Code
}

// doRPC sends one call through the real dispatch on behalf of remote (a client
// address), optionally under a request context.
func doRPC(tb testing.TB, srv *Server, ctx context.Context, remote, method string, params ...any) rpcCall {
	tb.Helper()
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	start := time.Now()
	srv.ServeHTTP(rec, req)
	call := rpcCall{Code: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes(), Duration: time.Since(start)}
	_ = json.Unmarshal(call.Body, &call.Resp)
	return call
}

// appendBlock adds one empty block on top of the chain, as the next block a
// running node would commit.
func (fx *costChain) appendBlock(tb testing.TB) {
	tb.Helper()
	restore := silenceStdout(tb)
	defer restore()
	chain := fx.Node.Chain()
	head := chain.CurrentHeader()
	root, err := core.ComputeTxRoot(nil)
	if err != nil {
		tb.Fatalf("tx root: %v", err)
	}
	header := &types.BlockHeader{
		Height:             head.Height + 1,
		Timestamp:          head.Timestamp + 2,
		PrevHash:           chain.Tip(),
		StateRoot:          head.StateRoot,
		TxRoot:             root,
		ExecutionGraphRoot: bytes.Repeat([]byte{0xE1}, 32),
		Validator:          costAddr(fx.Key),
	}
	if err := chain.AddBlock(&types.Block{Header: header}); err != nil {
		tb.Fatalf("add block: %v", err)
	}
	fx.Height = chain.GetHeight()
}
