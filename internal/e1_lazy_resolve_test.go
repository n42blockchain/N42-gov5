// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

func TestParseE1LazyResolve(t *testing.T) {
	for v, want := range map[string]int{"": e1LazyOff, "0": e1LazyOff, "off": e1LazyOff, "1": e1LazyOn, "true": e1LazyOn,
		"on": e1LazyOn, "2": e1LazyCheck, "check": e1LazyCheck, "yes": e1LazyOff} {
		require.Equal(t, want, parseE1LazyResolve(v), "%q", v)
	}
}

// TestHoldFramesLazy: the lazy frame step on a light node holds the frames
// it looked up and claims them only when the walked gas is within the
// limit; over the limit it leaves the decision to the full resolve, which
// then decodes the held bytes even after the store dropped the frames; a
// missing frame is ErrFramesUnavailable, as for the resolver; and the switch
// does nothing on a node that is not light.
func TestHoldFramesLazy(t *testing.T) {
	defer SetE1LightLazyResolveForTest("on")()
	keys := frametest.Keys(t, 5, 77)
	lc := newLagChainEngine(t, nil, importBenchEngine{})
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])

	mk := func(n uint64) (*block.Block, []types.Hash, []types.Hash) {
		raws, hashes, txs := fb.Frames(t, 3, 20)
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]))
		}
		hdr := &block.Header{ParentHash: types.Hash{byte(n)}, Number: uint256.NewInt(n), GasLimit: 1 << 40, Time: n}
		hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
		want := make([]types.Hash, len(txs))
		for i, tx := range txs {
			want[i] = tx.Hash()
		}
		return overWire(t, block.NewLaggedBlock(hdr, hashes, raws)), hashes, want
	}
	txHashes := func(b *block.Block) []types.Hash {
		var out []types.Hash
		for _, tx := range b.Body().(*block.Body).Txs {
			out = append(out, tx.Hash())
		}
		return out
	}
	const gas = 3 * 20 * 21000

	require.False(t, lc.bc.e1LazyApplies(&block.Body{FrameHashes: []types.Hash{{1}}}), "applies on a node that is not light")
	lc.bc.SetE1SourceForTest("http://127.0.0.1:1")
	require.True(t, lc.bc.e1LazyApplies(&block.Body{FrameHashes: []types.Hash{{1}}}))
	require.False(t, lc.bc.e1LazyApplies(&block.Body{}), "applies to a body without frames")

	// Within the limit (exactly at it): held, claimed, in flight, not decoded.
	b, hashes, want := mk(10)
	body := b.Body().(*block.Body)
	lazy, n, err := lc.bc.holdFramesLazy(b, body, gas)
	require.NoError(t, err)
	require.True(t, lazy)
	require.Equal(t, 60, n)
	require.Nil(t, body.Txs)
	_, held := body.HeldFrames()
	require.True(t, held)
	inFlight := lc.bc.FramesInFlight()
	for _, h := range hashes {
		require.True(t, inFlight(h))
		require.False(t, st.NeedsSenders(h))
	}
	// The import decodes the held frames, even once the store dropped them.
	st.Remove(hashes) // in flight but not included: Remove drops them
	require.False(t, st.Has(hashes[0]))
	require.NoError(t, lc.bc.ResolveFrames(b))
	require.Equal(t, want, txHashes(b))
	// Resolved: a second check leaves the decision to the decoded test.
	lazy, _, err = lc.bc.holdFramesLazy(b, body, gas)
	require.NoError(t, err)
	require.False(t, lazy)

	// One gas unit short: falls back, held but neither claimed nor decoded.
	_, fallback0, _ := E1LazyResolveStats()
	b2, hashes2, want2 := mk(11)
	body2 := b2.Body().(*block.Body)
	lazy, _, err = lc.bc.holdFramesLazy(b2, body2, gas-1)
	require.NoError(t, err)
	require.False(t, lazy)
	_, fallback1, _ := E1LazyResolveStats()
	require.Equal(t, fallback0+1, fallback1)
	require.Nil(t, body2.Txs)
	require.True(t, st.NeedsSenders(hashes2[0]), "claimed on the fallback")
	require.NoError(t, lc.bc.ResolveFrames(b2))
	require.Equal(t, want2, txHashes(b2))
	within, _ := availGasWithin(body2, gas-1)
	require.False(t, within, "the decoded test passes a block over its limit")

	// A frame held nowhere: unavailable, nothing held.
	b3, hashes3, _ := mk(12)
	st.Remove(hashes3[1:2])
	body3 := b3.Body().(*block.Body)
	_, _, err = lc.bc.holdFramesLazy(b3, body3, gas)
	require.True(t, errors.Is(err, ErrFramesUnavailable), "err %v", err)
	_, held = body3.HeldFrames()
	require.False(t, held)
	require.Nil(t, body3.Txs)
}

// TestE1LazyCompare: the "check" mode is silent on a resolved body and
// reports a body whose transactions do not match its frames.
func TestE1LazyCompare(t *testing.T) {
	keys := frametest.Keys(t, 4, 78)
	lc := newLagChainEngine(t, nil, importBenchEngine{})
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 2, 10)
	for i := range raws {
		require.True(t, st.Put(hashes[i], raws[i]))
	}
	hdr := &block.Header{Number: uint256.NewInt(3), GasLimit: 1 << 40, Time: 3}
	hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
	b := overWire(t, block.NewLaggedBlock(hdr, hashes, raws))
	require.NoError(t, lc.bc.ResolveFrames(b))
	body := b.Body().(*block.Body)

	_, _, m0 := E1LazyResolveStats()
	e1LazyCompare(body, 3, 1<<40)
	e1LazyCompare(body, 3, 1) // over the limit both ways: agreement
	_, _, m1 := E1LazyResolveStats()
	require.Equal(t, m0, m1)

	// A transaction too many: the decoded sum exceeds the walked one.
	body.Txs = append(body.Txs[:len(body.Txs):len(body.Txs)], body.Txs[0])
	e1LazyCompare(body, 3, 1<<40)
	_, _, m2 := E1LazyResolveStats()
	require.Equal(t, m1+1, m2)
}
