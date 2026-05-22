package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/empty"
)

// Reth's plain-state tables. PlainAccountState is keyed by 20-byte
// Address (NOT hashed) with Account-encoded value; PlainStorageState
// is DupSort keyed by 20-byte Address with 32-byte slot+value dups.
//
// These tables are the canonical input for HPH bootstrap because
// they contain the plain (unhashed) keys HPH expects, so we can use
// the standard KeyToHexNibbleHash and NewHexPatriciaHashed(length.Addr).
const (
	rethPlainAccountStateTable = "PlainAccountState"
	rethPlainStorageStateTable = "PlainStorageState"
)

// RethBackedReader exposes reth's PlainAccountState +
// PlainStorageState as commitment.AccountReader and
// commitment.StorageReader. plain keys are the actual addresses /
// slots — HPH hashes them via the standard hasher.
//
//	Account.plainKey  = 20-byte addr
//	Storage.plainKey  = 52-byte addr || slot
type RethBackedReader struct {
	src *RethHashedLeafSource
}

func NewRethBackedReader(src *RethHashedLeafSource) *RethBackedReader {
	return &RethBackedReader{src: src}
}

// Account implements commitment.AccountReader. plainKey is the
// 20-byte address. Returns nil if absent.
func (r *RethBackedReader) Account(plainKey []byte) (*commitment.Update, error) {
	if len(plainKey) != 20 {
		return nil, fmt.Errorf("RethBackedReader.Account: expected 20-byte addr, got %d", len(plainKey))
	}
	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(rethPlainAccountStateTable, plainKey)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	acct, err := DecodeRethAccount(v)
	if err != nil {
		return nil, fmt.Errorf("decode account at %x: %w", plainKey, err)
	}

	upd := new(commitment.Update)
	upd.Flags = commitment.BalanceUpdate | commitment.NonceUpdate
	upd.Nonce = acct.Nonce
	upd.Balance.Set(&acct.Balance)
	if acct.HasBytecode {
		copy(upd.CodeHash[:], acct.BytecodeHash[:])
		upd.Flags |= commitment.CodeUpdate
	} else {
		upd.CodeHash = empty.CodeHash
	}
	return upd, nil
}

// Storage implements commitment.StorageReader. plainKey is the
// 52-byte address||slot. Returns nil if absent.
func (r *RethBackedReader) Storage(plainKey []byte) (*commitment.Update, error) {
	if len(plainKey) != 52 {
		return nil, fmt.Errorf("RethBackedReader.Storage: expected 52-byte addr||slot, got %d", len(plainKey))
	}
	addr := plainKey[:20]
	slot := plainKey[20:]

	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethPlainStorageStateTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	v, err := c.SeekBothRange(addr, slot)
	if err != nil {
		return nil, err
	}
	if v == nil || len(v) < 32 || !bytes.Equal(v[:32], slot) {
		return nil, nil // absent
	}
	val := v[32:] // BE-stripped U256 bytes (StorageEntry.value)

	upd := new(commitment.Update)
	upd.Flags = commitment.StorageUpdate
	upd.StorageLen = int8(len(val))
	copy(upd.Storage[:], val)
	return upd, nil
}
