package main

import (
	"flag"
	"fmt"
	"os"

	"nhbchain/tools/docs/snippets"
)

func main() {
	docRoot := flag.String("root", "docs", "documentation root to scan")
	flag.Parse()

	if err := snippets.Verify(*docRoot); err != nil {
		fmt.Fprintf(os.Stderr, "snippet verification failed: %v\n", err)
		os.Exit(1)
	}
}
