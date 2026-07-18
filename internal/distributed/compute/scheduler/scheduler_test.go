// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Acceptance tests for the compute scheduler (slice 1 of the distributed
// compute rollout, see docs/distributed-compute-design-2026-07-06.md).
// These tests are the acceptance contract: every behavior promised by the
// design doc's §2 (lifecycle), §3 (dispatch), §4 (acceptance) and §5
// (timeout/fault-tolerance) that is in scope for slice 1 has a test here.

package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// ---------------------------------------------------------------------------
// test harness

// fakeBehavior scripts one worker's response to an execution request.
type fakeBehavior struct {
	output []byte        // returned on success
	err    error         // returned instead, if set
	delay  time.Duration // sleep before responding (ctx-aware)
	block  bool          // never respond until ctx is cancelled
}

// fakeExecutor scripts per-worker behaviors and records dispatch counts.
type fakeExecutor struct {
	mu        sync.Mutex
	behaviors map[types.Address]fakeBehavior
	calls     map[types.Address]int
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		behaviors: make(map[types.Address]fakeBehavior),
		calls:     make(map[types.Address]int),
	}
}

func (f *fakeExecutor) set(addr types.Address, b fakeBehavior) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.behaviors[addr] = b
}

func (f *fakeExecutor) callCount(addr types.Address) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[addr]
}

