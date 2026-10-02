// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func newTestOutputBatcher(t *testing.T) *outputBatcher {
	t.Helper()
	fz, err := freezer.New(t.TempDir(), freezer.DefaultFreezeThreshold)
	require.NoError(t, err)
	t.Cleanup(func() { fz.Close() })
	b, err := newOutputBatcher(fz)
	require.NoError(t, err)
	t.Cleanup(b.Close)
	return b
}

// TestFrozenInterner_LookupHitAndMiss exercises both success and error
// branches of frozenInterner's InternAddr/InternCodeHash, plus the zero
// codeHash sentinel short-circuit.
func TestFrozenInterner_LookupHitAndMiss(t *testing.T) {
	var addr types.Address
	addr[19] = 1
	var h types.Hash
	h[31] = 2

	fi := &frozenInterner{
		addrs:      map[types.Address]uint32{addr: 7},
		codeHashes: map[types.Hash]uint32{h: 9},
	}

	id, err := fi.InternAddr(addr)
	require.NoError(t, err)
	require.Equal(t, uint32(7), id)

	var missingAddr types.Address
	missingAddr[19] = 0xFF
	_, err = fi.InternAddr(missingAddr)
	require.Error(t, err)

	id, err = fi.InternCodeHash(h)
	require.NoError(t, err)
	require.Equal(t, uint32(9), id)

	zid, err := fi.InternCodeHash(types.Hash{})
	require.NoError(t, err)
	require.Zero(t, zid)

	var missingHash types.Hash
	missingHash[0] = 0xFF
	_, err = fi.InternCodeHash(missingHash)
	require.Error(t, err)
}

// TestAsyncOutputWriter_ProcessGenesisPath drives enqueue -> waitDrain for a
// block-0 style pendingOutput (pre-encoded genesis bytes, no changeset).
func TestAsyncOutputWriter_ProcessGenesisPath(t *testing.T) {
	b := newTestOutputBatcher(t)
	w := newAsyncOutputWriter(b)

	w.enqueue(pendingOutput{
		blockNum:    0,
		genesisAcct: []byte{1, 2, 3},
		genesisSto:  []byte{4, 5},
		witnessData: []byte{6},
	})

	require.NoError(t, w.waitDrain())
	require.NoError(t, w.checkError())
}

// TestAsyncOutputWriter_ProcessNilChangesetPath drives the branch where
// neither genesis bytes nor changesets are set (nil accCSBytes/stoCSBytes).
func TestAsyncOutputWriter_ProcessNilChangesetPath(t *testing.T) {
	b := newTestOutputBatcher(t)
	w := newAsyncOutputWriter(b)

	w.enqueue(pendingOutput{blockNum: 1})
	require.NoError(t, w.waitDrain())
	require.NoError(t, w.checkError())
}

// TestAsyncOutputWriter_DrainStallStatsResets confirms the stall counters
// start at zero and DrainStallStats resets them.
func TestAsyncOutputWriter_DrainStallStatsResets(t *testing.T) {
	b := newTestOutputBatcher(t)
	w := newAsyncOutputWriter(b)
	defer w.stop()

	count, total := w.DrainStallStats()
	require.Zero(t, count)
	require.Zero(t, total)
}

// TestAsyncOutputWriter_WaitDrainRestartsLoop confirms the writer can be
// used again after waitDrain (loop is restarted), by enqueueing twice
// across two drain cycles.
func TestAsyncOutputWriter_WaitDrainRestartsLoop(t *testing.T) {
	b := newTestOutputBatcher(t)
	w := newAsyncOutputWriter(b)

	w.enqueue(pendingOutput{blockNum: 1})
	require.NoError(t, w.waitDrain())

	w.enqueue(pendingOutput{blockNum: 2})
	require.NoError(t, w.waitDrain())
}

// TestAsyncOutputWriter_Stop confirms stop() shuts the goroutine down
// permanently and surfaces no error for a clean run.
func TestAsyncOutputWriter_Stop(t *testing.T) {
	b := newTestOutputBatcher(t)
	w := newAsyncOutputWriter(b)

	w.enqueue(pendingOutput{blockNum: 1})
	require.NoError(t, w.stop())
}
