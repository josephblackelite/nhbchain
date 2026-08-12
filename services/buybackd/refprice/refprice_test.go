package refprice

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/services/buybackd/rpcclient"
)

type mockSource struct {
	rate *big.Rat
	ts   time.Time
	err  error
}

func (m *mockSource) Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error) {
	if m.err != nil {
		return nil, nil, time.Time{}, m.err
	}
	return m.rate, []string{"mock"}, m.ts, nil
}

type mockSigner struct {
	sig []byte
	err error
}

func (m *mockSigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	if m.err != nil {
		return nil, "", m.err
	}
	return m.sig, "mock-signer", nil
}

func fakeSig(b byte) []byte {
	sig := make([]byte, 65)
	sig[0] = b
	return sig
}

type mockChain struct {
	status       *rpcclient.RefPriceStatus
	statusErr    error
	submittedTx  string
	submitErr    error
	submitCalled bool
	gotRateNum   *big.Int
	gotRateDenom *big.Int
	gotEpoch     uint64
	gotSigs      [][]byte
}

func (m *mockChain) GetRefPriceStatus(ctx context.Context, epoch *uint64) (*rpcclient.RefPriceStatus, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.status, nil
}

func (m *mockChain) SubmitRefPrice(ctx context.Context, rateNum, rateDenom *big.Int, epoch, timestamp uint64, signatures [][]byte) (string, error) {
	m.submitCalled = true
	m.gotRateNum = rateNum
	m.gotRateDenom = rateDenom
	m.gotEpoch = epoch
	m.gotSigs = signatures
	if m.submitErr != nil {
		return "", m.submitErr
	}
	return m.submittedTx, nil
}

func TestNew_RejectsFewerSignersThanThreshold(t *testing.T) {
	source := &mockSource{}
	chain := &mockChain{}
	_, err := New(source, []Signer{&mockSigner{}}, 2, chain, "ZNHB/USD")
	if err == nil {
		t.Fatalf("expected an error when fewer local signers than threshold are configured")
	}
}

func TestAttempt_SkipsWhenAlreadyRecorded(t *testing.T) {
	source := &mockSource{rate: big.NewRat(5, 100), ts: time.Now()}
	chain := &mockChain{status: &rpcclient.RefPriceStatus{Epoch: 7, HasRefPrice: true}}
	svc, err := New(source, []Signer{&mockSigner{sig: fakeSig(1)}, &mockSigner{sig: fakeSig(2)}}, 2, chain, "ZNHB/USD")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	submitted, _, err := svc.Attempt(context.Background())
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if submitted {
		t.Fatalf("expected no submission when the epoch already has a recorded reference price")
	}
	if chain.submitCalled {
		t.Fatalf("expected SubmitRefPrice not to be called")
	}
}

func TestAttempt_SubmitsFreshPriceSignedByEverySigner(t *testing.T) {
	rate := big.NewRat(5, 100)
	ts := time.Unix(1700000000, 0).UTC()
	source := &mockSource{rate: rate, ts: ts}
	chain := &mockChain{status: &rpcclient.RefPriceStatus{Epoch: 9, HasRefPrice: false}, submittedTx: "0xabc"}
	sig1 := fakeSig(0xaa)
	sig2 := fakeSig(0xbb)
	svc, err := New(source, []Signer{&mockSigner{sig: sig1}, &mockSigner{sig: sig2}}, 2, chain, "ZNHB/USD")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	submitted, txHash, err := svc.Attempt(context.Background())
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if !submitted {
		t.Fatalf("expected a submission")
	}
	if txHash != "0xabc" {
		t.Fatalf("txHash = %q, want 0xabc", txHash)
	}
	if !chain.submitCalled {
		t.Fatalf("expected SubmitRefPrice to be called")
	}
	if chain.gotEpoch != 9 {
		t.Fatalf("submitted epoch = %d, want 9", chain.gotEpoch)
	}
	if chain.gotRateNum.Cmp(rate.Num()) != 0 || chain.gotRateDenom.Cmp(rate.Denom()) != 0 {
		t.Fatalf("submitted rate = %s/%s, want %s/%s", chain.gotRateNum, chain.gotRateDenom, rate.Num(), rate.Denom())
	}
	if len(chain.gotSigs) != 2 {
		t.Fatalf("expected 2 signatures submitted, got %d", len(chain.gotSigs))
	}
}

func TestAttempt_PropagatesQuoteError(t *testing.T) {
	source := &mockSource{err: errors.New("no fresh quote")}
	chain := &mockChain{status: &rpcclient.RefPriceStatus{Epoch: 3, HasRefPrice: false}}
	svc, err := New(source, []Signer{&mockSigner{sig: fakeSig(1)}, &mockSigner{sig: fakeSig(2)}}, 2, chain, "ZNHB/USD")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, _, err := svc.Attempt(context.Background()); err == nil {
		t.Fatalf("expected quote error to propagate")
	}
	if chain.submitCalled {
		t.Fatalf("expected SubmitRefPrice not to be called after a quote failure")
	}
}

func TestAttempt_PropagatesSignerError(t *testing.T) {
	source := &mockSource{rate: big.NewRat(5, 100), ts: time.Now()}
	chain := &mockChain{status: &rpcclient.RefPriceStatus{Epoch: 3, HasRefPrice: false}}
	svc, err := New(source, []Signer{&mockSigner{sig: fakeSig(1)}, &mockSigner{err: errors.New("keystore locked")}}, 2, chain, "ZNHB/USD")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, _, err := svc.Attempt(context.Background()); err == nil {
		t.Fatalf("expected signer error to propagate")
	}
	if chain.submitCalled {
		t.Fatalf("expected SubmitRefPrice not to be called after a signing failure")
	}
}

func TestAttempt_RejectsZeroEpoch(t *testing.T) {
	source := &mockSource{rate: big.NewRat(5, 100), ts: time.Now()}
	chain := &mockChain{status: &rpcclient.RefPriceStatus{Epoch: 0, HasRefPrice: false}}
	svc, err := New(source, []Signer{&mockSigner{sig: fakeSig(1)}, &mockSigner{sig: fakeSig(2)}}, 2, chain, "ZNHB/USD")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, _, err := svc.Attempt(context.Background()); err == nil {
		t.Fatalf("expected an error when the chain reports epoch 0")
	}
}
