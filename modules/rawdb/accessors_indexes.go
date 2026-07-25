// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Transaction hash -> (block, index) lookup index accessors.
// TxLookupEntry carries BlockHash, BlockIndex and per-block Index
// metadata. ReadTxLookupEntry returns the positional block number
// for a txn hash; WriteTxLookupEntries iterates block.Transactions
// and persists one lookup entry per tx into modules.TxLookup.

package rawdb

import (
	"bytes"
	"slices"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// TxLookupEntry is a positional metadata to help looking up the data content of
// a transaction or receipt given only its hash.
type TxLookupEntry struct {
	BlockHash  types.Hash
	BlockIndex uint64
	Index      uint64
}

// ReadTxLookupEntry retrieves the positional metadata associated with a transaction
// hash to allow retrieving the transaction or receipt by hash.
func ReadTxLookupEntry(db kv.Getter, txnHash types.Hash) (*uint64, error) {
	data, err := db.GetOne(modules.TxLookup, txnHash.Bytes())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	number := new(uint256.Int).SetBytes(data).Uint64()
	return &number, nil
}

// WriteTxLookupEntries stores a positional metadata for every transaction from
// a block, enabling hash based transaction and receipt lookups.
func WriteTxLookupEntries(db kv.Putter, block *block.Block) {
	blockNumber, err := requireBlockNumber(block, "block number unavailable")
	if err != nil {
		log.Error("Skipping transaction lookup entries", "err", err)
		return
	}

	data := blockNumber.Bytes()
	txs := block.Transactions()
	if len(txs) == 0 {
		return
	}

	// Insert in ascending key order. The key is a transaction hash, so in
	// transaction order these land on random leaves of a B+tree that already
	// spans the whole chain's transactions: each Put descends to an unrelated
	// page and dirties it, and the commit then has to copy out every one of
	// those scattered pages. Sorting first turns one block's inserts into a
	// small number of contiguous page runs — the same key/value pairs, far
	// fewer dirty pages. At 14k transactions per block this is the largest
	// random-write source in the block write path.
	hashes := make([]types.Hash, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash()
	}
	slices.SortFunc(hashes, func(a, b types.Hash) int {
		return bytes.Compare(a.Bytes(), b.Bytes())
	})
	for i := range hashes {
		if err := db.Put(modules.TxLookup, hashes[i].Bytes(), data); err != nil {
			log.Crit("Failed to store transaction lookup entry", "err", err)
		}
	}
}

// DeleteTxLookupEntry removes all transaction data associated with a hash.
func DeleteTxLookupEntry(db kv.Deleter, hash types.Hash) error {
	return db.Delete(modules.TxLookup, hash.Bytes())
}

// ReadTransactionByHash retrieves a specific transaction from the database, along with
// its added positional metadata.
func ReadTransactionByHash(db kv.Tx, hash types.Hash) (*transaction.Transaction, types.Hash, uint64, uint64, error) {
	blockNumber, err := ReadTxLookupEntry(db, hash)
	if err != nil {
		return nil, types.Hash{}, 0, 0, err
	}
	if blockNumber == nil {
		return nil, types.Hash{}, 0, 0, nil
	}
	blockHash, err := ReadCanonicalHash(db, *blockNumber)
	if err != nil {
		return nil, types.Hash{}, 0, 0, err
	}
	if blockHash == (types.Hash{}) {
		return nil, types.Hash{}, 0, 0, nil
	}
	body := ReadCanonicalBodyWithTransactions(db, blockHash, *blockNumber)
	if body == nil {
		log.Error("Transaction referenced missing", "number", blockNumber, "hash", blockHash)
		return nil, types.Hash{}, 0, 0, nil
	}
	senders, err := ReadSenders(db, blockHash, *blockNumber)
	if err != nil {
		return nil, types.Hash{}, 0, 0, err
	}
	body.SendersToTxs(senders)
	for txIndex, tx := range body.Txs {
		if tx.Hash() == hash {
			return tx, blockHash, *blockNumber, uint64(txIndex), nil
		}
	}
	log.Error("Transaction not found", "number", blockNumber, "hash", blockHash, "txhash", hash)
	return nil, types.Hash{}, 0, 0, nil
}

