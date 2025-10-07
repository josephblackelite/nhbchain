package trie

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"nhbchain/storage"
)

func TestTrieCommitFlushPersistsData(t *testing.T) {
	dir := t.TempDir()

	db1, err := storage.NewLevelDB(dir)
	require.NoError(t, err)

	tr, err := NewTrie(db1, nil)
	require.NoError(t, err)

	key := crypto.Keccak256Hash([]byte("key"))
	value := []byte("value")

	require.NoError(t, tr.Update(key.Bytes(), value))
	root, err := tr.Commit(common.Hash{}, 0)
	require.NoError(t, err)

	db1.Close()

	db2, err := storage.NewLevelDB(dir)
	require.NoError(t, err)
	defer db2.Close()

	restored, err := NewTrie(db2, root.Bytes())
	require.NoError(t, err)

	got, err := restored.Get(key.Bytes())
	require.NoError(t, err)
	require.Equal(t, value, got)
}
