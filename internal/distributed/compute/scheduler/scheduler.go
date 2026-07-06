// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package scheduler orchestrates distributed compute tasks end-to-end over
// the existing coprocessor components (slice 1 of
// docs/distributed-compute-design-2026-07-06.md):
//
//	Submit → escrow lock → dispatch → execute (lease/hedge/reassign)
//	       → accept (quorum majority | optimistic + challenge window)
//	       → settle (pay/refund, reward/penalize/slash)
//
// Two verification modes map to the two halves of the heterogeneous fleet:
//
//   - VerifyOptimistic (IDC): a reverse auction (coprocessor.Marketplace)
//     picks one staked provider; its result parks behind a challenge window;
//     a challenger triggers referee re-execution; an upheld challenge slashes
//     the provider (coprocessor.SlashManager, Verify-or-Slash).
//   - VerifyQuorum (phones): r replicas execute in parallel; the strict
//     majority output is accepted; disagreeing replicas take a reputation
//     penalty only (phones carry no meaningful stake). One escalation round
//     with r+2 fresh workers runs if the first round has no majority.
//
// Timeout hierarchy: per-attempt lease < task deadline < challenge window.
// A lease expiry reassigns to the next-best untried worker (and, in
// optimistic mode, slashes the hung provider for timeout); hedged dispatch
// optionally launches a backup copy when the primary is slow; the deadline
// expires the task and refunds the escrow.
package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// Config wires the scheduler to its backing components. All fields are
// required except Challenges (only needed when optimistic tasks are used).
type Config struct {
	Tasks      *coprocessor.TaskManager
	Registry   *coprocessor.ProviderRegistry
	Market     *coprocessor.Marketplace
	Slasher    *coprocessor.SlashManager
	Challenges *coprocessor.ChallengeManager
	Executor   Executor
}

// taskRun is the scheduler-side state for one submitted task.
type taskRun struct {
	spec      *TaskSpec
	submitter types.Address

	mu           sync.Mutex
	claimed      bool // a settlement path owns this task's terminal record
	result       TaskResult
	done         chan struct{}
	winner       types.Address // optimistic: provider whose result is parked
	winPrice     uint64        // optimistic: price owed to winner on finalize
	parkedOutput []byte        // optimistic: output awaiting the challenge window
	finalizer    *time.Timer   // optimistic: challenge-window expiry
}

// tryClaim marks this settlement path as the single owner of the task's
// terminal record. Exactly one caller wins; everyone else must back off.
func (r *taskRun) tryClaim() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return false
	}
	r.claimed = true
	if r.finalizer != nil {
		r.finalizer.Stop()
	}
	return true
}

// finish publishes the terminal record and releases waiters. MUST be called
// only by the claim owner, and only AFTER all settlement side effects
// (escrow payments/refunds, task-status updates) have completed — WaitTask
// returning implies the ledger is consistent.
func (r *taskRun) finish(res TaskResult) {
	r.mu.Lock()
	r.result = res
	r.mu.Unlock()
	close(r.done)
}

// Scheduler orchestrates task execution over a worker fleet.
type Scheduler struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	workers map[types.Address]WorkerInfo
	quotes  map[types.Address]uint64 // posted price per worker (default: spec.MaxPrice)
	referee types.Address
	hasRef  bool
	runs    map[types.Hash]*taskRun

	escrow *Escrow
}

// New creates a scheduler. Call Close to stop background work.
func New(cfg Config) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cfg:     cfg,
		ctx:     ctx,
		cancel:  cancel,
		workers: make(map[types.Address]WorkerInfo),
		quotes:  make(map[types.Address]uint64),
		runs:    make(map[types.Hash]*taskRun),
		escrow:  NewEscrow(),
	}
}

// Close cancels all in-flight work and waits for task goroutines to exit.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// Escrow exposes the settlement ledger (read-mostly; tests assert on it).
func (s *Scheduler) Escrow() *Escrow { return s.escrow }

