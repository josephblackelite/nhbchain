package core

import (
	"fmt"
	"math/big"
	"strings"
)

// GovernanceEngagementSummary is the engagement side of the weight snapshot that
// voting power is drawn from: the snapshot of the last processed POTSO reward
// epoch, the one native/governance's CastVote reads.
type GovernanceEngagementSummary struct {
	// Available is false until a reward epoch has been processed and its weight
	// snapshot stored; every other field is then zero.
	Available       bool
	Epoch           uint64
	TotalEngagement uint64
	TotalStake      *big.Int
	// EligibleVoters is the number of addresses the snapshot holds a weight for,
	// the only addresses that can cast a vote.
	EligibleVoters uint64
}

// GovernanceParams is what GovernanceParams reports about the voting rules this
// node applies.
type GovernanceParams struct {
	VotingPeriodSeconds uint64
	TimelockSeconds     uint64
	MinDepositWei       *big.Int
	QuorumBps           uint64
	PassThresholdBps    uint64
	AllowedParams       []string
	AllowedRoles        []string
	Engagement          GovernanceEngagementSummary
}

// GovernanceParams returns the voting rules this node applies to a new
// governance proposal -- the [governance] policy it was configured with, in the
// form the proposal engine uses it (names trimmed, blanks and repeats dropped) --
// and the engagement total of the weight snapshot behind voting power. It reads
// the node's own policy and a copy of the state, and changes neither.
//
// The rules are not chain state: they come from this node's configuration file,
// so another validator can report different ones.
func (n *Node) GovernanceParams() (GovernanceParams, error) {
	if n == nil {
		return GovernanceParams{}, fmt.Errorf("node unavailable")
	}
	policy := n.governancePolicy()
	out := GovernanceParams{
		VotingPeriodSeconds: policy.VotingPeriodSeconds,
		TimelockSeconds:     policy.TimelockSeconds,
		MinDepositWei:       new(big.Int),
		QuorumBps:           policy.QuorumBps,
		PassThresholdBps:    policy.PassThresholdBps,
		AllowedParams:       cleanGovernanceNames(policy.AllowedParams),
		AllowedRoles:        cleanGovernanceNames(policy.AllowedRoles),
		Engagement:          GovernanceEngagementSummary{TotalStake: new(big.Int)},
	}
	if policy.MinDepositWei != nil {
		out.MinDepositWei.Set(policy.MinDepositWei)
	}

	manager, err := n.readSnapshot()
	if err != nil {
		return GovernanceParams{}, err
	}
	epoch, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		return GovernanceParams{}, err
	}
	if !ok {
		return out, nil
	}
	snapshot, ok, err := manager.SnapshotPotsoWeights(epoch)
	if err != nil {
		return GovernanceParams{}, err
	}
	if !ok || snapshot == nil {
		return out, nil
	}
	out.Engagement = GovernanceEngagementSummary{
		Available:       true,
		Epoch:           epoch,
		TotalEngagement: snapshot.TotalEngagement,
		TotalStake:      new(big.Int),
		EligibleVoters:  uint64(len(snapshot.Entries)),
	}
	if snapshot.TotalStake != nil {
		out.Engagement.TotalStake.Set(snapshot.TotalStake)
	}
	return out, nil
}

// cleanGovernanceNames trims each name and drops blanks and repeats, keeping the
// first of each in order, as native/governance's SetPolicy does when it builds
// its allow-lists. The result is never nil.
func cleanGovernanceNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
