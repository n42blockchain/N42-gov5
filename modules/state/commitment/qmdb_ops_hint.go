// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Root ops from the run (r100a; design r97 I2(a) and I3, owner decision
// OD-1). A block the transfer lanes applied (internal/transfer_lanes.go)
// knows every account it changed and its final value before the fold and
// the pre-hook walk have rebuilt them: one final account per sender, base +
// summed credits per pure recipient. AccountOpsHint carries those finals,
// each with its QMDB op (key hash, encoded value) already computed and the
// whole set sorted by key hash, built off the import chain while the block
// is folded. ComputeRootHinted then takes an account's op from the hint
// when the dirty set's account equals the hint's (the op is a function of
// the address and the account alone, so it is the op buildAccountOps would
// make), computes the rest as today, and merges instead of sorting.
//
// The dirty set handed to the root stays the only authority: an account
// the hint lacks, holds with another value, or that the dirty set omits
// (the unchanged-account rule) is handled exactly as today. A hint that is
// not ready when the root starts is ignored.
//
// N42_ROOT_OPS_FROM_RUN: unset or "0" off (default; no hint is built); "1"
// (or "true", "on") uses the hint; "check" (or "2") builds the ops both
// ways, compares them element by element (key hash, value, order), logs an
// Error on any difference (a panic in test binaries) and uses today's.

package commitment

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
)

// EnvRootOpsFromRun names the switch.
const EnvRootOpsFromRun = "N42_ROOT_OPS_FROM_RUN"

const (
	rootOpsOff = iota
	rootOpsOn
	rootOpsCheck
)

// Read once at init (Set...ForTest in tests).
var rootOpsFromRunMode = parseRootOpsMode(os.Getenv(EnvRootOpsFromRun))

func parseRootOpsMode(v string) int {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return rootOpsOn
	case "2", "check":
		return rootOpsCheck
	}
	return rootOpsOff
}

func rootOpsModeName(m int) string {
	switch m {
	case rootOpsOn:
		return "on"
	case rootOpsCheck:
		return "check"
	}
	return "off"
}

func setRootOpsMode(p *int, mode string) func() {
	prev := *p
	*p = parseRootOpsMode(mode)
	if mode == "off" {
		*p = rootOpsOff
	}
	return func() { *p = prev }
}

// SetRootOpsFromRunForTest sets N42_ROOT_OPS_FROM_RUN ("off", "on" or
// "check") and returns the restore.
func SetRootOpsFromRunForTest(mode string) func() { return setRootOpsMode(&rootOpsFromRunMode, mode) }

// RootOpsFromRunModeName is the switch for the stamps.
func RootOpsFromRunModeName() string { return rootOpsModeName(rootOpsFromRunMode) }

// RootOpsHintWanted reports whether a lane block should hand its root a
// hint.
func RootOpsHintWanted() bool { return rootOpsFromRunMode != rootOpsOff }

// rootOpsStats: hinted roots, their check comparisons and differences
// (must stay 0), and hints not ready or unusable when the root started.
var rootOpsStats struct {
	hinted, checked, mismatches, late, unusable atomic.Uint64
}

// RootOpsStats returns the counters since start.
func RootOpsStats() (hinted, checked, mismatches, late, unusable uint64) {
	return rootOpsStats.hinted.Load(), rootOpsStats.checked.Load(), rootOpsStats.mismatches.Load(),
		rootOpsStats.late.Load(), rootOpsStats.unusable.Load()
}

// HintAccount is one final account of the run.
type HintAccount struct {
	Addr types.Address
	Acct account.StateAccount
}

// AccountOpsHint is a lane block's final accounts with their ops, sorted by
// key hash. Build fills it once; the root reads it only after done.
type AccountOpsHint struct {
	// cache: N42_ROOT_OPS_FROM_RUN was on when the hint was made (else the
	// hint only marks a lane block).
	cache bool
	done  chan struct{}
	// Written by Build before done is closed.
	ops   []qmdb.Op              // sorted by key hash
	accts []account.StateAccount // the account each op encodes
	pos   map[types.Address]int32
	bad   string // why the hint cannot be used ("" when it can)
	took  time.Duration
}

// NewAccountOpsHint returns an empty hint for a lane block; Build (or
// MarkBuilt when there is nothing to cache) must follow.
func NewAccountOpsHint() *AccountOpsHint {
	return &AccountOpsHint{cache: rootOpsFromRunMode != rootOpsOff, done: make(chan struct{})}
}

// Caches reports whether the hint is meant to carry ops (Build has work).
func (h *AccountOpsHint) Caches() bool { return h != nil && h.cache }

