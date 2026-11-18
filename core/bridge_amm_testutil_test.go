package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/config"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// bcFixture is a state processor with an admin wallet, bootstrapped ZNHB pools (so CheckZNHBSupplyInvariant means
// something) and a few funded accounts, for the bridge and pool tests (ledger PL-BC-C, PL-BC-B).
type bcFixture struct {
	t     *testing.T
	sp    *StateProcessor
	admin [20]byte
	keys  map[string]*crypto.PrivateKey
	addr  map[string][]byte
}

func newBCFixture(t *testing.T, bridge BridgeSettings, pool AmmSettings) *bcFixture {
	t.Helper()
	sp := newRewardTestState(t)
	admin := withRewardPoolAdminWallet(t, sp)
	f := &bcFixture{t: t, sp: sp, admin: admin, keys: map[string]*crypto.PrivateKey{}, addr: map[string][]byte{}}
	for _, name := range []string{"alice", "bob", "relayer", "provider", "signer1", "signer2", "signer3", "guardian"} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate %s key: %v", name, err)
		}
		f.keys[name] = key
		f.addr[name] = key.PubKey().Address().Bytes()
	}
	if bridge.ActivationHeight != 0 && len(bridge.Params) == 0 {
		bridge.Params = []BridgeParams{f.defaultParams(bridge.ActivationHeight)}
	}
	if pool.ActivationHeight != 0 {
		copy(pool.Provider[:], f.addr["provider"])
		pool.HasProvider = true
		if pool.MinReserveZNHB == nil {
			pool.MinReserveZNHB = big.NewInt(1)
		}
		if pool.MinReserveNHB == nil {
			pool.MinReserveNHB = big.NewInt(1)
		}
		if pool.MaxSwapBps == 0 {
			pool.MaxSwapBps = 500
		}
	}
	sp.SetBridge(bridge)
	sp.SetAmm(pool)
	f.atHeight(100)
	return f
}

func (f *bcFixture) atHeight(h uint64) {
	f.t.Helper()
	f.sp.BeginBlock(h, time.Unix(rewardBlockTimestamp1, 0).UTC())
}

func wei(n int64) *big.Int { return weiZNHB(n) }

func rlpEncode(v interface{}) ([]byte, error) { return rlp.EncodeToBytes(v) }

func newTestConfigForBridge() *config.Config { return &config.Config{} }

// fund sets an account's balances (the admin wallet is left alone).
func (f *bcFixture) fund(name string, znhb, nhb *big.Int) {
	f.t.Helper()
	acc, err := f.sp.getAccount(f.addr[name])
	if err != nil {
		f.t.Fatalf("load %s: %v", name, err)
	}
	acc.BalanceZNHB, acc.BalanceNHB = new(big.Int).Set(znhb), new(big.Int).Set(nhb)
	if err := f.sp.setAccount(f.addr[name], acc); err != nil {
		f.t.Fatalf("fund %s: %v", name, err)
	}
}

func (f *bcFixture) balances(addr []byte) (znhb, nhb *big.Int) {
	f.t.Helper()
	acc, err := f.sp.getAccount(addr)
	if err != nil {
		f.t.Fatalf("load balances: %v", err)
	}
	return new(big.Int).Set(znhbBalanceOf(acc)), new(big.Int).Set(nhbBalanceOf(acc))
}

