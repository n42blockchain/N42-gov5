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

package parallel

import (
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

const (
	// MaxWaves is the safety limit on re-execution waves.
	// Block-STM converges in O(numTxs) waves worst-case.
	MaxWaves = 64
)

// TxExecuteFunc is called to execute a single transaction.
// It receives the transaction index and the read/write set to populate.
// Returns an error if execution fails (non-retryable).
type TxExecuteFunc func(txIndex int, rw *ReadWriteSet) error

// TxResult stores the execution result for a single transaction.
type TxResult struct {
	Err error // nil if successful
}

// Executor runs Block-STM wave-based parallel execution for a set of transactions.
//
// Algorithm:
//  1. Execute all transactions in parallel (initial wave).
//  2. Validate in order (tx0, tx1, ...). For each validation failure,
//     mark the failed tx and all later txs as needing re-execution.
//  3. Re-execute marked txs in parallel (next wave).
//  4. Repeat until all txs are validated.
//
// This is simpler than full Block-STM (no overlapping execute/validate phases)
// but still provides significant speedup for blocks with mostly independent txs.
type Executor struct {
	mvs     *MVS
	numTxs  int
	workers int

	// Per-transaction state (indexed by txIndex).
	status      []TxStatus
	incarnation []uint32
	rwSets      []*ReadWriteSet
	results     []TxResult

	// Execution function provided by caller.
	execFn TxExecuteFunc

	arena *txArena // per-transaction arrays on loan from arenaFree

	// workerSetup, when set, runs once per worker goroutine per wave and
	// returns a per-worker context handed to every execFn call that worker
	// makes, plus a teardown run when the worker exits. It exists so each
	// worker can own resources that cannot be shared across goroutines -- a
	// read transaction bound to its OS thread and a state reader over it
	// (3709ca6a: the workers used to share one MDBX cursor).
	workerSetup WorkerSetupFunc
	execCtxFn   TxExecuteWithCtxFunc

	// affinity, when set, pins every transaction with the same key to the
	// same worker, which executes its transactions in index order. A
	// sender's nonce chain then never conflicts with itself: each link reads
	// the previous link's write from the multi-version store, already there
	// because the same worker applied it. Round 35: without this, a block of
	// 4,000 senders x ~25 transactions each hit the 64-wave limit on every
	// block and fell back to sequential, 43 s for 163k.
	affinity func(txIndex int) uint64

	// Metrics.
	totalExecutions atomic.Int64
	totalAborts     atomic.Int64
	waves           int
	fellBack        bool
	execNanos       int64 // wall time in executeParallel across waves
	validateNanos   int64 // wall time in validateInOrder across waves
	traceLeft       int   // N42_PARALLEL_TRACE: validation failures still to log this Run

	// busyNanos sums, across every worker and every call, the wall time spent
	// inside e.exec (the transaction body: EVM run + state reads/writes). It
	// is measurement-only (S81, waveBusyMs): idle = workers*runMs - busyMs/workers
	// approximates time workers spent NOT inside a transaction body (blocked on
	// the scheduler's work handoff, or simply between waves).
	busyNanos atomic.Int64

	// workerStats holds one entry per worker goroutine from the most recent
	// executeParallel call (S82, measurement-only). Each goroutine only ever
	// writes its own index, so no atomics are needed on the hot per-tx path;
	// the slice itself is only read after wg.Wait() in executeParallel's
	// caller. It is overwritten (not accumulated) on every call, so it
	// reflects the last wave only -- the first (initial) wave dominates
	// transaction count on a full block, so that is the wave worth seeing.
	workerStats []workerStat

	// S83: distinct affinity keys and the longest per-key chain of the most
	// recent executeParallel call. Only computed in LPT mode (0 otherwise).
	waveKeys     int
	waveTopChain int
}

// waveLPT (N42_WAVE_LPT=1) replaces the modulo sender-to-worker assignment
// with longest-processing-time greedy assignment (S83). Read once at init.
var waveLPT = os.Getenv("N42_WAVE_LPT") == "1"

// partitionByAffinity splits txIndices (ascending) into one in-order queue per
// worker. Equal affinity keys always land on one worker. With lpt false the
// worker is key % workers; with lpt true keys are sorted by chain length
// (descending, ties by first appearance) and each goes to the least-loaded
// worker. keys and topChain are reported only in LPT mode.
func partitionByAffinity(txIndices []int, workers int, affinity func(int) uint64, lpt bool) (queues [][]int, keys, topChain int) {
	queues = make([][]int, workers)
	if !lpt {
		for _, idx := range txIndices {
			w := int(affinity(idx) % uint64(workers))
			queues[w] = append(queues[w], idx)
		}
		return queues, 0, 0
	}
	type chain struct {
		key   uint64
		count int
	}
	pos := make(map[uint64]int)
	var chains []chain
	for _, idx := range txIndices {
		k := affinity(idx)
		i, ok := pos[k]
		if !ok {
			i = len(chains)
			pos[k] = i
			chains = append(chains, chain{key: k})
		}
		chains[i].count++
	}
	order := make([]int, len(chains))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return chains[order[a]].count > chains[order[b]].count })
	load := make([]int, workers)
	assigned := make(map[uint64]int, len(chains))
	for _, ci := range order {
		best := 0
		for w := 1; w < workers; w++ {
			if load[w] < load[best] {
				best = w
			}
		}
		load[best] += chains[ci].count
		assigned[chains[ci].key] = best
		if chains[ci].count > topChain {
			topChain = chains[ci].count
		}
	}
	for _, idx := range txIndices {
		w := assigned[affinity(idx)]
		queues[w] = append(queues[w], idx)
	}
	return queues, len(chains), topChain
}

