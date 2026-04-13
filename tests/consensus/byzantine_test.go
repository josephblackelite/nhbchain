package consensus

import (
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"nhbchain/consensus/bft"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type noopBroadcaster struct{}

func (noopBroadcaster) Broadcast(*p2p.Message) error { return nil }

type stubNode struct {
	validators map[string]*big.Int
	height     uint64
}

func (s *stubNode) GetMempool() []*types.Transaction { return nil }
func (s *stubNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	header := &types.BlockHeader{Height: s.height + 1}
	return types.NewBlock(header, txs), nil
}
func (s *stubNode) CommitBlock(block *types.Block) error { return nil }
func (s *stubNode) GetValidatorSet() map[string]*big.Int { return s.validators }
func (s *stubNode) GetAccount(addr []byte) (*types.Account, error) {
	weight := s.validators[string(addr)]
	if weight == nil {
		weight = big.NewInt(0)
	}
	return &types.Account{Stake: new(big.Int).Set(weight)}, nil
}
func (s *stubNode) GetLastCommitHash() []byte { return nil }
func (s *stubNode) GetHeight() uint64         { return s.height }

func TestHandleVoteRejectsUnknownValidator(t *testing.T) {
	authorisedKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate authorised key: %v", err)
	}
	authorisedAddr := authorisedKey.PubKey().Address().Bytes()

	node := &stubNode{
		validators: map[string]*big.Int{string(authorisedAddr): big.NewInt(10)},
		height:     1,
	}
	engine := bft.NewEngine(node, authorisedKey, noopBroadcaster{})

	unauthorisedKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate unauthorised key: %v", err)
	}
	vote := &bft.Vote{
		BlockHash: []byte("deterministic-block"),
		Round:     0,
		Type:      bft.Prevote,
		Height:    node.height + 1,
	}
	votePayload, err := json.Marshal(vote)
	if err != nil {
		t.Fatalf("marshal vote: %v", err)
	}
	hash := sha256.Sum256(votePayload)
	sig, err := ethcrypto.Sign(hash[:], unauthorisedKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign vote: %v", err)
	}
	signed := &bft.SignedVote{
		Vote:      vote,
		Validator: unauthorisedKey.PubKey().Address().Bytes(),
		Signature: &bft.Signature{Scheme: bft.SignatureSchemeSecp256k1, Signature: sig},
	}

	if err := engine.HandleVote(signed); err == nil {
		t.Fatal("expected vote from non-validator to be rejected")
	} else if !strings.Contains(err.Error(), "non-validator") {
		t.Fatalf("unexpected error: %v", err)
	}
}
