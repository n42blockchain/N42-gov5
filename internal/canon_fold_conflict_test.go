// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestCanonFoldRefusesConflictingCommit: under N42_CANON_FOLD a commit that
// does not extend the committed head is refused on both fold paths -- the
// read-transaction pre-check (commitToCanonicalFold) and the write folded
// into an ordered block write (canonFoldApply) -- with the conflict evidence
// and without touching a table, exactly like CommitToCanonicalWith's own
// transaction. A commit extending the committed head still folds.
func TestCanonFoldRefusesConflictingCommit(t *testing.T) {
	defer SetCanonFoldForTest(true)()
	f := newCanonFoldFixture()
	db := newRealignTestDB(t)
	// Canonical g, A1, A2 committed; A4 applied; S3 descends from A1 only.
	f.seed(t, db, []*block.Block{f.a[0], f.a[1], f.a[2]}, f.a[4], nil)
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteHotStuffCommittedHead(tx, f.a[2].Hash())
	}))
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), qmdbEnabled: true}
	bc.currentBlock.Store(f.a[2])
	before := dumpTables(t, db)

	var ce *consensus.ConflictingCommitError
	for _, c := range []*block.Block{f.s[2], f.s[3]} {
		handled, err := bc.commitToCanonicalFold(c.Hash(), nil)
		require.True(t, handled, "conflict must be settled on the fold path")
		require.True(t, errors.As(err, &ce), "fold pre-check err = %v", err)
		require.Equal(t, uint64(2), ce.Height)
		require.Equal(t, f.a[2].Hash(), ce.Committed)
		require.Equal(t, f.s[2].Hash(), ce.Incoming)

		// The public entry point reports the same refusal.
		err = bc.CommitToCanonical(c.Hash())
		require.True(t, errors.Is(err, consensus.ErrConflictingCommit), "commit err = %v", err)

		job := &lagWriteJob{hash: f.a[4].Hash(), number: 4, blk: f.a[4]}
		r := &canonFoldReq{hash: c.Hash()}
		require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
			return bc.canonFoldApply(tx, r, job)
		}))
		require.True(t, errors.As(r.err, &ce), "fold apply err = %v", r.err)
		require.True(t, r.dropped)
		require.False(t, r.applied)
		requireSameTables(t, before, dumpTables(t, db), "after conflicting "+c.Hash().Hex()[:10])
	}

	// Extending the committed head folds as before.
	job := &lagWriteJob{hash: f.a[4].Hash(), number: 4, blk: f.a[4]}
	r := &canonFoldReq{hash: f.a[3].Hash()}
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		return bc.canonFoldApply(tx, r, job)
	}))
	require.NoError(t, r.err)
	require.True(t, r.applied)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		require.Equal(t, f.a[3].Hash(), rawdb.ReadHotStuffCommittedHead(tx))
		return nil
	}))
}
