// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_source_synth_test.go covers the headersBodiesSource
// plumbing: OpenHeadersBodiesSource's geth-freezer auto-detection (no
// headerc.cidx present), the exported/internal adapter wrappers, the
// gethFreezerSource accessors, and the exported MakeBlockHashFn wrapper.
// Reuses buildSyntheticGethFreezer from executor_e2e_test.go.

package ethel

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestOpenHeadersBodiesSource_GethFallback builds a small synthetic
// geth-format ancient dir (no headerc.cidx), confirms
// OpenHeadersBodiesSource picks the geth freezer backend, and exercises
// Header/Body/MaxBlock/Close through the exported interface.
func TestOpenHeadersBodiesSource_GethFallback(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 3)
	fz.Close() // release before reopening read-only

	src, err := OpenHeadersBodiesSource(dir)
	require.NoError(t, err)
	defer src.Close()

	require.EqualValues(t, 3, src.MaxBlock())

	h, err := src.Header(0)
	require.NoError(t, err)
	require.EqualValues(t, 0, h.Number.Uint64())

	b, err := src.Body(1)
	require.NoError(t, err)
	require.NotNil(t, b)

	// internalSource adapter wraps the exported interface back to the
	// unexported one (used by SenderStage's shared-close lifecycle).
	inner := &internalSource{s: src}
	require.EqualValues(t, 3, inner.maxBlock())
	h2, err := inner.header(0)
	require.NoError(t, err)
	require.Equal(t, h.Hash(), h2.Hash())
	b2, err := inner.body(1)
	require.NoError(t, err)
	require.NotNil(t, b2)
}

// TestOpenGethFreezerSource_FreezerAccessor exercises the unexported
// gethFreezerSource directly, including its freezer() escape hatch for
// callers needing direct Ancient access.
func TestOpenGethFreezerSource_FreezerAccessor(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 2)
	fz.Close()

	s, err := openGethFreezerSource(dir)
	require.NoError(t, err)
	defer s.close()

	require.EqualValues(t, 2, s.maxBlock())
	require.NotNil(t, s.freezer())

	h, err := s.header(1)
	require.NoError(t, err)
	require.EqualValues(t, 1, h.Number.Uint64())

	_, err = s.header(999)
	require.Error(t, err)
}

// TestMakeBlockHashFn_ExportedWrapper pins MakeBlockHashFn (the exported
// wrapper used by single-block tools) against the internal resolver.
func TestMakeBlockHashFn_ExportedWrapper(t *testing.T) {
	recent := []types.Hash{
		types.HexToHash("0x01"),
		types.HexToHash("0x02"),
		types.HexToHash("0x03"),
	}
	fn := MakeBlockHashFn(10, recent)
	require.Equal(t, recent[2], fn(9))
	require.Equal(t, recent[0], fn(7))
	require.Equal(t, types.Hash{}, fn(10))  // currentBlock itself: out of window
	require.Equal(t, types.Hash{}, fn(100)) // future block
}
