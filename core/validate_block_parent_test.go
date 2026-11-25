package core

import (
	"errors"
	"testing"
)

// Ledger PL-EXT-72: ValidateBlock, which the validators run before they vote for a
// proposal, never compared the block's parent with the chain tip; only AddBlock did, at
// commit. A proposal that does not extend the tip must be refused at validation.
func TestValidateBlockRefusesAProposalThatDoesNotExtendTheTip(t *testing.T) {
	node := newTestNode(t)
	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("a proposal that extends the tip was refused: %v", err)
	}

	wrongParent := *block
	header := *block.Header
	header.PrevHash = append([]byte{0xFF}, block.Header.PrevHash...)
	wrongParent.Header = &header
	err = node.ValidateBlock(&wrongParent)
	if !errors.Is(err, errBlockPrevHashMismatch) {
		t.Fatalf("a proposal with another parent: error %v, want errBlockPrevHashMismatch", err)
	}

	emptyParent := *block
	emptyHeader := *block.Header
	emptyHeader.PrevHash = nil
	emptyParent.Header = &emptyHeader
	if err := node.ValidateBlock(&emptyParent); !errors.Is(err, errBlockPrevHashMismatch) {
		t.Fatalf("a proposal with no parent: error %v, want errBlockPrevHashMismatch", err)
	}
}
