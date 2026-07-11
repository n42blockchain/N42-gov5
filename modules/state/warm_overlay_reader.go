// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// WarmOverlayReader: tiered StateReader for minimal/full snapshot-direct nodes.
//
// snapshot (H0) is immutable; everything after H0 — including DELETIONS — lives
// only in the warm MDBX tier (Account/Storage tables, populated by catch-up).
// Read order, per a deliberate three-state rule:
//
//	warm has value          -> use warm
//	warm has tombstone       -> key was deleted after H0; return absent, DO NOT
//	                            fall through to the snapshot (else we'd serve a
//	                            stale H0 value — the #1 overlay correctness bug)
//	warm absent (no key)     -> fall through to the cold snapshot reader
//
// A tombstone is a PRESENT key with an EMPTY value, distinguished from an absent
// key via cursor SeekExact. Storage tombstones are REQUIRED: SSTORE slot->0
// (independent of SELFDESTRUCT, unchanged by EIP-6780, still frequent
// post-Cancun) deletes an H0 slot and must not fall through. Account tombstones
// are kept for completeness/defence: post-EIP-6780 an H0 account is not deleted
// by SELFDESTRUCT, so they rarely occur in a post-Cancun catch-up.

package state

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// WarmOverlayReader overlays a warm MDBX tier (H0+1..tip deltas + tombstones)
// on top of a cold snapshot StateReader (the immutable H0 snapshot).
//
// The reader is created once per block and consulted on the first touch of each
// account/slot (IBS caches subsequent touches), so a busy block issues thousands
// of warmGet reads. Each read is a cursor SeekExact; rather than open+close a
// fresh MDBX cursor every time, cursors are cached per table and reused across
// the block's reads, then released by Close(). All reads happen before the
// block's writes (the executor flushes to MDBX only at CommitBlock, after the
// read phase), so a long-lived read cursor never straddles a write to its table.
type WarmOverlayReader struct {
	tx       kv.Tx
	cold     StateReader // snapshot (H0), e.g. snapshotreader.StateReader
	accTable string
	stoTable string
	curs     map[string]kv.Cursor // lazily opened, reused per read, freed by Close
}

// NewWarmOverlayReader builds the overlay. cold serves keys absent from warm.
func NewWarmOverlayReader(tx kv.Tx, cold StateReader) *WarmOverlayReader {
	return &WarmOverlayReader{
		tx:       tx,
		cold:     cold,
		accTable: modules.Account,
		stoTable: modules.Storage,
	}
}

// Close releases the cached cursors. Call once per block after execution; safe
// to call multiple times. Cursors would otherwise be freed when the tx ends, but
// the batch tx spans many blocks, so closing per block keeps cursor count bounded.
func (r *WarmOverlayReader) Close() {
	for _, c := range r.curs {
		c.Close()
	}
	r.curs = nil
}

// warmGet distinguishes a present key (found=true; value may be empty =
// tombstone) from an absent key (found=false). Uses SeekExact so the DupSort
// AutoConv on the Storage table (52B composite -> 20B dup-key) is handled. The
// per-table cursor is opened once and reused across reads (see type doc).
func (r *WarmOverlayReader) warmGet(table string, key []byte) (val []byte, found bool, err error) {
	c := r.curs[table]
	if c == nil {
		c, err = r.tx.Cursor(table)
		if err != nil {
			return nil, false, err
		}
		if r.curs == nil {
			r.curs = make(map[string]kv.Cursor, 2)
		}
		r.curs[table] = c
	}
	k, v, err := c.SeekExact(key)
	if err != nil {
		return nil, false, err
	}
	if k == nil {
		return nil, false, nil // absent
	}
	return v, true, nil // present (v empty => tombstone)
}

func (r *WarmOverlayReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	v, found, err := r.warmGet(r.accTable, addr[:])
	if err != nil {
		return nil, err
	}
	if found {
		if len(v) == 0 {
			return nil, nil // tombstone: deleted after H0, no fall-through
		}
		a := &account.StateAccount{}
		if err := a.DecodeForStorageV2(v); err != nil {
			return nil, err
		}
		return a, nil
	}
	return r.cold.ReadAccountData(addr) // fall through to snapshot
}

func (r *WarmOverlayReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	ck := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), key.Bytes())
	v, found, err := r.warmGet(r.stoTable, ck)
	if err != nil {
		return nil, err
	}
	if found {
		if len(v) == 0 {
			return nil, nil // tombstone: slot cleared after H0, no fall-through
		}
		return v, nil
	}
	return r.cold.ReadAccountStorage(addr, key) // fall through to snapshot
}

// ReadAccountCode: code is content-addressed by codeHash. Catch-up's newly
// deployed contracts land in the warm Code table; H0 contract code lives in the
// codes freezer reached via the cold reader's CodeSource. Check warm first,
// then fall through to cold.
func (r *WarmOverlayReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if account.IsEmptyCodeHash(codeHash) {
		return nil, nil
	}
	if v, err := r.tx.GetOne(modules.Code, codeHash[:]); err == nil && len(v) > 0 {
		return v, nil // warm: catch-up-deployed contract
	}
	return r.cold.ReadAccountCode(addr, codeHash) // cold: H0 code via codes freezer
}

func (r *WarmOverlayReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

var _ StateReader = (*WarmOverlayReader)(nil)
