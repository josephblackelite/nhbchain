package common

import (
	"math/big"
	"strconv"
	"strings"
	"testing"
)

// PL-R5T-92: the governed-number readers accept quoted, padded and plus-prefixed
// spellings, and have since r1, with no activation height. Gating that now
// would either change nothing or push the live chain back to the strict readers.
// This test writes the pre-r1 reader out as an oracle (strings.TrimSpace, then
// strconv.ParseUint or big.Int.SetString) and pins the two facts that make the
// ungated change safe:
//
//  1. For every canonical decimal (digits only, nothing around them) the old and
//     the new reader return the same number, so a value written in the plain
//     spelling reads identically before and after.
//  2. Every other spelling is an intended difference: the old reader rejected it
//     (the caller fell back to its default) and the new one reads the number the
//     proposal meant, never a different number.
func TestGovernedNumberReadersAgreeWithTheOldReaderOnCanonicalDecimals(t *testing.T) {
	canonical := []string{
		"0", "1", "7", "20", "604800", "172800", "1250", "18446744073709551615",
		"20000000000000000000000", "340282366920938463463374607431768211455",
	}
	for _, raw := range canonical {
		oldText := strings.TrimSpace(raw)
		newText := ParamDecimal([]byte(raw))
		if oldText != newText {
			t.Fatalf("canonical %q: old text %q, new text %q", raw, oldText, newText)
		}
		oldBig, oldOK := new(big.Int).SetString(oldText, 10)
		newBig, newOK := new(big.Int).SetString(newText, 10)
		if oldOK != newOK || (oldOK && oldBig.Cmp(newBig) != 0) {
			t.Fatalf("canonical %q: big.Int readers disagree (%v %v / %v %v)", raw, oldBig, oldOK, newBig, newOK)
		}
		oldU, oldErr := strconv.ParseUint(oldText, 10, 64)
		newU, newErr := strconv.ParseUint(newText, 10, 64)
		if (oldErr == nil) != (newErr == nil) || (oldErr == nil && oldU != newU) {
			t.Fatalf("canonical %q: ParseUint readers disagree (%d %v / %d %v)", raw, oldU, oldErr, newU, newErr)
		}
	}
}

func TestGovernedNumberReadersOnlyDifferFromTheOldReaderWhereIntended(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want uint64
	}{
		{"quoted", `"1250"`, 1250},
		{"padded", "  1250\n", 1250},
		{"padded and quoted", " \t\"1250\" ", 1250},
		{"whitespace inside the quotes", `"  1250  "`, 1250},
		{"one plus", "+1250", 1250},
		{"two pluses", "++1250", 1250},
		{"quoted with pluses", `"++1250"`, 1250},
	}
	for _, tc := range cases {
		oldText := strings.TrimSpace(tc.raw)
		if tc.raw == "  1250\n" {
			// TrimSpace alone already reads a padded bare number; that is the one
			// non-canonical spelling the old reader handled, and the new reader
			// must return the same value for it.
			if got, err := strconv.ParseUint(oldText, 10, 64); err != nil || got != tc.want {
				t.Fatalf("%s: the old reader should read %d, got %d (%v)", tc.name, tc.want, got, err)
			}
		} else if _, err := strconv.ParseUint(oldText, 10, 64); err == nil {
			t.Fatalf("%s: the old reader unexpectedly read %q; this spelling is meant to be an intended difference", tc.name, oldText)
		}

		newText := ParamDecimal([]byte(tc.raw))
		got, err := strconv.ParseUint(newText, 10, 64)
		if err != nil {
			t.Fatalf("%s: the new reader could not read %q (text %q): %v", tc.name, tc.raw, newText, err)
		}
		if got != tc.want {
			t.Fatalf("%s: the new reader read %d from %q, want %d (it must never read a different number)", tc.name, got, tc.raw, tc.want)
		}
		if b, ok := new(big.Int).SetString(newText, 10); !ok || b.Cmp(new(big.Int).SetUint64(tc.want)) != 0 {
			t.Fatalf("%s: the big.Int reader read %v (%v) from %q, want %d", tc.name, b, ok, tc.raw, tc.want)
		}
	}
}

// A value that is not a plain non-negative number stays unreadable by the
// ParseUint readers whichever way it is spelled: the new reader never turns
// garbage or a negative number into a number.
func TestGovernedNumberReadersNeverInventANumber(t *testing.T) {
	for _, raw := range []string{"", " ", `""`, "+", "++", "-5", `"-5"`, "+-5", "1e3", "1.5", `"1.5"`, "abc", `"abc"`, "0x10", "1_000", `12"3`, `"123" x`} {
		text := ParamDecimal([]byte(raw))
		if v, err := strconv.ParseUint(text, 10, 64); err == nil {
			t.Fatalf("%q read as %d through %q: the reader invented a number", raw, v, text)
		}
	}
}
