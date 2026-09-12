// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
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

// ErrDeferredResultUnknown: the parent's execution result is not on this
// node (the parent was not applied here yet).
var ErrDeferredResultUnknown = fmt.Errorf("deferred execution: parent result unknown")

// checkDeferredHeader verifies that header's execution fields are this
// node's result of the parent. parent is the parent's header.
func checkDeferredHeader(config *params.ChainConfig, tx kv.Getter, header, parent *block.Header) error {
	want, err := ExecutedResultOfHeader(config, tx, parent)
	if err != nil {
		return err
	}
	if header.Root != want.Root {
		return fmt.Errorf("deferred execution: header %d carries parent state root %x, this node executed %x", header.Number.Uint64(), header.Root[:8], want.Root[:8])
	}
	if header.ReceiptHash != want.ReceiptHash {
		return fmt.Errorf("deferred execution: header %d carries parent receipts root %x, this node executed %x", header.Number.Uint64(), header.ReceiptHash[:8], want.ReceiptHash[:8])
	}
	if header.Bloom != want.Bloom {
		return fmt.Errorf("deferred execution: header %d carries a parent bloom that differs from this node's", header.Number.Uint64())
	}
	if header.GasUsed != want.GasUsed {
		return fmt.Errorf("deferred execution: header %d carries parent gas used %d, this node executed %d", header.Number.Uint64(), header.GasUsed, want.GasUsed)
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