// workerStat is one worker's timings for a single executeParallel call
// (S82). All fields are wall time in nanoseconds except queueLen.
type workerStat struct {
	queueLen    int
	setupNs     int64
	wallNs      int64
	busyNs      int64
	mvsWriteNs  int64
	mvsDeleteNs int64
}

// WorkerStatsSummary aggregates the per-worker S82 timings from the most
// recent executeParallel call into the handful of numbers worth logging.
type WorkerStatsSummary struct {
	WallMaxMs     float64
	WallMinMs     float64
	WallMeanMs    float64
	QMax          int
	QMin          int
	SetupMs       int64
	MvsWriteMs    int64
	MvsDeleteMs   int64
	SlowestWorker int
	Keys          int // S83: distinct affinity keys (LPT mode only)
	TopChain      int // S83: longest per-key chain (LPT mode only)
}

// WorkerStats summarizes the per-worker timings recorded during the last
// executeParallel call (S82, measurement-only). Safe to call only after
// Run() has returned -- workerStats is written without synchronization
// under the assumption that each worker goroutine only touches its own
// slot and that the caller waits for all workers (wg.Wait) before reading.
func (e *Executor) WorkerStats() WorkerStatsSummary {
	var s WorkerStatsSummary
	if len(e.workerStats) == 0 {
		return s
	}
	var setupNs, mvsWriteNs, mvsDeleteNs, sumWallNs int64
	s.WallMinMs = -1
	s.QMin = -1
	for i, ws := range e.workerStats {
		wallMs := float64(ws.wallNs) / 1e6
		if wallMs > s.WallMaxMs {
			s.WallMaxMs = wallMs
			s.SlowestWorker = i
		}
		if s.WallMinMs < 0 || wallMs < s.WallMinMs {
			s.WallMinMs = wallMs
		}
		if s.QMin < 0 || ws.queueLen < s.QMin {
			s.QMin = ws.queueLen
		}
		if ws.queueLen > s.QMax {
			s.QMax = ws.queueLen
		}
		setupNs += ws.setupNs
		mvsWriteNs += ws.mvsWriteNs
		mvsDeleteNs += ws.mvsDeleteNs
		sumWallNs += ws.wallNs
	}
	s.Keys, s.TopChain = e.waveKeys, e.waveTopChain
	s.WallMeanMs = float64(sumWallNs) / float64(len(e.workerStats)) / 1e6
	s.SetupMs = setupNs / 1e6
	s.MvsWriteMs = mvsWriteNs / 1e6
	s.MvsDeleteMs = mvsDeleteNs / 1e6
	return s
}