func (f *fakeExecutor) Execute(ctx context.Context, worker types.Address, spec *TaskSpec) ([]byte, error) {
	f.mu.Lock()
	b, ok := f.behaviors[worker]
	f.calls[worker]++
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no behavior scripted for worker %s", worker.Hex())
	}
	if b.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if b.delay > 0 {
		select {
		case <-time.After(b.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	return b.output, nil
}

// addr derives a distinct test address from a seed byte.
func addr(seed byte) types.Address {
	var a types.Address
	for i := range a {
		a[i] = seed
	}
	return a
}

// harness bundles a scheduler with its backing coprocessor components.
type harness struct {
	sched    *Scheduler
	tasks    *coprocessor.TaskManager
	registry *coprocessor.ProviderRegistry
	slasher  *coprocessor.SlashManager
	exec     *fakeExecutor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tasks := coprocessor.NewTaskManager(1024, time.Hour)
	registry := coprocessor.NewProviderRegistry(100) // min stake 100
	market := coprocessor.NewMarketplace(registry)
	slasher := coprocessor.NewSlashManager(registry, 10) // slash 10% of stake
	challenges := coprocessor.NewChallengeManager(60)
	exec := newFakeExecutor()

	sched := New(Config{
		Tasks:      tasks,
		Registry:   registry,
		Market:     market,
		Slasher:    slasher,
		Challenges: challenges,
		Executor:   exec,
	})
	t.Cleanup(sched.Close)
	return &harness{sched: sched, tasks: tasks, registry: registry, slasher: slasher, exec: exec}
}

// addWorker registers a provider and adds it to the scheduler's worker set.
func (h *harness) addWorker(t *testing.T, a types.Address, class DeviceClass, group string, stake uint64) {
	t.Helper()
	if err := h.registry.Register(a, stake, []coprocessor.Capability{coprocessor.CapWASM}); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	if err := h.sched.AddWorker(a, class, group); err != nil {
		t.Fatalf("add worker: %v", err)
	}
}

func baseSpec(mode VerifyMode) *TaskSpec {
	return &TaskSpec{
		ProgramHash:     crypto.Keccak256Hash([]byte("prog")),
		Bytecode:        []byte("\x00asm-test"),
		FuncName:        "run",
		Input:           []byte("input-1"),
		FuelLimit:       1_000_000,
		MaxPrice:        900,
		Deadline:        3 * time.Second,
		Lease:           300 * time.Millisecond,
		Mode:            mode,
		Redundancy:      3,
		ChallengeWindow: 150 * time.Millisecond,
		Capability:      coprocessor.CapWASM,
	}
}

// waitDone waits for terminal state and returns the completion record.
func (h *harness) waitDone(t *testing.T, id types.Hash) *TaskResult {
	t.Helper()
	res, err := h.sched.WaitTask(id, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitTask: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// acceptance tests

// A1: optimistic happy path — auction, execute, optimistic window passes,
// task finalizes Verified, winner rewarded, escrow settled exactly.
func TestOptimisticHappyPath(t *testing.T) {
	h := newHarness(t)
	w1, w2 := addr(1), addr(2)
	h.addWorker(t, w1, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, w2, ClassIDC, "dc-2", 10_000)
	h.exec.set(w1, fakeBehavior{output: []byte("result-A")})
	h.exec.set(w2, fakeBehavior{output: []byte("result-A")})
	// w2 quotes cheaper → must win the reverse auction.
	h.sched.SetQuote(w1, 800)
	h.sched.SetQuote(w2, 500)

	spec := baseSpec(VerifyOptimistic)
	id, err := h.sched.Submit(spec, addr(0xEE))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified (err=%s)", res.Status, res.Err)
	}
	if !bytes.Equal(res.Output, []byte("result-A")) {
		t.Fatalf("output = %q", res.Output)
	}
	if len(res.Workers) != 1 || res.Workers[0] != w2 {
		t.Fatalf("winner = %v, want cheapest bidder %v", res.Workers, w2)
	}
	// Settlement: winner paid its bid, remainder refunded.
	if got := h.sched.Escrow().PaidTo(w2); got != 500 {
		t.Fatalf("paid to winner = %d, want 500", got)
	}
	if got := h.sched.Escrow().Refunded(id); got != 400 {
		t.Fatalf("refunded = %d, want 400", got)
	}
	// Reputation reward recorded.
	if evts := h.slasher.RewardHistory(); len(evts) != 1 || evts[0].Provider != w2 {
		t.Fatalf("reward events = %+v", evts)
	}
	p, _ := h.registry.GetSnapshot(w2)
	if p.Reputation <= 5000 {
		t.Fatalf("winner reputation did not increase: %d", p.Reputation)
	}
}

// A2: optimistic challenge — provider returns a wrong result, challenger
// disputes within the window, referee re-execution upholds the challenge,
// provider is slashed, escrow refunded to submitter.
func TestOptimisticChallengeSlash(t *testing.T) {
	h := newHarness(t)
	cheat, referee := addr(3), addr(4)
	h.addWorker(t, cheat, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, referee, ClassIDC, "dc-2", 10_000)
	h.exec.set(cheat, fakeBehavior{output: []byte("WRONG")})
	h.exec.set(referee, fakeBehavior{output: []byte("truth")})
	h.sched.SetQuote(cheat, 100) // cheat wins the auction
	h.sched.SetQuote(referee, 9_999_999)
	h.sched.SetReferee(referee)

	spec := baseSpec(VerifyOptimistic)
	spec.ChallengeWindow = time.Hour // hold the window open for the challenge
	id, err := h.sched.Submit(spec, addr(0xEE))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Wait until the task parks in OptimisticVerified.
	waitStatus(t, h.tasks, id, coprocessor.TaskOptimisticVerified, 5*time.Second)

	upheld, err := h.sched.SubmitChallenge(id, addr(0xCC), 50, "wrong output")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if !upheld {
		t.Fatalf("challenge should be upheld")
	}
	res := h.waitDone(t, id)
	if res.Status != coprocessor.TaskFailed {
		t.Fatalf("status = %v, want Failed", res.Status)
	}
	// Provider slashed: stake reduced, status Slashed, slash event recorded.
	p, _ := h.registry.GetSnapshot(cheat)
	if p.Stake >= 10_000 {
		t.Fatalf("cheat stake not deducted: %d", p.Stake)
	}
	if p.Status != coprocessor.ProviderSlashed {
		t.Fatalf("cheat status = %v, want Slashed", p.Status)
	}
	evts := h.slasher.SlashHistory()
	if len(evts) != 1 || evts[0].Condition != coprocessor.SlashChallengeUpheld {
		t.Fatalf("slash events = %+v", evts)
	}
	// Full escrow refunded (no payment to the cheat).
	if got := h.sched.Escrow().PaidTo(cheat); got != 0 {
		t.Fatalf("cheat was paid %d", got)
	}
	if got := h.sched.Escrow().Refunded(id); got != spec.MaxPrice {
		t.Fatalf("refunded = %d, want %d", got, spec.MaxPrice)
	}
	// Good-faith challenger gets its 50 bond back.
	if got := h.sched.Escrow().PaidTo(addr(0xCC)); got != 50 {
		t.Fatalf("upheld challenger got %d back, want 50 (bond returned)", got)
	}
}

// A3: rejected challenge — result was correct; challenger's bond is forfeited
// and the task still finalizes Verified.
func TestOptimisticChallengeRejected(t *testing.T) {
	h := newHarness(t)
	honest, referee := addr(5), addr(6)
	h.addWorker(t, honest, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, referee, ClassIDC, "dc-2", 10_000)
	h.exec.set(honest, fakeBehavior{output: []byte("truth")})
	h.exec.set(referee, fakeBehavior{output: []byte("truth")})
	h.sched.SetQuote(honest, 100)
	h.sched.SetQuote(referee, 9_999_999)
	h.sched.SetReferee(referee)

	spec := baseSpec(VerifyOptimistic)
	spec.ChallengeWindow = time.Hour
	id, _ := h.sched.Submit(spec, addr(0xEE))
	waitStatus(t, h.tasks, id, coprocessor.TaskOptimisticVerified, 5*time.Second)

	upheld, err := h.sched.SubmitChallenge(id, addr(0xCC), 50, "spurious")
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if upheld {
		t.Fatalf("challenge should be rejected")
	}
	res := h.waitDone(t, id)
	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified", res.Status)
	}
	// The spurious challenger's 50 bond is forfeited to the wronged honest
	// provider on top of its 100 task reward — challenging is no longer free.
	if got := h.sched.Escrow().PaidTo(honest); got != 150 {
		t.Fatalf("honest provider paid %d, want 150 (100 reward + 50 forfeited bond)", got)
	}
	if got := h.sched.Escrow().PaidTo(addr(0xCC)); got != 0 {
		t.Fatalf("rejected challenger got %d back, want 0 (bond forfeited)", got)
	}
}

// A4: quorum majority — r=3, one faulty worker; majority output accepted,
// majority rewarded (price split), minority penalized in reputation but NOT
// slashed (phone-class workers carry no meaningful stake).
func TestQuorumMajorityAccept(t *testing.T) {
	h := newHarness(t)
	good1, good2, bad := addr(7), addr(8), addr(9)
	h.addWorker(t, good1, ClassPhone, "asn-1", 100)
	h.addWorker(t, good2, ClassPhone, "asn-2", 100)
	h.addWorker(t, bad, ClassPhone, "asn-3", 100)
	h.exec.set(good1, fakeBehavior{output: []byte("agree")})
	h.exec.set(good2, fakeBehavior{output: []byte("agree")})
	h.exec.set(bad, fakeBehavior{output: []byte("poison")})

	spec := baseSpec(VerifyQuorum)
	spec.Redundancy = 3
	spec.RequireDisperse = true
	spec.MaxPrice = 900
	id, err := h.sched.Submit(spec, addr(0xEE))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified (err=%s)", res.Status, res.Err)
	}
	if !bytes.Equal(res.Output, []byte("agree")) {
		t.Fatalf("output = %q", res.Output)
	}
	// Majority members split the price; the poisoner gets nothing.
	if got := h.sched.Escrow().PaidTo(good1); got != 450 {
		t.Fatalf("good1 paid %d, want 450", got)
	}
	if got := h.sched.Escrow().PaidTo(good2); got != 450 {
		t.Fatalf("good2 paid %d, want 450", got)
	}
	if got := h.sched.Escrow().PaidTo(bad); got != 0 {
		t.Fatalf("poisoner paid %d, want 0", got)
	}
	// Poisoner: reputation down + failure recorded, but stake untouched.
	p, _ := h.registry.GetSnapshot(bad)
	if p.Reputation >= 5000 {
		t.Fatalf("poisoner reputation = %d, want < 5000", p.Reputation)
	}
	if p.TasksFailed != 1 {
		t.Fatalf("poisoner TasksFailed = %d, want 1", p.TasksFailed)
	}
	if p.Stake != 100 {
		t.Fatalf("poisoner stake = %d, want untouched 100", p.Stake)
	}
}

// A5: quorum with no majority escalates once with a bigger, fresh worker set;
// the escalation round reaches a majority and the task verifies.
func TestQuorumNoMajorityEscalate(t *testing.T) {
	h := newHarness(t)
	// First round: 3-way disagreement.
	r1 := []types.Address{addr(10), addr(11), addr(12)}
	for i, a := range r1 {
		h.addWorker(t, a, ClassPhone, fmt.Sprintf("g%d", i), 100)
		h.exec.set(a, fakeBehavior{output: []byte{byte(i)}}) // all different
	}
	// Escalation pool: 5 fresh workers, all agree.
	for i := 0; i < 5; i++ {
		a := addr(byte(20 + i))
		h.addWorker(t, a, ClassPhone, fmt.Sprintf("e%d", i), 100)
		h.exec.set(a, fakeBehavior{output: []byte("consensus")})
	}

	spec := baseSpec(VerifyQuorum)
	spec.Redundancy = 3
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified after escalation (err=%s)", res.Status, res.Err)
	}
	if !bytes.Equal(res.Output, []byte("consensus")) {
		t.Fatalf("output = %q", res.Output)
	}
	if !res.Escalated {
		t.Fatalf("expected the escalation flag to be set")
	}
	// The escalation round must not reuse first-round workers.
	for _, w := range res.Workers {
		for _, old := range r1 {
			if w == old {
				t.Fatalf("escalation reused first-round worker %v", w)
			}
		}
	}
}

