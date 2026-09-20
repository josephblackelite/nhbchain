package rpc

import "context"

// refreshSnapshotForCost builds the explorer snapshot for a window the way the
// background loop does, and reports only whether it worked. It is the one place
// the cost tests call a scan directly, so that they can be pointed at a tree
// whose build function has another signature.
func refreshSnapshotForCost(srv *Server, window int) error {
	_, err := srv.buildExplorerSnapshot(context.Background(), window)
	return err
}