// WorkerSetupFunc prepares one worker's private context. It is called on the
// worker's own goroutine, so anything it opens is used on the goroutine that
// opened it. teardown may be nil.
type WorkerSetupFunc func(workerID int) (ctx any, teardown func(), err error)

// TxExecuteWithCtxFunc is TxExecuteFunc with the worker context.
type TxExecuteWithCtxFunc func(ctx any, txIndex int, rw *ReadWriteSet) error

// NewExecutorWithWorkerSetup is NewExecutor for callers whose workers own
// resources: setup runs per worker goroutine, execFn receives that worker's
// context. A setup error fails every transaction that worker would have run,
// which surfaces as a block execution error rather than a silent fallback.
func NewExecutorWithWorkerSetup(numTxs int, workers int, setup WorkerSetupFunc, execFn TxExecuteWithCtxFunc) *Executor {
	e := NewExecutor(numTxs, workers, nil)
	e.workerSetup = setup
	e.execCtxFn = execFn
	return e
}

// SetAffinity pins transactions with equal keys to one worker, in index
// order (see the affinity field). Call before Run.
func (e *Executor) SetAffinity(key func(txIndex int) uint64) { e.affinity = key }

// NewExecutor creates a Block-STM executor for numTxs transactions.
// workers specifies the number of goroutines; 0 means runtime.NumCPU().
// txArena holds the per-transaction arrays of an Executor so a block's worth
// of them is reused by the next block instead of allocated again: at 163k
// transactions the read/write sets alone are 163k allocations of three
// objects each, ~60 ms of a follower's import setup (round 35zzb). Arenas
// are kept in arenaFree; Release hands one back once the caller has
// consumed the results and the MVS.
type txArena struct {
	rwSets      []*ReadWriteSet
	status      []TxStatus
	incarnation []uint32
	results     []TxResult
}

// arenaFree keeps released arenas across blocks. A sync.Pool was tried first
// and gave nothing back: the collector empties it on every cycle, and a full
// block's import allocates enough to trigger one, so every block started
// from an empty pool (round 35zzc: executorMs 54 ms, unchanged). At most
// arenaFreeMax arenas are kept -- one for the import path and one for the
// builder, which can overlap on a leader.
var arenaFree struct {
	mu   sync.Mutex
	list []*txArena
}

const arenaFreeMax = 2

func arenaTake() *txArena {
	arenaFree.mu.Lock()
	defer arenaFree.mu.Unlock()
	if n := len(arenaFree.list); n > 0 {
		a := arenaFree.list[n-1]
		arenaFree.list = arenaFree.list[:n-1]
		return a
	}
	return nil
}

// arenaGive keeps a released arena. When the list is full it replaces the
// smallest kept arena if the released one is larger: the list must converge
// on the biggest blocks seen, or two arenas kept during a leg's ramp (a
// 23k-transaction A leg, the first 85k block of a B leg) sit in it for the
// rest of the run and every full block allocates its 163k sets fresh
// (round 35zzh's profile: arenaFor -> NewReadWriteSet 0.45 s of 25, with
// the arena code in place).
func arenaGive(a *txArena) {
	arenaFree.mu.Lock()
	defer arenaFree.mu.Unlock()
	if len(arenaFree.list) < arenaFreeMax {
		arenaFree.list = append(arenaFree.list, a)
		return
	}
	smallest := 0
	for i, k := range arenaFree.list {
		if cap(k.rwSets) < cap(arenaFree.list[smallest].rwSets) {
			smallest = i
		}
	}
	if cap(arenaFree.list[smallest].rwSets) < cap(a.rwSets) {
		arenaFree.list[smallest] = a
	}
}

