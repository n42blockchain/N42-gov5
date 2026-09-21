// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// S11 diagnostics for the commitWork pre-fill path (docs/QS_BLOCK_TIME_BUDGET.md
// 6by): round 35zzy found one 52 s leader build-queue stall between
// "miner: commitWork begin" and "miner: parallel fill", with nothing in the
// logs able to say what the build was waiting on. This file adds a per-step
// timer summary (prefillTimes, logged from commitWork/fillTransactions in
// worker.go) and a stall watchdog that dumps every goroutine's stack if a
// build has not reached its fill within a few seconds. Both are gated behind
// N42_BUILD_STALL_DIAG=1 and change no block-production behavior.

package miner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

// buildStallDiagEnabled gates the pre-fill step timers and the watchdog
// below. Read once at start-up, the same pattern as this package's other
// env switches (parallelFillEnabled's sibling N42_MINER_ADOPT_APPENDS,
// N42_PUSH_BEFORE_WRITE): off by default, so a round that has not opted in
// pays nothing -- no extra time.Now() calls, no timer allocation.
var buildStallDiagEnabled = os.Getenv("N42_BUILD_STALL_DIAG") == "1"

// buildStallThreshold is how long a build may run past "commitWork begin"
// without reaching its fill before the watchdog dumps every goroutine's
// stack. Fixed, not env-tunable: this is a tripwire for one specific shape
// (6by's 52 s stall), not an operating knob.
const buildStallThreshold = 3 * time.Second

// buildStallDumpMinInterval rate-limits the stack dump to at most one per
// process per interval: one dump already answers the question a round is
// asking, and a wedged fleet retrying the same stall must not turn into a
// dump-per-attempt storm.
const buildStallDumpMinInterval = 60 * time.Second

// buildStallDumpCap bounds the growing dump buffer (runtime.Stack keeps
// asking for more when the true dump is larger than the buffer).
const buildStallDumpCap = 64 << 20 // 64 MiB

// lastBuildStallDumpUnixNano is process-wide, not per-watchdog: the rate
// limit is "at most one dump per process per interval", not per build.
var lastBuildStallDumpUnixNano atomic.Int64

// allowBuildStallDump reports whether a dump may be written now, and if so
// atomically claims the slot so a concurrent caller (in practice: at most
// one build per node, but the check is written to be safe regardless)
// cannot also claim it.
func allowBuildStallDump(now time.Time) bool {
	nowNano := now.UnixNano()
	for {
		last := lastBuildStallDumpUnixNano.Load()
		if nowNano-last < int64(buildStallDumpMinInterval) {
			return false
		}
		if lastBuildStallDumpUnixNano.CompareAndSwap(last, nowNano) {
			return true
		}
	}
}

// buildStallWatchdog fires at most once if a build does not reach its fill
// within buildStallThreshold of being armed. Armed at "miner: commitWork
// begin"; cancelled at the first sign of progress (the fill starting) or
// when the build is abandoned (any early return unwinds via the caller's
// deferred Cancel). Costs one time.AfterFunc per build when the diagnostic
// is enabled; nil (zero cost, every method a no-op) otherwise.
type buildStallWatchdog struct {
	timer *time.Timer
	start time.Time
	dir   string // datadir log directory ("" -> dump goes to stderr)

	mu       sync.Mutex
	step     string
	blockNum uint64
}

// newBuildStallWatchdog arms the watchdog if diag is enabled, else returns
// nil. dir is the directory a stack dump is written into ("" falls back to
// stderr); pass log.LogDir().
func newBuildStallWatchdog(diag bool, dir string) *buildStallWatchdog {
	return newBuildStallWatchdogWithThreshold(diag, dir, buildStallThreshold)
}

// newBuildStallWatchdogWithThreshold is newBuildStallWatchdog with an
// injectable threshold, so a test can arm-and-wait in milliseconds instead
// of buildStallThreshold's real 3 s.
func newBuildStallWatchdogWithThreshold(diag bool, dir string, threshold time.Duration) *buildStallWatchdog {
	if !diag {
		return nil
	}
	wd := &buildStallWatchdog{start: time.Now(), dir: dir, step: "commitWork begin"}
	wd.timer = time.AfterFunc(threshold, wd.fire)
	return wd
}

// SetStep records the pre-fill step currently in progress, reported in the
// stall log line and, if the watchdog fires mid-step, in the dump filename's
// neighbouring log entry. Nil-safe: every method here is a no-op on a nil
// watchdog so call sites never need a diag-enabled check of their own.
func (wd *buildStallWatchdog) SetStep(step string) {
	if wd == nil {
		return
	}
	wd.mu.Lock()
	wd.step = step
	wd.mu.Unlock()
}

// SetBlockNumber records the block under construction once prepareWork
// resolves it, for the dump file name. Left at zero if the stall fires
// before that resolves -- unusual in its own right and worth knowing.
func (wd *buildStallWatchdog) SetBlockNumber(n uint64) {
	if wd == nil {
		return
	}
	wd.mu.Lock()
	wd.blockNum = n
	wd.mu.Unlock()
}

