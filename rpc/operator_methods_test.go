package rpc

import (
	"strings"
	"testing"
)

// TestIsOperatorOnlyMethodKnownEntries spot-checks a representative sample of
// OperatorOnlyMethods (one from each category in the table's comments) plus
// a representative method under each OperatorOnlyMethodPrefixes namespace,
// and confirms a handful of ordinary, non-operator methods are NOT caught by
// either the exact-match table or an accidental prefix match.
func TestIsOperatorOnlyMethodKnownEntries(t *testing.T) {
	operatorOnly := []string{
		"net_ban",                  // p2p control
		"sync_snapshot_import",     // node-internal sync
		"swap_setManualQuote",      // swap admin
		"buyback_submitRefPrice",   // oracle ref price
		"tx_setSponsorshipEnabled", // paymaster
		"potso_reward_claim",       // POTSO treasury payout
		"nhb_getOwnerWalletStats",  // admin analytics
		"escrow_milestoneCreate",   // escrow milestone writer
		"stake_delegate",           // legacy direct-state mutator
		"gov_finalize",             // removed governance writer
		// prefix-only matches (not individually listed, caught by namespace)
		"admin_shutdown",
		"debug_pprof",
		"operator_restart",
		"personal_sign",
		"miner_start",
	}
	for _, method := range operatorOnly {
		if !IsOperatorOnlyMethod(method) {
			t.Errorf("expected %q to be operator-only", method)
		}
	}

	notOperatorOnly := []string{
		"nhb_getBalance",
		"nhb_sendTransaction",
		"gov_list",
		"escrow_get",
		"escrow_getRealm",
		"nhb_getSwapQuote",
		"swap_getRiskParams",
		"potso_userMeters",
		"",
	}
	for _, method := range notOperatorOnly {
		if IsOperatorOnlyMethod(method) {
			t.Errorf("expected %q to NOT be operator-only", method)
		}
	}
}

// Characters used to build decorated/evasive method name variants below.
// Built from their code points rather than embedded literally so the source
// file never contains a raw byte-order-mark or other invisible character --
// Go's lexer rejects a literal U+FEFF anywhere outside the very start of the
// file, and burying zero-width characters directly in source is illegible
// and easy to corrupt by accident anyway.
var (
	zeroWidthSpace       = string(rune(0x200B)) // ZERO WIDTH SPACE (Cf)
	byteOrderMark        = string(rune(0xFEFF)) // ZERO WIDTH NO-BREAK SPACE / BOM (Cf)
	variationSelector16  = string(rune(0xFE0F)) // VARIATION SELECTOR-16
	mongolianVowelSepar  = string(rune(0x180E)) // MONGOLIAN VOWEL SEPARATOR
	nonBreakingSpaceChar = string(rune(0x00A0)) // NO-BREAK SPACE (Zs)
)

// toFullwidth converts each ASCII letter or underscore in s to its Unicode
// "fullwidth" compatibility form (the U+FF00 block) -- the kind of lookalike
// character NFKC compatibility folding exists to normalize away, and a
// classic homoglyph trick for slipping a denied name past a naive
// exact-match filter.
func toFullwidth(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(rune(0xFF21 + (r - 'A')))
		case r >= 'a' && r <= 'z':
			b.WriteRune(rune(0xFF41 + (r - 'a')))
		case r == '_':
			b.WriteRune(rune(0xFF3F))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestNormalizeRPCMethodNameAntiEvasion proves the ported normalization
// catches the same decorated/evasive spellings nhbportal's
// normalizeRpcMethodName (src/routes/api/rpc/operatorMethods.ts) does: NFKC
// compatibility folding, stripped control/format/separator characters
// (including the two characters outside Unicode's main whitespace blocks --
// U+180E and the variation-selector range -- that it also strips explicitly),
// and lowercasing. Each variant below must still resolve to an operator-only
// method even though none of them are themselves literal entries in
// OperatorOnlyMethods.
func TestNormalizeRPCMethodNameAntiEvasion(t *testing.T) {
	cases := []struct {
		name   string
		method string
	}{
		{"zero-width space injected mid-name", "net_" + zeroWidthSpace + "ban"},
		{"BOM / zero-width no-break space prefix", byteOrderMark + "net_ban"},
		{"trailing variation selector", "net_ban" + variationSelector16},
		{"Mongolian vowel separator injected", "net_ba" + mongolianVowelSepar + "n"},
		{"non-breaking space suffix", "net_ban" + nonBreakingSpaceChar},
		{"re-cased", "NET_BAN"},
		{"mixed case with padding", "  Net_Ban  "},
		{"fullwidth Latin letters (NFKC compatibility fold)", toFullwidth("NET_BAN")},
		{"fullwidth plus zero-width plus re-case", toFullwidth("net") + "_" + zeroWidthSpace + "ban"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeRPCMethodName(tc.method); got != "net_ban" {
				t.Fatalf("NormalizeRPCMethodName(%q) = %q, want %q", tc.method, got, "net_ban")
			}
			if !IsOperatorOnlyMethod(tc.method) {
				t.Fatalf("IsOperatorOnlyMethod(%q) = false, want true (decorated variant of a denied method)", tc.method)
			}
		})
	}
}

// TestNormalizeRPCMethodNameDoesNotOverMatch proves the same stripping rules
// do not make an unrelated, non-operator method name collide with an
// operator-only one -- decoration alone must not flip the verdict.
func TestNormalizeRPCMethodNameDoesNotOverMatch(t *testing.T) {
	decoratedPublicMethods := []string{
		"nhb" + zeroWidthSpace + "_getBalance",
		byteOrderMark + "nhb_getBalance",
		"NHB_GETBALANCE",
		"nhb_getBalance" + variationSelector16,
	}
	for _, method := range decoratedPublicMethods {
		if IsOperatorOnlyMethod(method) {
			t.Errorf("decorated public method %q must not be classified operator-only", method)
		}
	}
}