// arenaFor returns an arena sized for numTxs, reusing a kept one when it is
// large enough. Every per-transaction slot is reset.
func arenaFor(numTxs int) *txArena {
	a := arenaTake()
	if a != nil && cap(a.rwSets) < numTxs {
		// Too small: drop it rather than keep it for a smaller block. The
		// larger arena built below takes its place on Release.
		a = nil
	}
	if a == nil {
		a = &txArena{
			rwSets:      make([]*ReadWriteSet, numTxs),
			status:      make([]TxStatus, numTxs),
			incarnation: make([]uint32, numTxs),
			results:     make([]TxResult, numTxs),
		}
	} else {
		a.rwSets = a.rwSets[:numTxs]
		a.status = a.status[:numTxs]
		a.incarnation = a.incarnation[:numTxs]
		a.results = a.results[:numTxs]
		clear(a.status)
		clear(a.incarnation)
		clear(a.results)
	}
	// Only fill empty slots. A kept set is not touched here: executeSingle
	// clears it before every execution, its TxIndex is its slot, and
	// walking 163k scattered sets on a cold cache cost ~55 ms a block
	// (round 35zzf's profile: NewExecutor 64 ms with the arena kept).
	for i, rw := range a.rwSets {
		if rw == nil {
			a.rwSets[i] = NewReadWriteSet(i)
		}
	}
	return a
}

func NewExecutor(numTxs int, workers int, execFn TxExecuteFunc) *Executor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > numTxs {
		workers = numTxs
	}

	a := arenaFor(numTxs)
	return &Executor{
		mvs:         NewMVS(),
		numTxs:      numTxs,
		workers:     workers,
		status:      a.status,
		incarnation: a.incarnation,
		rwSets:      a.rwSets,
		results:     a.results,
		execFn:      execFn,
		arena:       a,
	}
}

// Release returns the executor's per-transaction arrays to the pool. Call it
// once the results and the MVS are no longer read; the executor must not be
// used afterwards.
func (e *Executor) Release() {
	if e.arena == nil {
		return
	}
	a := e.arena
	e.arena, e.rwSets, e.status, e.incarnation, e.results = nil, nil, nil, nil, nil
	arenaGive(a)
}

// Run executes all transactions using wave-based Block-STM.
func (e *Executor) Run() []TxResult {
	if e.numTxs == 0 {
		return nil
	}

	// For very small blocks, sequential is more efficient.
	if e.numTxs <= 2 {
		e.runSequential()
		return e.results
	}

	if os.Getenv("N42_PARALLEL_TRACE") != "" {
		e.traceLeft = 12
	}
	for wave := 0; wave < MaxWaves; wave++ {
		e.waves = wave + 1
		// Collect txs that need (re-)execution.
		pending := e.collectPending()
		if len(pending) == 0 {
			break // all validated
		}
		if e.traceLeft >= 0 && os.Getenv("N42_PARALLEL_TRACE") != "" {
			log.Info("parallel trace: wave", "wave", wave, "pending", len(pending), "txs", e.numTxs, "first", pending[0], "last", pending[len(pending)-1])
		}

		// Execute pending txs in parallel.
		t0 := time.Now()
		e.executeParallel(pending)
		t1 := time.Now()
		e.execNanos += t1.Sub(t0).Nanoseconds()

		// Validate in order. On first failure, mark it and all later txs as pending.
		allValid := e.validateInOrder()
		e.validateNanos += time.Since(t1).Nanoseconds()
		if allValid {
			break
		}
	}

	// Safety check: if not all validated after MaxWaves, fall back to sequential.
	if !e.allValidated() {
		log.Warn("Block-STM: wave limit reached, sequential fallback",
			"txs", e.numTxs,
			"waves", MaxWaves,
			"executions", e.totalExecutions.Load(),
			"aborts", e.totalAborts.Load(),
		)
		e.fellBack = true
		e.mvs = NewMVS()
		e.runSequential()
		return e.results
	}

	log.Debug("Block-STM execution completed",
		"txs", e.numTxs,
		"executions", e.totalExecutions.Load(),
		"aborts", e.totalAborts.Load(),
	)

	return e.results
}

