// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// publisher_test.go covers BridgePublisher's run loop end-to-end against
// scripted IBlockChain + ProofSubmitter mocks. The tests stand in for a
// devnet smoke: with the wiring node.go assembles, the publisher must
// drive the fetch → prove → submit → advance pipeline correctly when
// chain head moves forward.
//
// scriptedChain embeds common.IBlockChain as a nil interface and only
// overrides the two methods the publisher actually calls. Any new chain
// dependency the publisher grows will surface as a panic on its first
// call from inside Run, which is exactly the failure mode we want.

package bridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// scriptedChain is a minimal IBlockChain mock for publisher tests.
type scriptedChain struct {
	common.IBlockChain // embedded; nil — any unimplemented call panics

	mu       sync.Mutex
	blocks   []block.IBlock // index 0 = block 1; len(blocks) = head height
	lastHash types.Hash
}

func newScriptedChain() *scriptedChain { return &scriptedChain{} }

// appendBlock adds the next block in sequence with a deterministic state root.
func (c *scriptedChain) appendBlock() {
	c.mu.Lock()
	defer c.mu.Unlock()
	num := uint64(len(c.blocks)) + 1
	hdr := newTestHeader(num, c.lastHash)
	blk := block.NewBlock(hdr, nil)
	c.blocks = append(c.blocks, blk)
	c.lastHash = blk.Hash()
}

// appendBlocks adds N consecutive blocks.
func (c *scriptedChain) appendBlocks(n int) {
	for i := 0; i < n; i++ {
		c.appendBlock()
	}
}

func (c *scriptedChain) CurrentBlock() block.IBlock {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.blocks) == 0 {
		return nil
	}
	return c.blocks[len(c.blocks)-1]
}

func (c *scriptedChain) GetBlockByNumber(num *uint256.Int) (block.IBlock, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := int(num.Uint64()) - 1
	if idx < 0 || idx >= len(c.blocks) {
		return nil, fmt.Errorf("block %s not found", num.String())
	}
	return c.blocks[idx], nil
}

// newTestHeader builds a header whose Extra layout matches the production
// HotStuff QC encoding. Reuses hotstuffExtraForTest from header_prover_test.go.
func newTestHeader(num uint64, parentHash types.Hash) *block.Header {
	var stateRoot types.Hash
	binary.BigEndian.PutUint64(stateRoot[:8], num) // unique per block

	return &block.Header{
		ParentHash: parentHash,
		Number:     uint256.NewInt(num),
		Time:       num,
		Root:       stateRoot,
		Extra:      hotstuffExtraForTest(num, nil),
	}
}

// recordingSubmitter records every SubmitHeaderChainProof call. When notify
// is non-nil it is closed once `target` proofs have been recorded — used by
// the run-loop test for deterministic synchronisation without busy polling.
type recordingSubmitter struct {
	mu     sync.Mutex
	proofs []*HeaderChainProof
	err    error // injected error

	target       int
	notify       chan struct{} // immutable after construction
	notifyClosed bool          // guards close(notify) against double-close
}

func (s *recordingSubmitter) SubmitHeaderChainProof(_ context.Context, proof *HeaderChainProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.proofs = append(s.proofs, proof)
	if s.notify != nil && !s.notifyClosed && len(s.proofs) >= s.target {
		close(s.notify)
		s.notifyClosed = true
	}
	return nil
}

func (s *recordingSubmitter) submittedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.proofs)
}

func (s *recordingSubmitter) lastProof() *HeaderChainProof {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.proofs) == 0 {
		return nil
	}
	return s.proofs[len(s.proofs)-1]
}

func (s *recordingSubmitter) clearErr() {
	s.mu.Lock()
	s.err = nil
	s.mu.Unlock()
}

// Happy path: full batch ⇒ submit ⇒ lastBlock advances.
func TestBridgePublisher_BatchAdvance(t *testing.T) {
	const batchSize uint64 = 10

	chain := newScriptedChain()
	chain.appendBlocks(int(batchSize))

	submitter := &recordingSubmitter{}
	pub := NewBridgePublisher(chain, nil, nil, submitter, &PublisherConfig{
		BatchSize:    batchSize,
		PollInterval: 10 * time.Millisecond,
	})

	if err := pub.checkAndPublish(context.Background()); err != nil {
		t.Fatalf("checkAndPublish: %v", err)
	}

	if got := pub.LastProvenBlock(); got != batchSize {
		t.Fatalf("LastProvenBlock = %d, want %d", got, batchSize)
	}
	if submitter.submittedCount() != 1 {
		t.Fatalf("submitter got %d proofs, want 1", submitter.submittedCount())
	}
	last := submitter.lastProof()
	if last.StartBlock != 1 || last.EndBlock != batchSize {
		t.Fatalf("proof range = [%d,%d], want [1,%d]", last.StartBlock, last.EndBlock, batchSize)
	}
	var want types.Hash
	binary.BigEndian.PutUint64(want[:8], batchSize)
	if last.StateRoot != want {
		t.Fatalf("StateRoot = %x, want %x", last.StateRoot, want)
	}
}

