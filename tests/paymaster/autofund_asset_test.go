package paymaster

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// A paymaster's sponsored gas is paid in NHB, so an automatic top-up has to
// watch, debit and credit NHB unless the policy names another asset. These
// tests drive a sponsored transfer through the real transaction path.

type topUpCase struct {
	sp            *core.StateProcessor
	manager       *nhbstate.Manager
	actors        autoTopUpActors
	policy        core.PaymasterAutoTopUpPolicy
	paymasterKey  *crypto.PrivateKey
	paymasterAddr crypto.Address
	senderKey     *crypto.PrivateKey
	senderAddr    crypto.Address
}

// newTopUpCase builds a processor with a policy on token, a funding account, a
// paymaster and a sender. Balances are seeded by the caller.
func newTopUpCase(t *testing.T, token string, amount *big.Int) *topUpCase {
	t.Helper()
	sp, manager, actors, policy, _ := newFeeTestSetup(t, amount, big.NewInt(1_000_000))
	policy.Token = token
	sp.SetPaymasterAutoTopUpPolicy(policy)
	sp.SetPaymasterEnabled(true)

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	return &topUpCase{
		sp: sp, manager: manager, actors: actors, policy: policy,
		paymasterKey: paymasterKey, paymasterAddr: paymasterKey.PubKey().Address(),
		senderKey: senderKey, senderAddr: senderKey.PubKey().Address(),
	}
}

func (c *topUpCase) setFundingAccount(t *testing.T, account [20]byte) {
	t.Helper()
	c.policy.FundingAccount = account
	c.sp.SetPaymasterAutoTopUpPolicy(c.policy)
}

func (c *topUpCase) seed(t *testing.T, addr crypto.Address, nhb, znhb int64) {
	t.Helper()
	putAccountBalances(t, c.sp, addr, big.NewInt(nhb), big.NewInt(znhb))
}

func (c *topUpCase) balances(t *testing.T, addr []byte) (nhb, znhb *big.Int) {
	t.Helper()
	account, err := c.sp.GetAccount(addr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	nhb, znhb = big.NewInt(0), big.NewInt(0)
	if account.BalanceNHB != nil {
		nhb = account.BalanceNHB
	}
	if account.BalanceZNHB != nil {
		znhb = account.BalanceZNHB
	}
	return nhb, znhb
}

func (c *topUpCase) requireBalances(t *testing.T, name string, addr []byte, wantNHB, wantZNHB int64) {
	t.Helper()
	nhb, znhb := c.balances(t, addr)
	if nhb.Cmp(big.NewInt(wantNHB)) != 0 || znhb.Cmp(big.NewInt(wantZNHB)) != 0 {
		t.Fatalf("%s: NHB %s ZNHB %s, want NHB %d ZNHB %d", name, nhb, znhb, wantNHB, wantZNHB)
	}
}

// total sums both assets over the listed accounts.
func (c *topUpCase) total(t *testing.T, addrs ...[]byte) (nhb, znhb *big.Int) {
	t.Helper()
	nhb, znhb = big.NewInt(0), big.NewInt(0)
	seen := map[string]bool{}
	for _, addr := range addrs {
		if seen[string(addr)] {
			continue
		}
		seen[string(addr)] = true
		n, z := c.balances(t, addr)
		nhb.Add(nhb, n)
		znhb.Add(znhb, z)
	}
	return nhb, znhb
}

// sponsoredTransfer builds the transfer of 1 NHB from the sender to `to`,
// sponsored by the paymaster.
func (c *topUpCase) sponsoredTransfer(t *testing.T, to []byte) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        to,
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: c.paymasterAddr.Bytes(),
	}
	if err := tx.Sign(c.senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, c.paymasterKey)
	return tx
}

// apply runs tx in a block and returns the auto top-up event, if any.
func (c *topUpCase) apply(t *testing.T, tx *types.Transaction) *types.Event {
	t.Helper()
	c.sp.BeginBlock(1, time.Unix(1_700_700_000, 0).UTC())
	defer c.sp.EndBlock()
	assessment, err := c.sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment.Status != core.SponsorshipStatusReady {
		t.Fatalf("expected a ready sponsorship, got %s (%s)", assessment.Status, assessment.Reason)
	}
	if err := c.sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply sponsored transfer: %v", err)
	}
	return findEventByType(c.sp.Events(), events.TypePaymasterAutoTopUp)
}

