package escrow

import (
	"fmt"
	"math/big"
	"testing"

	"nhbchain/core/types"
)

// escrowCollisionPartitions returns every way n roles can be split into groups
// of roles that share one address, as role -> group index slices (restricted
// growth strings: [0 0 1] is "roles 0 and 1 share an address, role 2 has its
// own"). n=4 yields 15 partitions.
func escrowCollisionPartitions(n int) [][]int {
	var out [][]int
	var rec func(cur []int, max int)
	rec = func(cur []int, max int) {
		if len(cur) == n {
			out = append(out, append([]int(nil), cur...))
			return
		}
		for g := 0; g <= max+1; g++ {
			next := max
			if g > max {
				next = g
			}
			rec(append(cur, g), next)
		}
	}
	rec(nil, -1)
	return out
}

func tokenBalance(acc *types.Account, token string) *big.Int {
	if token == "ZNHB" {
		return acc.BalanceZNHB
	}
	return acc.BalanceNHB
}

// Escrow funds move payer -> vault on funding and vault -> payee/fee treasury
// on release (or vault -> payer on refund and expiry). The payee, the fee
// treasury and the payer are chosen by the escrow's creator or by
// configuration, so any of them can be the module vault address itself, and
// they can coincide with each other. Whichever of those four roles share an
// address (all 15 combinations), each transfer leg must move exactly its
// amount and the total of the token across the touched addresses must not
// change. A leg whose two ends were one address used to load two copies of the
// account and credit the amount from nothing.
func TestEscrowRoleCollisionsConserveValue(t *testing.T) {
	const (
		amount   = 1_000
		feeBps   = 250
		fee      = 25
		deadline = 1_700_002_000
	)
	// roles: 0 payer, 1 payee, 2 fee treasury, 3 module vault.
	for _, token := range []string{"NHB", "ZNHB"} {
		for _, roles := range escrowCollisionPartitions(4) {
			for _, path := range []string{"release", "refund", "expire"} {
				token, roles, path := token, roles, path
				t.Run(fmt.Sprintf("%s/%s/payer=%d payee=%d treasury=%d vault=%d", token, path, roles[0], roles[1], roles[2], roles[3]), func(t *testing.T) {
					state := newMockState()
					engine := NewEngine()
					engine.SetState(state)
					engine.SetNowFunc(func() int64 { return 1_700_000_000 })

					vault, err := state.EscrowVaultAddress(token)
					if err != nil {
						t.Fatalf("vault address: %v", err)
					}
					addrs := map[int][20]byte{roles[3]: vault}
					for _, group := range roles {
						if _, ok := addrs[group]; !ok {
							addrs[group] = newTestAddress(byte(0x50 + group))
						}
					}
					payer, payee, treasury := addrs[roles[0]], addrs[roles[1]], addrs[roles[2]]
					engine.SetFeeTreasury(treasury)

					initial := map[[20]byte][2]*big.Int{}
					for group, addr := range addrs {
						nhb := big.NewInt(100_000 + int64(group)*1_000)
						znhb := big.NewInt(200_000 + int64(group)*1_000)
						initial[addr] = [2]*big.Int{nhb, znhb}
						state.setAccount(addr, &types.Account{BalanceNHB: new(big.Int).Set(nhb), BalanceZNHB: new(big.Int).Set(znhb), Stake: big.NewInt(0)})
					}

					esc, err := engine.Create(payer, payee, token, big.NewInt(amount), feeBps, deadline, 1, nil, [32]byte{}, "")
					if err != nil {
						t.Fatalf("create: %v", err)
					}
					if err := engine.Fund(esc.ID, payer); err != nil {
						t.Fatalf("fund: %v", err)
					}
					switch path {
					case "release":
						err = engine.Release(esc.ID, payee)
					case "refund":
						err = engine.Refund(esc.ID, payer)
					case "expire":
						err = engine.Expire(esc.ID, deadline+1)
					}
					if err != nil {
						t.Fatalf("%s: %v", path, err)
					}

					idx := 0
					if token == "ZNHB" {
						idx = 1
					}
					expected := map[[20]byte]*big.Int{}
					for addr, balances := range initial {
						expected[addr] = new(big.Int).Set(balances[idx])
					}
					// Funding: payer -> vault.
					expected[payer].Sub(expected[payer], big.NewInt(amount))
					expected[vault].Add(expected[vault], big.NewInt(amount))
					if path == "release" {
						expected[vault].Sub(expected[vault], big.NewInt(amount))
						expected[payee].Add(expected[payee], big.NewInt(amount-fee))
						expected[treasury].Add(expected[treasury], big.NewInt(fee))
					} else {
						expected[vault].Sub(expected[vault], big.NewInt(amount))
						expected[payer].Add(expected[payer], big.NewInt(amount))
					}

					totalBefore, totalAfter := new(big.Int), new(big.Int)
					for addr, balances := range initial {
						acc := state.account(addr)
						if got := tokenBalance(acc, token); got.Cmp(expected[addr]) != 0 {
							t.Errorf("address %x: %s balance %s, want %s", addr[0], token, got, expected[addr])
						}
						other := "NHB"
						if token == "NHB" {
							other = "ZNHB"
						}
						if got, want := tokenBalance(acc, other), balances[1-idx]; got.Cmp(want) != 0 {
							t.Errorf("address %x: %s balance %s changed from %s", addr[0], other, got, want)
						}
						totalBefore.Add(totalBefore, balances[idx])
						totalAfter.Add(totalAfter, tokenBalance(acc, token))
					}
					if totalBefore.Cmp(totalAfter) != 0 {
						t.Fatalf("total %s across the touched addresses changed from %s to %s", token, totalBefore, totalAfter)
					}
					if remaining, err := state.EscrowBalance(esc.ID, token); err != nil || remaining.Sign() != 0 {
						t.Fatalf("the escrow's own ledger must be empty after %s: balance=%v err=%v", path, remaining, err)
					}
				})
			}
		}
	}
}
