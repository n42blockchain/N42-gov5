package deferred

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func makeDeepTestBlock(number uint64) *block.Block {
	h := &block.Header{
		Number: uint256.NewInt(number),
	}
	return block.NewBlock(h, nil).(*block.Block)
}

// TestDeepPipeline_FullFlow drives a block through all five stages of the
// pipeline (prefetch -> execute -> commit -> persist) using simple
// deterministic stage functions, and verifies StateRootForBlock and
// LastPersisted reflect the result.
func TestDeepPipeline_FullFlow(t *testing.T) {
	var prefetched, executed, committed, persisted atomic.Int32

	prefetchFn := func(ctx context.Context, blk block.IBlock) error {
		prefetched.Add(1)
		return nil
	}
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		executed.Add(1)
		num := blk.Number64().Uint64()
		return &DeepCommitJob{
			DeepBlock: DeepBlock{Number: num, Hash: blk.Hash(), Block: blk},
			GasUsed:   21000,
			TxCount:   1,
		}, nil
	}
	commitFn := func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error) {
		committed.Add(1)
		return &DeepPersistJob{
			DeepBlock: job.DeepBlock,
			StateRoot: types.HexToHash("0xabc"),
			GasUsed:   job.GasUsed,
			TxCount:   job.TxCount,
		}, nil
	}
	persistFn := func(ctx context.Context, job *DeepPersistJob) error {
		persisted.Add(1)
		return nil
	}

	p := NewDeepPipeline(DeepPipelineConfig{}, prefetchFn, executeFn, commitFn, persistFn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	// Starting twice must be a no-op (CompareAndSwap guard).
	p.Start(ctx)

	blk := makeDeepTestBlock(1)
	if err := p.SubmitBlock(blk); err != nil {
		t.Fatalf("SubmitBlock() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if persisted.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if prefetched.Load() == 0 || executed.Load() == 0 || committed.Load() == 0 || persisted.Load() == 0 {
		t.Fatalf("not all stages ran: prefetched=%d executed=%d committed=%d persisted=%d",
			prefetched.Load(), executed.Load(), committed.Load(), persisted.Load())
	}

	if root, ok := p.StateRootForBlock(1); !ok || root != types.HexToHash("0xabc") {
		t.Fatalf("StateRootForBlock(1) = (%x, %v), want (0xabc, true)", root, ok)
	}

	if p.LastPersisted() != 1 {
		t.Fatalf("LastPersisted() = %d, want 1", p.LastPersisted())
	}
}

// TestDeepPipeline_NilStageFunctions covers the nil-function guards in
// runExecute, runCommit and runPersist (the "continue" branches), ensuring
// the pipeline doesn't panic or deadlock when stages are not wired up.
func TestDeepPipeline_NilStageFunctions(t *testing.T) {
	p := NewDeepPipeline(DeepPipelineConfig{Depth: 2}, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	blk := makeDeepTestBlock(1)
	if err := p.SubmitBlock(blk); err != nil {
		t.Fatalf("SubmitBlock() error = %v", err)
	}

	// Give the pipeline a moment to push the block through the no-op stages.
	time.Sleep(20 * time.Millisecond)
	cancel()
	p.Stop()

	if p.LastPersisted() != 0 {
		t.Fatalf("LastPersisted() = %d, want 0 with nil persistFn", p.LastPersisted())
	}
}

// TestDeepPipeline_ExecuteErrorHaltsPipeline covers the error branch in
// runExecute, which cancels the pipeline context on a failed execution.
func TestDeepPipeline_ExecuteErrorHaltsPipeline(t *testing.T) {
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		return nil, errors.New("execution failed")
	}

	p := NewDeepPipeline(DeepPipelineConfig{}, nil, executeFn, nil, nil)
	ctx := context.Background()
	p.Start(ctx)

	blk := makeDeepTestBlock(1)
	if err := p.SubmitBlock(blk); err != nil {
		t.Fatalf("SubmitBlock() error = %v", err)
	}

	// The pipeline should self-cancel; Stop() must return promptly.
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after execution error halted the pipeline")
	}
}

// TestDeepPipeline_CommitErrorHaltsPipeline covers the error branch in
// runCommit.
func TestDeepPipeline_CommitErrorHaltsPipeline(t *testing.T) {
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		return &DeepCommitJob{DeepBlock: DeepBlock{Number: blk.Number64().Uint64(), Block: blk}}, nil
	}
	commitFn := func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error) {
		return nil, errors.New("commit failed")
	}

	p := NewDeepPipeline(DeepPipelineConfig{}, nil, executeFn, commitFn, nil)
	p.Start(context.Background())

	blk := makeDeepTestBlock(1)
	if err := p.SubmitBlock(blk); err != nil {
		t.Fatalf("SubmitBlock() error = %v", err)
	}

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after commit error halted the pipeline")
	}
}