// collectPending returns indices of txs that need (re-)execution.
func (e *Executor) collectPending() []int {
	// Only transactions the validator marked pending re-execute (with their
	// incarnation already advanced). An executed-but-unvalidated transaction
	// is provisional and gets re-VALIDATED next pass, not re-run: re-running
	// it here rewrote its value under an unchanged incarnation, which a
	// dependent that had recorded that incarnation could never detect.
	var pending []int
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] == StatusPending {
			pending = append(pending, i)
		}
	}
	return pending
}

// executeParallel executes the given tx indices in parallel using the worker pool.
func (e *Executor) executeParallel(txIndices []int) {
	var wg sync.WaitGroup
	// With an affinity key, each worker gets its own in-order queue of the
	// transactions that hash to it; without one, a shared channel.
	var queues [][]int
	work := make(chan int, len(txIndices))
	if e.affinity != nil {
		queues, e.waveKeys, e.waveTopChain = partitionByAffinity(txIndices, e.workers, e.affinity, waveLPT)
	}

	// S82: fresh per-call worker stats. Each goroutine below writes only its
	// own index, so no synchronization is needed on the hot path.
	e.workerStats = make([]workerStat, e.workers)

	// Start workers.
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			tWallStart := time.Now()
			ws := &e.workerStats[workerID]
			defer wg.Done()
			defer func() {
				ws.wallNs = int64(time.Since(tWallStart))
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Error("panic in parallel executor worker, recovered", "panic", r, "stack", string(buf[:n]))
				}
			}()
			var ctx any
			var setupErr error
			if e.workerSetup != nil {
				tSetup := time.Now()
				var teardown func()
				ctx, teardown, setupErr = e.workerSetup(workerID)
				ws.setupNs = int64(time.Since(tSetup))
				if teardown != nil {
					defer teardown()
				}
			}
			run := func(txIndex int) {
				if setupErr != nil {
					// A kept set would carry the previous block's reads into
					// validation and re-execute for MaxWaves before the
					// sequential fallback reports the setup error.
					if rw := e.rwSets[txIndex]; rw != nil {
						rw.Clear()
					}
					e.results[txIndex] = TxResult{Err: setupErr}
					e.status[txIndex] = StatusExecuted
					return
				}
				e.executeSingle(ctx, txIndex, ws)
			}
			if queues != nil {
				ws.queueLen = len(queues[workerID])
				for _, txIndex := range queues[workerID] {
					run(txIndex)
				}
				return
			}
			for txIndex := range work {
				ws.queueLen++
				run(txIndex)
			}
		}(i)
	}

	// Feed work (channel mode only).
	if queues == nil {
		for _, idx := range txIndices {
			work <- idx
		}
	}
	close(work)

	wg.Wait()
}

