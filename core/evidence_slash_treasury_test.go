package core

import (
	"bytes"
	"testing"
)

// PL-R5T-95: a slash's forfeited stake goes to the admin wallet when the chain
// has one (as the old treasury address was), and to a fixed derived module
// address otherwise. No transaction, operator or config picks the destination.
func TestEvidenceSlashTreasuryIsTheAdminWalletWhenThereIsOne(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var admin [20]byte
	admin[0] = 0xAD
	sp.SetAdminWallet(admin, true)
	if got := sp.evidenceSlashTreasury(); got != admin {
		t.Fatalf("with an admin wallet the slash destination is %x, want the admin wallet %x", got, admin)
	}

	var other [20]byte
	other[0] = 0xBE
	sp.SetAdminWallet(other, true)
	if got := sp.evidenceSlashTreasury(); got != other {
		t.Fatalf("after the admin wallet changed the slash destination is %x, want %x", got, other)
	}
}

func TestEvidenceSlashTreasuryFallsBackToAFixedModuleAddress(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetAdminWallet([20]byte{}, false)

	first := sp.evidenceSlashTreasury()
	if first == ([20]byte{}) {
		t.Fatalf("without an admin wallet the slash destination must not be the zero address")
	}
	if second := sp.evidenceSlashTreasury(); !bytes.Equal(first[:], second[:]) {
		t.Fatalf("the fallback slash destination changed between calls: %x then %x", first, second)
	}

	var admin [20]byte
	admin[0] = 0xAD
	sp.SetAdminWallet(admin, true)
	if got := sp.evidenceSlashTreasury(); got == first {
		t.Fatalf("the fallback address %x must differ from a real admin wallet", first)
	}
}
