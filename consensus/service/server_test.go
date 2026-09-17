package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
	"nhbchain/network"
	consensusv1 "nhbchain/proto/consensus/v1"
	swapv1 "nhbchain/proto/swap/v1"
	sdkconsensus "nhbchain/sdk/consensus"
)

type fakeConsensusNode struct {
	mu         sync.Mutex
	validators map[string]*big.Int
	commits    int
	envelopes  int
	lastEnv    *consensusv1.SignedTxEnvelope
}

func newFakeConsensusNode() *fakeConsensusNode {
	return &fakeConsensusNode{validators: map[string]*big.Int{"validator-0": big.NewInt(1)}}
}

func (f *fakeConsensusNode) SubmitTransaction(tx *types.Transaction) error { return nil }

func buildSignedEnvelope(t testing.TB) *consensusv1.SignedTxEnvelope {
	t.Helper()
	key, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity,
		Nonce:    1,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
		Data:     []byte("alice"),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	protoTx, err := codec.TransactionToProto(tx)
	if err != nil {
		t.Fatalf("transaction to proto: %v", err)
	}
	payload, err := anypb.New(protoTx)
	if err != nil {
		t.Fatalf("pack payload: %v", err)
	}
	envelope := &consensusv1.TxEnvelope{
		Payload: payload,
		Nonce:   tx.Nonce,
	}
	if tx.ChainID != nil {
		envelope.ChainId = tx.ChainID.String()
	}
	raw, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	digest := sha256.Sum256(raw)
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign envelope: %v", err)
	}
	signature := &consensusv1.TxSignature{
		PublicKey: ethcrypto.FromECDSAPub(&key.PrivateKey.PublicKey),
		Signature: sig,
	}
	return &consensusv1.SignedTxEnvelope{Envelope: envelope, Signature: signature}
}

func buildModuleEnvelope(t testing.TB, msg proto.Message) (*consensusv1.SignedTxEnvelope, *nhbcrypto.PrivateKey) {
	t.Helper()
	key, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	envelope, err := sdkconsensus.NewTx(msg, 1, types.NHBChainID().String(), "", "", "", "")
	if err != nil {
		t.Fatalf("build module envelope: %v", err)
	}
	signed, err := sdkconsensus.Sign(envelope, key)
	if err != nil {
		t.Fatalf("sign module envelope: %v", err)
	}
	return signed, key
}

func sampleSwapPayoutReceipt() *swapv1.PayoutReceipt {
	return &swapv1.PayoutReceipt{
		ReceiptId:    "rcpt-1",
		IntentId:     "intent-1",
		StableAsset:  "USDC",
		StableAmount: "1000",
		NhbAmount:    "1000",
		TxHash:       "0xabc",
		EvidenceUri:  "https://example.com/receipt",
		SettledAt:    time.Now().UTC().Unix(),
	}
}

func marshalSwapPayoutPayload(t testing.TB) []byte {
	t.Helper()
	msg := &swapv1.MsgPayoutReceipt{Authority: "treasury", Receipt: sampleSwapPayoutReceipt()}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payout receipt: %v", err)
	}
	return raw
}

type enforcingConsensusNode struct {
	*fakeConsensusNode
}

func newEnforcingConsensusNode() *enforcingConsensusNode {
	return &enforcingConsensusNode{fakeConsensusNode: newFakeConsensusNode()}
}

func (f *enforcingConsensusNode) SubmitTransaction(tx *types.Transaction) error {
	if err := enforceRecoverableSignature(tx); err != nil {
		return err
	}
	return f.fakeConsensusNode.SubmitTransaction(tx)
}

func (f *enforcingConsensusNode) SubmitTxEnvelope(env *consensusv1.SignedTxEnvelope) error {
	if err := f.enforceEnvelope(env); err != nil {
		return err
	}
	return f.fakeConsensusNode.SubmitTxEnvelope(env)
}

func (f *enforcingConsensusNode) enforceEnvelope(env *consensusv1.SignedTxEnvelope) error {
	tx, err := codec.TransactionFromEnvelope(env)
	if err != nil {
		return err
	}
	return enforceRecoverableSignature(tx)
}

func enforceRecoverableSignature(tx *types.Transaction) error {
	if types.RequiresSignature(tx.Type) {
		if _, err := tx.From(); err != nil {
			return fmt.Errorf("%w: recover sender: %w", core.ErrInvalidTransaction, err)
		}
	}
	return nil
}

func (f *fakeConsensusNode) SubmitTxEnvelope(tx *consensusv1.SignedTxEnvelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envelopes++
	f.lastEnv = tx
	return nil
}

