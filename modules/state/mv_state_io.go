// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mv_state_io.go: StateReader / StateWriter adapters that delegate to
// an EVMStateView. Together they let an unmodified IntraBlockState
// execute against the Block-STM MVHashMap:
//
//   EVM                                         Block-STM
//   ┌────────────────┐                          ┌──────────────┐
//   │                │  Get/SetBalance/etc.     │              │
//   │ IntraBlockState├──────────────────────────▶ EVMStateView │
//   │                │                          │              │
//   │ uses           │  stateReader/            │              │
//   │ state.StateDB  │  stateWriter             │ over MVHashMap│
//   │                │                          │              │
//   │                │  ◄─── MVStateReader ─────┤              │
//   │                │  ◄─── MVStateWriter ─────┤              │
//   └────────────────┘                          └──────────────┘
//
// The MVStateReader + MVStateWriter pair are the ONLY new code at the
// IntraBlockState boundary; no change to IntraBlockState, journal,
// stateObject, or EVM internals. Phase 4 integration is then a matter
// of constructing IntraBlockState with these adapters and calling
// CommitBlock with the MVStateWriter.
//
// Lifetime: one adapter pair per (txIdx, incarnation). Reusing across
// txs is unsafe because the readSet accumulates per tx.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// MVStateReader is a StateReader that reads through an EVMStateView,
// recording every read in the view's readSet for later validation.
// Lives for the lifetime of one tx incarnation.
type MVStateReader struct {
	view *EVMStateView
}

// NewMVStateReader wraps a view as a StateReader.
func NewMVStateReader(view *EVMStateView) *MVStateReader {
	return &MVStateReader{view: view}
}

// ReadAccountData reads an account from the MV view.
func (r *MVStateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	return r.view.ReadAccount(addr)
}

// ReadAccountStorage reads a storage slot through the MV view,
// honoring SELFDESTRUCT wipe markers.
func (r *MVStateReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	val, err := r.view.ReadStorage(addr, *key)
	if err != nil {
		return nil, err
	}
	if val == nil || val.IsZero() {
		return nil, nil
	}
	return val.Bytes(), nil
}

// ReadAccountCode reads contract code by codeHash. address is ignored
// (code is content-addressed by hash in our storage layout).
func (r *MVStateReader) ReadAccountCode(_ types.Address, codeHash types.Hash) ([]byte, error) {
	return r.view.ReadCode(codeHash)
}

// ReadAccountCodeSize returns the byte-length of the code.
func (r *MVStateReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// --- Writer ---

// MVStateWriter is a StateWriter that funnels IntraBlockState's writes
// into an EVMStateView's write buffer. Lives for one tx incarnation.
//
// IntraBlockState calls this at end-of-block via CommitBlock; we
// translate each method into the appropriate view.Write* call and
// record additional signals (SELFDESTRUCT wipe) the commit phase
// needs.
type MVStateWriter struct {
	view *EVMStateView
}

// NewMVStateWriter wraps a view as a StateWriter.
func NewMVStateWriter(view *EVMStateView) *MVStateWriter {
	return &MVStateWriter{view: view}
}

// UpdateAccountData writes the new account. We ignore `original`
// because MVHashMap doesn't use it (the changeset writer that DOES
// use it is a separate concern — Phase 4 Part 2 will route acctcs
// entries through a parallel-aware ChangeSetWriter).
func (w *MVStateWriter) UpdateAccountData(addr types.Address, _, acct *account.StateAccount) error {
	w.view.WriteAccount(addr, acct)
	return nil
}

// UpdateAccountCode writes code content-addressed by hash.
func (w *MVStateWriter) UpdateAccountCode(_ types.Address, codeHash types.Hash, code []byte) error {
	w.view.WriteCode(codeHash, code)
	return nil
}

// DeleteAccount nils out the account entry.
func (w *MVStateWriter) DeleteAccount(addr types.Address, _ *account.StateAccount) error {
	w.view.WriteAccount(addr, nil)
	return nil
}

// WriteAccountStorage writes a slot.
func (w *MVStateWriter) WriteAccountStorage(addr types.Address, key types.Hash, _, value uint256.Int) error {
	w.view.WriteStorage(addr, key, &value)
	return nil
}

// CreateContract triggers a SELFDESTRUCT / CREATE-on-existing-addr
// storage wipe. IntraBlockState calls this from updateAccountWithWipe
// whenever needsWipe is true, i.e. the address's storage should be
// cleared before new slots get written. The MV path translates this
// to a single poison-write via WipeAddress.
func (w *MVStateWriter) CreateContract(addr types.Address) error {
	w.view.WipeAddress(addr)
	return nil
}

// Compile-time interface conformance.
var (
	_ StateReader = (*MVStateReader)(nil)
	_ StateWriter = (*MVStateWriter)(nil)
)
