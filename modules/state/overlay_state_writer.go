// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// OverlayStateWriter: warm-tier writer for minimal/full snapshot-direct nodes.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules"
)

// OverlayStateWriter writes H0+1..tip deltas to the warm MDBX Account/Storage
// tables (no changesets / no history) and records DELETIONS as empty-value
// TOMBSTONES instead of real Deletes. This is the write-side counterpart to
// WarmOverlayReader: a tombstone makes the reader return "deleted" without
// falling through to the immutable snapshot's stale H0 value.
//
//   - storage SSTORE slot->0 -> Put(Storage, key, []byte{})  (REQUIRED — SSTORE
//     clear is independent of SELFDESTRUCT, unchanged by EIP-6780, frequent)
//   - account delete         -> Put(Account, addr, []byte{}) (defensive — after
//     EIP-6780 an H0 account is not SELFDESTRUCT-deleted, so this rarely fires
//     in a post-Cancun catch-up)
//
// Everything else inherits PlainStateWriter (no-history). CreateContract's warm
// storage wipe is safe post-Cancun: SELFDESTRUCT+CREATE only fires in the same
// tx as creation, so the wiped account is new (absent from the snapshot) and a
// warm-only wipe is sufficient (no snapshot slots to tombstone).
type OverlayStateWriter struct {
	*PlainStateWriter
	db putDel
}

// NewOverlayStateWriter wraps db with no-history + tombstone-on-delete semantics.
func NewOverlayStateWriter(db putDel) *OverlayStateWriter {
	return &OverlayStateWriter{
		PlainStateWriter: NewPlainStateWriterNoHistory(db),
		db:               db,
	}
}

func (w *OverlayStateWriter) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	if original == value {
		return nil
	}
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
	if value.ByteLen() == 0 {
		// SSTORE slot->0: empty-value tombstone, NOT Delete.
		return w.db.Put(modules.Storage, compositeKey, []byte{})
	}
	v := make([]byte, value.ByteLen())
	value.WriteToSlice(v)
	return w.db.Put(modules.Storage, compositeKey, v)
}

func (w *OverlayStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	// Empty-value tombstone, NOT Delete (defensive; see type doc).
	return w.db.Put(modules.Account, address[:], []byte{})
}

var _ WriterWithChangeSets = (*OverlayStateWriter)(nil)