// AddWorker attaches scheduler-side device metadata to a registered
// coprocessor provider. The provider must already be registered (staked).
func (s *Scheduler) AddWorker(addr types.Address, class DeviceClass, group string) error {
	if _, ok := s.cfg.Registry.GetSnapshot(addr); !ok {
		return ErrUnknownProvider
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers[addr] = WorkerInfo{Addr: addr, Class: class, Group: group}
	return nil
}

// SetQuote posts a worker's standing price for auctioned tasks. Workers
// without a quote bid the task's MaxPrice.
func (s *Scheduler) SetQuote(addr types.Address, price uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotes[addr] = price
}

// SetReferee designates the trusted re-execution worker used to resolve
// challenges. The referee is excluded from regular task selection so it can
// never be asked to referee its own work.
func (s *Scheduler) SetReferee(addr types.Address) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.referee = addr
	s.hasRef = true
}

// Submit validates the spec, locks the escrow, creates the coprocessor task
// and starts the orchestration goroutine. Returns the task ID.
func (s *Scheduler) Submit(spec *TaskSpec, submitter types.Address) (types.Hash, error) {
	if err := spec.Validate(); err != nil {
		return types.Hash{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return types.Hash{}, ErrClosed
	}
	s.mu.Unlock()

	id, err := s.cfg.Tasks.Submit(spec.ProgramHash, spec.Input, submitter)
	if err != nil {
		return types.Hash{}, err
	}
	s.escrow.Lock(id, spec.MaxPrice)

	specCopy := *spec
	run := &taskRun{spec: &specCopy, submitter: submitter, done: make(chan struct{})}
	s.mu.Lock()
	s.runs[id] = run
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		switch specCopy.Mode {
		case VerifyQuorum:
			s.runQuorum(id, run)
		default:
			s.runOptimistic(id, run)
		}
	}()
	return id, nil
}

// WaitTask blocks until the task reaches a terminal record or the timeout.
func (s *Scheduler) WaitTask(id types.Hash, timeout time.Duration) (*TaskResult, error) {
	s.mu.Lock()
	run, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return nil, ErrTaskUnknown
	}
	select {
	case <-run.done:
	case <-time.After(timeout):
		return nil, ErrWaitTimeout
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	res := run.result
	return &res, nil
}

// ---------------------------------------------------------------------------
// worker selection

// eligibleWorkers returns active, capability-matching workers not in
// `excluded`, sorted by (quote asc, reputation desc, address) so selection is
// deterministic. The referee is never eligible for regular execution.
func (s *Scheduler) eligibleWorkers(spec *TaskSpec, excluded map[types.Address]bool) []WorkerInfo {
	s.mu.Lock()
	referee, hasRef := s.referee, s.hasRef
	candidates := make([]WorkerInfo, 0, len(s.workers))
	for _, w := range s.workers {
		candidates = append(candidates, w)
	}
	quotes := make(map[types.Address]uint64, len(s.quotes))
	for a, q := range s.quotes {
		quotes[a] = q
	}
	s.mu.Unlock()

	quoteOf := func(a types.Address) uint64 {
		if q, ok := quotes[a]; ok {
			return q
		}
		return spec.MaxPrice
	}

	out := candidates[:0]
	reputation := make(map[types.Address]uint64, len(candidates))
	for _, w := range candidates {
		if excluded[w.Addr] {
			continue
		}
		if hasRef && w.Addr == referee {
			continue
		}
		p, ok := s.cfg.Registry.GetSnapshot(w.Addr)
		if !ok || p.Status != coprocessor.ProviderActive || !p.HasCapability(spec.Capability) {
			continue
		}
		if quoteOf(w.Addr) > spec.MaxPrice {
			continue
		}
		reputation[w.Addr] = p.Reputation
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		qi, qj := quoteOf(out[i].Addr), quoteOf(out[j].Addr)
		if qi != qj {
			return qi < qj
		}
		if ri, rj := reputation[out[i].Addr], reputation[out[j].Addr]; ri != rj {
			return ri > rj
		}
		return bytes.Compare(out[i].Addr[:], out[j].Addr[:]) < 0
	})
	return out
}

// quoteFor returns a worker's effective bid for a task.
func (s *Scheduler) quoteFor(addr types.Address, spec *TaskSpec) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.quotes[addr]; ok {
		return q
	}
	return spec.MaxPrice
}

