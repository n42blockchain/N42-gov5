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
	"encoding/binary"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	_ "unsafe" // go:linkname

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
	// waveAffKeys: the current wave's affinity key per transaction index
	// (nil without affinity).
	waveAffKeys []uint64
	// preKeys: every transaction's affinity key, computed by
	// PrepareAffinity before Run (nil: computed by the first wave).
	preKeys []uint64
	// prePart: the full-set partition PrepareAffinity computed (nil:
	// computed by the first wave).
	prePart *wavePartition
}

// wavePartition is a precomputed partitionByAffinity result, plus the
// per-worker chain lists when work stealing is on.
type wavePartition struct {
	queues   [][]int
	keys     int
	topChain int
	chains   [][]waveChain
	// chainAt[w][p] is the index in chains[w] of the chain queues[w][p] is in.
	chainAt [][]int32
}

// waveChain is one affinity key's transactions on one worker, in index
// order. state is claimed (0 -> 1) by whoever runs the chain.
type waveChain struct {
	txs   []int
	key   uint64
	state atomic.Int32
}

// stealQueue is one worker's chains in LPT assignment order (longest first).
// The owner walks its queue in index order and claims a chain at its first
// transaction; thieves claim from the back of chains. top bounds the chains
// thieves have not yet passed, remaining counts unclaimed transactions.
type stealQueue struct {
	chains    []waveChain
	chainAt   []int32
	top       atomic.Int32
	remaining atomic.Int64
}

// waveSteal (N42_WAVE_STEAL=1) lets a worker that ran out of chains take
// whole not-yet-started chains from the busiest worker (LPT mode only).
var waveSteal = os.Getenv("N42_WAVE_STEAL") == "1"

