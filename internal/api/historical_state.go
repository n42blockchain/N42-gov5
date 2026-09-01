// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// historical_state.go — RPC historical state queries via the
// SegmentStore .ef inverted index plus block-keyed storcs/acctcs
// freezer changesets.
//
// Architecture (mirrors Erigon E3's domain reads):
//
//	storhist (RecSplit MPHF + Elias-Fano-style block list, per HistSegmentSize=1M block window)
//	    ↓ HistoryReader.Lookup(addr || slot, blockN)
//	    → returns largest blockK ≤ blockN where this slot was modified
//
//	storcs (block-keyed changeset, per-block batched zstd)
//	    ↓ FreezerTable.Retrieve(blockK)
//	    → returns the changeset blob for blockK
//
//	DecodeStorageChanges(blob)
//	    → walks all (addr, slot, oldVal, newVal) entries
//	    → finds (addr, slot) match → returns oldVal
//
// "oldVal at blockK" is the slot's value BEFORE blockK executed = the
// value visible at any block in [blockK_prev_modification+1, blockK-1].
// For blockN = blockK exactly, the slot's value is also oldVal (the
// pre-block value at the same block where it changed).
//
// For blockN > the latest entry in storhist, the slot's value is what
// PlainState currently has — fall through to PlainStateReader.
//
// Same architecture for accthist + acctcs for ReadAccountAt.

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// HistoricalStateReader serves eth_getStorageAt / eth_getBalance /
// eth_getCode at a HISTORICAL block. Uses the storhist/accthist
// SegmentStore to find the last block where a key was modified, then
// reads the per-block changeset (storcs/acctcs) to get the pre-block
// value. Falls through to current PlainState (MDBX) when blockN is
// past the latest history segment.
//
// Concurrent-safe for reads; the underlying freezer + SegmentStore
// readers use mmap and atomic-pointered caches.
type HistoricalStateReader struct {
	plainDB    kv.RoDB           // PlainState fallback (Storage / Account tables)
	storhist   *cscompact.HistoryReader
	accthist   *cscompact.HistoryReader
	storcs     *freezer.FreezerTable // block → storage changeset blob
	acctcs     *freezer.FreezerTable // block → account  changeset blob
}

// NewHistoricalStateReader opens the history readers + freezer tables
// rooted at chainDir = <datadir>/chain/freezer.
func NewHistoricalStateReader(plainDB kv.RoDB, chainDir string) (*HistoricalStateReader, error) {
	storhist, err := cscompact.NewHistoryReader(chainDir, "storhist")
	if err != nil {
		return nil, fmt.Errorf("open storhist: %w", err)
	}
	accthist, err := cscompact.NewHistoryReader(chainDir, "accthist")
	if err != nil {
		storhist.Close()
		return nil, fmt.Errorf("open accthist: %w", err)
	}
	storcs, err := freezer.NewFreezerTableCompressedReadOnly(chainDir, freezer.TableStorageChanges, "c")
	if err != nil {
		storhist.Close()
		accthist.Close()
		return nil, fmt.Errorf("open storcs: %w", err)
	}
	acctcs, err := freezer.NewFreezerTableCompressedReadOnly(chainDir, freezer.TableAccountChanges, "c")
	if err != nil {
		storhist.Close()
		accthist.Close()
		storcs.Close()
		return nil, fmt.Errorf("open acctcs: %w", err)
	}

	return &HistoricalStateReader{
		plainDB:  plainDB,
		storhist: storhist,
		accthist: accthist,
		storcs:   storcs,
		acctcs:   acctcs,
	}, nil
}