// executeSingle executes a single transaction. ws is the calling worker's
// S82 stat slot, or nil from runSequential (which has no worker to charge).
func (e *Executor) executeSingle(ctx any, txIndex int, ws *workerStat) {
	e.totalExecutions.Add(1)

	// Reuse the transaction's read/write set. NewExecutor allocates one per
	// transaction; a fresh one per execution made that allocation dead and
	// added another 163k (two slices each) per full block on the import's
	// setup, plus one per re-execution. The MVS copies every value it
	// stores, so the descriptor arrays can be overwritten once the previous
	// incarnation's writes are withdrawn.
	//
	// Withdraw only that incarnation's keys: DeleteAll walks every entry in
	// the store and was 95% of a follower's CPU at 54k transactions (35e).
	tDelete := time.Now()
	rw := e.rwSets[txIndex]
	if rw != nil {
		for _, wd := range rw.Writes {
			e.mvs.Delete(wd.Key, txIndex)
		}
		rw.Clear()
	} else {
		rw = NewReadWriteSet(txIndex)
	}
	if ws != nil {
		ws.mvsDeleteNs += int64(time.Since(tDelete))
	}

	// Execute the transaction.
	tCall := time.Now()
	err := e.exec(ctx, txIndex, rw)
	busy := int64(time.Since(tCall))
	e.busyNanos.Add(busy)
	if ws != nil {
		ws.busyNs += busy
	}

	e.results[txIndex] = TxResult{Err: err}
	e.rwSets[txIndex] = rw

	// Apply writes to MVS with incarnation tag.
	tWrite := time.Now()
	inc := e.incarnation[txIndex]
	for _, wd := range rw.Writes {
		if wd.Delta != nil {
			e.mvs.WriteDelta(wd.Key, txIndex, inc, wd.Delta)
			continue
		}
		e.mvs.Write(wd.Key, txIndex, inc, wd.Value)
	}
	if ws != nil {
		ws.mvsWriteNs += int64(time.Since(tWrite))
	}

	e.status[txIndex] = StatusExecuted
}

// validateInOrder validates all executed txs in order.
// On first failure, marks the failed tx and all later txs as pending.
// Returns true if all txs are validated.
func (e *Executor) validateInOrder() bool {
	// A pass validates every executed transaction in order. A failure at i
	// re-executes i alone (incarnation++); every transaction after i is
	// provisional from then on -- it may have read i's old write -- so at the
	// end of the pass everything after the FIRST failure is demoted to
	// Executed and re-validated next pass, when i's new write is in the
	// store. Transactions that fail later in the same pass are marked pending
	// too, so independent conflicts are all re-executed in one wave rather
	// than one per wave. Re-executing every later transaction on the first
	// failure (the old rule) cost a wave per conflict; round 35 hit the
	// 64-wave limit on every block with it.
	// The checks are independent (the store is quiescent between waves), so
	// they run on the worker count in strides; only the status pass below is
	// in order. Serial, this was 595 ms of a 163k-transaction block's 2.1 s
	// (round 35f: ten passes over the whole block).
	valid := e.validateExecuted()
	firstFail := -1
	for i := 0; i < e.numTxs; i++ {
		switch e.status[i] {
		case StatusValidated:
			continue // settled in an earlier pass, and nothing before it moved
		case StatusPending:
			if firstFail < 0 {
				firstFail = i
			}
			continue
		}
		if valid[i] {
			e.status[i] = StatusValidated
			continue
		}
		if e.traceLeft > 0 {
			e.traceLeft--
			e.traceFailure(i)
		}
		e.totalAborts.Add(1)
		e.status[i] = StatusPending
		e.incarnation[i]++
		if firstFail < 0 {
			firstFail = i
		}
	}
	if firstFail < 0 {
		return true
	}
	for j := firstFail + 1; j < e.numTxs; j++ {
		if e.status[j] == StatusValidated {
			e.status[j] = StatusExecuted
		}
	}
	return false
}

// validateExecuted runs Validate over every executed transaction in
// parallel and returns the verdicts by index.
func (e *Executor) validateExecuted() []bool {
	valid := make([]bool, e.numTxs)
	workers := e.workers
	if workers < 1 {
		workers = 1
	}
	if workers > e.numTxs {
		workers = e.numTxs
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for i := start; i < e.numTxs; i += workers {
				if e.status[i] == StatusExecuted {
					valid[i] = Validate(e.mvs, e.rwSets[i])
				}
			}
		}(w)
	}
	wg.Wait()
	return valid
}

