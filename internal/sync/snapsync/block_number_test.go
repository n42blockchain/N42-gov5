package snapsync

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
)

type snapsyncBlockStub struct {
	block.IBlock
	number *uint256.Int
}

func (b *snapsyncBlockStub) Number64() *uint256.Int { return b.number }

type snapsyncChainStub struct {
	common.IBlockChain
	current block.IBlock
}

func (s *snapsyncChainStub) CurrentBlock() block.IBlock { return s.current }

func TestCurrentBlockNumberNilChain(t *testing.T) {
	if got := currentBlockNumber(nil).Uint64(); got != 0 {
		t.Fatalf("currentBlockNumber(nil) = %d, want 0", got)
	}
}

func TestCurrentBlockNumberNilCurrentBlock(t *testing.T) {
	chain := &snapsyncChainStub{current: nil}
	if got := currentBlockNumber(chain).Uint64(); got != 0 {
		t.Fatalf("currentBlockNumber() = %d, want 0", got)
	}
}

func TestCurrentBlockNumberNilNumber(t *testing.T) {
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{}}
	if got := currentBlockNumber(chain).Uint64(); got != 0 {
		t.Fatalf("currentBlockNumber() = %d, want 0", got)
	}
}

func TestCurrentBlockNumberValid(t *testing.T) {
	chain := &snapsyncChainStub{current: &snapsyncBlockStub{number: uint256.NewInt(77)}}
	if got := currentBlockNumber(chain).Uint64(); got != 77 {
		t.Fatalf("currentBlockNumber() = %d, want 77", got)
	}
	if got := currentBlockNumberOrZero(chain); got != 77 {
		t.Fatalf("currentBlockNumberOrZero() = %d, want 77", got)
	}
}
