package core

import (
	"os"
	"strings"
	"testing"
)

// TestStateTransitionDoesNotUseFmtPrintf guards against reintroducing noisy debug
// statements that bypass structured logging when processing transactions.
func TestStateTransitionDoesNotUseFmtPrintf(t *testing.T) {
	content, err := os.ReadFile("state_transition.go")
	if err != nil {
		t.Fatalf("failed to read state_transition.go: %v", err)
	}
	source := string(content)
	if strings.Contains(source, "fmt.Printf(") {
		t.Fatalf("state_transition.go should not use fmt.Printf; prefer structured logging")
	}
	if strings.Contains(source, "log.Printf(") {
		t.Fatalf("state_transition.go should not use log.Printf; prefer structured logging")
	}
}
