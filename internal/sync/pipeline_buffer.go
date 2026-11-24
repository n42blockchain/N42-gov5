// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reorder buffer of the parallel catch-up pipeline (sync v2 design §4.2).
// Slots arrive out of order from several peers; the single importer takes
// only the contiguous run starting at its cursor. The buffer is bounded in
// blocks and in (estimated decoded) bytes. The block the importer needs next
// is always admitted, so a full buffer can never deadlock the importer: the
// hard bound is the configured limit plus that one block.

package sync

import (
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
)

// pipeBlock is one buffered block and who served it.
type pipeBlock struct {
	blk   block.IBlock
	bytes uint64
	peer  peer.ID
	seg   uint64 // id of the response that delivered it
}

// reorderBuffer is not safe for concurrent use; the pipeline run guards it.
type reorderBuffer struct {
	maxBlocks int
	maxBytes  uint64
	blocks    map[uint64]pipeBlock
	bytes     uint64
}

func newReorderBuffer(maxBlocks int, maxBytes uint64) *reorderBuffer {
	if maxBlocks < 1 {
		maxBlocks = 1
	}
	return &reorderBuffer{maxBlocks: maxBlocks, maxBytes: maxBytes, blocks: make(map[uint64]pipeBlock)}
}

func (b *reorderBuffer) len() int     { return len(b.blocks) }
func (b *reorderBuffer) size() uint64 { return b.bytes }
func (b *reorderBuffer) has(n uint64) bool {
	_, ok := b.blocks[n]
	return ok
}

// fits reports whether blocks more blocks of bytes more bytes stay inside
// both bounds.
func (b *reorderBuffer) fits(blocks int, bytes uint64) bool {
	return len(b.blocks)+blocks <= b.maxBlocks && b.bytes+bytes <= b.maxBytes
}

// admit buffers in (ascending, one response) for an importer whose cursor is
// next. Blocks below next or already buffered (a duplicate request) are
// skipped. Admission stops at the first block that would exceed a bound,
// unless it is the block at next. It returns how many blocks were admitted
// and whether some were refused for space (the caller re-plans them).
func (b *reorderBuffer) admit(next uint64, in []pipeBlock) (admitted int, full bool) {
	for _, pb := range in {
		n := pb.blk.Number64().Uint64()
		if n < next || b.has(n) {
			continue
		}
		if n != next && !b.fits(1, pb.bytes) {
			return admitted, true
		}
		b.blocks[n] = pb
		b.bytes += pb.bytes
		admitted++
	}
	return admitted, false
}

// take removes and returns the contiguous run starting at next that came
// from one response, at most max blocks.
func (b *reorderBuffer) take(next uint64, max int) []pipeBlock {
	first, ok := b.blocks[next]
	if !ok {
		return nil
	}
	out := make([]pipeBlock, 0, 16)
	for n := next; len(out) < max; n++ {
		pb, ok := b.blocks[n]
		if !ok || pb.seg != first.seg {
			break
		}
		out = append(out, pb)
		b.remove(n)
	}
	return out
}

func (b *reorderBuffer) remove(n uint64) {
	if pb, ok := b.blocks[n]; ok {
		b.bytes -= pb.bytes
		delete(b.blocks, n)
	}
}

// dropBelow removes every block numbered below n (already consumed).
func (b *reorderBuffer) dropBelow(n uint64) {
	for k := range b.blocks {
		if k < n {
			b.remove(k)
		}
	}
}

// dropSeg removes every block that one response delivered and returns how
// many were removed.
func (b *reorderBuffer) dropSeg(seg uint64) int {
	dropped := 0
	for k, pb := range b.blocks {
		if pb.seg == seg {
			b.remove(k)
			dropped++
		}
	}
	return dropped
}

// clear empties the buffer.
func (b *reorderBuffer) clear() {
	b.blocks = make(map[uint64]pipeBlock)
	b.bytes = 0
}
