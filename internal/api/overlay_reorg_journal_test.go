package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/state"
)

// TestOverlayReorgJournalRing checks the ring semantics that keep the journal
// consistent with committed state: pending diffs promote only on flush, drop on
// discard, the ring is bounded to N, and unwindOne pops newest-first.
func TestOverlayReorgJournalRing(t *testing.T) {
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
		return d
	}).MustOpen()
	defer db.Close()
	newTx := func() kv.RwTx {
		tx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		return tx
	}

	j := newOverlayReorgJournal(3)

	// discard drops pending without touching the ring
	j.addPending(1, types.Hash{1}, types.Hash{}, state.NewBlockRevDiff())
	j.addPending(2, types.Hash{2}, types.Hash{}, state.NewBlockRevDiff())
	j.discard()
	require.Equal(t, 0, j.depth(), "discard must drop pending")

	// flush promotes pending; ring bounded to N=3 (keeps the last 3 of 5)
	for i := uint64(100); i <= 104; i++ {
		var h types.Hash
		h[0] = byte(i)
		j.addPending(i, h, types.Hash{}, state.NewBlockRevDiff())
	}
	j.flush()
	require.Equal(t, 3, j.depth(), "ring bounded to N")

	// unwindOne pops newest-first: 104, 103, 102, then empty
	for _, want := range []uint64{104, 103, 102} {
		tx := newTx()
		num, _, ok, err := j.unwindOne(tx)
		tx.Rollback()
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, num)
	}
	tx := newTx()
	_, _, ok, err := j.unwindOne(tx)
	tx.Rollback()
	require.NoError(t, err)
	require.False(t, ok, "ring empty → cannot unwind (deep reorg / below finality)")
}
