package state

import (
	"encoding/binary"
	"math/big"
	"testing"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestStakingKeyFormats(t *testing.T) {
	globalKey := StakingGlobalIndexKey()
	if string(globalKey) != "staking/globalIndex" {
		t.Fatalf("unexpected global index key: %s", string(globalKey))
	}

	lastTsKey := StakingLastIndexUpdateTsKey()
	if string(lastTsKey) != "staking/lastUpdate" {
		t.Fatalf("unexpected last index timestamp key: %s", string(lastTsKey))
	}

	emissionKey := StakingEmissionYTDKey(2024)
	if string(emissionKey) != "staking/ytdEmissions/2024" {
		t.Fatalf("unexpected emission key: %s", string(emissionKey))
	}

	mintKey := MintEmissionYTDKey("znhb", 2024)
	if string(mintKey) != "mint/ZNHB/ytdEmissions/2024" {
		t.Fatalf("unexpected mint emission key: %s", string(mintKey))
	}

	acctKey := StakingAcctKey([]byte{0x01, 0x02, 0x03})
	expectedAcct := append([]byte("staking/account/"), 0x01, 0x02, 0x03)
	if string(acctKey) != string(expectedAcct) {
		t.Fatalf("unexpected account key: %x", acctKey)
	}
}

func TestMintEmissionHelpers(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	mgr := NewManager(tr)

	if err := mgr.SetMintEmissionYTD("NHB", 2025, big.NewInt(150)); err != nil {
		t.Fatalf("set mint emission: %v", err)
	}
	total, err := mgr.MintEmissionYTD("nhb", 2025)
	if err != nil {
		t.Fatalf("get mint emission: %v", err)
	}
	if total.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("unexpected mint emission total: %s", total)
	}

	updated, err := mgr.IncrementMintEmissionYTD("nhb", 2025, big.NewInt(25))
	if err != nil {
		t.Fatalf("increment mint emission: %v", err)
	}
	if updated.Cmp(big.NewInt(175)) != 0 {
		t.Fatalf("unexpected updated mint emission: %s", updated)
	}

	stored, err := mgr.MintEmissionYTD("NHB", 2025)
	if err != nil {
		t.Fatalf("reload mint emission: %v", err)
	}
	if stored.Cmp(big.NewInt(175)) != 0 {
		t.Fatalf("unexpected stored mint emission: %s", stored)
	}
}

func TestStakingKVReadWrite(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	mgr := NewManager(tr)

	indexValue := big.NewInt(0).Mul(big.NewInt(1234), big.NewInt(1e9)).Bytes()
	if err := mgr.trie.TryUpdate(StakingGlobalIndexKey(), indexValue); err != nil {
		t.Fatalf("write global index: %v", err)
	}

	tsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBytes, 1700000000)
	if err := mgr.trie.TryUpdate(StakingLastIndexUpdateTsKey(), tsBytes); err != nil {
		t.Fatalf("write timestamp: %v", err)
	}

	ytdBytes := big.NewInt(987654321).Bytes()
	if err := mgr.trie.TryUpdate(StakingEmissionYTDKey(2024), ytdBytes); err != nil {
		t.Fatalf("write emissions: %v", err)
	}

	acctBytes := []byte("snapshot")
	if err := mgr.trie.TryUpdate(StakingAcctKey([]byte{0xaa}), acctBytes); err != nil {
		t.Fatalf("write account snapshot: %v", err)
	}

	if fetched, err := mgr.trie.TryGet(StakingGlobalIndexKey()); err != nil {
		t.Fatalf("get global index: %v", err)
	} else if string(fetched) != string(indexValue) {
		t.Fatalf("unexpected global index payload: %x", fetched)
	}

	if fetched, err := mgr.trie.TryGet(StakingLastIndexUpdateTsKey()); err != nil {
		t.Fatalf("get timestamp: %v", err)
	} else if binary.BigEndian.Uint64(fetched) != 1700000000 {
		t.Fatalf("unexpected timestamp payload: %d", binary.BigEndian.Uint64(fetched))
	}

	if fetched, err := mgr.trie.TryGet(StakingEmissionYTDKey(2024)); err != nil {
		t.Fatalf("get emissions: %v", err)
	} else if string(fetched) != string(ytdBytes) {
		t.Fatalf("unexpected emission payload: %x", fetched)
	}

	if fetched, err := mgr.trie.TryGet(StakingAcctKey([]byte{0xaa})); err != nil {
		t.Fatalf("get account snapshot: %v", err)
	} else if string(fetched) != string(acctBytes) {
		t.Fatalf("unexpected account payload: %x", fetched)
	}
}

func TestStakingEmissionHelpers(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	mgr := NewManager(tr)

	if err := mgr.SetStakingEmissionYTD(2025, big.NewInt(100)); err != nil {
		t.Fatalf("set emission: %v", err)
	}
	total, err := mgr.StakingEmissionYTD(2025)
	if err != nil {
		t.Fatalf("get emission: %v", err)
	}
	if total.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected emission total: %s", total)
	}

	updated, err := mgr.IncrementStakingEmissionYTD(2025, big.NewInt(25))
	if err != nil {
		t.Fatalf("increment emission: %v", err)
	}
	if updated.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("unexpected updated emission: %s", updated)
	}

	stored, err := mgr.StakingEmissionYTD(2025)
	if err != nil {
		t.Fatalf("reload emission: %v", err)
	}
	if stored.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("unexpected stored emission: %s", stored)
	}
}
