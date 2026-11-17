package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// PL-R5T-79: a state trie in which every branch points at the same child sixteen
// times is a handful of nodes on disk but 16^depth paths to a walk that visits
// every path. An archive built that way used to make `nhb-snapshot extract` run
// for as long as the attacker liked. verifyTrieNodes now hashes each distinct
// node once and does not descend into one it has already verified.
type sharedTrie struct {
	disk ethdb.Database
	root common.Hash
	leaf common.Hash
	// hashes lists the distinct nodes, the leaf first and the root last.
	hashes []common.Hash
}

func leafBlob(t *testing.T, value string) []byte {
	t.Helper()
	// A leaf with no remaining key nibbles: compact key 0x20, then the value.
	blob, err := rlp.EncodeToBytes([][]byte{{0x20}, []byte(value)})
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < 32 {
		t.Fatalf("leaf encodes to %d bytes: it would be embedded in its parent instead of stored under a hash", len(blob))
	}
	return blob
}

// buildSharedTrie writes a leaf and `depth` branch levels, each branch holding the
// level below it sixteen times, and returns them.
func buildSharedTrie(t *testing.T, depth int) *sharedTrie {
	t.Helper()
	disk := rawdb.NewMemoryDatabase()
	st := &sharedTrie{disk: disk}
	leaf := leafBlob(t, strings.Repeat("v", 40))
	hash := crypto.Keccak256Hash(leaf)
	rawdb.WriteLegacyTrieNode(disk, hash, leaf)
	st.leaf = hash
	st.hashes = append(st.hashes, hash)
	for level := 0; level < depth; level++ {
		children := make([][]byte, 17)
		for i := 0; i < 16; i++ {
			children[i] = hash.Bytes()
		}
		children[16] = []byte{}
		blob, err := rlp.EncodeToBytes(children)
		if err != nil {
			t.Fatal(err)
		}
		hash = crypto.Keccak256Hash(blob)
		rawdb.WriteLegacyTrieNode(disk, hash, blob)
		st.hashes = append(st.hashes, hash)
	}
	st.root = hash
	return st
}

func (st *sharedTrie) open(t *testing.T) *gethtrie.Trie {
	t.Helper()
	tr, err := gethtrie.New(gethtrie.TrieID(st.root), triedb.NewDatabase(st.disk, triedb.HashDefaults))
	if err != nil {
		t.Fatalf("open the shared-subtree trie: %v", err)
	}
	return tr
}

func verifyWithin(t *testing.T, tr *gethtrie.Trie, limit time.Duration) (uint64, error, bool) {
	t.Helper()
	type result struct {
		nodes uint64
		err   error
	}
	done := make(chan result, 1)
	go func() {
		nodes, err := verifyTrieNodes(tr)
		done <- result{nodes, err}
	}()
	select {
	case r := <-done:
		return r.nodes, r.err, true
	case <-time.After(limit):
		return 0, nil, false
	}
}

func TestVerifyTrieNodesDoesNotWalkEveryPathOfASharedSubtree(t *testing.T) {
	const depth = 10 // 16^10, about 1.1e12 paths, over 11 stored nodes
	st := buildSharedTrie(t, depth)
	nodes, err, finished := verifyWithin(t, st.open(t), 20*time.Second)
	if !finished {
		t.Fatalf("verifyTrieNodes did not finish within 20s on a trie of %d distinct nodes (16^%d paths): it walks every path", depth+1, depth)
	}
	if err != nil {
		t.Fatalf("an honest shared-subtree trie was refused: %v", err)
	}
	if want := uint64(depth + 1); nodes != want {
		t.Fatalf("counted %d nodes, want %d (distinct nodes, each hashed once)", nodes, want)
	}
}

func TestVerifyTrieNodesStillCatchesATamperedSharedNode(t *testing.T) {
	for _, which := range []struct {
		name  string
		index int // into st.hashes: 0 is the leaf every path ends in, the last is the root
	}{
		{"the leaf every path ends in", 0},
		{"a branch in the middle", 5},
	} {
		t.Run(which.name, func(t *testing.T) {
			st := buildSharedTrie(t, 8)
			// Stored bytes that are a valid node but do not hash to the key.
			forged := leafBlob(t, strings.Repeat("x", 41))
			if which.index != 0 {
				children := make([][]byte, 17)
				for i := range children {
					children[i] = []byte{}
				}
				children[0] = st.hashes[which.index-1].Bytes()
				blob, err := rlp.EncodeToBytes(children)
				if err != nil {
					t.Fatal(err)
				}
				forged = blob
			}
			if crypto.Keccak256Hash(forged) == st.hashes[which.index] {
				t.Fatalf("the forged bytes hash to the real node")
			}
			rawdb.WriteLegacyTrieNode(st.disk, st.hashes[which.index], forged)
			_, err, finished := verifyWithin(t, st.open(t), 20*time.Second)
			if !finished {
				t.Fatalf("verifyTrieNodes did not finish within 20s")
			}
			if err == nil {
				t.Fatalf("SECURITY: a node whose bytes do not hash to its key passed verification")
			}
			if !strings.Contains(err.Error(), "hash to") && !strings.Contains(err.Error(), "missing") {
				t.Logf("refused with: %v", err)
			}
		})
	}
}

func TestVerifyTrieNodesCountsADistinctNodeOnceInAPlainTrie(t *testing.T) {
	// No sharing at all: a branch with two distinct leaves counts three nodes.
	disk := rawdb.NewMemoryDatabase()
	a := leafBlob(t, strings.Repeat("a", 40))
	b := leafBlob(t, strings.Repeat("b", 40))
	ha, hb := crypto.Keccak256Hash(a), crypto.Keccak256Hash(b)
	rawdb.WriteLegacyTrieNode(disk, ha, a)
	rawdb.WriteLegacyTrieNode(disk, hb, b)
	children := make([][]byte, 17)
	for i := range children {
		children[i] = []byte{}
	}
	children[1], children[2] = ha.Bytes(), hb.Bytes()
	root, err := rlp.EncodeToBytes(children)
	if err != nil {
		t.Fatal(err)
	}
	hr := crypto.Keccak256Hash(root)
	rawdb.WriteLegacyTrieNode(disk, hr, root)
	tr, err := gethtrie.New(gethtrie.TrieID(hr), triedb.NewDatabase(disk, triedb.HashDefaults))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := verifyTrieNodes(tr)
	if err != nil {
		t.Fatal(err)
	}
	if nodes != 3 {
		t.Fatalf("counted %d nodes, want 3", nodes)
	}
}
