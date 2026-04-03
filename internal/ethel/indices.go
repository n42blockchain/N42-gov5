// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

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

// Log bitmap indices are written via rawdb.WriteLogIndex() called from executor.go.
// No additional stub needed here.

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
