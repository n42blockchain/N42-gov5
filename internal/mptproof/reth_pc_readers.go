package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/empty"
)

// RethBackedReader exposes reth's HashedAccounts + HashedStorages
// tables as commitment.AccountReader and commitment.StorageReader.
//
// IMPORTANT — plainKey convention:
//
//	For accounts, plainKey is the 32-byte ADDR HASH (i.e. keccak256
//	of the 20-byte address). Reth's HashedAccounts is keyed by this
//	hash, so we don't need plain addresses (which we couldn't recover
//	from a hashed-state-only source anyway).
//
//	For storage, plainKey is the 64-byte composite ADDR HASH || SLOT
//	HASH. We split it on the 32-byte boundary to query the DupSort
//	cursor.
//
// Pairing the reader with NibbleIdentityHasher (see below) prevents
// HPH from re-hashing the already-hashed keys.
type RethBackedReader struct {
	src *RethHashedLeafSource
}

func NewRethBackedReader(src *RethHashedLeafSource) *RethBackedReader {
	return &RethBackedReader{src: src}
}

// Account implements commitment.AccountReader. plainKey must be the
// 32-byte addrHash. Returns nil if absent.
func (r *RethBackedReader) Account(plainKey []byte) (*commitment.Update, error) {
	if len(plainKey) != 32 {
		return nil, fmt.Errorf("RethBackedReader.Account: expected 32-byte addrHash, got %d", len(plainKey))
	}
	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(rethHashedAccountsTable, plainKey)
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
	upd.Flags = 0 // build the present-flags below
	upd.Nonce = acct.Nonce
	upd.Balance.Set(&acct.Balance)
	upd.Flags |= commitment.BalanceUpdate | commitment.NonceUpdate
	if acct.HasBytecode {
		// CodeHash field is types.Hash (32B array).
		copy(upd.CodeHash[:], acct.BytecodeHash[:])
		upd.Flags |= commitment.CodeUpdate
	} else {
		upd.CodeHash = empty.CodeHash
	}
	return upd, nil
}

// Storage implements commitment.StorageReader. plainKey must be the
// 64-byte addrHash||slotHash composite. Returns nil if absent.
func (r *RethBackedReader) Storage(plainKey []byte) (*commitment.Update, error) {
	if len(plainKey) != 64 {
		return nil, fmt.Errorf("RethBackedReader.Storage: expected 64-byte addrHash||slotHash, got %d", len(plainKey))
	}
	addrHash := plainKey[:32]
	slotHash := plainKey[32:]

	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethHashedStoragesTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	v, err := c.SeekBothRange(addrHash, slotHash)
	if err != nil {
		return nil, err
	}
	if v == nil || len(v) < 32 || !bytes.Equal(v[:32], slotHash) {
		return nil, nil // absent
	}
	val := v[32:] // BE-stripped U256 bytes (StorageEntry.value)

	upd := new(commitment.Update)
	upd.Flags = commitment.StorageUpdate
	upd.StorageLen = int8(len(val))
	copy(upd.Storage[:], val)
	return upd, nil
}

// NibbleIdentityHasher converts an already-hashed key (32-byte
// addrHash for accounts, 64-byte addrHash||slotHash for storage)
// into nibblized form WITHOUT re-hashing. Pair with RethBackedReader
// to feed HPH a hashed-state source while preserving canonical
// state-root semantics.
//
// Layout produced (matches KeyToHexNibbleHash's output for the
// nibblized half):
//
//	account:  64 nibbles  (keccak addr expanded to nibbles)
//	storage:  128 nibbles (keccak addr + keccak slot expanded)
func NibbleIdentityHasher(key []byte) []byte {
	out := make([]byte, len(key)*2)
	for i, b := range key {
		out[i*2] = (b >> 4) & 0xf
		out[i*2+1] = b & 0xf
	}
	return out
}
