package main

import (
	"fmt"
	"os"

	"nhbchain/tools/docs/snippets"
)

func main() {
	if err := snippets.Verify("docs"); err != nil {
		fmt.Fprintf(os.Stderr, "docs verification failed: %v\n", err)
		os.Exit(1)
	}
}