func (f *fakeConsensusNode) GetValidatorSet() map[string]*big.Int {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := make(map[string]*big.Int, len(f.validators))
	for addr, power := range f.validators {
		if power != nil {
			snapshot[addr] = new(big.Int).Set(power)
		} else {
			snapshot[addr] = nil
		}
	}
	return snapshot
}

func (f *fakeConsensusNode) GetBlockByHeight(height uint64) (*types.Block, error) { return nil, nil }
func (f *fakeConsensusNode) GetHeight() uint64                                    { return 0 }
func (f *fakeConsensusNode) GetMempool() []*types.Transaction                     { return nil }
func (f *fakeConsensusNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	return nil, nil
}
func (f *fakeConsensusNode) CommitBlock(block *types.Block) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	return nil
}
func (f *fakeConsensusNode) GetLastCommitHash() []byte { return nil }
func (f *fakeConsensusNode) QueryState(namespace, key string) (*core.QueryResult, error) {
	return nil, nil
}
func (f *fakeConsensusNode) QueryPrefix(namespace, prefix string) ([]core.QueryRecord, error) {
	return nil, nil
}
func (f *fakeConsensusNode) SimulateTx(txBytes []byte) (*core.SimulationResult, error) {
	return nil, nil
}

func TestServerGetValidatorSetConcurrentMutation(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	const iterations = 1000
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			node.mu.Lock()
			key := fmt.Sprintf("validator-%d", i%5)
			node.validators[key] = big.NewInt(int64(i))
			if i%3 == 0 {
				delete(node.validators, key)
			}
			node.mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic: %v", r)
			}
		}()
		for i := 0; i < iterations; i++ {
			resp, err := srv.GetValidatorSet(ctx, &consensusv1.GetValidatorSetRequest{})
			if err != nil {
				errCh <- err
				return
			}
			for range resp.GetValidators() {
				// Iterate to mimic downstream processing.
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("GetValidatorSet encountered error: %v", err)
	default:
	}
}

