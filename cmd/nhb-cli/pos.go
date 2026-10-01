package main

import (
	"fmt"
	"io"
)

func runPOSCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, posUsage())
		return 1
	}
	switch args[0] {
	case "sweep-voids":
		return reportRetired(stderr, "nhb-cli pos sweep-voids", "pos_sweepVoids")
	default:
		fmt.Fprintf(stderr, "Unknown pos subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, posUsage())
		return 1
	}
}

func posUsage() string {
	return "Usage: nhb-cli pos <subcommand>\nSubcommands:\n  sweep-voids   (retired) the node no longer serves pos_sweepVoids; every block voids expired authorizations itself"
}