// traceFailure logs why transaction i failed validation (N42_PARALLEL_TRACE).
func (e *Executor) traceFailure(i int) {
	rw := e.rwSets[i]
	for ri := range rw.Reads {
		rd := &rw.Reads[ri]
		if readValid(e.mvs, i, rd) {
			continue
		}
		cur, wtx, winc, found := e.mvs.Read(rd.Key, i)
		log.Info("parallel trace: stale read", "tx", i, "inc", e.incarnation[i], "addr", rd.Key.Address.Hex(), "field", rd.Key.Field, "slot", rd.Key.Slot.Hex()[:10],
			"fromBase", rd.FromBase, "readWriter", rd.WriterTx, "readInc", rd.WriterIncarnation, "hasValue", rd.HasValue, "readLen", len(rd.Value),
			"hadDelta", rd.HadDelta, "ignoreBalance", rd.IgnoreBalance,
			"nowFound", found, "nowWriter", wtx, "nowInc", winc, "nowLen", len(cur), "reads", len(rw.Reads), "writes", len(rw.Writes))
		return
	}
	log.Info("parallel trace: failed without a stale read", "tx", i, "reads", len(rw.Reads))
}

// WaveTimes returns the wall time the last Run spent executing and validating.
func (e *Executor) WaveTimes() (execNanos, validateNanos int64) {
	return e.execNanos, e.validateNanos
}

// Waves is the number of execute+validate passes the last Run took.
func (e *Executor) Waves() int { return e.waves }

// FellBack reports whether the last Run gave up on Block-STM and executed
// the block sequentially.
func (e *Executor) FellBack() bool { return e.fellBack }

// allValidated returns true if all transactions are validated.
func (e *Executor) allValidated() bool {
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] != StatusValidated {
			return false
		}
	}
	return true
}

// exec dispatches to whichever execution function the executor was built with.
func (e *Executor) exec(ctx any, txIndex int, rw *ReadWriteSet) error {
	if e.execCtxFn != nil {
		return e.execCtxFn(ctx, txIndex, rw)
	}
	return e.execFn(txIndex, rw)
}

// runSequential falls back to sequential execution.
func (e *Executor) runSequential() {
	var ctx any
	if e.workerSetup != nil {
		c, teardown, err := e.workerSetup(0)
		if err != nil {
			for i := 0; i < e.numTxs; i++ {
				e.results[i] = TxResult{Err: err}
			}
			return
		}
		if teardown != nil {
			defer teardown()
		}
		ctx = c
	}
	for i := 0; i < e.numTxs; i++ {
		rw := e.rwSets[i]
		rw.Clear()

		err := e.exec(ctx, i, rw)
		e.results[i] = TxResult{Err: err}

		// Mirror the parallel write-back: a delta write carries a nil Value,
		// so replaying it through Write would record a DELETION of the
		// account. On the fallback path that silently selfdestructed every
		// recipient the block only credited (round 35zzx, block 13659302:
		// the leader's build fell back, 17,036 credited accounts came out
		// empty and its root diverged from all six followers).
		for _, wd := range rw.Writes {
			if wd.Delta != nil {
				e.mvs.WriteDelta(wd.Key, i, 0, wd.Delta)
				continue
			}
			e.mvs.Write(wd.Key, i, 0, wd.Value)
		}
	}
}

// MVS returns the multi-version store (for testing/debugging).
func (e *Executor) MVS() *MVS {
	return e.mvs
}

// Stats returns execution statistics.
func (e *Executor) Stats() (executions, aborts int64) {
	return e.totalExecutions.Load(), e.totalAborts.Load()
}

// BusyNanos returns the sum, across every worker and every executeSingle
// call in this Run, of wall time spent inside e.exec (measurement-only,
// S81's waveBusyMs).
func (e *Executor) BusyNanos() int64 {
	return e.busyNanos.Load()
}
