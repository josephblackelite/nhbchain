package pos

import (
	"math/big"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	"nhbchain/observability"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

type eventForwarder struct {
	sp *core.StateProcessor
}

func (e eventForwarder) Emit(evt events.Event) {
	if e.sp == nil || evt == nil {
		return
	}
	if provider, ok := evt.(interface{ Event() *types.Event }); ok {
		if payload := provider.Event(); payload != nil {
			e.sp.AppendEvent(payload)
		}
		return
	}
	e.sp.AppendEvent(&types.Event{Type: evt.EventType(), Attributes: map[string]string{}})
}

func newStateProcessor(t *testing.T) *core.StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := core.NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func TestPOSSweepVoids(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer: %v", err)
	}
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant: %v", err)
	}
	payerAddr := payerKey.PubKey().Address()
	merchantAddr := merchantKey.PubKey().Address()

	initialBalance := big.NewInt(25_000)
	if err := manager.PutAccount(payerAddr.Bytes(), &types.Account{
		BalanceNHB:        big.NewInt(0),
		BalanceZNHB:       new(big.Int).Set(initialBalance),
		LockedZNHB:        big.NewInt(0),
		Stake:             big.NewInt(0),
		StakeShares:       big.NewInt(0),
		CollateralBalance: big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed payer account: %v", err)
	}
	if err := manager.PutAccount(merchantAddr.Bytes(), &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed merchant account: %v", err)
	}

	lifecycle := pos.NewLifecycle(manager)
	lifecycle.SetEmitter(eventForwarder{sp: sp})

	baseTime := time.Unix(1_700_000_000, 0).UTC()
	lifecycle.SetNowFunc(func() time.Time { return baseTime })

	amount := big.NewInt(5_000)
	expiry := uint64(baseTime.Add(time.Hour).Unix())
	auth, err := lifecycle.Authorize(bytes20(payerAddr), bytes20(merchantAddr), amount, expiry, nil)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}

	sweepTime := baseTime.Add(2 * time.Hour)
	metrics := observability.POSLifecycle()
	before := testutil.ToFloat64(metrics.AuthExpiredCounter())
	count, err := sp.SweepExpiredPOSAuthorizations(sweepTime)
	if err != nil {
		t.Fatalf("sweep expired: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 voided authorization, got %d", count)
	}
	after := testutil.ToFloat64(metrics.AuthExpiredCounter())
	if diff := after - before; diff != 1 {
		t.Fatalf("expected metric increment of 1, got %f", diff)
	}

	account, err := sp.GetAccount(payerAddr.Bytes())
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if account.LockedZNHB.Sign() != 0 {
		t.Fatalf("expected locked balance 0, got %s", account.LockedZNHB)
	}
	if account.BalanceZNHB.Cmp(initialBalance) != 0 {
		t.Fatalf("expected balance %s, got %s", initialBalance, account.BalanceZNHB)
	}

	lifecycle.SetNowFunc(func() time.Time { return sweepTime })
	snapshot, err := lifecycle.Void(auth.ID, "")
	if err != nil {
		t.Fatalf("load authorization: %v", err)
	}
	if snapshot.Status != pos.AuthorizationStatusExpired {
		t.Fatalf("expected status expired, got %v", snapshot.Status)
	}
	if snapshot.RefundedAmount == nil || snapshot.RefundedAmount.Cmp(amount) != 0 {
		t.Fatalf("expected refunded amount %s, got %v", amount, snapshot.RefundedAmount)
	}

	emitted := sp.Events()
	if len(emitted) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(emitted))
	}
	last := emitted[len(emitted)-1]
	if last.Type != events.TypePosAuthAutoVoided {
		t.Fatalf("expected last event %s, got %s", events.TypePosAuthAutoVoided, last.Type)
	}
	if last.Attributes["authorizationId"] == "" || last.Attributes["amount"] != amount.String() {
		t.Fatalf("unexpected event payload: %#v", last.Attributes)
	}
}

func bytes20(addr crypto.Address) [20]byte {
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}
