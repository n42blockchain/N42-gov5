// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/log"
)

// N42_E1_LIGHT_LAZY_RESOLVE (default off) moves the frame decode of an E=1
// light node (N42_E1_SOURCE) off its vote path. Before the commit vote a
// light node needs only three things from a frame block: every frame held
// (bound to the header by the frame root, checked before), the gas limit,
// and the lagged result. The decoded transactions are first read by its
// import (e1LightImport), which stores the block; so the availability check
// looks the frames up (store, Frames table, peer pull, as ResolveFrames
// does), keeps their bytes on the body (Body.SetHeldFrames), claims them as
// the resolver does, and decides the gas limit from a walk of the raw RLP
// (frame.GasWalkFrames) instead of a decode. The import's ResolveFrames then
// decodes the held bytes.
//
// The gas decision is the decoded one's: the walked sum is never below the
// decoded transactions' saturating sum (equal when no entry is dropped,
// frame.GasWalk), so a walked sum within the limit proves the serial pass
// passes. A walked sum over the limit, or a frame the walk refuses, falls
// back to the full resolve and the decoded test, as without the switch.
//
// "1" (or "true", "on") checks lazily; "check" (or "2") resolves as without
// the switch, walks the same frames too and logs an Error on any
// disagreement between the walk and the decoded transactions.
const EnvE1LightLazyResolve = "N42_E1_LIGHT_LAZY_RESOLVE"

const (
	e1LazyOff = iota
	e1LazyOn
	e1LazyCheck
)

var e1LazyMode = parseE1LazyResolve(os.Getenv(EnvE1LightLazyResolve))

func parseE1LazyResolve(v string) int {
	switch v {
	case "1", "true", "on":
		return e1LazyOn
	case "2", "check":
		return e1LazyCheck
	}
	return e1LazyOff
}

// SetE1LightLazyResolveForTest sets the N42_E1_LIGHT_LAZY_RESOLVE mode
// ("off", "on" or "check") and returns the restore.
func SetE1LightLazyResolveForTest(mode string) func() {
	prev := e1LazyMode
	switch mode {
	case "on":
		e1LazyMode = e1LazyOn
	case "check":
		e1LazyMode = e1LazyCheck
	default:
		e1LazyMode = e1LazyOff
	}
	return func() { e1LazyMode = prev }
}

// e1LazyStats: availability checks passed without a decode, checks that fell
// back to the full resolve, and "check"-mode disagreements (must stay 0).
var e1LazyStats struct {
	lazy, fallback, mismatches atomic.Uint64
}

// E1LazyResolveStats returns the lazy checks, the fallbacks to a full
// resolve and the "check"-mode disagreements since start.
func E1LazyResolveStats() (lazy, fallback, mismatches uint64) {
	return e1LazyStats.lazy.Load(), e1LazyStats.fallback.Load(), e1LazyStats.mismatches.Load()
}

// e1LazyApplies reports whether N42_E1_LIGHT_LAZY_RESOLVE concerns the
// check of body on this node: the switch on or checking, a light node, a
// frame body.
func (bc *BlockChain) e1LazyApplies(body *block.Body) bool {
	return e1LazyMode != e1LazyOff && body.IsFrameBody() && bc.E1Light()
}

// holdFramesLazy is the availability check's frame step on a light node
// under N42_E1_LIGHT_LAZY_RESOLVE=1. It looks b's frames up as ResolveFrames
// does (a missing frame is ErrFramesUnavailable, after the same pull) and
// keeps them on the body. lazy reports that the walked gas sum is within
// limit: the frames are then claimed and marked in flight here, exactly as
// the resolver would have, and the decode is left to the import. Otherwise
// (already resolved, a frame the walk refuses, a sum over the limit) the
// caller resolves in full; the held bytes spare that resolve the lookup.
// txCount is the frames' header transaction count when lazy.
func (bc *BlockChain) holdFramesLazy(b block.IBlock, body *block.Body, limit uint64) (lazy bool, txCount int, err error) {
	fs := &bc.frames
	mu := &fs.resolveMu[b.Hash()[0]%byte(len(fs.resolveMu))]
	mu.Lock()
	defer mu.Unlock()
	if body.Txs != nil {
		// Resolved already (an import or an earlier check got here first):
		// the decoded test decides, at no cost.
		return false, 0, nil
	}
	hashes := body.FrameHashes
	raws, held := body.HeldFrames()
	if !held {
		if raws, _, _, err = bc.collectFrames(b, hashes); err != nil {
			return false, 0, err
		}
		body.SetHeldFrames(raws)
	}
	gas, n, werr := frame.GasWalkFrames(raws, 0)
	if werr != nil || gas > limit {
		e1LazyStats.fallback.Add(1)
		log.Debug("availability: lazy frame check falls back to the full resolve", "number", b.Number64().Uint64(),
			"gas", gas, "limit", limit, "err", werr)
		return false, 0, nil
	}
	// What ResolveFrames does once it has decoded: a chained build skips
	// these frames, and admission recovery leaves them to the block. The
	// bytes themselves are held by the body, so an eviction from the store
	// before the import's decode cannot lose them.
	bc.MarkFramesInFlight(hashes, b.Number64().Uint64())
	if fs.store != nil {
		fs.store.Claim(hashes)
	}
	e1LazyStats.lazy.Add(1)
	return true, n, nil
}

// e1LazyCompare is the "check" mode: body was resolved in full; the walk of
// its frames must agree with the decoded transactions -- never below their
// saturating sum, equal to it when no entry was dropped, and a walked sum
// within limit only when the serial pass passes. Every disagreement is
// logged as an Error and counted.
func e1LazyCompare(body *block.Body, number, limit uint64) {
	if body.Txs == nil || len(body.FrameData) != len(body.FrameHashes) {
		return
	}
	walked, n, err := frame.GasWalkFrames(body.FrameData, 0)
	decoded := saturatingGasSum(body.Txs)
	serial := serialGasWithin(body.Txs, limit)
	dropped := n - len(body.Txs)
	if err != nil || walked < decoded || (dropped == 0 && walked != decoded) || (walked <= limit && !serial) {
		e1LazyStats.mismatches.Add(1)
		log.Error("availability: lazy gas walk disagrees with the decoded transactions", "number", number,
			"walked", walked, "decoded", decoded, "limit", limit, "serialWithin", serial, "txs", len(body.Txs),
			"headerTxs", n, "err", err)
	}
}