// A6: lease timeout — assigned worker hangs; lease expires; task is
// reassigned to a fresh worker and completes. The hung worker takes a
// reputation hit; in optimistic mode it is also slash-able for timeout.
func TestLeaseTimeoutReassign(t *testing.T) {
	h := newHarness(t)
	hang, backup := addr(30), addr(31)
	h.addWorker(t, hang, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, backup, ClassIDC, "dc-2", 10_000)
	h.exec.set(hang, fakeBehavior{block: true})
	h.exec.set(backup, fakeBehavior{output: []byte("saved")})
	h.sched.SetQuote(hang, 100) // hang wins the auction first
	h.sched.SetQuote(backup, 200)

	spec := baseSpec(VerifyOptimistic)
	spec.Lease = 100 * time.Millisecond
	spec.ChallengeWindow = 50 * time.Millisecond
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v, want Verified via reassignment (err=%s)", res.Status, res.Err)
	}
	if len(res.Workers) != 1 || res.Workers[0] != backup {
		t.Fatalf("final worker = %v, want backup %v", res.Workers, backup)
	}
	if h.exec.callCount(hang) != 1 {
		t.Fatalf("hung worker dispatched %d times, want 1", h.exec.callCount(hang))
	}
	// Timeout slash recorded against the hung worker.
	evts := h.slasher.SlashHistory()
	if len(evts) != 1 || evts[0].Provider != hang || evts[0].Condition != coprocessor.SlashTimeout {
		t.Fatalf("slash events = %+v, want one SlashTimeout for hang", evts)
	}
	if got := h.sched.Escrow().PaidTo(hang); got != 0 {
		t.Fatalf("hung worker paid %d", got)
	}
	if got := h.sched.Escrow().PaidTo(backup); got != 200 {
		t.Fatalf("backup paid %d, want its bid 200", got)
	}
}

