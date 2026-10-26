// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// mvsApplyCheck is the store check's apply-walk part (check 3,
// N42_MVS_CHECK=1; checks 1, 2 and 4 are parallel.Executor.CheckStore):
// every address appears once in the walk's output; a full-write account's
// walked value and a credited account's walked delta equal what the store's
// entry gives (found by lookup, not by the walk); and the account fold
// moves a credited account's balance (live object or pending increase) by
// exactly that delta. Violations are logged (error, "mvs check:").
type mvsApplyCheck struct {
	block        uint64
	numTxs       int
	base         *parallel.BaseCache // the block's base reads (expected balances)
	storeIssues  int                 // what CheckStore found
	storeWrites  int
	storeEntries int

	mu                         sync.Mutex
	issues, unchecked, credits int
	logged                     int
	took                       time.Duration
}

// mvsCheckBlocks and mvsCheckFailedBlocks count the blocks the check ran on
// and those it found violations in.
var mvsCheckBlocks, mvsCheckFailedBlocks, mvsCheckNanos, mvsCheckApplyNanos atomic.Int64

// mvsCreditProbe is one walked account with the state's balance before the fold.
type mvsCreditProbe struct {
	addr    types.Address
	value   []byte
	delta   *uint256.Int
	pre     uint256.Int
	preKind int
}

func (c *mvsApplyCheck) report(msg string, kv ...any) {
	c.mu.Lock()
	c.issues++
	c.logged++
	n := c.logged
	c.mu.Unlock()
	if n > parallel.MVSCheckMaxReports {
		return
	}
	log.Error(msg, append([]any{"block", c.block, "numTxs", c.numTxs, "presize", parallel.PresizeEnabled()}, kv...)...)
}

// before checks the walk's addresses are distinct and records the credited
// accounts' balances before the fold.
func (c *mvsApplyCheck) before(ps []mvsCreditProbe, ibs *state.IntraBlockState) {
	t0 := time.Now()
	defer func() { c.took += time.Since(t0) }()
	seen := make(map[types.Address]int, len(ps))
	for i := range ps {
		p := &ps[i]
		if j, dup := seen[p.addr]; dup {
			c.report("mvs check: address walked twice", "check", 3, "addr", p.addr.Hex(), "first", j, "again", i,
				"firstDelta", deltaString(ps[j].delta), "againDelta", deltaString(p.delta))
			continue
		}
		seen[p.addr] = i
	}
	mvsCheckParallel(len(ps), func(i int) {
		if p := &ps[i]; p.value == nil && p.delta != nil {
			p.pre, p.preKind = ibs.BalanceProbe(p.addr)
		}
	})
}

// mvsCheckParallel calls fn(i) for i in [0, n) on a few goroutines: the
// state's maps and the store are only read while the check runs.
func mvsCheckParallel(n int, fn func(i int)) {
	workers := min(runtime.GOMAXPROCS(0), 16)
	if n < 4096 || workers < 2 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	const chunk = 1024
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				lo := int(next.Add(chunk) - chunk)
				if lo >= n {
					return
				}
				for i := lo; i < min(lo+chunk, n); i++ {
					fn(i)
				}
			}
		}()
	}
	wg.Wait()
}

// after compares the walk with the store and the fold's result.
func (c *mvsApplyCheck) after(ps []mvsCreditProbe, mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) {
	t0 := time.Now()
	defer func() { c.took += time.Since(t0) }()
	var credits, unchecked atomic.Int64
	mvsCheckParallel(len(ps), func(i int) {
		p := &ps[i]
		ac := mvs.CheckAccount(parallel.LocationKey{Address: p.addr, Field: parallel.FieldBalance}, numTxs)
		if p.value != nil || p.delta == nil {
			if p.value != nil && (!ac.HasFull || !bytes.Equal(ac.Composed(), p.value)) {
				c.report("mvs check: walked account differs from its entry", "check", 3, "addr", p.addr.Hex(), "entryFull", ac.HasFull, "deltas", ac.NDeltas)
			}
			return
		}
		credits.Add(1)
		if ac.HasFull || ac.Delta.Cmp(p.delta) != 0 {
			c.report("mvs check: walked credit differs from its entry", "check", 3, "addr", p.addr.Hex(), "walkDelta", p.delta.String(),
				"entryDelta", ac.Delta.String(), "deltas", ac.NDeltas, "entryFull", ac.HasFull, "entryFound", ac.Found)
		}
		post, postKind := ibs.BalanceProbe(p.addr)
		var base uint256.Int
		baseKnown := false
		if c.base != nil {
			if acc, ok := c.base.Peek(p.addr); ok {
				baseKnown = true
				if acc != nil {
					base = acc.Balance
				}
			}
		}
		var got, want uint256.Int
		switch {
		case postKind == state.BalanceProbeNone:
			c.report("mvs check: credit missing after the fold", "check", 3, "addr", p.addr.Hex(), "delta", p.delta.String(), "deltas", ac.NDeltas, "preKind", p.preKind)
			return
		case postKind == p.preKind || (p.preKind == state.BalanceProbeNone && postKind == state.BalanceProbePending):
			// The same holder before and after: it moved by the credit.
			got.Sub(&post, &p.pre)
			want = *p.delta
		case baseKnown:
			// The fold loaded the account: base + earlier increase + credit.
			got = post
			want.Add(&base, &p.pre)
			want.Add(&want, p.delta)
		default:
			unchecked.Add(1)
			return
		}
		if got.Cmp(&want) != 0 {
			var expect uint256.Int
			expect.Add(&base, &p.pre)
			expect.Add(&expect, &ac.Delta)
			c.report("mvs check: fold credit mismatch", "check", 3, "addr", p.addr.Hex(), "got", got.String(), "want", want.String(),
				"expectedFinal", expect.String(), "baseKnown", baseKnown, "post", post.String(), "postKind", postKind, "pre", p.pre.String(), "preKind", p.preKind,
				"walkDelta", p.delta.String(), "entryDelta", ac.Delta.String(), "deltas", ac.NDeltas)
		}
	})
	c.credits += int(credits.Load())
	c.unchecked += int(unchecked.Load())
}

// summary logs the block's result: an error line when anything failed, a
// debug line otherwise.
func (c *mvsApplyCheck) summary(took time.Duration) {
	kv := []any{"block", c.block, "numTxs", c.numTxs, "presize", parallel.PresizeEnabled(), "storeIssues", c.storeIssues, "applyIssues", c.issues,
		"entries", c.storeEntries, "writes", c.storeWrites, "credits", c.credits, "unchecked", c.unchecked, "us", took.Microseconds()}
	mvsCheckBlocks.Add(1)
	mvsCheckNanos.Add(int64(took))
	mvsCheckApplyNanos.Add(int64(c.took))
	if c.storeIssues+c.issues > 0 {
		mvsCheckFailedBlocks.Add(1)
		log.Error("mvs check: block failed", kv...)
		return
	}
	log.Debug("mvs-check ok", kv...)
}

func deltaString(d *uint256.Int) string {
	if d == nil {
		return "nil"
	}
	return d.String()
}
