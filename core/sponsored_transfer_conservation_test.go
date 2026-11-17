package core

import (
	"fmt"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// A sponsored native transfer moves the value from the sender to the recipient
// and the protocol fee from the sponsor to the fee collector. The fast path
// loads the sender and the recipient once and persists them after the fee has
// been routed, so a sponsor (or a fee collector) that is the sender or the
// recipient has to be that same account object: a second copy loaded from state
// is overwritten by the later write of the first, and the sponsor's debit (or
// the collector's credit) is lost -- NHB is created or destroyed by a transfer
// that applies without error. These tests run the transfer with the sponsor and
// the fee collector on every combination of addresses and check every balance.

const (
	sponsoredTransferValue = 10_000
	sponsoredTransferFee   = 1_000 // 10% of the value, the policy below
	sponsoredTransferStart = 1_000_000
)

// sponsoredTransferRoles names the addresses one transfer can use.
type sponsoredTransferRoles struct {
	sender, recipient, sponsor, collector string
}

func TestSponsoredTransferConservesNHBForEveryRoleAssignment(t *testing.T) {
	// The sponsor can be the sender, the recipient or a wallet of its own; the
	// fee collector can additionally be the sponsor's wallet or a module wallet.
	sponsors := []string{"sender", "recipient", "sponsor"}
	collectors := []string{"sender", "recipient", "sponsor", "collector"}
	for _, sponsor := range sponsors {
		for _, collector := range collectors {
			name := fmt.Sprintf("sponsor=%s collector=%s", sponsor, collector)
			t.Run(name, func(t *testing.T) {
				runSponsoredTransfer(t, sponsoredTransferRoles{sender: "sender", recipient: "recipient", sponsor: sponsor, collector: collector})
			})
		}
	}
}

func runSponsoredTransfer(t *testing.T, roles sponsoredTransferRoles) {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetPaymasterEnabled(true)

	keys := map[string]*crypto.PrivateKey{}
	addrs := map[string][]byte{}
	for _, name := range []string{"sender", "recipient", "sponsor"} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate %s key: %v", name, err)
		}
		keys[name] = key
		addrs[name] = key.PubKey().Address().Bytes()
	}
	addrs["collector"] = make([]byte, 20)
	addrs["collector"][19] = 0x55

	var collector [20]byte
	copy(collector[:], addrs[roles.collector])
	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(0),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
		FeeBps:            1_000,
	})

	// Every distinct address starts with the same balance.
	distinct := map[string][]byte{}
	for _, role := range []string{roles.sender, roles.recipient, roles.sponsor, roles.collector} {
		distinct[string(addrs[role])] = addrs[role]
	}
	for _, addr := range distinct {
		if err := sp.setAccount(addr, &types.Account{BalanceNHB: big.NewInt(sponsoredTransferStart), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	totalNHB := func() *big.Int {
		total := new(big.Int)
		for _, addr := range distinct {
			account, err := sp.getAccount(addr)
			if err != nil {
				t.Fatalf("load account: %v", err)
			}
			total.Add(total, account.BalanceNHB)
		}
		return total
	}
	before := totalNHB()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        append([]byte(nil), addrs[roles.recipient]...),
		Value:     big.NewInt(sponsoredTransferValue),
		GasLimit:  21_000,
		GasPrice:  big.NewInt(1),
		Paymaster: append([]byte(nil), addrs[roles.sponsor]...),
	}
	signPaymaster(t, tx, keys[roles.sponsor])
	signTransaction(t, tx, keys[roles.sender])
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply sponsored transfer: %v", err)
	}

	// Each address ends where its roles put it: the sender paid the value, the
	// recipient received it, the sponsor paid the fee and the collector got it.
	want := map[string]*big.Int{}
	for key := range distinct {
		want[key] = big.NewInt(sponsoredTransferStart)
	}
	move := func(role string, delta int64) {
		want[string(addrs[role])].Add(want[string(addrs[role])], big.NewInt(delta))
	}
	move(roles.sender, -sponsoredTransferValue)
	move(roles.recipient, sponsoredTransferValue)
	move(roles.sponsor, -sponsoredTransferFee)
	move(roles.collector, sponsoredTransferFee)
	for key, addr := range distinct {
		account, err := sp.getAccount(addr)
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		if account.BalanceNHB.Cmp(want[key]) != 0 {
			t.Fatalf("balance of %x = %s, want %s", addr[:4], account.BalanceNHB, want[key])
		}
	}
	if after := totalNHB(); after.Cmp(before) != 0 {
		t.Fatalf("total NHB across the touched accounts changed from %s to %s", before, after)
	}
}
