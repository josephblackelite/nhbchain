package core

import (
	"testing"
	"time"

	"nhbchain/native/subscriptions"
)

// subscriptionLifecycleRootsWithoutSubscriptions are the state roots the block
// lifecycle produces, block after block, on a processor that has subscriptions
// configured but no subscription at all -- the situation of every block the
// live chain has committed. They were recorded from release/hardening-r4, before
// settlement stopped clearing whole due buckets, so this test fails if the
// per-block step ever writes anything more or less than it did then: a node
// that replays history must reach these same roots.
var subscriptionLifecycleRootsWithoutSubscriptions = []string{
	"0xe064ecabdbf2873205d66640075a433e960de70d6ed50817c820b8c074fbd5dc",
	"0xe064ecabdbf2873205d66640075a433e960de70d6ed50817c820b8c074fbd5dc",
	"0xe064ecabdbf2873205d66640075a433e960de70d6ed50817c820b8c074fbd5dc",
	"0xe064ecabdbf2873205d66640075a433e960de70d6ed50817c820b8c074fbd5dc",
	"0xf6b1d68aa819ec49aaae49f0b69c34385ab2397b41da71c215241463254f3c92",
	"0xf6b1d68aa819ec49aaae49f0b69c34385ab2397b41da71c215241463254f3c92",
	"0xf6b1d68aa819ec49aaae49f0b69c34385ab2397b41da71c215241463254f3c92",
	"0xf6b1d68aa819ec49aaae49f0b69c34385ab2397b41da71c215241463254f3c92",
	"0x1385b57b78d0dc2476dc2541cdcc49b995c81afba81a8ad313e00ce9b6872ea9",
	"0x1385b57b78d0dc2476dc2541cdcc49b995c81afba81a8ad313e00ce9b6872ea9",
	"0x1385b57b78d0dc2476dc2541cdcc49b995c81afba81a8ad313e00ce9b6872ea9",
	"0x1385b57b78d0dc2476dc2541cdcc49b995c81afba81a8ad313e00ce9b6872ea9",
}

// TestSubscriptionLifecycleOverNoSubscriptionsKeepsItsRoots runs the whole
// block lifecycle over three days at six-hour steps (day rollovers included,
// which is where the settlement step writes its watermark) and compares every
// root with the recorded ones.
func TestSubscriptionLifecycleOverNoSubscriptionsKeepsItsRoots(t *testing.T) {
	sp := newStakingStateProcessor(t)
	if err := sp.SetSubscriptionsConfig(subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400}); err != nil {
		t.Fatalf("configure subscriptions: %v", err)
	}
	start := time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC)
	var roots []string
	for step := 0; step < 12; step++ {
		now := start.Add(time.Duration(step) * 6 * time.Hour)
		height := uint64(step + 1)
		sp.BeginBlock(height, now)
		if err := sp.ProcessBlockLifecycle(height, now.Unix()); err != nil {
			sp.EndBlock()
			t.Fatalf("lifecycle at %s: %v", now.Format(time.RFC3339), err)
		}
		roots = append(roots, sp.PendingRoot().Hex())
		sp.EndBlock()
	}
	if len(subscriptionLifecycleRootsWithoutSubscriptions) != len(roots) {
		for i, root := range roots {
			t.Logf("RECORDED step %d %s", i, root)
		}
		t.Fatalf("no recorded roots to compare with (%d recorded, %d produced)", len(subscriptionLifecycleRootsWithoutSubscriptions), len(roots))
	}
	for i, root := range roots {
		if root != subscriptionLifecycleRootsWithoutSubscriptions[i] {
			t.Fatalf("lifecycle root at step %d = %s, recorded %s", i, root, subscriptionLifecycleRootsWithoutSubscriptions[i])
		}
	}
}
