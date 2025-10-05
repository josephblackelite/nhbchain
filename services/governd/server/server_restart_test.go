package server

import (
	"context"
	"path/filepath"
	"testing"

	"nhbchain/crypto"
	consensusv1 "nhbchain/proto/consensus/v1"
	govv1 "nhbchain/proto/gov/v1"
	"nhbchain/services/governd/config"
)

type fakeConsensusClient struct {
	submitted []*consensusv1.SignedTxEnvelope
	lastNonce uint64
}

func (f *fakeConsensusClient) QueryState(ctx context.Context, namespace, key string) ([]byte, []byte, error) {
	return nil, nil, nil
}

func (f *fakeConsensusClient) SubmitEnvelope(ctx context.Context, tx *consensusv1.SignedTxEnvelope) error {
	if tx == nil {
		return nil
	}
	f.submitted = append(f.submitted, tx)
	if env := tx.GetEnvelope(); env != nil {
		f.lastNonce = env.GetNonce()
	}
	return nil
}

func TestServicePersistsNonceAcrossRestarts(t *testing.T) {
	t.Parallel()

	signer, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	voter := signer.PubKey().Address().String()

	dir := t.TempDir()
	storePath := filepath.Join(dir, "nonce.store")
	store, err := NewFileNonceStore(storePath)
	if err != nil {
		t.Fatalf("create nonce store: %v", err)
	}

	const baseline uint64 = 7
	restored, err := RestoreNonce(store, baseline)
	if err != nil {
		t.Fatalf("restore nonce: %v", err)
	}
	if restored != baseline {
		t.Fatalf("unexpected initial nonce: want %d, got %d", baseline, restored)
	}

	cfg := config.Config{
		ChainID:        "localnet",
		NonceStart:     restored,
		NonceStorePath: storePath,
	}

	ctx := markAuthenticated(context.Background())
	msg := &govv1.MsgVote{Voter: voter, ProposalId: 1, Option: "yes"}

	// First service run uses the baseline nonce and persists the incremented value.
	consensus1 := &fakeConsensusClient{}
	svc1, err := New(consensus1, signer, cfg, store)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc1.Vote(ctx, msg); err != nil {
		t.Fatalf("first vote: %v", err)
	}
	if consensus1.lastNonce != baseline {
		t.Fatalf("unexpected first nonce: want %d, got %d", baseline, consensus1.lastNonce)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("load persisted nonce: %v", err)
	}
	if persisted != baseline+1 {
		t.Fatalf("unexpected persisted nonce: want %d, got %d", baseline+1, persisted)
	}

	// Simulate restart: reload the store and ensure the persisted nonce is used.
	store2, err := NewFileNonceStore(storePath)
	if err != nil {
		t.Fatalf("reopen nonce store: %v", err)
	}
	restored2, err := RestoreNonce(store2, baseline)
	if err != nil {
		t.Fatalf("restore nonce after restart: %v", err)
	}
	if restored2 != baseline+1 {
		t.Fatalf("unexpected restored nonce: want %d, got %d", baseline+1, restored2)
	}
	cfg.NonceStart = restored2

	consensus2 := &fakeConsensusClient{}
	svc2, err := New(consensus2, signer, cfg, store2)
	if err != nil {
		t.Fatalf("restart service: %v", err)
	}
	if _, err := svc2.Vote(ctx, msg); err != nil {
		t.Fatalf("second vote: %v", err)
	}
	if consensus2.lastNonce != baseline+1 {
		t.Fatalf("unexpected nonce after restart: want %d, got %d", baseline+1, consensus2.lastNonce)
	}
	persisted2, err := store2.Load()
	if err != nil {
		t.Fatalf("load persisted nonce after restart: %v", err)
	}
	if persisted2 != baseline+2 {
		t.Fatalf("unexpected persisted nonce after restart: want %d, got %d", baseline+2, persisted2)
	}
}
