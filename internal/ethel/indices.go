// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// indices.go — TxLookup maintenance helpers.
//
// WriteBlockIndices appends a tx-hash → block-number entry to kv.TxLookup
// for every transaction in a block, which powers eth_getTransactionByHash
// and eth_getTransactionReceipt. LookupTransaction is the read-side
// helper. This file intentionally covers TxLookup only so the dependency
// graph stays small.
//
// Note: the ethexec executor does NOT write log-index bitmaps — receipts
// are re-derived per block for in-memory verification but never
// persisted. Log bitmap maintenance lives in the live-node write path
// (internal/blockchain_write.go).

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// WriteBlockIndices writes TxLookup entries for all transactions in a block.
// TxLookup maps tx hash → block number, enabling eth_getTransactionByHash.
func WriteBlockIndices(tx kv.RwTx, blockNum uint64, txs []*transaction.Transaction) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], blockNum)
	for _, txn := range txs {
		hash := txn.Hash()
		if err := tx.Put(kv.TxLookup, hash[:], buf[:]); err != nil {
			return err
		}
	}
	return nil
}

// Log bitmap indices: not maintained by ethexec. The executor re-derives
// receipts per block for verification, then discards them. The live node
// path (internal/blockchain_write.go) writes log indices via
// rawdb.WriteLogIndex during insertBlock.

// LookupTransaction retrieves the block number for a transaction hash.
func LookupTransaction(tx kv.Tx, txHash types.Hash) (uint64, bool, error) {
	v, err := tx.GetOne(kv.TxLookup, txHash[:])
	if err != nil {
		return 0, false, err
	}
	if v == nil || len(v) < 8 {
		return 0, false, nil
	}
	return binary.BigEndian.Uint64(v), true, nil
}