func TestServerSubmitTxEnvelope(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	envelope := buildSignedEnvelope(t)
	resp, err := srv.SubmitTxEnvelope(context.Background(), &consensusv1.SubmitTxEnvelopeRequest{Tx: envelope})
	if err != nil {
		t.Fatalf("submit envelope: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected response")
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.envelopes != 1 {
		t.Fatalf("expected envelope submission recorded")
	}
	if node.lastEnv != envelope {
		t.Fatalf("unexpected envelope pointer stored")
	}
}

func TestServerSubmitTxEnvelopeInvalidSignature(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	envelope := buildSignedEnvelope(t)
	tampered := proto.Clone(envelope).(*consensusv1.SignedTxEnvelope)
	sig := append([]byte(nil), tampered.GetSignature().GetSignature()...)
	sig[0] ^= 0xFF
	tampered.Signature.Signature = sig

	if _, err := srv.SubmitTxEnvelope(context.Background(), &consensusv1.SubmitTxEnvelopeRequest{Tx: tampered}); err == nil {
		t.Fatalf("expected signature validation error")
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.envelopes != 0 {
		t.Fatalf("unexpected envelope submission on failure")
	}
}

func TestServerSubmitTxEnvelopeModulePayload(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	msg := &swapv1.MsgPayoutReceipt{Authority: "treasury", Receipt: sampleSwapPayoutReceipt()}
	envelope, _ := buildModuleEnvelope(t, msg)

	if _, err := srv.SubmitTxEnvelope(context.Background(), &consensusv1.SubmitTxEnvelopeRequest{Tx: envelope}); err != nil {
		t.Fatalf("submit module envelope: %v", err)
	}
}

func TestServerSubmitTxEnvelopeUnsupportedModulePayload(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	msg := &swapv1.MsgAbortCashOutIntent{Authority: "treasury", IntentId: "intent-1"}
	envelope, _ := buildModuleEnvelope(t, msg)

	if _, err := srv.SubmitTxEnvelope(context.Background(), &consensusv1.SubmitTxEnvelopeRequest{Tx: envelope}); err == nil {
		t.Fatalf("expected unsupported module payload error")
	}
}

func TestServerSubmitTransactionRejectsUnsignedSwapPayoutReceipt(t *testing.T) {
	node := newEnforcingConsensusNode()
	srv := NewServer(node)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		Nonce:    1,
		GasPrice: big.NewInt(0),
		Data:     marshalSwapPayoutPayload(t),
	}
	protoTx, err := codec.TransactionToProto(tx)
	if err != nil {
		t.Fatalf("transaction to proto: %v", err)
	}
	_, err = srv.SubmitTransaction(context.Background(), &consensusv1.SubmitTransactionRequest{Transaction: protoTx})
	if err == nil {
		t.Fatalf("expected signature enforcement error")
	}
	if !errors.Is(err, core.ErrInvalidTransaction) {
		t.Fatalf("expected ErrInvalidTransaction, got %v", err)
	}
}

func TestServerSubmitTxEnvelopeRejectsUnsignedSwapPayoutReceipt(t *testing.T) {
	node := newEnforcingConsensusNode()
	srv := NewServer(node)

	msg := &swapv1.MsgPayoutReceipt{Authority: "treasury", Receipt: sampleSwapPayoutReceipt()}
	envelope, _ := buildModuleEnvelope(t, msg)

	if _, err := srv.SubmitTxEnvelope(context.Background(), &consensusv1.SubmitTxEnvelopeRequest{Tx: envelope}); err == nil {
		t.Fatalf("expected signature enforcement error")
	} else if !errors.Is(err, core.ErrInvalidTransaction) {
		t.Fatalf("expected ErrInvalidTransaction, got %v", err)
	}

	node.mu.Lock()
	defer node.mu.Unlock()
	if node.envelopes != 0 {
		t.Fatalf("expected envelope submission to be rejected")
	}
}

func TestServerAuthorization(t *testing.T) {
	node := newFakeConsensusNode()
	secret := "shared-secret"
	header := "x-nhb-consensus-token"
	authorizer := network.NewTokenAuthenticator(header, secret)

	server := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.ChainUnaryInterceptor(UnaryAuthInterceptor(authorizer)),
		grpc.ChainStreamInterceptor(StreamAuthInterceptor(authorizer)),
	)
	srv := NewServer(node, WithAuthorizer(authorizer))
	if srv.auth == nil {
		t.Fatalf("expected server authorizer to be configured")
	}
	if err := srv.authorize(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected authorizer to reject missing metadata, got %v", err)
	}
	consensusv1.RegisterConsensusServiceServer(server, srv)
	consensusv1.RegisterQueryServiceServer(server, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go server.Serve(lis)
	defer server.Stop()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	unauthConn, err := grpc.DialContext(dialCtx, lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauthenticated: %v", err)
	}
	defer unauthConn.Close()

	block := &types.Block{Header: &types.BlockHeader{Height: 1}}
	pbBlock, err := codec.BlockToProto(block)
	if err != nil {
		t.Fatalf("block to proto: %v", err)
	}

	_, err = consensusv1.NewConsensusServiceClient(unauthConn).CommitBlock(dialCtx, &consensusv1.CommitBlockRequest{Block: pbBlock})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}

	queryUnauth := consensusv1.NewQueryServiceClient(unauthConn)

	if _, err := queryUnauth.QueryState(context.Background(), &consensusv1.QueryStateRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated query state error, got %v", err)
	}

	if _, err := queryUnauth.SimulateTx(context.Background(), &consensusv1.SimulateTxRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated simulate tx error, got %v", err)
	}

	unauthStreamCtx, cancelUnauthStream := context.WithTimeout(context.Background(), time.Second)
	defer cancelUnauthStream()
	unauthStream, err := queryUnauth.QueryPrefix(unauthStreamCtx, &consensusv1.QueryPrefixRequest{})
	if err == nil {
		_, err = unauthStream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated query prefix error, got %v", err)
	}

	authConn, err := grpc.DialContext(
		dialCtx,
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(network.NewStaticTokenCredentialsAllowInsecure(header, secret)),
	)
	if err != nil {
		t.Fatalf("dial authenticated: %v", err)
	}
	defer authConn.Close()

	_, err = consensusv1.NewConsensusServiceClient(authConn).CommitBlock(context.Background(), &consensusv1.CommitBlockRequest{Block: pbBlock})
	if err != nil {
		t.Fatalf("authenticated commit failed: %v", err)
	}

	queryAuth := consensusv1.NewQueryServiceClient(authConn)

	if _, err := queryAuth.QueryState(context.Background(), &consensusv1.QueryStateRequest{}); err != nil {
		t.Fatalf("authenticated query state failed: %v", err)
	}

	if _, err := queryAuth.SimulateTx(context.Background(), &consensusv1.SimulateTxRequest{}); err != nil {
		t.Fatalf("authenticated simulate tx failed: %v", err)
	}

	authStreamCtx, cancelAuthStream := context.WithTimeout(context.Background(), time.Second)
	defer cancelAuthStream()
	authStream, err := queryAuth.QueryPrefix(authStreamCtx, &consensusv1.QueryPrefixRequest{})
	if err != nil {
		t.Fatalf("authenticated query prefix failed to open: %v", err)
	}
	if _, err := authStream.Recv(); err != io.EOF {
		t.Fatalf("expected EOF from authorized query prefix, got %v", err)
	}

	node.mu.Lock()
	commits := node.commits
	node.mu.Unlock()
	if commits != 1 {
		t.Fatalf("expected exactly one committed block, got %d", commits)
	}
}
