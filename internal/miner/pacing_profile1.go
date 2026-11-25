// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 block pacing (mixed production standard A17/F1, AM27): slots are
// counted from the genesis timestamp G; a leader stamps the start of its
// proposal's slot, G + s*I, and publishes no earlier than that.

package miner

import (
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// maxHeldParents bounds the parent-held table; pacing only ever looks at the
// parent of the block being built, so a few recent entries suffice.
const maxHeldParents = 64

// parentHeld records when this node first held each recent block: when it
// sealed the block itself, or, for a block it imported, when the first build
// on it was triggered (consensus triggers a build only once the parent is
// held, so that time is never earlier than the real one: the wait can only
// be longer than the rule asks, never shorter).
type parentHeld struct {
	mu sync.Mutex
	at map[types.Hash]time.Time
}

// note records t as the time hash was held unless an earlier time is known.
func (p *parentHeld) note(hash types.Hash, t time.Time) {
	if hash == (types.Hash{}) {
		return
	}
	if t.IsZero() {
		t = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.at == nil {
		p.at = make(map[types.Hash]time.Time)
	}
	if old, ok := p.at[hash]; ok && !t.Before(old) {
		return
	}
	if len(p.at) >= maxHeldParents {
		p.evictOldestLocked()
	}
	p.at[hash] = t
}

func (p *parentHeld) evictOldestLocked() {
	var oldest types.Hash
	var oldestAt time.Time
	for h, t := range p.at {
		if oldestAt.IsZero() || t.Before(oldestAt) {
			oldest, oldestAt = h, t
		}
	}
	delete(p.at, oldest)
}

// heldAt returns when hash was first held, recording now if it is unknown.
func (p *parentHeld) heldAt(hash types.Hash) time.Time {
	now := time.Now()
	p.note(hash, now)
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.at[hash]; ok {
		return t
	}
	return now
}

// pace holds a build until the pacing rule of its chain allows the seal. On a
// Profile-1 chain that is blockIntervalMs (from the genesis, copied into the
// miner config at start-up) after the parent was first held (A17); elsewhere
// it is the legacy wall-clock grid of paceBlock.
func (w *worker) pace(num uint64, parent types.Hash) error {
	if !w.chainConfig.IsProfile1() {
		return w.paceBlock(num)
	}
	iv := w.minerConf.BlockIntervalMs
	if iv == 0 || !w.isRunning() || parent == (types.Hash{}) {
		return nil
	}
	wait := time.Until(w.parentHeld.heldAt(parent).Add(time.Duration(iv) * time.Millisecond))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}

// profile1SlotSeconds is the slot interval I in seconds: the genesis
// blockIntervalMs, or the miner's copy of it when the chain config carries
// none (tests). 0 when it is not a positive whole number of seconds.
func (w *worker) profile1SlotSeconds() uint64 {
	if iv := w.chainConfig.Profile1SlotSeconds(); iv != 0 {
		return iv
	}
	if ms := w.minerConf.BlockIntervalMs; ms != 0 && ms%1000 == 0 {
		return ms / 1000
	}
	return 0
}

// profile1Target is the A17 slot start for a block on parent proposed at
// now: G + s*I with s = max(sP+1, ceil((now-G)/I)). ok is false when the
// parent or genesis header or the interval is unavailable.
func (w *worker) profile1Target(parent types.Hash, now time.Time) (uint64, bool) {
	iv := w.profile1SlotSeconds()
	if iv == 0 || parent == (types.Hash{}) || w.chain == nil {
		return 0, false
	}
	gi := w.chain.GetHeaderByNumber(uint256.NewInt(0))
	g, ok := gi.(*block.Header)
	if gi == nil || !ok || g == nil {
		return 0, false
	}
	pi, err := w.chain.GetHeaderByHash(parent)
	if err != nil || pi == nil {
		return 0, false
	}
	ph, ok := pi.(*block.Header)
	if !ok || ph == nil {
		return 0, false
	}
	return params.Profile1SlotTime(g.Time, iv, ph.Time, now), true
}

// waitUntilUnix holds the caller until the wall clock reaches the unix second
// t (A17: publish no earlier than the slot start).
func (w *worker) waitUntilUnix(t uint64) error {
	wait := time.Until(time.Unix(int64(t), 0))
	if wait <= 0 || !w.isRunning() {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}