// selectQuorum picks r workers for a quorum round, honoring group dispersal.
// Returns nil if the pool cannot satisfy the request.
func (s *Scheduler) selectQuorum(spec *TaskSpec, r int, excluded map[types.Address]bool) []WorkerInfo {
	pool := s.eligibleWorkers(spec, excluded)
	// Quorum selection prefers reputation over price (replicas are paid an
	// equal split regardless): re-sort by reputation desc over one coherent
	// snapshot per worker.
	reputation := make(map[types.Address]uint64, len(pool))
	for _, w := range pool {
		if p, ok := s.cfg.Registry.GetSnapshot(w.Addr); ok {
			reputation[w.Addr] = p.Reputation
		}
	}
	sort.SliceStable(pool, func(i, j int) bool {
		return reputation[pool[i].Addr] > reputation[pool[j].Addr]
	})
	if !spec.RequireDisperse {
		if len(pool) < r {
			return nil
		}
		return pool[:r]
	}
	seen := make(map[string]bool, r)
	var picked []WorkerInfo
	for _, w := range pool {
		if seen[w.Group] {
			continue
		}
		seen[w.Group] = true
		picked = append(picked, w)
		if len(picked) == r {
			return picked
		}
	}
	return nil // not enough distinct groups — caller fails the task loudly
}

// ---------------------------------------------------------------------------
// attempt execution

type attemptOutcome struct {
	worker types.Address
	output []byte
	err    error
}

// executeAttempt runs one lease-bounded attempt.
func (s *Scheduler) executeAttempt(parent context.Context, worker types.Address, spec *TaskSpec) attemptOutcome {
	ctx, cancel := context.WithTimeout(parent, spec.Lease)
	defer cancel()
	out, err := s.cfg.Executor.Execute(ctx, worker, spec)
	return attemptOutcome{worker: worker, output: out, err: err}
}

// penalizeAttempt applies the failure policy for one failed attempt.
// Lease timeouts on optimistic (staked) workers are slashable; everything
// else costs reputation only. Hedge-cancelled attempts are no-fault and must
// not reach here.
func (s *Scheduler) penalizeAttempt(id types.Hash, worker types.Address, err error, mode VerifyMode) {
	if errors.Is(err, context.DeadlineExceeded) && mode == VerifyOptimistic {
		s.cfg.Slasher.Slash(worker, coprocessor.SlashTimeout, id)
		return
	}
	s.cfg.Registry.IncrementTasks(worker, true)
	s.cfg.Registry.UpdateReputation(worker, -500)
}

// ---------------------------------------------------------------------------
// optimistic mode

func (s *Scheduler) runOptimistic(id types.Hash, run *taskRun) {
	spec := run.spec
	deadlineCtx, cancel := context.WithTimeout(s.ctx, spec.Deadline)
	defer cancel()

	if _, err := s.cfg.Tasks.TransitionToProving(id); err != nil {
		s.settleFail(id, run, coprocessor.TaskFailed, fmt.Sprintf("transition: %v", err))
		return
	}

	tried := make(map[types.Address]bool)

	// Initial assignment through the real reverse auction.
	first := s.auctionWinner(id, spec)
	if first == nil {
		s.expire(id, run, "no eligible providers")
		return
	}
	current := *first

	for {
		outcome := s.runWithHedge(deadlineCtx, id, run, current, tried, spec)
		if outcome == nil { // deadline hit while executing
			s.expire(id, run, "deadline exceeded")
			return
		}
		if outcome.err == nil {
			s.parkOptimistic(id, run, outcome.worker, outcome.output)
			return
		}
		// Failed attempt: penalize and reassign to the next-best untried worker.
		tried[outcome.worker] = true
		s.penalizeAttempt(id, outcome.worker, outcome.err, VerifyOptimistic)
		if deadlineCtx.Err() != nil {
			s.expire(id, run, "deadline exceeded")
			return
		}
		next := s.eligibleWorkers(spec, tried)
		if len(next) == 0 {
			s.expire(id, run, "no remaining workers")
			return
		}
		current = next[0]
	}
}