// The default policy refills NHB. A sponsor short of NHB used to be missed by
// a top-up that only looked at its ZNHB balance.
func TestPaymasterAutoTopUpRefillsTheAssetGasIsPaidIn(t *testing.T) {
	for _, token := range []string{"", "NHB"} {
		t.Run("token="+token, func(t *testing.T) {
			c := newTopUpCase(t, token, big.NewInt(2_500))
			funding := c.actors.fundingAddr
			c.seed(t, funding, 25_000, 30_000)
			c.seed(t, c.paymasterAddr, 200, 5_000) // NHB below the 1000 floor, ZNHB well above it
			c.seed(t, c.senderAddr, 1, 0)

			evt := c.apply(t, c.sponsoredTransfer(t, common.Address{0x21}.Bytes()))
			if evt == nil || evt.Attributes["status"] != "success" {
				t.Fatalf("expected a successful top-up, got %#v", evt)
			}
			if evt.Attributes["token"] != "NHB" {
				t.Fatalf("top-up event names token %q, want NHB", evt.Attributes["token"])
			}
			c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 2_700, 5_000)
			c.requireBalances(t, "funding", funding.Bytes(), 22_500, 30_000)
		})
	}
}

// The asset that is not sponsored gas is not watched: a paymaster that is
// short of ZNHB but has NHB to spare is left alone.
func TestPaymasterAutoTopUpDoesNotWatchTheOtherAsset(t *testing.T) {
	c := newTopUpCase(t, "", big.NewInt(2_500))
	funding := c.actors.fundingAddr
	c.seed(t, funding, 25_000, 30_000)
	c.seed(t, c.paymasterAddr, 5_000, 0) // ZNHB below the floor, NHB above it
	c.seed(t, c.senderAddr, 1, 0)

	if evt := c.apply(t, c.sponsoredTransfer(t, common.Address{0x22}.Bytes())); evt != nil {
		t.Fatalf("unexpected auto top-up event: %#v", evt.Attributes)
	}
	c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 5_000, 0)
	c.requireBalances(t, "funding", funding.Bytes(), 25_000, 30_000)
}

// A policy that names ZNHB still works, all in ZNHB: check, debit and credit.
func TestPaymasterAutoTopUpZNHBPolicyStaysInZNHB(t *testing.T) {
	c := newTopUpCase(t, "ZNHB", big.NewInt(2_500))
	funding := c.actors.fundingAddr
	c.seed(t, funding, 25_000, 30_000)
	c.seed(t, c.paymasterAddr, 5_000, 200) // ZNHB below the floor, NHB above it
	c.seed(t, c.senderAddr, 1, 0)

	if evt := c.apply(t, c.sponsoredTransfer(t, common.Address{0x23}.Bytes())); evt == nil || evt.Attributes["status"] != "success" {
		t.Fatalf("expected a successful top-up, got %#v", evt)
	}
	c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 5_000, 2_700)
	c.requireBalances(t, "funding", funding.Bytes(), 25_000, 27_500)
}

// The governed fee is taken in the same asset, and what leaves the funding
// account is exactly what reaches the paymaster and the fee treasury.
func TestPaymasterAutoTopUpFeeIsTakenInTheTopUpAsset(t *testing.T) {
	const amount, fee = 2_500, 100
	c := newTopUpCase(t, "", big.NewInt(amount))
	if err := c.manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte("100")); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}
	var treasury [20]byte
	treasury[19] = 0x77 // the treasury newFeeTestSetup wires in
	funding := c.actors.fundingAddr
	c.seed(t, funding, 25_000, 30_000)
	c.seed(t, c.paymasterAddr, 200, 5_000)
	c.seed(t, c.senderAddr, 1, 0)
	nhbBefore, znhbBefore := c.total(t, funding.Bytes(), c.paymasterAddr.Bytes(), treasury[:], c.senderAddr.Bytes())

	if evt := c.apply(t, c.sponsoredTransfer(t, common.Address{0x24}.Bytes())); evt == nil || evt.Attributes["status"] != "success" {
		t.Fatalf("expected a successful top-up, got %#v", evt)
	}
	c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 200+amount, 5_000)
	c.requireBalances(t, "funding", funding.Bytes(), 25_000-amount-fee, 30_000)
	c.requireBalances(t, "treasury", treasury[:], fee, 0)
	nhbAfter, znhbAfter := c.total(t, funding.Bytes(), c.paymasterAddr.Bytes(), treasury[:], c.senderAddr.Bytes())
	// The 1 NHB the sender transferred went to an address outside the set.
	if want := new(big.Int).Sub(nhbBefore, big.NewInt(1)); nhbAfter.Cmp(want) != 0 {
		t.Fatalf("NHB across the touched accounts = %s, want %s", nhbAfter, want)
	}
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB across the touched accounts changed from %s to %s", znhbBefore, znhbAfter)
	}
}