// MarkBuilt closes a hint that carries no ops.
func (h *AccountOpsHint) MarkBuilt() { close(h.done) }

// ready reports whether Build has finished, without waiting.
func (h *AccountOpsHint) ready() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// Took is Build's run time (0 until ready).
func (h *AccountOpsHint) Took() time.Duration {
	if h == nil || !h.ready() {
		return 0
	}
	return h.took
}

// Build computes every final's op and sorts them by key hash. finals must
// name each address once (a repeated address makes the hint unusable). It
// may run on any goroutine; it closes the hint when done.
func (h *AccountOpsHint) Build(finals []HintAccount) {
	defer close(h.done)
	t0 := time.Now()
	defer func() { h.took = time.Since(t0) }()
	n := len(finals)
	type keyed struct {
		op qmdb.Op
		i  int32
	}
	items := make([]keyed, n)
	fill := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			f := &finals[i]
			items[i].i = int32(i)
			items[i].op = accountOp(f.Addr, &f.Acct)
		}
	}
	workers := min(runtime.GOMAXPROCS(0), 8, n/(accountOpsParallelMin/4))
	if n < accountOpsParallelMin || workers < 2 {
		fill(0, n)
	} else {
		var wg sync.WaitGroup
		per := (n + workers - 1) / workers
		for lo := 0; lo < n; lo += per {
			wg.Add(1)
			go func(lo, hi int) {
				defer wg.Done()
				fill(lo, hi)
			}(lo, min(lo+per, n))
		}
		wg.Wait()
	}
	slices.SortFunc(items, func(a, b keyed) int { return compareOps(a.op, b.op) })
	h.ops = make([]qmdb.Op, n)
	h.accts = make([]account.StateAccount, n)
	h.pos = make(map[types.Address]int32, n)
	for k := range items {
		f := &finals[items[k].i]
		h.ops[k] = items[k].op
		h.accts[k] = f.Acct
		if _, dup := h.pos[f.Addr]; dup {
			h.bad = fmt.Sprintf("address %x twice", f.Addr)
		}
		h.pos[f.Addr] = int32(k)
	}
}

// accountOp is buildAccountOps' op for one account.
func accountOp(addr types.Address, acct *account.StateAccount) qmdb.Op {
	op := qmdb.Op{KeyHash: qmdb.Hash(AccountKeyHash(addr))}
	if acct != nil && !isAccountEmpty(acct) {
		op.Value = EncodeAccountValue(acct)
	}
	return op
}

// RootOpsHintedComputer is a root computer that takes a run's hint (the
// QMDB computer).
type RootOpsHintedComputer interface {
	ComputeRootHinted(accounts map[types.Address]*account.StateAccount,
		storage map[types.Address]map[types.Hash]*uint256.Int, hint any) (types.Hash, error)
}

// opsHintStats is one hinted op build's stamps.
type opsHintStats struct {
	used                 bool // the hint's ops were taken (or compared, in check mode)
	hits, misses, unused int
	why                  string // why the hint was not used ("" when used)
	check                string // "same", "diff" or ""
}

// buildRootOpsToday is the root's op build without a hint: every account
// op and storage op, sorted by key hash.
func buildRootOpsToday(accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int) []qmdb.Op {
	// An op with a nil Value is a delete (ApplyOps' convention); every
	// live value here is non-empty (an encoded account or a 32-byte word).
	ops := buildAccountOps(accounts, len(storage))
	ops = appendStorageOps(ops, storage)
	sortOpsByKey(ops)
	return ops
}

// appendStorageOps appends storage's ops (a zero or nil value deletes).
func appendStorageOps(ops []qmdb.Op, storage map[types.Address]map[types.Hash]*uint256.Int) []qmdb.Op {
	for addr, slots := range storage {
		for slot, val := range slots {
			kh := qmdb.Hash(StorageKeyHash(addr, slot))
			if val == nil || val.IsZero() {
				ops = append(ops, qmdb.Op{KeyHash: kh})
			} else {
				var buf [32]byte
				val.WriteToSlice(buf[:])
				v := make([]byte, 32)
				copy(v, buf[:])
				ops = append(ops, qmdb.Op{KeyHash: kh, Value: v})
			}
		}
	}
	return ops
}