// StorageAt returns slot's value as of block blockN. Three cases:
//
//	(1) blockN is the LATEST execution height — return current
//	    PlainStorageState value.
//	(2) storhist contains the slot, with an entry blockK ≤ blockN —
//	    decode storcs[blockK] and return the (slot's) NewValue (which
//	    is the post-block value at blockK = the value visible from
//	    blockK..nextChange-1 = visible at blockN).
//	(3) storhist has no entry ≤ blockN — slot was untouched up to
//	    blockN, value is 0.
//
// NOT SAFE TO WIRE TO RPC YET. Case (3) cannot currently be told apart
// from "the last write was in an earlier segment", because
// HistoryReader.Lookup only searches the segment holding blockN — see the
// defect note on that method. Both land in the PlainState fallback, so a
// key that changed again after blockN answers with its CURRENT value.
//
// The 0-or-PlainState distinction matters: if storhist is incomplete
// (segment build hasn't covered blockN yet), case (1) is the correct
// fallback. The caller (RPC) should know if blockN is past the last
// history segment and prefer PlainState in that case.
func (r *HistoricalStateReader) StorageAt(addr types.Address, slot types.Hash, blockN uint64) ([]byte, error) {
	var compositeKey [52]byte
	copy(compositeKey[:20], addr[:])
	copy(compositeKey[20:], slot[:])

	blockK, found := r.storhist.Lookup(compositeKey[:], blockN)
	if !found {
		// Slot was never modified up to blockN — value is 0.
		// Fallback: query PlainState in case storhist is incomplete.
		return r.storageAtPlain(addr, slot)
	}

	// Decode storcs[blockK] to find the slot's NewValue.
	blob, err := r.storcs.Retrieve(blockK)
	if err != nil {
		return nil, fmt.Errorf("retrieve storcs[%d]: %w", blockK, err)
	}
	entries, err := ethel.DecodeStorageChanges(blob)
	if err != nil {
		return nil, fmt.Errorf("decode storcs[%d]: %w", blockK, err)
	}
	for i := range entries {
		if bytes.Equal(entries[i].CompositeKey, compositeKey[:]) {
			// NewValue is the post-blockK state = visible at blockN.
			return entries[i].NewValue, nil
		}
	}
	// storhist said this block had a change but storcs disagrees —
	// changeset/index inconsistency. Fall back to PlainState.
	return r.storageAtPlain(addr, slot)
}

// AccountAt returns the account's encoded bytes (V2 StateAccount) as
// of block blockN. Same three-case logic as StorageAt. Returns nil if
// the account did not exist at blockN.
func (r *HistoricalStateReader) AccountAt(addr types.Address, blockN uint64) ([]byte, error) {
	blockK, found := r.accthist.Lookup(addr[:], blockN)
	if !found {
		return r.accountAtPlain(addr)
	}

	blob, err := r.acctcs.Retrieve(blockK)
	if err != nil {
		return nil, fmt.Errorf("retrieve acctcs[%d]: %w", blockK, err)
	}
	entries, err := ethel.DecodeAccountChanges(blob)
	if err != nil {
		return nil, fmt.Errorf("decode acctcs[%d]: %w", blockK, err)
	}
	for i := range entries {
		if entries[i].Address == addr {
			return entries[i].NewValue, nil
		}
	}
	return r.accountAtPlain(addr)
}

// storageAtPlain reads slot's current value from PlainState
// (modules.Storage). Used as the "blockN past last segment" or
// "history-says-no-change" fallback.
func (r *HistoricalStateReader) storageAtPlain(addr types.Address, slot types.Hash) ([]byte, error) {
	if r.plainDB == nil {
		return nil, errors.New("HistoricalStateReader: no PlainState DB configured")
	}
	tx, err := r.plainDB.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
	v, err := tx.GetOne(modules.Storage, key)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// accountAtPlain reads account's current bytes from PlainState
// (modules.Account).
func (r *HistoricalStateReader) accountAtPlain(addr types.Address) ([]byte, error) {
	if r.plainDB == nil {
		return nil, errors.New("HistoricalStateReader: no PlainState DB configured")
	}
	tx, err := r.plainDB.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(modules.Account, addr[:])
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Close releases the freezer + history-segment file handles.
func (r *HistoricalStateReader) Close() {
	if r.storhist != nil {
		r.storhist.Close()
	}
	if r.accthist != nil {
		r.accthist.Close()
	}
	if r.storcs != nil {
		r.storcs.Close()
	}
	if r.acctcs != nil {
		r.acctcs.Close()
	}
}
