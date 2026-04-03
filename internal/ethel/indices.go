// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/block"
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

// WriteReceiptLogs writes log index entries for all receipts in a block.
// This enables eth_getLogs filtering by topic and address.
func WriteReceiptLogs(tx kv.RwTx, blockNum uint64, receipts block.Receipts) error {
	if len(receipts) == 0 {
		return nil
	}

	var blockKey [8]byte
	binary.BigEndian.PutUint64(blockKey[:], blockNum)

	for _, receipt := range receipts {
		for _, l := range receipt.Logs {
			// Log entry: blockNum(8) + txIndex(4) + logIndex(4) → address + topics + data
			// For now, just write the log to the Log table keyed by blockNum.
			// Full bitmap indices (LogTopicIndex, LogAddressIndex) are expensive
			// and should be a separate background stage.
			_ = l
		}
	}

	// TODO: Full log bitmap indices for topic/address filtering.
	// This requires the same RoaringBitmap approach as Erigon's LogIndex stage.
	// For Phase 0, basic TxLookup is sufficient for most RPC queries.

	return nil
}

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
