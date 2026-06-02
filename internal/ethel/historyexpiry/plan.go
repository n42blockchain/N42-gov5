// Package historyexpiry implements EIP-4444-style recent-window retention for
// the eth-el full node: the node keeps bodies/receipts for a recent window of
// blocks hot, and offloads older (cold) segments to era archives that are
// published (SHA256 + block range) via a torrentsync manifest under a 1-of-N
// availability assumption (N42 archive/seeder nodes retain + seed the cold
// segments). See docs/ethel/body-compression-design.md §8.5.
//
// This package is pure policy: it computes which segments are cold vs hot. The
// actual export/relocate/publish is done by cmd/n42-history-expiry; the
// transparent cold-read resolver consults a torrentsync.Manifest.
package historyexpiry

// SegSize is the columnar body segment size (blocks per bodyc.NNNN segment),
// matching HeaderSegmentSize.
const SegSize = 8192

// DefaultWindowBlocks is ~1 year at 12s/block — the EIP-4444 retention horizon
// (mainnet uses HISTORY_PRUNE_EPOCHS=82125 ≈ 1 earth year). 365.25*24*3600/12.
const DefaultWindowBlocks = 2_629_800

// SegRange is one segment's block coverage.
type SegRange struct {
	Seg        uint64
	FirstBlock uint64
	LastBlock  uint64
}

// Plan is the retention decision for a tip + window.
type Plan struct {
	Tip          uint64
	WindowBlocks uint64
	SegSize      uint64
	// HotFromBlock is the first block retained hot (>= this stays hot).
	HotFromBlock uint64
	// HotFromSeg is the first segment retained hot. Segments below this are cold.
	HotFromSeg uint64
	// ColdUntilSeg is the exclusive upper bound of cold segments [0, ColdUntilSeg).
	ColdUntilSeg uint64
}

// Compute returns the retention plan. Cold = segments fully below the hot
// boundary; the boundary is aligned DOWN to a segment so the hot window keeps
// at least windowBlocks (a partial boundary segment stays hot). With
// windowBlocks==0 or tip within the window, nothing is cold.
func Compute(tip, windowBlocks, segSize uint64) Plan {
	if segSize == 0 {
		segSize = SegSize
	}
	if windowBlocks == 0 {
		windowBlocks = DefaultWindowBlocks
	}
	p := Plan{Tip: tip, WindowBlocks: windowBlocks, SegSize: segSize}
	if tip <= windowBlocks {
		// Whole chain is within the window — nothing cold.
		p.HotFromBlock = 0
		p.HotFromSeg = 0
		p.ColdUntilSeg = 0
		return p
	}
	p.HotFromBlock = tip - windowBlocks
	p.HotFromSeg = p.HotFromBlock / segSize // align down → boundary segment stays hot
	p.ColdUntilSeg = p.HotFromSeg
	return p
}

// IsCold reports whether blockNum falls in a cold (offloaded) segment.
func (p Plan) IsCold(blockNum uint64) bool {
	return blockNum/p.SegSize < p.ColdUntilSeg
}

// ColdSegs lists the cold segments within [firstAvailSeg, lastAvailSeg] (the
// segments actually present in a store, e.g. a post-merge-only store starts at
// its merge segment, not 0). Only segments < ColdUntilSeg are returned.
func (p Plan) ColdSegs(firstAvailSeg, lastAvailSeg uint64) []SegRange {
	var out []SegRange
	end := p.ColdUntilSeg
	if lastAvailSeg+1 < end {
		end = lastAvailSeg + 1
	}
	for s := firstAvailSeg; s < end; s++ {
		out = append(out, SegRange{
			Seg:        s,
			FirstBlock: s * p.SegSize,
			LastBlock:  s*p.SegSize + p.SegSize - 1,
		})
	}
	return out
}
