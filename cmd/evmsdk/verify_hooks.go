// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Optional verify-side hooks consulted by vertify (V1 EntireCode path)
// and verifyV2 (V2 StreamPacket path) so that the mobile facade can
// dedup blocks pushed by multiple producers without modifying the
// EvmEngine pipeline.
//
// When unset (nil), the hooks are no-ops — this preserves the
// production V1 server-side path (internal/api/agg_sign.MachineVerify)
// completely unchanged.

package evmsdk

import (
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// VerifyShouldSkipFunc is called BEFORE the expensive EVM re-execution.
// If it returns (true, cached), the verifier short-circuits and returns
// the cached result instead. cached may be nil to indicate "skip but no
// cached payload" (the engine then drops this round entirely).
type VerifyShouldSkipFunc func(blockNumber uint64, blockHash types.Hash) (skip bool, cached []byte)

// VerifyCacheFunc is called AFTER a successful sign with the resulting
// payload bytes, so the dedup layer can serve subsequent duplicates.
type VerifyCacheFunc func(blockNumber uint64, blockHash types.Hash, signedResult []byte)

// Atomic pointers so the hooks can be installed/uninstalled
// concurrently without locking the verify hot path.
var (
	verifyShouldSkipHook atomic.Pointer[VerifyShouldSkipFunc]
	verifyCacheHook      atomic.Pointer[VerifyCacheFunc]
)

// SetVerifyHooks installs both hooks atomically. Pass nil to either
// argument to leave that hook untouched. This is a single entry point
// so callers cannot install one half of the pair and forget the other.
//
// Mobile facade calls this from MobileInit; non-mobile callers
// (server-side MachineVerify, tests) never call it and the hooks remain
// nil → fast path stays branch-only.
func SetVerifyHooks(shouldSkip VerifyShouldSkipFunc, cache VerifyCacheFunc) {
	if shouldSkip != nil {
		verifyShouldSkipHook.Store(&shouldSkip)
	}
	if cache != nil {
		verifyCacheHook.Store(&cache)
	}
}

// ClearVerifyHooks removes both hooks. Used by tests + MobileFree.
func ClearVerifyHooks() {
	verifyShouldSkipHook.Store(nil)
	verifyCacheHook.Store(nil)
}

// callShouldSkip is the cheap branch invoked from vertify/verifyV2.
// Returns (false, nil) when no hook is installed.
func callShouldSkip(num uint64, hash types.Hash) (bool, []byte) {
	p := verifyShouldSkipHook.Load()
	if p == nil {
		return false, nil
	}
	return (*p)(num, hash)
}

// callCache is the cheap post-sign hook from vertify/verifyV2.
func callCache(num uint64, hash types.Hash, payload []byte) {
	p := verifyCacheHook.Load()
	if p == nil {
		return
	}
	(*p)(num, hash, payload)
}
