// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// Deferred execution (ChainConfig.DeferredExecutionTime; agreed cross-client
// in n42-rs docs/PHASE_D_DEFERRED_EXECUTION.md): from the fork, a header's
// Root, ReceiptHash, Bloom and GasUsed are the PARENT's executed values.
// Every node stores its own execution result of each block it applies
// (rawdb.ExecutedResult); the next header is checked against it, and the
// builder stamps it into the header it builds on top. The first deferred
// header carries the same Root as its parent, which executed under the old
// rule -- ExecutedResultOfHeader returns a pre-fork header's own fields, so
// the invariant needs no special case.

// ExecutedResultOfHeader returns the execution result of the block whose
// header is h, as this node knows it: the header's own fields before the
// fork, the stored result after it.
func ExecutedResultOfHeader(config *params.ChainConfig, tx kv.Getter, h *block.Header) (rawdb.ExecutedResult, error) {
	if config == nil || !config.IsDeferredExecution(h.Time) || h.Number.Uint64() == 0 {
		// Before the fork a header carries its own result; so does the
		// genesis header (nothing executed before it, and nothing stores a
		// result for it).
		return rawdb.ExecutedResult{Root: h.Root, ReceiptHash: h.ReceiptHash, Bloom: h.Bloom, GasUsed: h.GasUsed}, nil
	}
	r, ok, err := rawdb.ReadExecutedResult(tx, h.Hash())
	if err != nil {
		return r, err
	}
	if !ok {
		return r, fmt.Errorf("%w: execution result of block %d (%x) not stored on this node", ErrDeferredResultUnknown, h.Number.Uint64(), h.Hash().Bytes()[:8])
	}
	return r, nil
}

// executedOrSeededResult is ExecutedResultOfHeader with the reseeded-node
// fallback: a node started from a state snapshot has no applied marker and
// may lack the stored result of the blocks below its head, whose state it
// never executed but holds (the snapshot's state is the head's post-state).
// For a canonical block below the head, the result is then what its
// canonical child carries: the snapshot's history was written at depth 1.
// Only while no block has been applied here; any import writes the marker
// and the rule is off from then on.
func executedOrSeededResult(config *params.ChainConfig, tx kv.Getter, h *block.Header) (rawdb.ExecutedResult, error) {
	r, err := ExecutedResultOfHeader(config, tx, h)
	if err == nil || !errors.Is(err, ErrDeferredResultUnknown) {
		return r, err
	}
	if sr, ok := seededResult(tx, h); ok {
		return sr, nil
	}
	return r, err
}

func seededResult(tx kv.Getter, h *block.Header) (rawdb.ExecutedResult, bool) {
	if _, _, ok, err := rawdb.ReadQMDBApplied(tx); err != nil || ok {
		return rawdb.ExecutedResult{}, false
	}
	n := h.Number.Uint64()
	if c, err := rawdb.ReadCanonicalHash(tx, n); err != nil || c != h.Hash() {
		return rawdb.ExecutedResult{}, false
	}
	ch, err := rawdb.ReadCanonicalHash(tx, n+1)
	if err != nil || ch == (types.Hash{}) {
		return rawdb.ExecutedResult{}, false
	}
	child := rawdb.ReadHeader(tx, ch, n+1)
	if child == nil || child.ParentHash != h.Hash() {
		return rawdb.ExecutedResult{}, false
	}
	return rawdb.ExecutedResult{Root: child.Root, ReceiptHash: child.ReceiptHash, Bloom: child.Bloom, GasUsed: child.GasUsed}, true
}

// ExecutedOrSeededResult is executedOrSeededResult for the block builder.
// A block applied here whose write is still in flight has its result in
// memory (lag_gate_applied.go); one written after tx began is read again
// in a fresh transaction.
func (bc *BlockChain) ExecutedOrSeededResult(tx kv.Getter, h *block.Header) (rawdb.ExecutedResult, error) {
	r, err := executedOrSeededResult(bc.chainConfig, tx, h)
	if err == nil || !errors.Is(err, ErrDeferredResultUnknown) || !lagGateApplied {
		return r, err
	}
	if pr, ok := bc.pendingExecutedResult(h.Hash()); ok {
		return pr, nil
	}
	if bc.ChainDB != nil {
		var r2 rawdb.ExecutedResult
		var err2 error
		if verr := bc.ChainDB.View(bc.ctx, func(tx2 kv.Tx) error {
			r2, err2 = executedOrSeededResult(bc.chainConfig, tx2, h)
			return nil
		}); verr == nil && err2 == nil {
			return r2, nil
		}
	}
	return r, err
}

// ErrDeferredResultUnknown: the parent's execution result is not on this
// node (the parent was not applied here yet).
var ErrDeferredResultUnknown = fmt.Errorf("deferred execution: parent result unknown")

// checkDeferredHeader verifies that header's execution fields are this
// node's result of the block they report: the parent at depth 1, the
// ancestor DeferredDepthAt(header)-1 levels above the parent at depth k
// (block.DeferredDepthAt; N42_DEFERRED_DEPTH). parent is the parent's
// header; the ancestors above it are read from tx. A mismatch at depth k is
// the lagged equivocation P2a detects when header N+k arrives.
func checkDeferredHeader(config *params.ChainConfig, tx kv.Getter, header, parent *block.Header) error {
	target, err := deferredTarget(header, parent, func(h types.Hash, n uint64) *block.Header { return rawdb.ReadHeader(tx, h, n) })
	if err != nil {
		return err
	}
	want, err := executedOrSeededResult(config, tx, target)
	if err != nil {
		return err
	}
	if err := compareDeferredResult(header, want); err != nil {
		if target != parent {
			reportLaggedEquivocation(header, target, err)
		}
		return err
	}
	return nil
}