// auctionWinner publishes the task, collects one bid per eligible worker at
// its posted quote, and lets the marketplace pick the lowest price.
func (s *Scheduler) auctionWinner(id types.Hash, spec *TaskSpec) *WorkerInfo {
	s.cfg.Market.PublishTask(id)
	pool := s.eligibleWorkers(spec, nil)
	for _, w := range pool {
		// Errors here (suspended provider racing, duplicate bid) only shrink
		// the auction; selection below is the arbiter.
		_, _ = s.cfg.Market.SubmitBid(id, w.Addr, s.quoteFor(w.Addr, spec), spec.Lease)
	}
	asg, err := s.cfg.Market.SelectWinner(id, coprocessor.StrategyLowestPrice)
	if err != nil {
		return nil
	}
	_ = s.cfg.Tasks.AssignProvider(id, asg.ProviderID, asg.Price)
	s.mu.Lock()
	w, ok := s.workers[asg.ProviderID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return &w
}

// runWithHedge executes on `primary`, optionally launching one backup copy
// after spec.HedgeDelay. First success wins; the loser is cancelled without
// fault. Returns nil only when the task deadline expired mid-flight.
func (s *Scheduler) runWithHedge(deadlineCtx context.Context, id types.Hash, run *taskRun,
	primary WorkerInfo, tried map[types.Address]bool, spec *TaskSpec) *attemptOutcome {

	attemptCtx, cancelAttempts := context.WithCancel(deadlineCtx)
	defer cancelAttempts()

	results := make(chan attemptOutcome, 2)
	launch := func(w WorkerInfo) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			results <- s.executeAttempt(attemptCtx, w.Addr, spec)
		}()
	}
	launch(primary)
	launched := []types.Address{primary.Addr}

	var hedgeTimer <-chan time.Time
	if spec.HedgeDelay > 0 {
		t := time.NewTimer(spec.HedgeDelay)
		defer t.Stop()
		hedgeTimer = t.C
	}

	pending := 1
	for {
		select {
		case <-hedgeTimer:
			hedgeTimer = nil
			ex := make(map[types.Address]bool, len(tried)+len(launched))
			for a := range tried {
				ex[a] = true
			}
			for _, a := range launched {
				ex[a] = true
			}
			if pool := s.eligibleWorkers(spec, ex); len(pool) > 0 {
				launch(pool[0])
				launched = append(launched, pool[0].Addr)
				pending++
			}
		case out := <-results:
			pending--
			if out.err == nil {
				// Winner: cancel the other attempt (no-fault) and report.
				cancelAttempts()
				return &out
			}
			// A cancelled loser (hedge already resolved) never reaches here —
			// success returns immediately above. A genuine failure while a
			// sibling is still running: wait for the sibling.
			if deadlineCtx.Err() != nil {
				return nil
			}
			if errors.Is(out.err, context.Canceled) {
				// Attempt cancelled by scheduler shutdown, not worker fault.
				if pending == 0 {
					return nil
				}
				continue
			}
			if pending > 0 {
				// Penalize this failure now; sibling may still succeed.
				tried[out.worker] = true
				s.penalizeAttempt(id, out.worker, out.err, VerifyOptimistic)
				continue
			}
			return &out
		case <-deadlineCtx.Done():
			return nil
		}
	}
}

// parkOptimistic records the winner's output behind the challenge window and
// arms the finalizer.
func (s *Scheduler) parkOptimistic(id types.Hash, run *taskRun, winner types.Address, output []byte) {
	price := s.quoteFor(winner, run.spec)
	deadline := time.Now().Add(run.spec.ChallengeWindow)
	if err := s.cfg.Tasks.SetOptimisticVerified(id, nil, output, deadline); err != nil {
		s.settleFail(id, run, coprocessor.TaskFailed, fmt.Sprintf("park: %v", err))
		return
	}
	_ = s.cfg.Tasks.AssignProvider(id, winner, price)

	run.mu.Lock()
	run.winner = winner
	run.winPrice = price
	run.parkedOutput = append([]byte(nil), output...)
	run.finalizer = time.AfterFunc(run.spec.ChallengeWindow, func() {
		s.finalizeOptimistic(id, run)
	})
	run.mu.Unlock()
}

// finalizeOptimistic settles an unchallenged optimistic task: Verified,
// winner paid, remainder refunded. Settlement completes before waiters wake.
func (s *Scheduler) finalizeOptimistic(id types.Hash, run *taskRun) {
	if !run.tryClaim() {
		return // a challenge path owns settlement
	}
	run.mu.Lock()
	winner, price := run.winner, run.winPrice
	output := run.parkedOutput
	run.mu.Unlock()

	_ = s.cfg.Tasks.UpdateStatus(id, coprocessor.TaskVerified, nil, nil, "")
	if err := s.escrow.Pay(id, winner, price); err == nil {
		s.cfg.Slasher.Reward(winner, price, id)
	}
	s.escrow.RefundRemainder(id)

	run.finish(TaskResult{
		TaskID:  id,
		Status:  coprocessor.TaskVerified,
		Output:  output,
		Workers: []types.Address{winner},
	})
}

