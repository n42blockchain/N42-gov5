package sync

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
)

// realHeaderBlockStub wraps a real *block.Header so headIsConsensusBlock's
// type assertion to *block.Header succeeds, unlike syncBlockStub (whose
// Header() returns itself, an *syncBlockStub).
type realHeaderBlockStub struct {
	block.IBlock
	header *block.Header
}

func (b *realHeaderBlockStub) Header() block.IHeader { return b.header }

func TestHeadIsConsensusBlockNilHead(t *testing.T) {
	chain := &syncChainStub{current: nil}
	if headIsConsensusBlock(chain) {
		t.Fatalf("expected false for nil head")
	}
}

func TestHeadIsConsensusBlockWrongHeaderType(t *testing.T) {
	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(1)}}
	if headIsConsensusBlock(chain) {
		t.Fatalf("expected false when Header() is not *block.Header")
	}
}

func TestHeadIsConsensusBlockShortExtra(t *testing.T) {
	chain := &syncChainStub{current: &realHeaderBlockStub{header: &block.Header{Extra: []byte("N4")}}}
	if headIsConsensusBlock(chain) {
		t.Fatalf("expected false for extra shorter than magic")
	}
}

func TestHeadIsConsensusBlockWrongMagic(t *testing.T) {
	chain := &syncChainStub{current: &realHeaderBlockStub{header: &block.Header{Extra: []byte("XXXXrest")}}}
	if headIsConsensusBlock(chain) {
		t.Fatalf("expected false for non-matching magic")
	}
}

func TestHeadIsConsensusBlockMatch(t *testing.T) {
	chain := &syncChainStub{current: &realHeaderBlockStub{header: &block.Header{Extra: []byte("N42Hrest")}}}
	if !headIsConsensusBlock(chain) {
		t.Fatalf("expected true for matching N42H magic")
	}
}
