package main

import (
	"fmt"
	"io"
)

// reportRetired is what a subcommand does when the node method behind it is
// retired. The node answers those methods with HTTP 410 without running them:
// each one changed the state of the one validator that handled the call,
// outside block execution (see the *RPCDisabledMessage constants in rpc/), so
// a command that still sent the request could only fail, after a round trip and
// for most of them a token check. The command says so and exits non-zero
// without contacting the node.
func reportRetired(stderr io.Writer, command, method string) int {
	fmt.Fprintf(stderr, "Error: %s is retired: the node no longer serves %s (HTTP 410), because it changed validator-local state outside the block pipeline, and this CLI has no replacement for it yet.\n", command, method)
	return 1
}