// SubmitChallenge disputes a parked optimistic result. The referee re-executes
// the task; a differing output upholds the challenge (provider slashed, task
// failed, escrow refunded), an equal output rejects it (task finalizes
// immediately — slice-1 single-challenger simplification).
func (s *Scheduler) SubmitChallenge(id types.Hash, challenger types.Address, bond uint64, reason string) (bool, error) {
	s.mu.Lock()
	run, ok := s.runs[id]
	referee, hasRef := s.referee, s.hasRef
	s.mu.Unlock()
	if !ok {
		return false, ErrTaskUnknown
	}
	if !hasRef {
		return false, ErrNoReferee
	}
	if s.cfg.Challenges == nil {
		return false, ErrNotChallengeable
	}

	if err := s.cfg.Tasks.TransitionToChallenged(id, challenger); err != nil {
		return false, err
	}
	if !run.tryClaim() {
		// The finalizer claimed settlement between our window check and now.
		// Roll the task back to Verified (a legal Challenged→Verified
		// transition) so the states stay consistent; the finalizer's
		// settlement stands.
		_ = s.cfg.Tasks.UpdateStatus(id, coprocessor.TaskVerified, nil, nil, "")
		return false, ErrNotChallengeable
	}
	chID, err := s.cfg.Challenges.Submit(id, challenger, bond, reason)
	if err != nil {
		// Claim already taken by us: settle as verified so the task cannot
		// hang; the duplicate-challenge submitter gets the error.
		s.settleChallengedAsVerified(id, run)
		return false, err
	}

	// Referee re-execution under the task's own lease budget.
	refCtx, cancel := context.WithTimeout(s.ctx, run.spec.Lease)
	defer cancel()
	refOut, refErr := s.cfg.Executor.Execute(refCtx, referee, run.spec)

	run.mu.Lock()
	original := run.parkedOutput
	winner := run.winner
	run.mu.Unlock()

	upheld := refErr == nil && !bytes.Equal(refOut, original)
	_ = s.cfg.Challenges.Resolve(chID, upheld)

	if upheld {
		_ = s.cfg.Tasks.UpdateStatus(id, coprocessor.TaskFailed, nil, nil, "challenge upheld: "+reason)
		s.cfg.Slasher.Slash(winner, coprocessor.SlashChallengeUpheld, id)
		s.escrow.RefundRemainder(id)
		run.finish(TaskResult{TaskID: id, Status: coprocessor.TaskFailed, Err: "challenge upheld"})
		return true, nil
	}

	// Rejected: the original result stands; finalize immediately (slice-1
	// single-challenger simplification — a production window would reopen
	// for other challengers).
	s.settleChallengedAsVerified(id, run)
	return false, nil
}

// settleChallengedAsVerified completes a challenged-but-vindicated task:
// Verified, winner paid, remainder refunded. Caller must hold the claim.
func (s *Scheduler) settleChallengedAsVerified(id types.Hash, run *taskRun) {
	run.mu.Lock()
	winner, price := run.winner, run.winPrice
	output := run.parkedOutput
	run.mu.Unlock()

	_ = s.cfg.Tasks.UpdateStatus(id, coprocessor.TaskVerified, nil, nil, "")
	if err := s.escrow.Pay(id, winner, price); err == nil {
		s.cfg.Slasher.Reward(winner, price, id)
	}
	s.escrow.RefundRemainder(id)
	run.finish(TaskResult{
		TaskID:  id,
		Status:  coprocessor.TaskVerified,
		Output:  output,
		Workers: []types.Address{winner},
	})
}

// ---------------------------------------------------------------------------
// quorum mode