// A7: hedged dispatch — primary is slow; after HedgeDelay a backup copy is
// launched; the fast backup's result wins; the late primary result must not
// double-pay or change the outcome.
func TestHedgedDispatch(t *testing.T) {
	h := newHarness(t)
	slow, fast := addr(32), addr(33)
	h.addWorker(t, slow, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, fast, ClassIDC, "dc-2", 10_000)
	h.exec.set(slow, fakeBehavior{output: []byte("late"), delay: 600 * time.Millisecond})
	h.exec.set(fast, fakeBehavior{output: []byte("fast")})
	h.sched.SetQuote(slow, 100)
	h.sched.SetQuote(fast, 200)

	spec := baseSpec(VerifyOptimistic)
	spec.Lease = 2 * time.Second
	spec.HedgeDelay = 100 * time.Millisecond
	spec.ChallengeWindow = 50 * time.Millisecond
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v (err=%s)", res.Status, res.Err)
	}
	if !bytes.Equal(res.Output, []byte("fast")) {
		t.Fatalf("output = %q, want the hedge winner's", res.Output)
	}
	if h.exec.callCount(fast) != 1 {
		t.Fatalf("hedge worker dispatched %d times", h.exec.callCount(fast))
	}
	// Exactly one worker paid (no double settlement).
	total := h.sched.Escrow().PaidTo(slow) + h.sched.Escrow().PaidTo(fast)
	if total != 200 {
		t.Fatalf("total paid = %d, want exactly the hedge winner's 200", total)
	}
}