// compareDeferredResult compares header's execution fields with want.
func compareDeferredResult(header *block.Header, want rawdb.ExecutedResult) error {
	if header.Root != want.Root {
		return fmt.Errorf("deferred execution: header %d carries state root %x, this node executed %x", header.Number.Uint64(), header.Root[:8], want.Root[:8])
	}
	if header.ReceiptHash != want.ReceiptHash {
		return fmt.Errorf("deferred execution: header %d carries receipts root %x, this node executed %x", header.Number.Uint64(), header.ReceiptHash[:8], want.ReceiptHash[:8])
	}
	if header.Bloom != want.Bloom {
		return fmt.Errorf("deferred execution: header %d carries a bloom that differs from this node's", header.Number.Uint64())
	}
	if header.GasUsed != want.GasUsed {
		return fmt.Errorf("deferred execution: header %d carries gas used %d, this node executed %d", header.Number.Uint64(), header.GasUsed, want.GasUsed)
	}
	return nil
}

// ReceiptsRootFor is the receipts root a header carries for receipts, the
// rule ValidateState checks against: the native concat root, or the
// Ethereum receipt trie on Ethereum-EL chains past Byzantium.
func ReceiptsRootFor(config *params.ChainConfig, number uint64, receipts []*block.Receipt) types.Hash {
	if config != nil && config.EthereumReceiptEncoding() && config.IsByzantium(number) {
		return block.EthereumReceiptRoot(receipts, true)
	}
	return DeriveSha(block.Receipts(receipts))
}

// ExecutedResultFor assembles the stored result of a block from what
// executing it produced.
func ExecutedResultFor(config *params.ChainConfig, number uint64, root types.Hash, receipts []*block.Receipt) rawdb.ExecutedResult {
	var gasUsed uint64
	if n := len(receipts); n > 0 {
		gasUsed = receipts[n-1].CumulativeGasUsed
	}
	return rawdb.ExecutedResult{
		Root:        root,
		ReceiptHash: ReceiptsRootFor(config, number, receipts),
		Bloom:       block.CreateBloom(receipts),
		GasUsed:     gasUsed,
	}
}

// receiptsResultFuture is ExecutedResultFor's receipts part (receipts root,
// bloom, gas used) of one block, computed on its own goroutine as soon as
// the receipts are final. At 163k transactions it is ~45 ms of hashing that
// otherwise ran serially inside the write.
//
// It also encodes the block's Receipts row first (~15 ms at 163k), which
// the write appends before anything else.
type receiptsResultFuture struct {
	hash     types.Hash
	receipts []*block.Receipt
	done     chan struct{}
	result   rawdb.ExecutedResult // Root unset

	encodedDone chan struct{}
	encoded     []byte // nil: encode inline
}

// startReceiptsResult starts computing the receipts part of hash's result.
// receipts must not change afterwards (they are final once execution and
// collection are done; the write only reads them).
func startReceiptsResult(config *params.ChainConfig, number uint64, hash types.Hash, receipts []*block.Receipt) *receiptsResultFuture {
	f := &receiptsResultFuture{hash: hash, receipts: receipts, done: make(chan struct{}), encodedDone: make(chan struct{})}
	go func() {
		defer close(f.done)
		if enc, err := rawdb.EncodeReceiptsForStorage(receipts); err == nil {
			f.encoded = enc
		}
		close(f.encodedDone)
		f.result = ExecutedResultFor(config, number, types.Hash{}, receipts)
	}()
	return f
}

// encodedFor returns the encoded Receipts row when the future is for hash
// and exactly these receipts (waiting for it), nil otherwise.
func (f *receiptsResultFuture) encodedFor(hash types.Hash, receipts []*block.Receipt) []byte {
	if !f.matches(hash, receipts) {
		return nil
	}
	<-f.encodedDone
	return f.encoded
}

func (f *receiptsResultFuture) matches(hash types.Hash, receipts []*block.Receipt) bool {
	return f != nil && f.hash == hash && len(f.receipts) == len(receipts) &&
		(len(receipts) == 0 || f.receipts[0] == receipts[0])
}

// resultFor returns the block's executed result with root, waiting for the
// receipts part, when the future is for hash and exactly these receipts;
// ok=false otherwise (the caller derives it inline).
func (f *receiptsResultFuture) resultFor(hash types.Hash, root types.Hash, receipts []*block.Receipt) (rawdb.ExecutedResult, bool) {
	if !f.matches(hash, receipts) {
		return rawdb.ExecutedResult{}, false
	}
	<-f.done
	r := f.result
	r.Root = root
	return r, true
}

// RememberExecutedResult hands a sealed block's write the execution result
// the leader's build already computed for it, so the write does not derive
// the receipts root and bloom a second time.
func (bc *BlockChain) RememberExecutedResult(hash types.Hash, r rawdb.ExecutedResult) {
	bc.executedHints.Store(hash, r)
}

// takeExecutedResultHint returns and forgets the result remembered for hash.
func (bc *BlockChain) takeExecutedResultHint(hash types.Hash) (rawdb.ExecutedResult, bool) {
	v, ok := bc.executedHints.LoadAndDelete(hash)
	if !ok {
		return rawdb.ExecutedResult{}, false
	}
	r, ok := v.(rawdb.ExecutedResult)
	return r, ok
}
