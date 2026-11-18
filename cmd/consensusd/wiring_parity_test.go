package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Ledger PL-EXT-47 (bug-bounty reports of 2026-11-18): the two validator binaries must configure the node the same way. A
// setting one of them never applies is a node that executes a transaction differently from the other (the staking rate that
// stayed 0, the lending risk limits, the swap sanctions check), which is a divergence of state roots, not a local matter.
//
// The test reads both main files and compares the node calls that configure the processor (Set..., Sync..., Validate...) and the
// options the consensus engine is built with (bft.With...). A call that one binary makes and the other does not has to be listed
// below with the reason, so a new setting added to one main cannot be forgotten in the other.

// networkOnly are the calls one binary makes and the other deliberately does not, with the reason. It is empty: the first version
// of this list excused three calls as "how a binary reaches the network" and a researcher then showed that one of them (the node's
// broadcaster) left a transaction submitted to consensusd stranded in its mempool, and the other two (the external commit
// notifier, the proposal frame limit) are liveness settings the engine and the proposer need whichever way the network is reached.
// An entry here needs a reason that survives that kind of reading.
var networkOnly = map[string]string{}

func nodeConfigCalls(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	calls := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		switch recv.Name {
		case "node":
			if strings.HasPrefix(name, "Set") || strings.HasPrefix(name, "Sync") || strings.HasPrefix(name, "Validate") {
				calls[name] = true
			}
		case "bft":
			// The options the consensus engine is built with (vote binding, the sign-state files, the timers).
			if strings.HasPrefix(name, "With") {
				calls["bft."+name] = true
			}
		}
		return true
	})
	return calls
}

func TestBothValidatorBinariesConfigureTheNodeTheSameWay(t *testing.T) {
	nhb := nodeConfigCalls(t, "../nhb/main.go")
	consensusd := nodeConfigCalls(t, "main.go")
	if len(nhb) < 40 || len(consensusd) < 40 {
		t.Fatalf("the scan found %d calls in cmd/nhb and %d in consensusd; it is reading the wrong thing", len(nhb), len(consensusd))
	}

	var missingFromConsensusd, missingFromNHB []string
	for name := range nhb {
		if !consensusd[name] {
			if _, ok := networkOnly[name]; !ok {
				missingFromConsensusd = append(missingFromConsensusd, name)
			}
		}
	}
	for name := range consensusd {
		if !nhb[name] {
			if _, ok := networkOnly[name]; !ok {
				missingFromNHB = append(missingFromNHB, name)
			}
		}
	}
	sort.Strings(missingFromConsensusd)
	sort.Strings(missingFromNHB)
	if len(missingFromConsensusd) > 0 {
		t.Errorf("cmd/nhb configures the node with %v and consensusd does not: the two binaries would execute transactions differently", missingFromConsensusd)
	}
	if len(missingFromNHB) > 0 {
		t.Errorf("consensusd configures the node with %v and cmd/nhb does not: the two binaries would execute transactions differently", missingFromNHB)
	}
}

func TestTheNetworkOnlyCallsAreReallyOnlyInOneBinary(t *testing.T) {
	// The allowance must not hide a setting that both binaries make, nor one that neither makes any longer.
	nhb := nodeConfigCalls(t, "../nhb/main.go")
	consensusd := nodeConfigCalls(t, "main.go")
	for name := range networkOnly {
		if nhb[name] && consensusd[name] {
			t.Errorf("%s is made by both binaries: remove it from the network-only list", name)
		}
		if !nhb[name] && !consensusd[name] {
			t.Errorf("%s is made by neither binary: remove it from the network-only list", name)
		}
	}
}