// A8: deadline expiry — every eligible worker hangs; the task fails at its
// deadline and the full escrow is refunded.
func TestDeadlineExpiry(t *testing.T) {
	h := newHarness(t)
	w1, w2 := addr(34), addr(35)
	h.addWorker(t, w1, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, w2, ClassIDC, "dc-2", 10_000)
	h.exec.set(w1, fakeBehavior{block: true})
	h.exec.set(w2, fakeBehavior{block: true})
	h.sched.SetQuote(w1, 100)
	h.sched.SetQuote(w2, 100)

	spec := baseSpec(VerifyOptimistic)
	spec.Deadline = 500 * time.Millisecond
	spec.Lease = 150 * time.Millisecond
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskExpired && res.Status != coprocessor.TaskFailed {
		t.Fatalf("status = %v, want Expired/Failed", res.Status)
	}
	if got := h.sched.Escrow().Refunded(id); got != spec.MaxPrice {
		t.Fatalf("refunded = %d, want full escrow %d", got, spec.MaxPrice)
	}
}

// A9: quorum group dispersal — with RequireDisperse, replicas must come from
// distinct groups; if not enough distinct groups exist the task fails fast
// (no silent weakening of the sybil defense).
func TestGroupDispersalEnforced(t *testing.T) {
	h := newHarness(t)
	// 3 workers but only 2 distinct groups.
	h.addWorker(t, addr(40), ClassPhone, "asn-1", 100)
	h.addWorker(t, addr(41), ClassPhone, "asn-1", 100)
	h.addWorker(t, addr(42), ClassPhone, "asn-2", 100)
	for _, a := range []types.Address{addr(40), addr(41), addr(42)} {
		h.exec.set(a, fakeBehavior{output: []byte("x")})
	}

	spec := baseSpec(VerifyQuorum)
	spec.Redundancy = 3
	spec.RequireDisperse = true
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskFailed {
		t.Fatalf("status = %v, want Failed (insufficient dispersal)", res.Status)
	}
	if got := h.sched.Escrow().Refunded(id); got != spec.MaxPrice {
		t.Fatalf("refunded = %d, want full escrow", got)
	}
}

// A10: escrow conservation — across a mixed batch of outcomes, every locked
// wei is either paid to exactly one worker set or refunded; nothing leaks.
func TestEscrowConservation(t *testing.T) {
	h := newHarness(t)
	good, bad := addr(50), addr(51)
	h.addWorker(t, good, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, bad, ClassIDC, "dc-2", 10_000)
	h.exec.set(good, fakeBehavior{output: []byte("ok")})
	h.exec.set(bad, fakeBehavior{block: true})
	h.sched.SetQuote(good, 300)
	h.sched.SetQuote(bad, 100)

	var ids []types.Hash
	var totalLocked uint64
	for i := 0; i < 6; i++ {
		spec := baseSpec(VerifyOptimistic)
		spec.Deadline = 2 * time.Second
		spec.Lease = 100 * time.Millisecond
		spec.ChallengeWindow = 50 * time.Millisecond
		id, err := h.sched.Submit(spec, addr(0xEE))
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		ids = append(ids, id)
		totalLocked += spec.MaxPrice
	}
	var refunded uint64
	for _, id := range ids {
		h.waitDone(t, id)
		refunded += h.sched.Escrow().Refunded(id)
	}
	paid := h.sched.Escrow().PaidTo(good) + h.sched.Escrow().PaidTo(bad)
	if paid+refunded != totalLocked {
		t.Fatalf("escrow leak: paid %d + refunded %d != locked %d", paid, refunded, totalLocked)
	}
}

