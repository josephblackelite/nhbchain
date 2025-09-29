package payoutd

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"nhbchain/crypto"
	consensusv1 "nhbchain/proto/consensus/v1"
	swapv1 "nhbchain/proto/swap/v1"
	"nhbchain/sdk/consensus"
)

// Receipt encapsulates the data required to emit a MsgPayoutReceipt.
type Receipt struct {
	ReceiptID    string
	IntentID     string
	StableAsset  string
	StableAmount *big.Int
	NhbAmount    *big.Int
	TxHash       string
	EvidenceURI  string
	SettledAt    time.Time
}

// Attestor defines the operations required to finalise or abort a cash-out intent.
type Attestor interface {
	SubmitReceipt(ctx context.Context, receipt Receipt) error
	AbortIntent(ctx context.Context, intentID, reason string) error
}

// TxClient abstracts the consensus client used to submit signed envelopes.
type TxClient interface {
	SubmitEnvelope(ctx context.Context, tx *consensusv1.SignedTxEnvelope) error
}

// NonceSource yields monotonically increasing nonces for the attestor key.
type NonceSource interface {
	Next(ctx context.Context) (uint64, error)
}

// StaticNonceSource satisfies NonceSource for single threaded flows.
type StaticNonceSource struct {
	mu   sync.Mutex
	next uint64
}

// Next returns the next available nonce.
func (s *StaticNonceSource) Next(context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	val := s.next
	s.next++
	return val, nil
}

// ConsensusAttestor submits receipts to the consensus service.
type ConsensusAttestor struct {
	Client    TxClient
	Signer    *crypto.PrivateKey
	Authority string
	ChainID   string
	FeeAmount string
	FeeDenom  string
	FeePayer  string
	Memo      string
	Nonces    NonceSource
}

// SubmitReceipt publishes a MsgPayoutReceipt for the supplied payout.
func (a *ConsensusAttestor) SubmitReceipt(ctx context.Context, receipt Receipt) error {
	if err := a.ensureReady(); err != nil {
		return err
	}
	if strings.TrimSpace(receipt.IntentID) == "" {
		return fmt.Errorf("payoutd: receipt intent id required")
	}
	msg := &swapv1.MsgPayoutReceipt{
		Authority: strings.TrimSpace(a.Authority),
		Receipt: &swapv1.PayoutReceipt{
			ReceiptId:    strings.TrimSpace(receipt.ReceiptID),
			IntentId:     strings.TrimSpace(receipt.IntentID),
			StableAsset:  strings.ToUpper(strings.TrimSpace(receipt.StableAsset)),
			StableAmount: amountString(receipt.StableAmount),
			NhbAmount:    amountString(receipt.NhbAmount),
			TxHash:       strings.TrimSpace(receipt.TxHash),
			EvidenceUri:  strings.TrimSpace(receipt.EvidenceURI),
			SettledAt:    receipt.SettledAt.UTC().Unix(),
		},
	}
	return a.submit(ctx, msg)
}

// AbortIntent emits a MsgAbortCashOutIntent for the specified intent ID.
func (a *ConsensusAttestor) AbortIntent(ctx context.Context, intentID, reason string) error {
	if err := a.ensureReady(); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(intentID)
	if trimmed == "" {
		return fmt.Errorf("payoutd: abort intent id required")
	}
	msg := &swapv1.MsgAbortCashOutIntent{
		Authority: strings.TrimSpace(a.Authority),
		IntentId:  trimmed,
		Reason:    strings.TrimSpace(reason),
	}
	return a.submit(ctx, msg)
}

func (a *ConsensusAttestor) submit(ctx context.Context, msg proto.Message) error {
	if a.Client == nil {
		return fmt.Errorf("payoutd: consensus client not configured")
	}
	if a.Nonces == nil {
		a.Nonces = &StaticNonceSource{}
	}
	nonce, err := a.Nonces.Next(ctx)
	if err != nil {
		return err
	}
	envelope, err := consensus.NewTx(msg, nonce, a.ChainID, a.FeeAmount, a.FeeDenom, a.FeePayer, a.Memo)
	if err != nil {
		return err
	}
	signed, err := consensus.Sign(envelope, a.Signer)
	if err != nil {
		return err
	}
	return a.Client.SubmitEnvelope(ctx, signed)
}

func (a *ConsensusAttestor) ensureReady() error {
	if a == nil {
		return fmt.Errorf("payoutd: attestor not configured")
	}
	if strings.TrimSpace(a.Authority) == "" {
		return fmt.Errorf("payoutd: authority not configured")
	}
	if strings.TrimSpace(a.ChainID) == "" {
		return fmt.Errorf("payoutd: chain id not configured")
	}
	if a.Signer == nil {
		return fmt.Errorf("payoutd: signer not configured")
	}
	return nil
}

func amountString(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	return amount.String()
}
