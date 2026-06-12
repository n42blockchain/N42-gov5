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

	// readAccount, when set (SetAccountReader), serves AddBalance: it must
	// return the CURRENT account as visible at apply time — i.e. through the
	// same buffer this target writes into, falling back to the base store —
	// so the Σtip credit composes with any direct coinbase update the block's
	// Writes already applied (Apply runs AddBalance last). Nil for absent.
	readAccount func(types.Address) (*account.StateAccount, error)
}

// NewPlainStateBufferApplyTarget constructs an adapter. Use the
// NoHistory variant of BufferedPlainStateWriter for Phase 5 MVP;
// parallel-aware changeset writing is a separate follow-up task.
func NewPlainStateBufferApplyTarget(w *BufferedPlainStateWriter) *PlainStateBufferApplyTarget {
	return &PlainStateBufferApplyTarget{writer: w}
}

// SetAccountReader enables AddBalance (lazy-coinbase Σtip application).
func (t *PlainStateBufferApplyTarget) SetAccountReader(r func(types.Address) (*account.StateAccount, error)) {
	t.readAccount = r
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
	var val uint256.Int
	if len(value) > 0 {
		val.SetBytes(value)
	}
	// Pass a zero original — we are in commit phase, not per-tx; changeset
	// writers that DO care about the true original are driven through a
	// different pipeline. BufferedPlainStateWriter short-circuits only
	// when csw is non-nil AND original.Equals(acct); we use the NoHistory
	// writer here so csw is nil and no short-circuit triggers.
	var orig uint256.Int
	return t.writer.WriteAccountStorage(addr, slot, orig, val)
}

// PutCode stores bytecode keyed by content hash. Idempotent.
func (t *PlainStateBufferApplyTarget) PutCode(codeHash types.Hash, code []byte) error {
	// UpdateAccountCode's addr parameter is unused by the buffer path
	// (code is content-addressed). Pass zero.
	return t.writer.UpdateAccountCode(types.Address{}, codeHash, code)
}

// AddBalance adds delta to addr — the lazy-coinbase Σtip credit, applied by
// BlockCommit.Apply AFTER all Writes so it composes with any direct coinbase
// account update from the block (e.g. a sender==coinbase tx, whose update
// bypassed the skip). Requires SetAccountReader; without it the lazy path is
// not wired and a non-zero delta is an invariant break — fail loudly.
func (t *PlainStateBufferApplyTarget) AddBalance(addr types.Address, delta *uint256.Int) error {
	if t.readAccount == nil {
		return fmt.Errorf("PlainStateBufferApplyTarget.AddBalance called (addr=%x delta=%s) without an account reader; lazy-coinbase needs SetAccountReader — see parallel_evm.go",
			addr, delta)
	}
	cur, err := t.readAccount(addr)
	if err != nil {
		return fmt.Errorf("AddBalance read %x: %w", addr, err)
	}
	var acct account.StateAccount
	if cur != nil {
		acct = *cur
	} else {
		// Mirrors IntraBlockState.AddBalance on an absent account: create it
		// with the credited balance (the tip makes it non-empty).
		acct = account.NewAccount()
	}
	acct.Balance.Add(&acct.Balance, delta)
	return t.writer.UpdateAccountData(addr, cur, &acct)
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