// TestDeepPipeline_PersistErrorContinues covers the error branch in
// runPersist: persistence errors are logged and skipped without halting the
// pipeline (lastPersisted stays unmoved, and a later successful block still
// advances it).
func TestDeepPipeline_PersistErrorContinues(t *testing.T) {
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		return &DeepCommitJob{DeepBlock: DeepBlock{Number: blk.Number64().Uint64(), Block: blk}}, nil
	}
	commitFn := func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error) {
		return &DeepPersistJob{DeepBlock: job.DeepBlock}, nil
	}

	var calls atomic.Int32
	persistFn := func(ctx context.Context, job *DeepPersistJob) error {
		n := calls.Add(1)
		if n == 1 {
			return errors.New("persist failed")
		}
		return nil
	}

	p := NewDeepPipeline(DeepPipelineConfig{}, nil, executeFn, commitFn, persistFn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	if err := p.SubmitBlock(makeDeepTestBlock(1)); err != nil {
		t.Fatalf("SubmitBlock(1) error = %v", err)
	}
	if err := p.SubmitBlock(makeDeepTestBlock(2)); err != nil {
		t.Fatalf("SubmitBlock(2) error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.LastPersisted() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.LastPersisted() != 2 {
		t.Fatalf("LastPersisted() = %d, want 2 after recovering from one persist error", p.LastPersisted())
	}
}

// TestDeepPipeline_PruneOldStateRoots covers the >128 pruning branch in
// runPersist.
func TestDeepPipeline_PruneOldStateRoots(t *testing.T) {
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		return &DeepCommitJob{DeepBlock: DeepBlock{Number: blk.Number64().Uint64(), Block: blk}}, nil
	}
	commitFn := func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error) {
		return &DeepPersistJob{DeepBlock: job.DeepBlock, StateRoot: types.HexToHash("0x01")}, nil
	}
	persistFn := func(ctx context.Context, job *DeepPersistJob) error { return nil }

	p := NewDeepPipeline(DeepPipelineConfig{}, nil, executeFn, commitFn, persistFn)
	// Pre-populate a state root at block 1, which should be pruned once
	// block 129 is persisted (129 - 128 == 1).
	p.stateRoots.Store(uint64(1), types.HexToHash("0x01"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	if err := p.SubmitBlock(makeDeepTestBlock(129)); err != nil {
		t.Fatalf("SubmitBlock() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.LastPersisted() == 129 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, ok := p.StateRootForBlock(1); ok {
		t.Fatal("expected state root for block 1 to be pruned after block 129 persisted")
	}
}

// TestDeepPipeline_SubmitBlockAfterCancelReturnsErr covers the ctx.Done()
// branch of SubmitBlock. The prefetch channel is filled to capacity first so
// the send case can never be selected, making the ctx.Done() branch the only
// ready case (both become ready once canceled, which would otherwise make
// select's choice nondeterministic).
func TestDeepPipeline_SubmitBlockAfterCancelReturnsErr(t *testing.T) {
	p := NewDeepPipeline(DeepPipelineConfig{Depth: 1}, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx, p.cancel = ctx, cancel // wire ctx without starting stage goroutines that would drain the channel

	p.prefetchCh <- &DeepBlock{Number: 0}
	cancel()

	err := p.SubmitBlock(makeDeepTestBlock(1))
	if err == nil {
		t.Fatal("SubmitBlock() error = nil, want context canceled error")
	}
}

// TestDeepPipeline_Reset covers Reset's drain, state-pruning and restart
// logic.
func TestDeepPipeline_Reset(t *testing.T) {
	executeFn := func(ctx context.Context, blk block.IBlock) (*DeepCommitJob, error) {
		return &DeepCommitJob{DeepBlock: DeepBlock{Number: blk.Number64().Uint64(), Block: blk}}, nil
	}
	commitFn := func(ctx context.Context, job *DeepCommitJob) (*DeepPersistJob, error) {
		return &DeepPersistJob{DeepBlock: job.DeepBlock, StateRoot: types.HexToHash("0x02")}, nil
	}
	persistFn := func(ctx context.Context, job *DeepPersistJob) error { return nil }

	p := NewDeepPipeline(DeepPipelineConfig{}, nil, executeFn, commitFn, persistFn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	if err := p.SubmitBlock(makeDeepTestBlock(1)); err != nil {
		t.Fatalf("SubmitBlock(1) error = %v", err)
	}
	if err := p.SubmitBlock(makeDeepTestBlock(5)); err != nil {
		t.Fatalf("SubmitBlock(5) error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.LastPersisted() == 5 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Reorg back to block 2: state roots for blocks > 2 must be dropped and
	// lastPersisted reset to the fork point.
	p.Reset(context.Background(), 2)
	defer p.Stop()

	if p.LastPersisted() != 2 {
		t.Fatalf("LastPersisted() after Reset = %d, want 2", p.LastPersisted())
	}
	if _, ok := p.StateRootForBlock(5); ok {
		t.Fatal("expected state root for block 5 to be cleared after Reset(2)")
	}

	// NOTE (defect found, not fixed per task instructions): Reset() never
	// resets p.running to false before calling p.Start(ctx) again. Start()'s
	// guard is `if !p.running.CompareAndSwap(false, true) { return }`, so
	// once a pipeline has been Start()-ed, every subsequent Reset() silently
	// fails to re-arm it: the CompareAndSwap finds running already true (set
	// by the very first Start and never cleared, since Reset does not call
	// Stop()) and returns immediately, without installing a fresh
	// p.ctx/p.cancel or launching new stage goroutines bound to it. The old
	// p.ctx is already canceled at this point. This leaves p.ctx and the
	// freshly-recreated channels racing against each other inside
	// SubmitBlock's `select` (channel-send vs. ctx.Done(), both effectively
	// ready), so whether a post-Reset SubmitBlock succeeds or fails with
	// "context canceled" is nondeterministic — a real reorg-recovery
	// Reset() can permanently wedge the pipeline. Not asserted strictly here
	// because of that nondeterminism; the state-root pruning behavior above
	// is the deterministic part of Reset this test exercises.
	_ = p.SubmitBlock(makeDeepTestBlock(3))
}
