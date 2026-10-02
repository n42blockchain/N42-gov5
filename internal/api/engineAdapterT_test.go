package api

// engineAdapterT_test.go covers EngineStateAdapter's simple field-level
// setters/getters in engine_state_adapter.go that don't require a running
// execution pipeline: freezer-sink/BAL/header-hash wiring, the hashed read
// cache purge, the reorg journal enable/flush/discard/unwind/commit/depth
// cycle (empty-journal branches), staged/snapshot-cold toggles and the
// WithCSSource/WithHashedCanonical builder methods.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/internal/ethel"
)

func TestEngineStateAdapterSimpleSetters(t *testing.T) {
	a := NewEngineStateAdapter(nil, nil, nil, nil)
	require.NotNil(t, a)

	// SetCSFreezerSink: nil is a legal "restore MDBX changesets" value too.
	a.SetCSFreezerSink(nil)
	require.Nil(t, a.csFreezerSink)
	sink := &ethel.CSFreezerSink{}
	a.SetCSFreezerSink(sink)
	require.Same(t, sink, a.csFreezerSink)

	// SetBALPrefetch.
	called := false
	a.SetBALPrefetch(func(h types.Hash) []byte {
		called = true
		return []byte{1, 2, 3}
	})
	require.NotNil(t, a.balPrefetch)
	got := a.balPrefetch(types.Hash{})
	require.True(t, called)
	require.Equal(t, []byte{1, 2, 3}, got)
	a.SetBALPrefetch(nil)
	require.Nil(t, a.balPrefetch)

	// SetHeaderHashReader.
	a.SetHeaderHashReader(nil)
	require.Nil(t, a.headerHashReader)

	// PurgeHashedReadCache must be a safe no-op when the cache was never
	// created (lazily initialized on first block).
	require.NotPanics(t, func() { a.PurgeHashedReadCache() })

	// SetStaged / SetSnapshotCold toggles.
	a.SetStaged(true)
	require.True(t, a.staged)
	a.SetStaged(false)
	require.False(t, a.staged)

	a.SetSnapshotCold(nil)
	require.Nil(t, a.snapshotCold)
}

func TestEngineStateAdapterBuilderMethods(t *testing.T) {
	a := NewEngineStateAdapter(nil, nil, nil, nil)

	ret := a.WithCSSource(nil)
	require.Same(t, a, ret)
	require.Nil(t, a.csSource)

	var src cs.Source
	ret = a.WithCSSource(src)
	require.Same(t, a, ret)

	ret = a.WithHashedCanonical(true)
	require.Same(t, a, ret)
	require.True(t, a.hashedCanonical)

	ret = a.WithHeadMarker(true)
	require.Same(t, a, ret)
	require.True(t, a.trackHeadMarker)
}

func TestEngineStateAdapterReorgJournalEmptyLifecycle(t *testing.T) {
	a := NewEngineStateAdapter(nil, nil, nil, nil)

	// Before EnableReorgJournal, every journal method is a documented no-op.
	require.Equal(t, 0, a.ReorgJournalDepth())
	require.NotPanics(t, func() { a.FlushReorgJournal() })
	require.NotPanics(t, func() { a.DiscardReorgJournal() })
	require.NotPanics(t, func() { a.CommitOverlayUnwind(1) })
	num, hash, ok, err := a.UnwindOverlayBlock(nil)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, num)
	require.Equal(t, types.Hash{}, hash)

	// After EnableReorgJournal with an empty ring, the same calls stay safe
	// and UnwindOverlayBlock still reports ok=false without touching tx
	// (nil tx is fine precisely because the ring is empty).
	a.EnableReorgJournal(4)
	require.Equal(t, 0, a.ReorgJournalDepth())
	a.SetJournalActive(true)
	require.True(t, a.journalActive)
	a.SetJournalActive(false)
	require.False(t, a.journalActive)

	require.NotPanics(t, func() { a.FlushReorgJournal() })
	require.NotPanics(t, func() { a.DiscardReorgJournal() })

	num, hash, ok, err = a.UnwindOverlayBlock(nil)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, num)
	require.Equal(t, types.Hash{}, hash)

	a.CommitOverlayUnwind(99) // ring empty: no-op, must not panic.
	require.Equal(t, 0, a.ReorgJournalDepth())
}

func TestEngineStateAdapterSetBatchTxToggle(t *testing.T) {
	a := NewEngineStateAdapter(nil, nil, nil, nil)

	// nil -> nil is the standalone (no batch) steady state.
	a.SetBatchTx(nil)
	require.Nil(t, a.batchTx)
	require.Nil(t, a.batchHeaders)
}
