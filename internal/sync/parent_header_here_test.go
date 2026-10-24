package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// headerHereChain holds the headers in known.
type headerHereChain struct {
	common.IBlockChain
	known map[types.Hash]uint64
}

func (c *headerHereChain) HeaderKnownHere(h types.Hash, n uint64) bool {
	got, ok := c.known[h]
	return ok && got == n
}

// TestParentHeaderHereGatesTheHeaderVote (round S132): the header-only
// prepare vote is released only for a block whose parent this node holds.
// A chain that cannot answer keeps the old behaviour.
func TestParentHeaderHereGatesTheHeaderVote(t *testing.T) {
	parent := types.Hash{0x39, 0xe2}
	chain := &headerHereChain{known: map[types.Hash]uint64{parent: 14152208}}
	svc := &Service{ctx: context.Background(), cfg: &config{chain: chain}}
	child := &block.Header{ParentHash: parent, Number: uint256.NewInt(14152209)}
	if !svc.parentHeaderHere(child) {
		t.Fatal("a block whose parent is held was refused")
	}
	orphan := &block.Header{ParentHash: types.Hash{0xd3}, Number: uint256.NewInt(14152209)}
	if svc.parentHeaderHere(orphan) {
		t.Fatal("a block whose parent is not held released the header vote")
	}
	wrongHeight := &block.Header{ParentHash: parent, Number: uint256.NewInt(14152300)}
	if svc.parentHeaderHere(wrongHeight) {
		t.Fatal("a parent at another height counted as held")
	}
	legacy := &Service{ctx: context.Background(), cfg: &config{chain: &orderTestChain{}}}
	if !legacy.parentHeaderHere(orphan) {
		t.Fatal("a chain without HeaderKnownHere must not block the vote")
	}
}
