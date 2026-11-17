package bft

// A validator that hears a peer's proposal or vote for a height beyond the one it is
// deciding asks the peers for the blocks it lacks. The block it lacks first is the one
// at the height it is deciding, which is the block after its chain's last one, so the
// request has to start there: a peer that is exactly one block ahead holds blocks only
// up to that one, and a node's answer to a request that starts beyond its last block is
// nothing at all.

import (
	"encoding/json"
	"testing"

	"nhbchain/p2p"
)

// catchUpRequests decodes the starting heights of the block requests broadcast.
func catchUpRequests(t *testing.T, br *recordingBroadcaster) []uint64 {
	t.Helper()
	var from []uint64
	for _, msg := range br.messages {
		if msg.Type != p2p.MsgTypeGetBlocks {
			continue
		}
		var payload p2p.GetBlocksPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatalf("decode block request: %v", err)
		}
		from = append(from, payload.From)
	}
	return from
}

func TestCatchUpRequestStartsAtTheBlockTheNodeLacks(t *testing.T) {
	const nodeHeight = 5

	cases := []struct {
		name string
		// send hands the engine a message of the peer (validator 1) for height.
		send func(t *testing.T, e *Engine, tv *testValidators) error
		// staleEngine moves the node's chain on after the engine was built, as a block
		// the node took from a peer does, leaving the engine's own height behind it.
		staleEngine uint64
		// want is the height the request must start at.
		want uint64
	}{
		{
			name: "proposal from a peer one block ahead",
			send: func(t *testing.T, e *Engine, tv *testValidators) error {
				block := candidateBlock(nodeHeight+2, 0, tv.addrs[1], 0x41)
				return e.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, 0))
			},
			want: nodeHeight + 1,
		},
		{
			name: "vote from a peer one block ahead",
			send: func(t *testing.T, e *Engine, tv *testValidators) error {
				hash := headerHash(t, candidateBlock(nodeHeight+2, 0, tv.addrs[1], 0x42))
				return e.HandleVote(signVoteAs(t, tv.keys[1], tv.addrs[1], Prevote, hash, 0, nodeHeight+2))
			},
			want: nodeHeight + 1,
		},
		{
			name: "proposal from a peer many blocks ahead",
			send: func(t *testing.T, e *Engine, tv *testValidators) error {
				block := candidateBlock(nodeHeight+20, 0, tv.addrs[1], 0x43)
				return e.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, 0))
			},
			want: nodeHeight + 1,
		},
		{
			name: "engine height behind the node's chain",
			send: func(t *testing.T, e *Engine, tv *testValidators) error {
				block := candidateBlock(nodeHeight+12, 0, tv.addrs[1], 0x44)
				return e.HandleProposal(newSignedProposal(t, tv.keys[1], tv.addrs[1], block, 0))
			},
			staleEngine: nodeHeight + 3,
			want:        nodeHeight + 4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tv := newTestValidators(t, 2)
			node := &trackingNode{validatorSet: tv.set, height: nodeHeight}
			br := &recordingBroadcaster{}
			engine := NewEngine(node, tv.keys[0], br)
			if tc.staleEngine != 0 {
				node.height = tc.staleEngine
			}

			if err := tc.send(t, engine, tv); err != nil {
				t.Fatalf("handle the peer's message: %v", err)
			}
			got := catchUpRequests(t, br)
			if len(got) != 1 {
				t.Fatalf("expected one block request, got %v", got)
			}
			if got[0] != tc.want {
				t.Fatalf("the block request starts at %d, want %d: the node's chain ends at %d, so the block it lacks is %d",
					got[0], tc.want, node.GetHeight(), node.GetHeight()+1)
			}
		})
	}
}