func (s *Scheduler) runQuorum(id types.Hash, run *taskRun) {
	spec := run.spec
	deadlineCtx, cancel := context.WithTimeout(s.ctx, spec.Deadline)
	defer cancel()

	if _, err := s.cfg.Tasks.TransitionToProving(id); err != nil {
		s.settleFail(id, run, coprocessor.TaskFailed, fmt.Sprintf("transition: %v", err))
		return
	}

	excluded := make(map[types.Address]bool)
	escalated := false
	r := spec.Redundancy

	for round := 0; round < 2; round++ {
		workers := s.selectQuorum(spec, r, excluded)
		if workers == nil {
			s.settleFail(id, run, coprocessor.TaskFailed,
				fmt.Sprintf("cannot select %d workers (dispersal=%v)", r, spec.RequireDisperse))
			return
		}

		outcomes := make([]attemptOutcome, len(workers))
		var wg sync.WaitGroup
		for i, w := range workers {
			wg.Add(1)
			go func(i int, w WorkerInfo) {
				defer wg.Done()
				outcomes[i] = s.executeAttempt(deadlineCtx, w.Addr, spec)
			}(i, w)
		}
		wg.Wait()
		if deadlineCtx.Err() != nil {
			s.expire(id, run, "deadline exceeded")
			return
		}

		// Group successful outputs by hash; find the strict majority of r.
		groups := make(map[types.Hash][]int)
		for i, o := range outcomes {
			excluded[o.worker] = true
			if o.err == nil {
				h := crypto.Keccak256Hash(o.output)
				groups[h] = append(groups[h], i)
			}
		}
		var majority []int
		for _, idxs := range groups {
			if len(idxs)*2 > r {
				majority = idxs
				break
			}
		}

		if majority != nil {
			// Penalize everyone outside the majority (wrong output or error).
			inMajority := make(map[types.Address]bool, len(majority))
			for _, i := range majority {
				inMajority[outcomes[i].worker] = true
			}
			for _, o := range outcomes {
				if !inMajority[o.worker] {
					s.cfg.Registry.IncrementTasks(o.worker, true)
					s.cfg.Registry.UpdateReputation(o.worker, -500)
				}
			}
			s.settleQuorum(id, run, outcomes, majority, escalated)
			return
		}

		// No majority: penalize every disagreeing participant, escalate once
		// with a bigger, fresh worker set.
		for _, o := range outcomes {
			s.cfg.Registry.IncrementTasks(o.worker, true)
			s.cfg.Registry.UpdateReputation(o.worker, -500)
		}
		if round == 1 {
			break
		}
		escalated = true
		r += 2
	}
	s.settleFail(id, run, coprocessor.TaskFailed, "no quorum majority after escalation")
}

// settleQuorum accepts the majority output: task Verified, MaxPrice split
// evenly across majority members, remainder refunded.
func (s *Scheduler) settleQuorum(id types.Hash, run *taskRun, outcomes []attemptOutcome, majority []int, escalated bool) {
	if !run.tryClaim() {
		return
	}
	output := outcomes[majority[0]].output
	members := make([]types.Address, 0, len(majority))
	for _, i := range majority {
		members = append(members, outcomes[i].worker)
	}
	share := run.spec.MaxPrice / uint64(len(members))

	_ = s.cfg.Tasks.UpdateStatus(id, coprocessor.TaskVerified, nil, output, "")
	for _, m := range members {
		if err := s.escrow.Pay(id, m, share); err == nil {
			s.cfg.Slasher.Reward(m, share, id)
		}
	}
	s.escrow.RefundRemainder(id)

	run.finish(TaskResult{
		TaskID:    id,
		Status:    coprocessor.TaskVerified,
		Output:    append([]byte(nil), output...),
		Workers:   members,
		Escalated: escalated,
	})
}

// ---------------------------------------------------------------------------
// terminal helpers

// settleFail records a terminal failure and refunds the escrow.
func (s *Scheduler) settleFail(id types.Hash, run *taskRun, status coprocessor.TaskStatus, msg string) {
	if !run.tryClaim() {
		return
	}
	_ = s.cfg.Tasks.UpdateStatus(id, status, nil, nil, msg)
	s.escrow.RefundRemainder(id)
	run.finish(TaskResult{TaskID: id, Status: status, Err: msg})
}

// expire marks the task Expired (deadline/no-workers) and refunds.
func (s *Scheduler) expire(id types.Hash, run *taskRun, msg string) {
	s.settleFail(id, run, coprocessor.TaskExpired, msg)
}
