// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// parallel_apply_target.go — ApplyTarget adapter that routes a
// BlockCommit's writes into the executor's shared PlainStateBuffer
// via BufferedPlainStateWriter. This is the bridge between the
// Block-STM commit phase (which produces a deterministic BlockCommit)
// and the existing async-flush pipeline (PlainStateBuffer → MDBX).
//
// After this adapter is used, the stateBuf holds exactly the same
// deltas the sequential path would have accumulated through per-tx
// IntraBlockState.CommitBlock. The async flusher persists them to
// MDBX unchanged.

package state

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// PlainStateBufferApplyTarget implements ApplyTarget on top of a
// BufferedPlainStateWriter. One instance per block; lifetime ends
// when the block's BlockCommit.Apply completes.
type PlainStateBufferApplyTarget struct {
	writer *BufferedPlainStateWriter
}

// NewPlainStateBufferApplyTarget constructs an adapter. Use the
// NoHistory variant of BufferedPlainStateWriter for Phase 5 MVP;
// parallel-aware changeset writing is a separate follow-up task.
func NewPlainStateBufferApplyTarget(w *BufferedPlainStateWriter) *PlainStateBufferApplyTarget {
	return &PlainStateBufferApplyTarget{writer: w}
}

// PutAccount decodes the V2-encoded account and routes to the buffer.
// An empty enc means delete.
func (t *PlainStateBufferApplyTarget) PutAccount(addr types.Address, enc []byte) error {
	if len(enc) == 0 {
		return t.writer.DeleteAccount(addr, nil)
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return fmt.Errorf("PutAccount decode %x: %w", addr, err)
	}
	return t.writer.UpdateAccountData(addr, nil, &a)
}

// PutStorage writes a storage slot. Empty value means delete.
func (t *PlainStateBufferApplyTarget) PutStorage(addr types.Address, slot types.Hash, value []byte) error {
	val := new(uint256.Int)
	if len(value) > 0 {
		val.SetBytes(value)
	}
	// Pass a zero original — we are in commit phase, not per-tx; changeset
	// writers that DO care about the true original are driven through a
	// different pipeline. BufferedPlainStateWriter short-circuits only
	// when csw is non-nil AND original.Equals(acct); we use the NoHistory
	// writer here so csw is nil and no short-circuit triggers.
	orig := new(uint256.Int)
	return t.writer.WriteAccountStorage(addr, &slot, orig, val)
}

// PutCode stores bytecode keyed by content hash. Idempotent.
func (t *PlainStateBufferApplyTarget) PutCode(codeHash types.Hash, code []byte) error {
	// UpdateAccountCode's addr parameter is unused by the buffer path
	// (code is content-addressed). Pass zero.
	return t.writer.UpdateAccountCode(types.Address{}, codeHash, code)
}

// AddBalance adds delta to addr. Phase 5 contract: this method is NEVER
// called because RealParallelEVM does not populate TxOutput.CoinbaseTip
// (the coinbase credit flows through IntraBlockState → MVStateWriter
// via the regular account update path inside ApplyTransaction). If this
// fires it means the invariant has been broken — fail loudly to catch
// the regression.
func (t *PlainStateBufferApplyTarget) AddBalance(addr types.Address, delta *uint256.Int) error {
	return fmt.Errorf("PlainStateBufferApplyTarget.AddBalance called (addr=%x delta=%s); Phase 5 expects CoinbaseDelta==0 because RealParallelEVM does not set TxOutput.CoinbaseTip — see parallel_evm.go",
		addr, delta)
}

// WipeStorage clears all storage entries for addr. Routed to
// BufferedPlainStateWriter.CreateContract which marks the address for
// a storage-wipe on MDBX flush and records the pre-wipe values in the
// changeset (no-op in NoHistory mode).
func (t *PlainStateBufferApplyTarget) WipeStorage(addr types.Address) error {
	return t.writer.CreateContract(addr)
}

// Compile-time interface assertion.
var _ ApplyTarget = (*PlainStateBufferApplyTarget)(nil)