// Don't submit partial batches: head < batchSize ⇒ no-op.
func TestBridgePublisher_NoOpUntilBatchFull(t *testing.T) {
	const batchSize uint64 = 100

	chain := newScriptedChain()
	chain.appendBlocks(50)

	submitter := &recordingSubmitter{}
	pub := NewBridgePublisher(chain, nil, nil, submitter, &PublisherConfig{
		BatchSize:    batchSize,
		PollInterval: 10 * time.Millisecond,
	})

	if err := pub.checkAndPublish(context.Background()); err != nil {
		t.Fatalf("checkAndPublish: %v", err)
	}

	if pub.LastProvenBlock() != 0 {
		t.Fatalf("lastBlock advanced to %d while batch was incomplete", pub.LastProvenBlock())
	}
	if submitter.submittedCount() != 0 {
		t.Fatalf("submitter got %d proofs while batch was incomplete", submitter.submittedCount())
	}
}

// Local-only mode (no submitter): publisher still proves and advances.
func TestBridgePublisher_DryRunWhenSubmitterNil(t *testing.T) {
	const batchSize uint64 = 5

	chain := newScriptedChain()
	chain.appendBlocks(int(batchSize))

	pub := NewBridgePublisher(chain, nil, nil, nil, &PublisherConfig{
		BatchSize:    batchSize,
		PollInterval: 10 * time.Millisecond,
	})

	if err := pub.checkAndPublish(context.Background()); err != nil {
		t.Fatalf("checkAndPublish (dry run): %v", err)
	}

	if got := pub.LastProvenBlock(); got != batchSize {
		t.Fatalf("dry-run mode failed to advance: LastProvenBlock = %d, want %d", got, batchSize)
	}
}

// Full Run goroutine + ctx cancel + 3 contiguous batches. Synchronisation is
// channel-based via recordingSubmitter.notify — no busy polling.
func TestBridgePublisher_RunLoopMultipleBatches(t *testing.T) {
	const batchSize uint64 = 10
	const totalBlocks = 30

	chain := newScriptedChain()
	chain.appendBlocks(totalBlocks)

	submitter := &recordingSubmitter{
		target: 3,
		notify: make(chan struct{}),
	}
	pub := NewBridgePublisher(chain, nil, nil, submitter, &PublisherConfig{
		BatchSize:    batchSize,
		PollInterval: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()

	select {
	case <-submitter.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for 3 submissions")
	}
	cancel()
	<-done

	if got := submitter.submittedCount(); got != 3 {
		t.Fatalf("submitted %d proofs, want 3", got)
	}
	if got := pub.LastProvenBlock(); got != uint64(totalBlocks) {
		t.Fatalf("LastProvenBlock = %d, want %d", got, totalBlocks)
	}
	submitter.mu.Lock()
	defer submitter.mu.Unlock()
	for i, p := range submitter.proofs {
		wantStart := uint64(i)*batchSize + 1
		wantEnd := wantStart + batchSize - 1
		if p.StartBlock != wantStart || p.EndBlock != wantEnd {
			t.Errorf("proof %d range = [%d,%d], want [%d,%d]", i, p.StartBlock, p.EndBlock, wantStart, wantEnd)
		}
	}
}

// Submit error must NOT advance lastBlock — same discipline as the EthEL
// freezer/MDBX ordering: never bump a watermark past unflushed work.
func TestBridgePublisher_SubmitFailureRetries(t *testing.T) {
	const batchSize uint64 = 5

	chain := newScriptedChain()
	chain.appendBlocks(int(batchSize))

	submitter := &recordingSubmitter{err: fmt.Errorf("eth rpc unavailable")}
	pub := NewBridgePublisher(chain, nil, nil, submitter, &PublisherConfig{
		BatchSize:    batchSize,
		PollInterval: 10 * time.Millisecond,
	})

	for i := 0; i < 2; i++ {
		if err := pub.checkAndPublish(context.Background()); err == nil {
			t.Fatalf("attempt %d: expected submit failure", i+1)
		}
		if pub.LastProvenBlock() != 0 {
			t.Fatalf("attempt %d: lastBlock advanced to %d", i+1, pub.LastProvenBlock())
		}
	}

	submitter.clearErr()

	if err := pub.checkAndPublish(context.Background()); err != nil {
		t.Fatalf("recovery attempt: %v", err)
	}
	if pub.LastProvenBlock() != batchSize {
		t.Fatalf("after recovery LastProvenBlock = %d, want %d", pub.LastProvenBlock(), batchSize)
	}
	if submitter.submittedCount() != 1 {
		t.Fatalf("submitter got %d successful proofs, want 1", submitter.submittedCount())
	}
}
