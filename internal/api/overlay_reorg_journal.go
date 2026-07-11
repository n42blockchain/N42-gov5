// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// overlay_reorg_journal.go — in-memory reverse-diff ring buffer that lets a
// snapshot-direct (minimal/full) node unwind a shallow tip reorg WITHOUT
// on-disk changesets. This is the geth "128 in-memory diff layers" model:
// the last N committed blocks' warm-table pre-images are retained, and a tip
// reorg (post-merge mainnet reorgs are essentially always depth-1, and never
// below finality) is resolved by replaying those diffs in reverse.
//
// Consistency with committed state is the invariant: a block's diff is captured
// into `pending` as it executes, and promoted into the bounded `ring` only when
// the batch tx actually commits (Flush). A rolled-back batch's diffs are
// Discarded, so the ring can never contain a diff for state that isn't durable.

package api

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

type journaledBlock struct {
	num    uint64
	hash   types.Hash
	parent types.Hash
	diff   *state.BlockRevDiff
}

// overlayReorgJournal is the bounded ring of recent committed-block reverse
// diffs plus the pending (uncommitted) diffs of the in-flight batch.
type overlayReorgJournal struct {
	mu      sync.Mutex
	n       int
	ring    []journaledBlock
	pending []journaledBlock
}

func newOverlayReorgJournal(n int) *overlayReorgJournal {
	if n <= 0 {
		n = 128
	}
	return &overlayReorgJournal{n: n}
}

// addPending records a just-executed (but not yet committed) block's diff.
func (j *overlayReorgJournal) addPending(num uint64, hash, parent types.Hash, diff *state.BlockRevDiff) {
	j.mu.Lock()
	j.pending = append(j.pending, journaledBlock{num: num, hash: hash, parent: parent, diff: diff})
	j.mu.Unlock()
}

// flush promotes the pending diffs into the ring (the batch committed), trimming
// the ring to the last n blocks.
func (j *overlayReorgJournal) flush() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.pending) == 0 {
		return
	}
	j.ring = append(j.ring, j.pending...)
	j.pending = nil
	if len(j.ring) > j.n {
		j.ring = append([]journaledBlock(nil), j.ring[len(j.ring)-j.n:]...)
	}
}

// discard drops the pending diffs (the batch rolled back).
func (j *overlayReorgJournal) discard() {
	j.mu.Lock()
	j.pending = nil
	j.mu.Unlock()
}

// unwindOne restores the highest committed block's warm state into tx and pops
// it from the ring, returning its number. ok=false when the ring is empty —
// the reorg is deeper than the journal (below finality; never happens on
// mainnet), and the caller must halt rather than corrupt state.
func (j *overlayReorgJournal) unwindOne(tx kv.RwTx) (num uint64, hash types.Hash, ok bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.ring) == 0 {
		return 0, types.Hash{}, false, nil
	}
	last := j.ring[len(j.ring)-1]
	if rerr := last.diff.Restore(tx); rerr != nil {
		return 0, types.Hash{}, false, rerr
	}
	j.ring = j.ring[:len(j.ring)-1]
	return last.num, last.hash, true, nil
}

// depth reports how many committed blocks are currently unwindable (diagnostics).
func (j *overlayReorgJournal) depth() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.ring)
}