// A11: executor errors (worker crash) are treated like lease expiry:
// reassign, penalize, and still complete on a healthy worker.
func TestWorkerErrorReassign(t *testing.T) {
	h := newHarness(t)
	crashy, healthy := addr(60), addr(61)
	h.addWorker(t, crashy, ClassIDC, "dc-1", 10_000)
	h.addWorker(t, healthy, ClassIDC, "dc-2", 10_000)
	h.exec.set(crashy, fakeBehavior{err: fmt.Errorf("worker exploded")})
	h.exec.set(healthy, fakeBehavior{output: []byte("ok")})
	h.sched.SetQuote(crashy, 100)
	h.sched.SetQuote(healthy, 200)

	spec := baseSpec(VerifyOptimistic)
	spec.ChallengeWindow = 50 * time.Millisecond
	id, _ := h.sched.Submit(spec, addr(0xEE))
	res := h.waitDone(t, id)

	if res.Status != coprocessor.TaskVerified {
		t.Fatalf("status = %v (err=%s)", res.Status, res.Err)
	}
	if len(res.Workers) != 1 || res.Workers[0] != healthy {
		t.Fatalf("workers = %v, want healthy only", res.Workers)
	}
	p, _ := h.registry.GetSnapshot(crashy)
	if p.TasksFailed != 1 {
		t.Fatalf("crashy TasksFailed = %d, want 1", p.TasksFailed)
	}
}

// A12: concurrent load — many tasks across both modes race through the
// scheduler; all reach a terminal state and escrow conserves. Run with -race.
func TestConcurrentSubmissions(t *testing.T) {
	h := newHarness(t)
	var pool []types.Address
	for i := 0; i < 8; i++ {
		a := addr(byte(70 + i))
		h.addWorker(t, a, ClassPhone, fmt.Sprintf("g%d", i), 100)
		h.exec.set(a, fakeBehavior{output: []byte("agree")})
		h.sched.SetQuote(a, 100)
		pool = append(pool, a)
	}
	_ = pool

	const n = 24
	var wg sync.WaitGroup
	ids := make([]types.Hash, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := VerifyQuorum
			if i%2 == 0 {
				mode = VerifyOptimistic
			}
			spec := baseSpec(mode)
			spec.ChallengeWindow = 30 * time.Millisecond
			spec.Input = []byte(fmt.Sprintf("input-%d", i))
			ids[i], errs[i] = h.sched.Submit(spec, addr(0xEE))
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("submit %d: %v", i, errs[i])
		}
		res := h.waitDone(t, ids[i])
		if res.Status != coprocessor.TaskVerified {
			t.Fatalf("task %d status = %v (err=%s)", i, res.Status, res.Err)
		}
	}
}

// A13: spec validation — the timeout hierarchy (lease < deadline) and quorum
// parameters are validated at submission, before any escrow is locked.
func TestSpecValidation(t *testing.T) {
	h := newHarness(t)
	h.addWorker(t, addr(80), ClassIDC, "dc-1", 10_000)

	bad := baseSpec(VerifyOptimistic)
	bad.Lease = 5 * time.Second
	bad.Deadline = 1 * time.Second // lease > deadline
	if _, err := h.sched.Submit(bad, addr(0xEE)); err == nil {
		t.Fatalf("want error for lease > deadline")
	}

	bad2 := baseSpec(VerifyQuorum)
	bad2.Redundancy = 0
	if _, err := h.sched.Submit(bad2, addr(0xEE)); err == nil {
		t.Fatalf("want error for redundancy < 1")
	}

	bad3 := baseSpec(VerifyQuorum)
	bad3.Redundancy = 4 // even quorum cannot produce a strict majority tie-break guarantee
	if _, err := h.sched.Submit(bad3, addr(0xEE)); err == nil {
		t.Fatalf("want error for even redundancy")
	}
}

// waitStatus polls the task manager until the task reaches the given status.
func waitStatus(t *testing.T, tm *coprocessor.TaskManager, id types.Hash, want coprocessor.TaskStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if snap, ok := tm.GetTaskSnapshot(id); ok && snap.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, _ := tm.GetTaskSnapshot(id)
	t.Fatalf("task never reached %v (now %v)", want, snap.Status)
}
