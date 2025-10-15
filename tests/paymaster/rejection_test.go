package paymaster

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/core"
	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

type accountSnapshot struct {
	nonce      uint64
	balanceNHB *big.Int
}

func snapshotAccount(t *testing.T, sp *core.StateProcessor, addr []byte) accountSnapshot {
	t.Helper()
	acc, err := sp.GetAccount(addr)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	snap := accountSnapshot{}
	if acc != nil {
		snap.nonce = acc.Nonce
		if acc.BalanceNHB != nil {
			snap.balanceNHB = new(big.Int).Set(acc.BalanceNHB)
		}
	}
	return snap
}

func (s accountSnapshot) equal(other accountSnapshot) bool {
	if s.nonce != other.nonce {
		return false
	}
	switch {
	case s.balanceNHB == nil && other.balanceNHB == nil:
		return true
	case s.balanceNHB == nil || other.balanceNHB == nil:
		return false
	default:
		return s.balanceNHB.Cmp(other.balanceNHB) == 0
	}
}

func TestSponsoredTransactionRejectionDoesNotMutateState(t *testing.T) {
	t.Run("invalid signature", func(t *testing.T) {
		sp := newStateProcessor(t)
		sp.SetPaymasterEnabled(true)
		sp.BeginBlock(1, time.Unix(10, 0).UTC())

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		senderAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.PutAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("put sender account: %v", err)
		}
		paymasterAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.PutAccount(paymasterAddr, paymasterAccount); err != nil {
			t.Fatalf("put paymaster account: %v", err)
		}

		tx := &types.Transaction{
			ChainID:   types.NHBChainID(),
			Type:      types.TxTypeTransfer,
			Nonce:     0,
			To:        append([]byte(nil), common.Address{0x11}.Bytes()...),
			Value:     big.NewInt(1),
			GasLimit:  21_000,
			GasPrice:  big.NewInt(1),
			Paymaster: append([]byte(nil), paymasterAddr...),
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign sender: %v", err)
		}

		rootBefore := sp.Trie.Hash()
		senderBefore := snapshotAccount(t, sp, senderAddr)
		paymasterBefore := snapshotAccount(t, sp, paymasterAddr)

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, core.ErrSponsorshipRejected) {
			t.Fatalf("expected ErrSponsorshipRejected, got %v", err)
		}

		if rootAfter := sp.Trie.Hash(); rootAfter != rootBefore {
			t.Fatalf("expected trie root unchanged, got %s vs %s", rootAfter.Hex(), rootBefore.Hex())
		}
		if senderAfter := snapshotAccount(t, sp, senderAddr); !senderAfter.equal(senderBefore) {
			t.Fatalf("sender mutated: before=%+v after=%+v", senderBefore, senderAfter)
		}
		if paymasterAfter := snapshotAccount(t, sp, paymasterAddr); !paymasterAfter.equal(paymasterBefore) {
			t.Fatalf("paymaster mutated: before=%+v after=%+v", paymasterBefore, paymasterAfter)
		}

		eventsList := sp.Events()
		if len(eventsList) == 0 {
			t.Fatalf("expected sponsorship failure event")
		}
		failure := eventsList[len(eventsList)-1]
		if failure.Type != events.TypeTxSponsorshipFailed {
			t.Fatalf("expected failure event, got %s", failure.Type)
		}
		if status := failure.Attributes["status"]; status != string(core.SponsorshipStatusSignatureMissing) {
			t.Fatalf("expected signature missing status, got %s", status)
		}
		for _, evt := range eventsList {
			if evt.Type == events.TypeFeeApplied {
				t.Fatalf("unexpected fee event: %#v", evt)
			}
		}
	})

	t.Run("throttled sponsorship", func(t *testing.T) {
		sp := newStateProcessor(t)
		sp.SetPaymasterEnabled(true)
		sp.SetPaymasterLimits(core.PaymasterLimits{DeviceDailyTxCap: 1})
		sp.BeginBlock(1, time.Unix(20, 0).UTC())

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		senderAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.PutAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("put sender account: %v", err)
		}
		paymasterAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.PutAccount(paymasterAddr, paymasterAccount); err != nil {
			t.Fatalf("put paymaster account: %v", err)
		}

		tx := &types.Transaction{
			ChainID:         types.NHBChainID(),
			Type:            types.TxTypeTransfer,
			Nonce:           0,
			To:              append([]byte(nil), common.Address{0x22}.Bytes()...),
			Value:           big.NewInt(1),
			GasLimit:        21_000,
			GasPrice:        big.NewInt(1),
			Paymaster:       append([]byte(nil), paymasterAddr...),
			MerchantAddress: "merchant-1",
		}
		if err := tx.Sign(senderKey.PrivateKey); err != nil {
			t.Fatalf("sign sender: %v", err)
		}
		signPaymaster(t, tx, paymasterKey)

		rootBefore := sp.Trie.Hash()
		senderBefore := snapshotAccount(t, sp, senderAddr)
		paymasterBefore := snapshotAccount(t, sp, paymasterAddr)

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, core.ErrSponsorshipRejected) {
			t.Fatalf("expected ErrSponsorshipRejected, got %v", err)
		}

		if rootAfter := sp.Trie.Hash(); rootAfter != rootBefore {
			t.Fatalf("expected trie root unchanged, got %s vs %s", rootAfter.Hex(), rootBefore.Hex())
		}
		if senderAfter := snapshotAccount(t, sp, senderAddr); !senderAfter.equal(senderBefore) {
			t.Fatalf("sender mutated: before=%+v after=%+v", senderBefore, senderAfter)
		}
		if paymasterAfter := snapshotAccount(t, sp, paymasterAddr); !paymasterAfter.equal(paymasterBefore) {
			t.Fatalf("paymaster mutated: before=%+v after=%+v", paymasterBefore, paymasterAfter)
		}

		eventsList := sp.Events()
		if len(eventsList) < 2 {
			t.Fatalf("expected failure and throttle events, got %#v", eventsList)
		}
		failure := eventsList[len(eventsList)-2]
		throttle := eventsList[len(eventsList)-1]
		if failure.Type != events.TypeTxSponsorshipFailed {
			t.Fatalf("expected failure event, got %s", failure.Type)
		}
		if status := failure.Attributes["status"]; status != string(core.SponsorshipStatusThrottled) {
			t.Fatalf("expected throttled status, got %s", status)
		}
		if throttle.Type != events.TypePaymasterThrottled {
			t.Fatalf("expected throttle event, got %s", throttle.Type)
		}
		for _, evt := range eventsList {
			if evt.Type == events.TypeFeeApplied {
				t.Fatalf("unexpected fee event: %#v", evt)
			}
		}
	})
}