// send builds, signs and applies one transaction from a named account at its current nonce.
func (f *bcFixture) send(name string, typ types.TxType, value *big.Int, payload interface{}) error {
	f.t.Helper()
	var data []byte
	if payload != nil {
		var err error
		data, err = rlp.EncodeToBytes(payload)
		if err != nil {
			f.t.Fatalf("encode payload: %v", err)
		}
	}
	acc, err := f.sp.getAccount(f.addr[name])
	if err != nil {
		f.t.Fatalf("load %s: %v", name, err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     typ,
		Nonce:    acc.Nonce,
		Value:    value,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	signTransaction(f.t, tx, f.keys[name])
	return f.sp.ApplyTransaction(tx)
}

// checkInvariants runs the supply invariant and both module invariants.
func (f *bcFixture) checkInvariants(when string) {
	f.t.Helper()
	if err := f.sp.CheckZNHBSupplyInvariant(); err != nil {
		f.t.Fatalf("%s: ZNHB supply invariant: %v", when, err)
	}
	if err := f.sp.CheckBridgeInvariant(); err != nil {
		f.t.Fatalf("%s: %v", when, err)
	}
	if err := f.sp.CheckAmmInvariant(); err != nil {
		f.t.Fatalf("%s: %v", when, err)
	}
}

// ---- bridge Stage 1 helpers: signers, guardian, limits

// defaultParams are the release rules the fixture's bridge runs with unless a test changes them: three signers of
// which two are needed, a guardian, 1,000 ZNHB per release, 5,000 per window of 1,000 blocks, nothing waits.
func (f *bcFixture) defaultParams(from uint64) BridgeParams {
	var p BridgeParams
	p.FromHeight = from
	for _, n := range []string{"signer1", "signer2", "signer3"} {
		var a [20]byte
		copy(a[:], f.addr[n])
		p.Signers = append(p.Signers, a)
	}
	p.Threshold = 2
	copy(p.Guardian[:], f.addr["guardian"])
	p.MaxReleaseWei = wei(1_000)
	p.WindowBlocks = 1_000
	p.WindowCapWei = wei(5_000)
	return p
}

// setParams replaces the bridge's rules with the defaults changed by mutate (one entry, in force from height 100).
func (f *bcFixture) setParams(mutate func(*BridgeParams)) {
	f.t.Helper()
	p := f.defaultParams(100)
	mutate(&p)
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, Params: []BridgeParams{p}})
}

// testBridgeParams is a rule set with made-up addresses, for tests that never sign.
func testBridgeParams(from uint64) BridgeParams {
	p := BridgeParams{FromHeight: from, Threshold: 2, MaxReleaseWei: big.NewInt(1_000), WindowBlocks: 1_000, WindowCapWei: big.NewInt(5_000)}
	for i := byte(1); i <= 3; i++ {
		var a [20]byte
		a[19] = i
		p.Signers = append(p.Signers, a)
	}
	p.Guardian[19] = 0x99
	return p
}

// signRelease is signer's signature over the attestation of one burn.
func (f *bcFixture) signRelease(signer string, burn string, to []byte, amount *big.Int) []byte {
	f.t.Helper()
	digest, err := BridgeReleaseDigest(bridgeNetworkID(f.sp), []byte(burn), to, amount)
	if err != nil {
		f.t.Fatalf("digest: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], f.keys[signer].PrivateKey)
	if err != nil {
		f.t.Fatalf("sign: %v", err)
	}
	return sig
}

// releasePayload is a release operation signed by the named signers.
func (f *bcFixture) releasePayload(burn string, to []byte, amount *big.Int, signers ...string) bridgeReleasePayload {
	f.t.Helper()
	p := bridgeReleasePayload{Op: BridgeOpRelease, BurnID: []byte(burn), Recipient: to, Amount: amount}
	for _, s := range signers {
		p.Signatures = append(p.Signatures, f.signRelease(s, burn, to, amount))
	}
	return p
}

// releaseTo sends a release of amount to the raw address, signed by signer1 and signer2 unless others are named, from
// the relayer account (anyone may send one: the signatures are what authorize it).
func (f *bcFixture) releaseTo(burn string, to []byte, amount *big.Int, signers ...string) error {
	f.t.Helper()
	if len(signers) == 0 {
		signers = []string{"signer1", "signer2"}
	}
	return f.send("relayer", types.TxTypeBridgeRelease, big.NewInt(0), f.releasePayload(burn, to, amount, signers...))
}

// release is releaseTo for a named account.
func (f *bcFixture) release(burn, to string, amount *big.Int, signers ...string) error {
	f.t.Helper()
	return f.releaseTo(burn, f.addr[to], amount, signers...)
}

// op sends a release-type operation that carries no signatures (execute, cancel, pause, unpause) from a named account.
func (f *bcFixture) op(sender string, op uint8, burn string) error {
	f.t.Helper()
	return f.send(sender, types.TxTypeBridgeRelease, big.NewInt(0), bridgeReleasePayload{Op: op, BurnID: []byte(burn)})
}
