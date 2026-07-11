// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashed_historical.go — state-as-of-block reader for reth-2.2 hashed-canonical
// nodes (archive). It mirrors PlainState's historical path exactly, but swaps
// the "current value" base: where PlainState falls back to the plain
// Account/Storage tables (empty under hashed-canonical), this reader falls back
// to the tip hashed state (HashedStateReader over HashedAccounts/HashedStorage).
//
// Correctness follows from reuse: the history index (AccountsHistory/
// StorageHistory) and changesets (AccountChangeSet/StorageChangeSet) are
// plain-keyed and identical to the plain node's (HashedCanonicalWriter records
// them the same way), so FindByHistory returns the exact as-of value for any key
// that changed at/after the target block; keys unchanged since then take the tip
// value from the hashed base. codeHash lives in the (historical) account leaf, so
// code — content-addressed by codeHash — comes from the base unchanged.

package state

import (
	"errors"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
)

// HashedHistoricalReader serves post-state of `blockNr-1` (the FindByHistory
// timestamp convention: value at the START of blockNr) over a hashed-canonical
// datadir. Construct with blockNr = targetPostStateBlock+1, matching PlainState.
type HashedHistoricalReader struct {
	tx      kv.Tx
	base    *HashedStateReader
	blockNr uint64

	accHistoryC     kv.Cursor
	storageHistoryC kv.Cursor
	accChangesC     kv.CursorDupSort
	storageChangesC kv.CursorDupSort
}

// NewHashedHistoricalReader opens the history/changeset cursors and the tip base.
func NewHashedHistoricalReader(tx kv.Tx, blockNr uint64) *HashedHistoricalReader {
	c1, _ := tx.Cursor(modules.AccountsHistory)
	c2, _ := tx.Cursor(modules.StorageHistory)
	c3, _ := tx.CursorDupSort(modules.AccountChangeSet)
	c4, _ := tx.CursorDupSort(modules.StorageChangeSet)
	return &HashedHistoricalReader{
		tx:              tx,
		base:            NewHashedStateReader(tx),
		blockNr:         blockNr,
		accHistoryC:     c1,
		storageHistoryC: c2,
		accChangesC:     c3,
		storageChangesC: c4,
	}
}

// SetCodeSource wires the codes.cdat fast path onto the tip base.
func (r *HashedHistoricalReader) SetCodeSource(c CodeSource) { r.base.SetCodeSource(c) }

// SetCache attaches the cross-block read cache to the tip base.
func (r *HashedHistoricalReader) SetCache(c *HashedReadCache) { r.base.SetCache(c) }

func (r *HashedHistoricalReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	enc, err := FindByHistory(r.tx, r.accHistoryC, r.accChangesC, false /* storage */, address[:], r.blockNr)
	if err != nil {
		if errors.Is(err, ethdb.ErrKeyNotFound) {
			// Unchanged at/after the target block → tip value.
			return r.base.ReadAccountData(address)
		}
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil // account did not exist at the target block
	}
	var a account.StateAccount
	if err = a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *HashedHistoricalReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
	enc, err := FindByHistory(r.tx, r.storageHistoryC, r.storageChangesC, true /* storage */, compositeKey, r.blockNr)
	if err != nil {
		if errors.Is(err, ethdb.ErrKeyNotFound) {
			return r.base.ReadAccountStorage(address, key)
		}
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil
	}
	return enc, nil
}

// ReadAccountCode / ReadAccountCodeSize delegate to the base: code is content-
// addressed by codeHash (which comes from the historical account leaf), so the
// tip Code table serves the right bytes regardless of block.
func (r *HashedHistoricalReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	return r.base.ReadAccountCode(address, codeHash)
}

func (r *HashedHistoricalReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	return r.base.ReadAccountCodeSize(address, codeHash)
}

var _ StateReader = (*HashedHistoricalReader)(nil)