// Cancel disarms the watchdog. Safe to call more than once (idempotent) and
// safe to race a firing timer: a dump that fires a few microseconds after
// the fill actually started is a harmless false positive, rate-limited to
// one per process anyway, not a correctness issue.
func (wd *buildStallWatchdog) Cancel() {
	if wd == nil {
		return
	}
	wd.timer.Stop()
}

// fire runs on its own goroutine (time.AfterFunc's contract): it holds none
// of the miner/pool/chain locks the stalled build might itself be waiting
// on, so the dump can never deadlock against the stall it is diagnosing.
func (wd *buildStallWatchdog) fire() {
	wd.mu.Lock()
	step, blockNum := wd.step, wd.blockNum
	wd.mu.Unlock()
	elapsed := time.Since(wd.start)

	path := ""
	if allowBuildStallDump(time.Now()) {
		path = writeGoroutineDump(wd.dir, blockNum)
	}
	log.Warn("miner: build stalled before fill", "number", blockNum, "step", step,
		"elapsedMs", elapsed.Milliseconds(), "dump", path)
}

// writeGoroutineDump captures every goroutine's stack (runtime.Stack(_, true),
// buffer grown until the dump fits or the cap is hit) and writes it to
// <dir>/build-stall-<blockNum>-<unixsec>.stacks. Falls back to the process's
// stderr when dir is empty (log directory unreachable) or the file cannot be
// created, and returns "" in that case; otherwise returns the path written.
func writeGoroutineDump(dir string, blockNum uint64) string {
	buf := make([]byte, 64<<10) // 64 KiB start, doubles until it fits
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		if len(buf) >= buildStallDumpCap {
			break // dump is truncated at the cap; still useful
		}
		buf = make([]byte, len(buf)*2)
	}

	if dir == "" {
		os.Stderr.Write(buf)
		return ""
	}
	name := fmt.Sprintf("build-stall-%d-%d.stacks", blockNum, time.Now().Unix())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		os.Stderr.Write(buf)
		return ""
	}
	return path
}

// prefillTimes carries S11's per-step timings from commitWork into
// fillTransactions, where the pre-fill total completes (pending-pool
// snapshot + stale trim) and the one "miner: prefill phases" summary line is
// logged, immediately before the parallel/serial fill decision -- the exact
// boundary 6by asked to be able to explain. Nil when the diagnostic is off:
// every accumulation below is guarded by a nil check at the call site, so
// steady state pays no extra work.
type prefillTimes struct {
	buildStart time.Time

	align          time.Duration // AlignAppliedBranch call(s)
	lockWait       time.Duration // bc.lock wait inside align/insertParent -- shared with import/write
	insertParent   time.Duration // InsertChainAuthorized call(s) (parent not yet applied/imported)
	persistWait    time.Duration // WaitBlockPersisted -- waiting for the parent to be applied
	roTxBegin      time.Duration // opening the state reader: DB().BeginRo
	specTreeReload time.Duration // NewMinerRootComputer: speculative-tree reload/undo peel
	rootLockWait   time.Duration // minerRCMu wait inside NewMinerRootComputer -- shared with the startup pre-warm
	headerPrepare  time.Duration // prepareWork (header assembly + engine.Prepare)
	blockStart     time.Duration // ProcessExecutionBlockStart (EIP-4788/2935 system calls)
}

// logIfSlow emits "miner: prefill phases" when the elapsed time since
// buildStart exceeds 50 ms, so steady-state builds (sub-millisecond pre-fill)
// stay quiet and only a genuine outlier -- the shape 6by found -- writes a
// line. pendingSnapshot/trim are fillTransactions' own timings (dPending/
// dTrim), passed in rather than stored on the struct since they are only
// ever read once, right here.
func (pf *prefillTimes) logIfSlow(blockNumber uint64, pendingSnapshot, trim time.Duration) {
	if pf == nil {
		return
	}
	total := time.Since(pf.buildStart)
	if total <= 50*time.Millisecond {
		return
	}
	// Field names deliberately differ from "miner: build phases"' "align"
	// (elapsed-since-start through pacing) -- alignCall here is only the
	// AlignAppliedBranch call itself, so the two lines are not comparable
	// field-for-field.
	log.Info("miner: prefill phases",
		"n", blockNumber,
		"alignCall", pf.align, "lockWait", pf.lockWait, "insertParent", pf.insertParent,
		"persistWait", pf.persistWait, "roTxBegin", pf.roTxBegin,
		"specTreeReload", pf.specTreeReload, "rootLockWait", pf.rootLockWait,
		"headerPrepare", pf.headerPrepare, "blockStart", pf.blockStart,
		"pendingSnapshot", pendingSnapshot, "trim", trim,
		"total", total, "tMs", time.Now().UnixMilli())
}
