// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	gosync "sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// profile1Chain reports whether the chain runs Standard Profile 1. A chain
// whose Config cannot be read (a partial stand-in) counts as not profile 1.
func (s *Service) profile1Chain() (p1 bool) {
	c, ok := s.cfg.chain.(interface{ Config() *params.ChainConfig })
	if !ok {
		return false
	}
	defer func() {
		if recover() != nil {
			p1 = false
		}
	}()
	return c.Config().IsProfile1()
}

// profile1CheckImported runs the deferred proposal check on a block that an
// import path has just applied. On profile 1 the engine casts Round 1 only on
// a checked block (A13, AM19, AM23): an import executes the block but does
// not apply the vote-time clock bound, so a block that reached this node only
// by an import path (fetch by hash, an already-stored re-push or re-gossip, a
// push whose check gave up) still needs the check to be voted. On an applied
// block the check is the static header rules plus the clock bound. No-op on
// other chains.
func (s *Service) profile1CheckImported(blk block.IBlock) {
	if blk == nil || !s.profile1Chain() || !s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		return
	}
	if _, known := s.p1Verdicts.get(blk.Hash()); known {
		return
	}
	s.deferredCheck(blk)
}

// maxProfile1Verdicts bounds the remembered final verdicts of the deferred
// proposal check.
const maxProfile1Verdicts = 256

// profile1Verdicts remembers the final verdict (passed or refused) of the
// deferred proposal check per block hash, so an import path does not run it
// a second time: a refused block stays refused (a timestamp refused by the
// clock bound must not pass a later re-check once the clock has moved on),
// and a passed block is not verified twice.
type profile1Verdicts struct {
	mu    gosync.Mutex
	m     map[types.Hash]bool
	order []types.Hash
}

func (v *profile1Verdicts) get(h types.Hash) (passed, known bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	passed, known = v.m[h]
	return passed, known
}

func (v *profile1Verdicts) put(h types.Hash, passed bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = make(map[types.Hash]bool)
	}
	if _, ok := v.m[h]; !ok {
		if len(v.order) >= maxProfile1Verdicts {
			delete(v.m, v.order[0])
			v.order = v.order[1:]
		}
		v.order = append(v.order, h)
	}
	v.m[h] = passed
}