// buildChains groups each queue into per-key chains ordered as LPT assigned
// them: by length descending, ties by first appearance.
func buildChains(queues [][]int, keys []uint64) (out [][]waveChain, chainAt [][]int32) {
	out = make([][]waveChain, len(queues))
	chainAt = make([][]int32, len(queues))
	for w, q := range queues {
		pos := make(map[uint64]int)
		var cs []waveChain
		var counts []int
		for _, idx := range q {
			k := keys[idx]
			i, ok := pos[k]
			if !ok {
				i = len(cs)
				pos[k] = i
				cs = append(cs, waveChain{key: k})
				counts = append(counts, 0)
			}
			counts[i]++
		}
		order := make([]int, len(cs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
		backing := make([]int, len(q))
		off := 0
		rank := make([]int, len(cs))
		for r, ci := range order {
			rank[ci] = r
		}
		sorted := make([]waveChain, len(cs))
		fill := make([]int, len(cs))
		for r, ci := range order {
			sorted[r].key = cs[ci].key
			sorted[r].txs = backing[off : off+counts[ci] : off+counts[ci]]
			off += counts[ci]
		}
		at := make([]int32, len(q))
		for p, idx := range q {
			r := rank[pos[keys[idx]]]
			sorted[r].txs[fill[r]] = idx
			fill[r]++
			at[p] = int32(r)
		}
		out[w], chainAt[w] = sorted, at
	}
	return out, chainAt
}

// steal claims the last unclaimed chain not yet passed by another thief.
func (sq *stealQueue) steal() *waveChain {
	for {
		t := sq.top.Load()
		if t <= 0 {
			return nil
		}
		if !sq.top.CompareAndSwap(t, t-1) {
			continue
		}
		c := &sq.chains[t-1]
		if c.state.CompareAndSwap(0, 1) {
			sq.remaining.Add(-int64(len(c.txs)))
			return c
		}
	}
}

// stealFrom steals one chain from the most loaded worker other than self,
// or returns nil when no other worker has unclaimed chains left.
func stealFrom(sqs []stealQueue, self int) *waveChain {
	for {
		best, bestRem := -1, int64(0)
		for w := range sqs {
			if w == self {
				continue
			}
			if r := sqs[w].remaining.Load(); r > bestRem && sqs[w].top.Load() > 0 {
				best, bestRem = w, r
			}
		}
		if best < 0 {
			return nil
		}
		if c := sqs[best].steal(); c != nil {
			return c
		}
	}
}

// waveLPT (N42_WAVE_LPT=1) replaces the modulo sender-to-worker assignment
// with longest-processing-time greedy assignment (S83). Read once at init.
var waveLPT = os.Getenv("N42_WAVE_LPT") == "1"

// SetWaveModesForTest sets N42_WAVE_LPT and N42_WAVE_STEAL and returns the
// restore.
func SetWaveModesForTest(lpt, steal bool) func() {
	pl, ps := waveLPT, waveSteal
	waveLPT, waveSteal = lpt, steal
	return func() { waveLPT, waveSteal = pl, ps }
}

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
	// chainOf[j] is the chain index of txIndices[j], so the placement pass
	// needs no second key lookup.
	pos := make(map[uint64]int)
	chainOf := make([]int32, len(txIndices))
	var chains []chain
	for j, idx := range txIndices {
		k := affinity(idx)
		i, ok := pos[k]
		if !ok {
			i = len(chains)
			pos[k] = i
			chains = append(chains, chain{key: k})
		}
		chains[i].count++
		chainOf[j] = int32(i)
	}
	order := make([]int, len(chains))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return chains[order[a]].count > chains[order[b]].count })
	load := make([]int, workers)
	assigned := make([]int, len(chains))
	for _, ci := range order {
		best := 0
		for w := 1; w < workers; w++ {
			if load[w] < load[best] {
				best = w
			}
		}
		load[best] += chains[ci].count
		assigned[ci] = best
		if chains[ci].count > topChain {
			topChain = chains[ci].count
		}
	}
	for w := range queues {
		queues[w] = make([]int, 0, load[w])
	}
	for j, idx := range txIndices {
		w := assigned[chainOf[j]]
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
	execs       int64 // executions, folded into totalExecutions after the wave
	// clock is when the worker's previous transaction finished its store
	// writes (or the worker started): the next transaction's start, so a
	// transaction reads the clock twice instead of five times (the reads
	// were ~2.5% of the exec wave's CPU at 163k transfers).
	clock int64 // monoNow
	// keyCount counts the worker's queued transactions per affinity key
	// (P10): a sender's account gets one version per transaction of its
	// chain, and its version list is sized for all of them at the first.
	keyCount map[uint64]int32
	// Pad to two cache lines: neighbouring workers write their slots on
	// every transaction.
	_ [40]byte
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
	MvsWriteNs    int64 // MvsWriteMs unrounded
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
	s.MvsWriteNs = mvsWriteNs
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

// ResolveWorkers is the worker count NewExecutor uses for numTxs
// transactions and a requested count (0 means runtime.NumCPU()).
func ResolveWorkers(numTxs, workers int) int {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > numTxs {
		workers = numTxs
	}
	return workers
}

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
	// pending is collectPending's buffer (a wave's indices; dead once the
	// wave returned): it grew by append to the block's size every wave,
	// ~40 bytes per transaction of a follower's import.
	pending []int
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

// Three: with N42_LAG_EARLY_SNAPSHOT a block's executor stays alive until
// its child's run ends, while the grandchild's run takes another.
const arenaFreeMax = 3

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
		// The sets and their first descriptors come from one slab of
		// txSlots, not three objects per transaction: the arena outlives the block, so
		// the collector marks it on every cycle, and at 163k transactions
		// the per-transaction arrays (6 reads, 3 writes) were 190 MB, over
		// half the bench's live heap. A transfer reads and writes two
		// accounts; a transaction that needs more grows its own arrays.
		slots := make([]txSlot, numTxs)
		for i := range slots {
			sl := &slots[i]
			sl.set = ReadWriteSet{TxIndex: i, Reads: sl.reads[:0:arenaReads], Writes: sl.writes[:0:arenaWrites]}
			a.rwSets[i] = &sl.set
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

// arenaReads and arenaWrites are the descriptor slots an arena gives each
// transaction's read and write set.
const (
	arenaReads  = 2
	arenaWrites = 2
)

// txSlot is one transaction's read/write set with its first descriptors,
// 512 bytes: transactions next to each other run on different workers at
// about the same time, and a slot that shared a cache line with its
// neighbour's was written from two cores at once.
type txSlot struct {
	set    ReadWriteSet
	_      [24]byte
	reads  [arenaReads]ReadDescriptor
	writes [arenaWrites]WriteDescriptor
}

func NewExecutor(numTxs int, workers int, execFn TxExecuteFunc) *Executor {
	workers = ResolveWorkers(numTxs, workers)

	a := arenaFor(numTxs)
	mvs := NewMVS()
	mvs.va = takeVersionArena()
	return &Executor{
		mvs:         mvs,
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
	// The store's version lists go back with it: the caller has applied
	// the store (version_arena.go).
	e.mvs.release()
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
	//
	// The arena's buffer holds the indices: the previous wave's are dead
	// (executeParallel keeps none of them past its return).
	var pending []int
	if scratchPool && e.arena != nil {
		if cap(e.arena.pending) < e.numTxs {
			e.arena.pending = make([]int, 0, e.numTxs)
		}
		pending = e.arena.pending[:0]
	}
	for i := 0; i < e.numTxs; i++ {
		if e.status[i] == StatusPending {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
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
	var keys []uint64
	var sqs []stealQueue
	if e.affinity != nil {
		if e.preKeys != nil && len(txIndices) == e.numTxs {
			// The first wave runs every transaction: PrepareAffinity's keys.
			keys = e.preKeys
		} else {
			keys = e.affinityKeys(txIndices)
		}
		part := e.prePart
		if part == nil || len(txIndices) != e.numTxs {
			part = e.partitionWave(txIndices, keys)
		}
		e.preKeys, e.prePart = nil, nil
		queues, e.waveKeys, e.waveTopChain = part.queues, part.keys, part.topChain
		if part.chains != nil {
			sqs = make([]stealQueue, e.workers)
			for w := range sqs {
				sqs[w].chains, sqs[w].chainAt = part.chains[w], part.chainAt[w]
				sqs[w].top.Store(int32(len(part.chains[w])))
				sqs[w].remaining.Store(int64(len(queues[w])))
			}
		}
	}
	e.waveAffKeys = keys

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
			ws.clock = monoNow()
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
				if presizeVersions && e.incarnationsZero(queues[workerID]) {
					ws.keyCount = make(map[uint64]int32, len(queues[workerID])/8+1)
					for _, txIndex := range queues[workerID] {
						ws.keyCount[keys[txIndex]]++
					}
				}
				if sqs != nil {
					sq := &sqs[workerID]
					// Own transactions run in index order; a chain thieves
					// already took is skipped at its first transaction.
					owned := make([]uint8, len(sq.chains))
					for p, txIndex := range queues[workerID] {
						ci := sq.chainAt[p]
						if owned[ci] == 0 {
							c := &sq.chains[ci]
							if !c.state.CompareAndSwap(0, 1) {
								owned[ci] = 2
								continue
							}
							owned[ci] = 1
							sq.remaining.Add(-int64(len(c.txs)))
						} else if owned[ci] == 2 {
							continue
						}
						run(txIndex)
					}
					for c := stealFrom(sqs, workerID); c != nil; c = stealFrom(sqs, workerID) {
						// A stolen chain is not in this worker's keyCount: size
						// its sender's version list from the chain itself.
						if ws.keyCount != nil {
							ws.keyCount[c.key] = int32(len(c.txs))
						}
						for _, txIndex := range c.txs {
							run(txIndex)
						}
					}
					return
				}
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
	var execs, busy int64
	for i := range e.workerStats {
		execs += e.workerStats[i].execs
		busy += e.workerStats[i].busyNs
	}
	e.totalExecutions.Add(execs)
	e.busyNanos.Add(busy)
}

// PrepareAffinity computes every transaction's affinity key now, on
// GOMAXPROCS goroutines, instead of at the start of the first wave (the
// key function may do per-transaction preparation the caller wants off
// the wave). Call after SetAffinity and before Run.
func (e *Executor) PrepareAffinity() {
	if e.affinity == nil || e.numTxs <= 2 {
		return
	}
	all := allIndices(e.numTxs)
	e.preKeys = e.affinityKeys(all)
	e.prePart = e.partitionWave(all, e.preKeys)
}

// identity is the read-only list 0, 1, 2, ... that allIndices hands out
// prefixes of; a longer one replaces it (the old one stays valid for its
// readers).
var identity struct {
	mu  sync.Mutex
	all atomic.Pointer[[]int]
}

// allIndices returns 0..n-1. The list is shared and must not be modified:
// the first wave's index list (PrepareAffinity) was a fresh numTxs ints a
// block, only ever read (the affinity keys and the partition copy what
// they keep).
func allIndices(n int) []int {
	if !scratchPool {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all
	}
	if p := identity.all.Load(); p != nil && len(*p) >= n {
		return (*p)[:n:n]
	}
	identity.mu.Lock()
	defer identity.mu.Unlock()
	if p := identity.all.Load(); p != nil && len(*p) >= n {
		return (*p)[:n:n]
	}
	all := make([]int, max(n, 1<<16))
	for i := range all {
		all[i] = i
	}
	identity.all.Store(&all)
	return all[:n:n]
}

// AffinityPrep is what PrepareAffinity computes -- every transaction's
// affinity key and the first wave's partition -- computed without an
// Executor, ahead of the block's run (the lag executor's queued child), and
// adopted by AdoptAffinity in place of PrepareAffinity.
type AffinityPrep struct {
	numTxs, workers int
	lpt, steal      bool
	keys            []uint64
	part            *wavePartition
	// taken: adopted once (the steal chains carry run state).
	taken atomic.Bool
}

// PrepareAffinityFor computes PrepareAffinity's result for numTxs
// transactions on an executor of workers (as requested of NewExecutor)
// with affinity key key, on at most goroutines goroutines. nil when
// PrepareAffinity would compute nothing.
func PrepareAffinityFor(numTxs, workers int, key func(txIndex int) uint64, goroutines int) *AffinityPrep {
	if key == nil || numTxs <= 2 {
		return nil
	}
	w := ResolveWorkers(numTxs, workers)
	all := allIndices(numTxs)
	keys := affinityKeysOn(numTxs, all, key, goroutines)
	return &AffinityPrep{numTxs: numTxs, workers: w, lpt: waveLPT, steal: waveSteal, keys: keys, part: partitionFor(all, w, keys)}
}

// AdoptAffinity takes p in place of PrepareAffinity when it was computed
// for this executor's transaction count, worker count and wave modes, and
// reports whether it did (false: the caller runs PrepareAffinity). Call
// after SetAffinity and before Run; p is used at most once.
func (e *Executor) AdoptAffinity(p *AffinityPrep) bool {
	if p == nil || e.affinity == nil || e.numTxs <= 2 || p.numTxs != e.numTxs || p.workers != e.workers ||
		p.lpt != waveLPT || p.steal != waveSteal || !p.taken.CompareAndSwap(false, true) {
		return false
	}
	e.preKeys, e.prePart = p.keys, p.part
	p.keys, p.part = nil, nil
	return true
}

// PreparedAffinityForTest returns the keys and first-wave queues prepared
// for the next Run (nil when none are).
func (e *Executor) PreparedAffinityForTest() (keys []uint64, queues [][]int) {
	if e.prePart != nil {
		queues = e.prePart.queues
	}
	return e.preKeys, queues
}

// partitionWave partitions txIndices (with their affinity keys) for a wave,
// adding the steal chain lists when stealing applies.
func (e *Executor) partitionWave(txIndices []int, keys []uint64) *wavePartition {
	return partitionFor(txIndices, e.workers, keys)
}

func partitionFor(txIndices []int, workers int, keys []uint64) *wavePartition {
	p := &wavePartition{}
	p.queues, p.keys, p.topChain = partitionByAffinity(txIndices, workers, func(i int) uint64 { return keys[i] }, waveLPT)
	if waveLPT && waveSteal {
		p.chains, p.chainAt = buildChains(p.queues, keys)
	}
	return p
}

// presizeVersions is N42_MV_PRESIZE (default on; "0" disables): see
// workerStat.keyCount.
var presizeVersions = os.Getenv("N42_MV_PRESIZE") != "0"

// incarnationsZero reports whether every transaction of q runs for the
// first time (the first wave): only then do the key counts size lists.
func (e *Executor) incarnationsZero(q []int) bool {
	for _, i := range q {
		if e.incarnation[i] != 0 {
			return false
		}
	}
	return true
}

// versionHint is the capacity a version list of key should start with when
// txIndex writes it first: the number of transactions of txIndex's
// affinity chain on this worker when key is the chain's sender account
// (its affinity key is the address's first 8 bytes), else 0.
func (e *Executor) versionHint(ws *workerStat, txIndex int, key *LocationKey) int {
	if ws == nil || ws.keyCount == nil || key.Field != FieldBalance || txIndex >= len(e.waveAffKeys) {
		return 0
	}
	k := e.waveAffKeys[txIndex]
	if binary.LittleEndian.Uint64(key.Address[:8]) != k {
		return 0
	}
	return int(ws.keyCount[k])
}

// affinityKeysChunk is the number of transactions below which the
// affinity keys are computed on the calling goroutine.
const affinityKeysChunk = 4096

// affinityKeys computes the affinity key of every transaction in txIndices,
// indexed by transaction index, on GOMAXPROCS goroutines. A key reads the
// transaction's sender, a cache miss per transaction: serial, that was
// ~13 ms of a 163k-transfer block's exec wave, with the workers waiting.
func (e *Executor) affinityKeys(txIndices []int) []uint64 {
	return affinityKeysOn(e.numTxs, txIndices, e.affinity, runtime.GOMAXPROCS(0))
}

// affinityKeysOn is affinityKeys with key on at most g goroutines.
func affinityKeysOn(numTxs int, txIndices []int, key func(int) uint64, g int) []uint64 {
	keys := make([]uint64, numTxs)
	n := len(txIndices)
	if g > n/affinityKeysChunk {
		g = n / affinityKeysChunk
	}
	if g <= 1 {
		for _, idx := range txIndices {
			keys[idx] = key(idx)
		}
		return keys
	}
	var wg sync.WaitGroup
	per := (n + g - 1) / g
	for lo := 0; lo < n; lo += per {
		hi := min(lo+per, n)
		wg.Add(1)
		go func(part []int) {
			defer wg.Done()
			for _, idx := range part {
				keys[idx] = key(idx)
			}
		}(txIndices[lo:hi])
	}
	wg.Wait()
	return keys
}

// monoNow is the monotonic clock in nanoseconds: the executor's per
// transaction timings (two reads a transaction) without time.Now's wall
// clock read, half of each read's cost (r35zzzco: time.Now 2 s of
// executeSingle's 76 s).
func monoNow() int64 { return nanotime() }

//go:linkname nanotime runtime.nanotime
func nanotime() int64

// executeSingle executes a single transaction. ws is the calling worker's
// S82 stat slot, or nil from runSequential (which has no worker to charge).
func (e *Executor) executeSingle(ctx any, txIndex int, ws *workerStat) {
	// A worker counts in its own slot (folded after the wave): a shared
	// atomic add per transaction bounced one cache line between workers.
	if ws != nil {
		ws.execs++
	} else {
		e.totalExecutions.Add(1)
	}

	// Reuse the transaction's read/write set. NewExecutor allocates one per
	// transaction; a fresh one per execution made that allocation dead and
	// added another 163k (two slices each) per full block on the import's
	// setup, plus one per re-execution. The MVS copies every value it
	// stores, so the descriptor arrays can be overwritten once the previous
	// incarnation's writes are withdrawn.
	//
	// Withdraw only that incarnation's keys: DeleteAll walks every entry in
	// the store and was 95% of a follower's CPU at 54k transactions (35e).
	rw := e.rwSets[txIndex]
	if rw != nil {
		// Incarnation 0 is the transaction's first execution against this
		// executor's fresh MVS: a kept set's writes are the previous block's
		// and were never stored here, so there is nothing to withdraw (the
		// deletes were ~9% of the exec wave at 163k transfers).
		if e.incarnation[txIndex] > 0 {
			tDelete := time.Now()
			for _, wd := range rw.Writes {
				e.mvs.Delete(wd.Key, txIndex)
			}
			if ws != nil {
				ws.mvsDeleteNs += int64(time.Since(tDelete))
				ws.clock = monoNow()
			}
		}
		rw.Clear()
	} else {
		rw = NewReadWriteSet(txIndex)
	}

	// Execute the transaction. A worker's call starts when its previous
	// transaction ended (ws.clock).
	var tCall int64
	if ws != nil {
		tCall = ws.clock
	} else {
		tCall = monoNow()
	}
	err := e.exec(ctx, txIndex, rw)
	tWrite := monoNow()
	busy := tWrite - tCall
	if ws != nil {
		ws.busyNs += busy
	} else {
		e.busyNanos.Add(busy)
	}

	// Store only what changed: neighbouring transactions run on other
	// workers at the same time, and a store of the value already there
	// still takes the cache line from them (r35zzzco: the load of
	// rwSets[txIndex] 2.1 s of executeSingle's 76 s, the lines bouncing).
	if err != nil || e.results[txIndex].Err != nil {
		e.results[txIndex] = TxResult{Err: err}
	}
	if e.rwSets[txIndex] != rw {
		e.rwSets[txIndex] = rw
	}

	// Apply writes to MVS with incarnation tag.
	inc := e.incarnation[txIndex]
	for i := range rw.Writes {
		wd := &rw.Writes[i]
		switch {
		case wd.Delta != nil:
			e.mvs.writeDelta(e.writeEntry(rw, &wd.Key), txIndex, inc, wd.Delta)
		case wd.Key.Field == FieldCode:
			// Code can alias EVM memory: copied.
			e.mvs.Write(wd.Key, txIndex, inc, wd.Value)
		default:
			// The state writer encodes every other value fresh and nothing
			// mutates a recorded write: the store adopts it.
			// The hint sizes only an empty version list: an entry with a
			// full write has a non-empty one, so it is computed only for an
			// entry without (its lookups were a cache miss per write).
			ent := e.writeEntry(rw, &wd.Key)
			hint := 0
			if ent.noFullWrites() {
				hint = e.versionHint(ws, txIndex, &wd.Key)
			}
			e.mvs.writeOwnedHint(ent, txIndex, inc, wd.Value, hint)
		}
	}
	if ws != nil {
		ws.clock = monoNow()
		ws.mvsWriteNs += ws.clock - tWrite
	}

	e.status[txIndex] = StatusExecuted
}

// writeEntry returns the store entry for a write of rw to key. An account
// the transaction read carries the entry its read was served from; when
// the read found none, the entry created here is handed back to that read
// so its validation skips the lookup as well.
func (e *Executor) writeEntry(rw *ReadWriteSet, key *LocationKey) *mvEntry {
	var rd *ReadDescriptor
	if key.Field == FieldBalance {
		for i := range rw.Reads {
			if r := &rw.Reads[i]; r.Key.Field == FieldBalance && r.Key.Address == key.Address {
				if r.entry != nil {
					return r.entry
				}
				rd = r
				break
			}
		}
	}
	ent := e.mvs.getOrCreateEntry(*key)
	if rd != nil {
		rd.entry = ent
	}
	return ent
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
	// Workers take contiguous chunks: with strides every verdict a worker
	// stored shared a cache line with its neighbours' verdicts and reads.
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				lo := int(next.Add(validateChunk)) - validateChunk
				if lo >= e.numTxs {
					return
				}
				hi := min(lo+validateChunk, e.numTxs)
				for i := lo; i < hi; i++ {
					if e.status[i] == StatusExecuted {
						valid[i] = Validate(e.mvs, e.rwSets[i])
					}
				}
			}
		}()
	}
	wg.Wait()
	return valid
}

// validateChunk is the transactions a validation worker takes at a time.
const validateChunk = 512

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