// buildRootOps is the root's op build for a block with hint h (nil: today's
// build): under N42_ROOT_OPS_FROM_RUN the hint's ops where the dirty set
// agrees with it, today's for the rest, merged in key order.
func buildRootOps(accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int, h *AccountOpsHint) ([]qmdb.Op, opsHintStats) {
	var st opsHintStats
	mode := rootOpsFromRunMode
	switch {
	case h == nil:
		return buildRootOpsToday(accounts, storage), st
	case !h.cache || mode == rootOpsOff:
		st.why = "off"
		return buildRootOpsToday(accounts, storage), st
	case !h.ready():
		st.why = "late"
		rootOpsStats.late.Add(1)
		return buildRootOpsToday(accounts, storage), st
	case h.bad != "":
		st.why = h.bad
		rootOpsStats.unusable.Add(1)
		return buildRootOpsToday(accounts, storage), st
	}
	ops := hintedRootOps(accounts, storage, h, &st)
	st.used = true
	rootOpsStats.hinted.Add(1)
	if mode == rootOpsCheck {
		ref := buildRootOpsToday(accounts, storage)
		st.check = "same"
		if d := diffOps(ref, ops); d != "" {
			st.check = "diff"
			noteRootOpsDiff("from-run", d)
		}
		rootOpsStats.checked.Add(1)
		return ref, st
	}
	return ops, st
}

// hintedRootOps builds the ops from the dirty set with h's ops taken where
// h holds the same account.
func hintedRootOps(accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int, h *AccountOpsHint, st *opsHintStats) []qmdb.Op {
	type entry struct {
		addr types.Address
		acct *account.StateAccount
	}
	es := make([]entry, 0, len(accounts))
	for addr, acct := range accounts {
		es = append(es, entry{addr, acct})
	}
	used := make([]bool, len(h.ops))
	type part struct {
		miss []qmdb.Op
		hits int
	}
	match := func(es []entry, p *part) {
		for i := range es {
			e := &es[i]
			if e.acct != nil {
				if j, ok := h.pos[e.addr]; ok && h.accts[j] == *e.acct {
					used[j] = true
					p.hits++
					continue
				}
			}
			p.miss = append(p.miss, accountOp(e.addr, e.acct))
		}
	}
	workers := min(runtime.GOMAXPROCS(0), 8, len(es)/(accountOpsParallelMin/4))
	var parts []part
	if len(es) < accountOpsParallelMin || workers < 2 {
		parts = make([]part, 1)
		match(es, &parts[0])
	} else {
		per := (len(es) + workers - 1) / workers
		parts = make([]part, (len(es)+per-1)/per)
		var wg sync.WaitGroup
		for k := range parts {
			lo := k * per
			wg.Add(1)
			go func(k, lo, hi int) {
				defer wg.Done()
				match(es[lo:hi], &parts[k])
			}(k, lo, min(lo+per, len(es)))
		}
		wg.Wait()
	}
	nRest := 0
	for k := range parts {
		st.hits += parts[k].hits
		nRest += len(parts[k].miss)
	}
	st.misses = nRest
	st.unused = len(h.ops) - st.hits
	rest := make([]qmdb.Op, 0, nRest+len(storage))
	for k := range parts {
		rest = append(rest, parts[k].miss...)
	}
	rest = appendStorageOps(rest, storage)
	sortOpsByKey(rest)
	if len(rest) == 0 && st.unused == 0 {
		return slices.Clone(h.ops)
	}
	out := make([]qmdb.Op, 0, st.hits+len(rest))
	j := 0
	for i := range h.ops {
		if !used[i] {
			continue
		}
		for j < len(rest) && compareOps(rest[j], h.ops[i]) < 0 {
			out = append(out, rest[j])
			j++
		}
		out = append(out, h.ops[i])
	}
	return append(out, rest[j:]...)
}

// diffOps returns the first difference between two op lists ("" when equal):
// length, then each element's key hash, delete/set and value bytes.
func diffOps(want, got []qmdb.Op) string {
	if len(want) != len(got) {
		return fmt.Sprintf("ops: today %d, hinted %d", len(want), len(got))
	}
	for i := range want {
		a, b := &want[i], &got[i]
		switch {
		case a.KeyHash != b.KeyHash:
			return fmt.Sprintf("op %d key: today %x, hinted %x", i, a.KeyHash, b.KeyHash)
		case (len(a.Value) == 0) != (len(b.Value) == 0):
			return fmt.Sprintf("op %d %x delete: today %v, hinted %v", i, a.KeyHash, len(a.Value) == 0, len(b.Value) == 0)
		case !bytes.Equal(a.Value, b.Value):
			return fmt.Sprintf("op %d %x value: today %x, hinted %x", i, a.KeyHash, a.Value, b.Value)
		}
	}
	return ""
}

// noteRootOpsDiff reports a check mode difference; a panic in test
// binaries.
func noteRootOpsDiff(which, diff string) {
	rootOpsStats.mismatches.Add(1)
	log.Error("root ops check: ops differ from today's build; today's used", "switch", which, "diff", diff)
	if testing.Testing() {
		panic("root ops check (" + which + "): " + diff)
	}
}
