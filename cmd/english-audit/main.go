package main

import (
	"flag"
	"fmt"
	"os"

	english "nhbchain/tools/audit/english"
)

func main() {
	out := flag.String("out", "docs/audit/english-latest.md", "output path for the audit report")
	strict := flag.Bool("strict", false, "exit with non-zero code when severity error findings exist")
	includeTests := flag.Bool("include-tests", false, "include *_test.go files in the scan")
	govuln := flag.Bool("govulncheck", false, "run govulncheck if available")
	flag.Parse()

	opts := english.Options{
		OutPath:      *out,
		Strict:       *strict,
		IncludeTests: *includeTests,
		Govulncheck:  *govuln,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	}

	if err := english.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if opts.Strict {
			os.Exit(1)
		}
	}
}
