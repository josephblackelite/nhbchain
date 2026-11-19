package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-R5T-64: the aggregate day record the top-up writes is rolled back with the rest
// of the mutation, to what it was before (absent, or an earlier total).
func TestPaymasterTopUpAggregateIsRolledBackWithTheMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		preexists *big.Int // aggregate total already on record, nil for none
	}{
		{"no earlier record", nil},
		{"earlier total of 1000", big.NewInt(1_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := newSponsorshipState(t)
			var funding, minter, approver [20]byte
			funding[19], minter[19], approver[19] = 0x01, 0x02, 0x03
			sp.SetPaymasterAutoTopUpPolicy(PaymasterAutoTopUpPolicy{
				Enabled: true, Token: "ZNHB",
				MinBalanceWei: big.NewInt(1_000), TopUpAmountWei: big.NewInt(2_500),
				DailyCapWei: big.NewInt(10_000), AggregateDailyCapWei: big.NewInt(10_000),
				FundingAccount: funding, Minter: minter, Approver: approver,
			})
			at := time.Unix(1_700_000_000, 0).UTC()
			sp.BeginBlock(1, at)
			day := at.Format(nhbstate.PaymasterDayFormat)
			manager := nhbstate.NewManager(sp.Trie)
			if tc.preexists != nil {
				if err := manager.PaymasterPutTopUpDay(&nhbstate.PaymasterTopUpDay{Paymaster: paymasterTopUpAggregateKey, Day: day, DebitedWei: new(big.Int).Set(tc.preexists)}); err != nil {
					t.Fatalf("seed aggregate: %v", err)
				}
			}
			if err := sp.setAccount(funding[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(50_000), Stake: big.NewInt(0)}); err != nil {
				t.Fatalf("seed funding: %v", err)
			}
			sponsor := common.Address{0x09}
			account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
			if err := sp.setAccount(sponsor.Bytes(), account); err != nil {
				t.Fatalf("seed sponsor: %v", err)
			}

			mutation, err := sp.maybeAutoTopUpPaymaster(sponsor, sponsor.Bytes(), account)
			if err != nil || mutation == nil {
				t.Fatalf("top-up did not run: mutation=%v err=%v", mutation, err)
			}
			rec, _, _ := manager.PaymasterGetTopUpDay(paymasterTopUpAggregateKey, day)
			want := big.NewInt(2_500)
			if tc.preexists != nil {
				want = new(big.Int).Add(tc.preexists, big.NewInt(2_500))
			}
			if rec == nil || rec.DebitedWei.Cmp(want) != 0 {
				t.Fatalf("aggregate after the top-up: %#v, want %s", rec, want)
			}

			if err := mutation.Rollback(sp); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			rec, _, _ = manager.PaymasterGetTopUpDay(paymasterTopUpAggregateKey, day)
			switch {
			case tc.preexists == nil && rec != nil:
				t.Fatalf("aggregate record survived the rollback: %#v", rec)
			case tc.preexists != nil && (rec == nil || rec.DebitedWei.Cmp(tc.preexists) != 0):
				t.Fatalf("aggregate after the rollback: %#v, want %s", rec, tc.preexists)
			}
			fundingAcc, _ := sp.getAccount(funding[:])
			if fundingAcc.BalanceZNHB.Cmp(big.NewInt(50_000)) != 0 {
				t.Fatalf("funding not restored: %s", fundingAcc.BalanceZNHB)
			}
		})
	}
}