// Two of the top-up's roles can be one address. Whatever the assignment, what
// is debited equals what is credited (no asset is created or destroyed), or the
// top-up refuses to run.
func TestPaymasterAutoTopUpRoleCollisionsConserveValue(t *testing.T) {
	const amount, fee = 2_500, 100
	newCase := func(t *testing.T) *topUpCase {
		c := newTopUpCase(t, "", big.NewInt(amount))
		if err := c.manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte("100")); err != nil {
			t.Fatalf("set governed fee: %v", err)
		}
		return c
	}
	other := common.Address{0x25}.Bytes()

	t.Run("fee treasury is the funding account", func(t *testing.T) {
		c := newCase(t)
		funding := c.actors.fundingAddr
		c.sp.SetEscrowFeeTreasury(c.actors.fundingBytes)
		c.seed(t, funding, 25_000, 0)
		c.seed(t, c.paymasterAddr, 200, 0)
		c.seed(t, c.senderAddr, 1, 0)
		if evt := c.apply(t, c.sponsoredTransfer(t, other)); evt == nil || evt.Attributes["status"] != "success" {
			t.Fatalf("expected a successful top-up, got %#v", evt)
		}
		// The fee returns to where it came from: the funding account pays the amount only.
		c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 200+amount, 0)
		c.requireBalances(t, "funding", funding.Bytes(), 25_000-amount, 0)
	})

	t.Run("fee treasury is the paymaster", func(t *testing.T) {
		c := newCase(t)
		funding := c.actors.fundingAddr
		var paymasterRaw [20]byte
		copy(paymasterRaw[:], c.paymasterAddr.Bytes())
		c.sp.SetEscrowFeeTreasury(paymasterRaw)
		c.seed(t, funding, 25_000, 0)
		c.seed(t, c.paymasterAddr, 200, 0)
		c.seed(t, c.senderAddr, 1, 0)
		if evt := c.apply(t, c.sponsoredTransfer(t, other)); evt == nil || evt.Attributes["status"] != "success" {
			t.Fatalf("expected a successful top-up, got %#v", evt)
		}
		// The paymaster is also the fee treasury: it receives the amount and the fee,
		// and the funding account pays both.
		c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 200+amount+fee, 0)
		c.requireBalances(t, "funding", funding.Bytes(), 25_000-amount-fee, 0)
	})

	t.Run("funding account is the paymaster", func(t *testing.T) {
		c := newCase(t)
		var paymasterRaw [20]byte
		copy(paymasterRaw[:], c.paymasterAddr.Bytes())
		c.setFundingAccount(t, paymasterRaw)
		c.seed(t, c.paymasterAddr, 200, 0)
		c.seed(t, c.senderAddr, 1, 0)
		evt := c.apply(t, c.sponsoredTransfer(t, other))
		if evt == nil || evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "funding_account_in_use" {
			t.Fatalf("expected the top-up to refuse a funding account that is the paymaster, got %#v", evt)
		}
		c.requireBalances(t, "paymaster", c.paymasterAddr.Bytes(), 200, 0)
	})

	t.Run("funding account is the recipient of the sponsored transfer", func(t *testing.T) {
		c := newCase(t)
		funding := c.actors.fundingAddr
		c.seed(t, funding, 25_000, 0)
		c.seed(t, c.paymasterAddr, 200, 0)
		c.seed(t, c.senderAddr, 1, 0)
		nhbBefore, _ := c.total(t, funding.Bytes(), c.paymasterAddr.Bytes(), c.senderAddr.Bytes())
		evt := c.apply(t, c.sponsoredTransfer(t, funding.Bytes()))
		if evt == nil || evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "funding_account_in_use" {
			t.Fatalf("expected the top-up to refuse a funding account the transfer pays, got %#v", evt)
		}
		// The transfer itself went through, and nothing was created or destroyed.
		c.requireBalances(t, "funding", funding.Bytes(), 25_001, 0)
		c.requireBalances(t, "sender", c.senderAddr.Bytes(), 0, 0)
		nhbAfter, _ := c.total(t, funding.Bytes(), c.paymasterAddr.Bytes(), c.senderAddr.Bytes())
		if nhbAfter.Cmp(nhbBefore) != 0 {
			t.Fatalf("NHB across the touched accounts changed from %s to %s", nhbBefore, nhbAfter)
		}
	})

	t.Run("fee treasury is the recipient of the sponsored transfer", func(t *testing.T) {
		c := newCase(t)
		funding := c.actors.fundingAddr
		var recipient [20]byte
		copy(recipient[:], other)
		c.sp.SetEscrowFeeTreasury(recipient)
		c.seed(t, funding, 25_000, 0)
		c.seed(t, c.paymasterAddr, 200, 0)
		c.seed(t, c.senderAddr, 1, 0)
		evt := c.apply(t, c.sponsoredTransfer(t, other))
		if evt == nil || evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "treasury_account_in_use" {
			t.Fatalf("expected the top-up to refuse a fee treasury the transfer pays, got %#v", evt)
		}
		c.requireBalances(t, "funding", funding.Bytes(), 25_000, 0)
		c.requireBalances(t, "recipient", other, 1, 0)
	})
}
