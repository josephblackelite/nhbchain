package core

import "testing"

// GovernanceParams reads the node's own policy and a copy of its state. A node
// with no state, or no node at all, is an error to report, never a panic and
// never an answer made of zeros that a client would take for the real rules.
func TestGovernanceParamsReportsAnUnavailableNodeAsAnError(t *testing.T) {
	var missing *Node
	if _, err := missing.GovernanceParams(); err == nil {
		t.Fatal("a nil node answered")
	}
	if _, err := (&Node{}).GovernanceParams(); err == nil {
		t.Fatal("a node with no state answered")
	}
}

func TestCleanGovernanceNamesKeepsTheFirstOfEachInOrder(t *testing.T) {
	got := cleanGovernanceNames([]string{" b ", "a", "", "  ", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if empty := cleanGovernanceNames(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("a nil list came back as %#v, want an empty non-nil list", empty)
	}
}
