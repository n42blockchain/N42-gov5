// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestOpenCodesHashIndex_AbsentIsNilNil confirms a dir with neither the
// .hidx nor .hoff file returns (nil, nil) — the "no hash lookup" default
// that CodesFreezerReader falls back from to the address path.
func TestOpenCodesHashIndex_AbsentIsNilNil(t *testing.T) {
	dir := t.TempDir()
	idx, err := openCodesHashIndex(dir)
	require.NoError(t, err)
	require.Nil(t, idx)
}

// TestOpenCodesHashIndex_OffsetsMissingIsNilNil covers the case where the
// .hidx file exists (stat succeeds) but the paired .hoff file is absent:
// mapOrReadFile fails and the half-written pair is treated as absent.
func TestOpenCodesHashIndex_OffsetsMissingIsNilNil(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, CodesHashIndexFile), []byte{1}, 0o644))
	idx, err := openCodesHashIndex(dir)
	require.NoError(t, err)
	require.Nil(t, idx)
}

// TestOpenCodesHashIndex_MalformedOffsetsSize covers the explicit size-
// validation error path: offsets present but not a multiple of the
// per-slot entry size.
func TestOpenCodesHashIndex_MalformedOffsetsSize(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, CodesHashIndexFile), []byte{1}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, CodesHashOffsetsFile), []byte{1, 2, 3}, 0o644))
	_, err := openCodesHashIndex(dir)
	require.Error(t, err)
}

// TestCodesHashIndex_NilReceiverIsSafe confirms lookup/close on a nil
// *codesHashIndex (the "no hash index built" state) never panics.
func TestCodesHashIndex_NilReceiverIsSafe(t *testing.T) {
	var h *codesHashIndex
	_, _, _, ok := h.lookup(types.Hash{})
	require.False(t, ok)
	h.close() // must not panic
}

// TestCodesHashIndex_NoReaderIsSafe covers the "reader nil" short-circuit
// distinct from a fully-nil receiver.
func TestCodesHashIndex_NoReaderIsSafe(t *testing.T) {
	h := &codesHashIndex{}
	_, _, _, ok := h.lookup(types.Hash{})
	require.False(t, ok)
}
