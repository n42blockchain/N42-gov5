// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// CollectFreezeData reads block data from the hot database for a range of
// blocks [start, start+count) and packages it for the freezer.
func CollectFreezeData(tx kv.Tx, start, count uint64) (*freezer.FreezeData, error) {
	data := &freezer.FreezeData{
		Headers:    make([][]byte, count),
		Bodies:     make([][]byte, count),
		Receipts:   make([][]byte, count),
		Hashes:     make([][]byte, count),
		Difficulty: make([][]byte, count),
	}

	for i := uint64(0); i < count; i++ {
		number := start + i

		// Canonical hash.
		hash, err := ReadCanonicalHash(tx, number)
		if err != nil {
			return nil, fmt.Errorf("freeze: read canonical hash %d: %w", number, err)
		}
		if hash == (types.Hash{}) {
			return nil, fmt.Errorf("freeze: missing canonical hash for block %d", number)
		}
		data.Hashes[i] = hash.Bytes()

		// Header (raw protobuf).
		headerRaw := ReadHeaderRAW(tx, hash, number)
		if headerRaw == nil {
			return nil, fmt.Errorf("freeze: missing header for block %d", number)
		}
		data.Headers[i] = headerRaw

		// Body (raw protobuf — using ReadBodyRAW which returns the full serialized body).
		bodyRaw := ReadBodyRAW(tx, hash, number)
		if bodyRaw == nil {
			// Some blocks may have empty bodies, store empty bytes.
			data.Bodies[i] = []byte{}
		} else {
			data.Bodies[i] = bodyRaw
		}

		// Receipts (raw protobuf).
		receiptRaw, err := tx.GetOne(modules.Receipts, modules.EncodeBlockNumber(number))
		if err != nil {
			return nil, fmt.Errorf("freeze: read receipts %d: %w", number, err)
		}
		if receiptRaw == nil {
			data.Receipts[i] = []byte{}
		} else {
			data.Receipts[i] = receiptRaw
		}

		// Total difficulty.
		td, err := ReadTd(tx, hash, number)
		if err != nil {
			return nil, fmt.Errorf("freeze: read td %d: %w", number, err)
		}
		if td != nil {
			b32 := td.Bytes32()
			data.Difficulty[i] = b32[:]
		} else {
			data.Difficulty[i] = []byte{}
		}
	}

	return data, nil
}

// CleanupFrozenData removes frozen block data from the hot database.
// Only removes data that has been safely persisted to the freezer.
func CleanupFrozenData(tx kv.RwTx, start, count uint64) error {
	for i := uint64(0); i < count; i++ {
		number := start + i

		hash, err := ReadCanonicalHash(tx, number)
		if err != nil {
			continue
		}
		if hash == (types.Hash{}) {
			continue
		}

		key := modules.HeaderKey(number, hash)

		// Delete header.
		if err := tx.Delete(modules.Headers, key); err != nil {
			return fmt.Errorf("freeze cleanup: delete header %d: %w", number, err)
		}

		// Delete header TD.
		if err := tx.Delete(modules.HeaderTD, key); err != nil {
			return fmt.Errorf("freeze cleanup: delete td %d: %w", number, err)
		}

		// Delete body.
		bodyKey := modules.BlockBodyKey(number, hash)
		if err := tx.Delete(modules.BlockBody, bodyKey); err != nil {
			return fmt.Errorf("freeze cleanup: delete body %d: %w", number, err)
		}

		// Delete receipts.
		if err := tx.Delete(modules.Receipts, modules.EncodeBlockNumber(number)); err != nil {
			return fmt.Errorf("freeze cleanup: delete receipts %d: %w", number, err)
		}

		// Note: We keep HeaderCanonical, HeaderNumber, BlockTx, TxLookup, and Senders
		// in the hot DB — they're small and needed for chain navigation.
	}

	return nil
}
