// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Per-call split of FlushTo and EvictTwigsThrough, for the block write's
// stamp line (r96 stage 0). A handful of time.Now calls per block; nothing
// here changes what is written or evicted.

package qmdb

import "time"

// FlushStats is the split of the last FlushTo on a tree.
type FlushStats struct {
	// Root: the Root() that settles dirty twig roots before the rows.
	Root time.Duration
	// Entries: the entry-row appends [flushedThrough, nextSlot).
	Entries time.Duration
	// Dead: the dead-row deletes.
	Dead time.Duration
	// TwigWalk: choosing which twigs get a meta row (full scan of the
	// forest, or the changed-twig set).
	TwigWalk time.Duration
	// TwigRows: writing the chosen twigs' meta rows (leaf blobs excluded).
	TwigRows time.Duration
	// Leaf: encoding and writing the leaf blobs.
	Leaf time.Duration
	// Meta: the three recovery meta rows.
	Meta time.Duration

	EntryRows    int // entry rows written
	DeadRows     int // dead rows deleted
	TwigsForest  int // twigs in the forest
	TwigsVisited int // twigs the walk looked at
	TwigRowsN    int // twig meta rows written
	LeafRowsN    int // leaf blobs written
	// SetMode: the walk used (TwigWalkFull, TwigWalkSet, TwigWalkCheck).
	SetMode uint8
	// SetRebuilt: the changed-twig set was rebuilt by a full scan first.
	SetRebuilt bool
}

// EvictStats is the split of the last EvictTwigsThrough on a tree.
type EvictStats struct {
	Fold    time.Duration // foldTouched before the walk
	Walk    time.Duration // the walk and the evictions
	Visited int           // twigs the walk looked at
	Evicted int           // twigs whose node heap was dropped
	SetMode uint8
}

// Twig walk kinds reported in FlushStats/EvictStats.SetMode.
const (
	TwigWalkFull  uint8 = 0
	TwigWalkSet   uint8 = 1
	TwigWalkCheck uint8 = 2
)

// LastFlushStats returns the split of the last FlushTo. Same lock as FlushTo.
func (t *Tree) LastFlushStats() FlushStats { return t.lastFlush }

// LastEvictStats returns the split of the last EvictTwigsThrough. Same lock
// as EvictTwigsThrough.
func (t *Tree) LastEvictStats() EvictStats { return t.lastEvict }
