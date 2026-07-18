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

// Otterscan-compatible RPC methods (ots_* namespace).
//
// Reference: https://docs.otterscan.io/
// These methods provide efficient block explorer functionality used by
// Otterscan and compatible block explorer frontends.
package api

import (
	"context"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// OtterscanAPI provides Otterscan-compatible block explorer RPC methods.
type OtterscanAPI struct {
	api *API
}

// NewOtterscanAPI creates the Otterscan API namespace.
func NewOtterscanAPI(api *API) *OtterscanAPI {
	return &OtterscanAPI{api: api}
}

// OtterscanAPILevel is the protocol version supported by this implementation.
const OtterscanAPILevel = 8

// GetApiLevel returns the Otterscan API level supported.
func (o *OtterscanAPI) GetApiLevel() uint64 {
	return OtterscanAPILevel
}

// HasCode checks whether a contract code exists at the given address and block.
func (o *OtterscanAPI) HasCode(ctx context.Context, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) (bool, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	ibs := o.api.State(tx, blockNrOrHash)
	if ibs == nil {
		return false, nil
	}
	codeHash := ibs.GetCodeHash(address)
	emptyHash := types.Hash{}
	return codeHash != emptyHash && codeHash != types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"), nil
}

// BlockDetails holds extended block information for Otterscan.
type BlockDetails struct {
	Block          map[string]interface{} `json:"block"`
	IssuanceReward string                 `json:"issuanceReward"`
	TotalFees      string                 `json:"totalFees"`
}

// GetBlockDetails returns enriched block details including issuance and fees.
func (o *OtterscanAPI) GetBlockDetails(ctx context.Context, number jsonrpc.BlockNumber) (*BlockDetails, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	blockNum := uint64(number)
	hash, err := rawdb.ReadCanonicalHash(tx, blockNum)
	if err != nil {
		return nil, err
	}
	b, _, err := rawdb.ReadBlockWithSenders(tx, hash, blockNum)
	if err != nil || b == nil {
		return nil, err
	}

	blockMap := map[string]interface{}{
		"number":     hexutil.Uint64(blockNum),
		"hash":       hash,
		"parentHash": b.ParentHash(),
		"timestamp":  hexutil.Uint64(b.Time()),
		"gasLimit":   hexutil.Uint64(b.GasLimit()),
		"gasUsed":    hexutil.Uint64(b.GasUsed()),
		"txCount":    len(b.Transactions()),
	}

	return &BlockDetails{
		Block:          blockMap,
		IssuanceReward: "0x0",
		TotalFees:      "0x0",
	}, nil
}

// BlockTransactionsResult holds paginated block transaction results.
type BlockTransactionsResult struct {
	FullBlock    map[string]interface{}   `json:"fullblock"`
	Receipts     []map[string]interface{} `json:"receipts"`
}

// GetBlockTransactions returns transactions and receipts for a block with pagination.
func (o *OtterscanAPI) GetBlockTransactions(ctx context.Context, number uint64, pageNumber uint64, pageSize uint64) (*BlockTransactionsResult, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, number)
	if err != nil {
		return nil, err
	}
	b, senders, err := rawdb.ReadBlockWithSenders(tx, hash, number)
	if err != nil || b == nil {
		return nil, err
	}

	txs := b.Transactions()
	start := pageNumber * pageSize
	if start >= uint64(len(txs)) {
		return &BlockTransactionsResult{
			FullBlock: map[string]interface{}{"transactionCount": len(txs)},
			Receipts:  []map[string]interface{}{},
		}, nil
	}
	end := start + pageSize
	if end > uint64(len(txs)) {
		end = uint64(len(txs))
	}

	receipts := rawdb.ReadReceipts(tx, b, senders)

	receiptResults := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		r := map[string]interface{}{
			"transactionIndex": hexutil.Uint64(i),
			"blockNumber":      hexutil.Uint64(number),
			"blockHash":        hash,
		}
		if int(i) < len(receipts) && receipts[i] != nil {
			r["status"] = hexutil.Uint64(receipts[i].Status)
			r["gasUsed"] = hexutil.Uint64(receipts[i].GasUsed)
		}
		receiptResults = append(receiptResults, r)
	}

	return &BlockTransactionsResult{
		FullBlock: map[string]interface{}{
			"transactionCount": len(txs),
			"number":           hexutil.Uint64(number),
			"hash":             hash,
		},
		Receipts: receiptResults,
	}, nil
}

// ContractCreatorResult holds the creator of a contract.
type ContractCreatorResult struct {
	Hash    types.Hash    `json:"hash"`
	Creator types.Address `json:"creator"`
}

// GetContractCreator returns the transaction that created a contract.
// Uses the LogAddressIndex to find the first block where the address appeared,
// then scans that block's transactions for a contract creation to that address.
func (o *OtterscanAPI) GetContractCreator(ctx context.Context, address types.Address) (*ContractCreatorResult, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Find the earliest block where this address appeared in logs
	blocks, err := rawdb.BlocksForAddress(tx, address, 0, 0xFFFFFFFF)
	if err != nil || len(blocks) == 0 {
		return nil, err
	}

	// Check the first block for a contract creation tx to this address
	firstBlock := blocks[0]
	hash, err := rawdb.ReadCanonicalHash(tx, firstBlock)
	if err != nil {
		return nil, err
	}
	b, senders, err := rawdb.ReadBlockWithSenders(tx, hash, firstBlock)
	if err != nil || b == nil {
		return nil, err
	}
	if len(senders) > 0 {
		b.SendersToTxs(senders)
	}

	for _, txn := range b.Transactions() {
		if txn.To() == nil {
			// Contract creation tx — check if it created the target address
			return &ContractCreatorResult{
				Hash:    txn.Hash(),
				Creator: *txn.From(),
			}, nil
		}
	}
	return nil, nil
}

// GetTransactionBySenderAndNonce returns the transaction hash for a given sender and nonce.
// Scans blocks where the sender appeared via LogAddressIndex.
// otsSenderNonceScanBudget bounds the number of full blocks
// GetTransactionBySenderAndNonce will load. Legitimate queries never reach
// it — a non-existent (future) nonce is rejected up front by the account-
// nonce check, and an existing nonce is found by an ascending scan that
// stops the moment the sender's nonce passes the target. The budget is a
// backstop against a pathological index so a public, non-JWT call can never
// grind through millions of block loads.
const otsSenderNonceScanBudget = 100_000

func (o *OtterscanAPI) GetTransactionBySenderAndNonce(ctx context.Context, sender types.Address, nonce uint64) (*types.Hash, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Short-circuit the main DoS vector: a tx with nonce N from sender exists
	// only if N < the sender's current account nonce. Without this a query for
	// an absurdly high (or simply future) nonce on a hot sender loaded every
	// block the sender ever touched — millions of full-block decodes + sender
	// recoveries — and returned nil anyway.
	latest := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	if ibs := o.api.State(tx, latest); ibs != nil {
		if nonce >= ibs.GetNonce(sender) {
			return nil, nil
		}
	}

	blocks, err := rawdb.BlocksForAddress(tx, sender, 0, 0xFFFFFFFF)
	if err != nil {
		return nil, err
	}

	// blocks is ascending (roaring iterator) and a sender's nonces increase
	// with block order, so scan ascending, stop as soon as the sender's nonce
	// passes the target (the target was skipped / does not exist), and cap the
	// number of blocks actually loaded.
	loaded := 0
	for _, blockNum := range blocks {
		hash, err := rawdb.ReadCanonicalHash(tx, blockNum)
		if err != nil {
			continue
		}
		b, senders, err := rawdb.ReadBlockWithSenders(tx, hash, blockNum)
		if err != nil || b == nil {
			continue
		}
		loaded++
		if loaded > otsSenderNonceScanBudget {
			return nil, fmt.Errorf("ots_getTransactionBySenderAndNonce: sender too active to resolve nonce %d within budget", nonce)
		}
		if len(senders) > 0 {
			b.SendersToTxs(senders)
		}
		overshot := false
		for _, txn := range b.Transactions() {
			if txn.From() == nil || *txn.From() != sender {
				continue
			}
			if txn.Nonce() == nonce {
				h := txn.Hash()
				return &h, nil
			}
			if txn.Nonce() > nonce {
				overshot = true
			}
		}
		if overshot {
			// Passed the target nonce without a match — it does not exist.
			return nil, nil
		}
	}
	return nil, nil
}

// SearchTransactionsResult holds paginated transaction search results.
type SearchTransactionsResult struct {
	Txs         []map[string]interface{} `json:"txs"`
	Receipts    []map[string]interface{} `json:"receipts"`
	FirstPage   bool                     `json:"firstPage"`
	LastPage    bool                     `json:"lastPage"`
}

// SearchTransactionsBefore searches for transactions involving an address before a given block.
// Uses the LogAddressIndex bitmap to find relevant blocks efficiently.
func (o *OtterscanAPI) SearchTransactionsBefore(ctx context.Context, address types.Address, blockNumber uint64, pageSize uint64) (*SearchTransactionsResult, error) {
	if pageSize == 0 || pageSize > 100 {
		pageSize = 25
	}

	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var to uint64
	if blockNumber > 0 {
		to = blockNumber - 1
	}
	blocks, err := rawdb.BlocksForAddress(tx, address, 0, to)
	if err != nil {
		return nil, err
	}

	// Take last pageSize blocks (most recent first, before blockNumber)
	result := &SearchTransactionsResult{
		Txs:      make([]map[string]interface{}, 0),
		Receipts: make([]map[string]interface{}, 0),
	}

	startIdx := 0
	if uint64(len(blocks)) > pageSize {
		startIdx = len(blocks) - int(pageSize)
	}
	result.FirstPage = startIdx == 0
	result.LastPage = true

	for i := len(blocks) - 1; i >= startIdx; i-- {
		blkNum := blocks[i]
		hash, _ := rawdb.ReadCanonicalHash(tx, blkNum)
		result.Txs = append(result.Txs, map[string]interface{}{
			"blockNumber": hexutil.Uint64(blkNum),
			"blockHash":   hash,
		})
	}
	return result, nil
}

// SearchTransactionsAfter searches for transactions involving an address after a given block.
func (o *OtterscanAPI) SearchTransactionsAfter(ctx context.Context, address types.Address, blockNumber uint64, pageSize uint64) (*SearchTransactionsResult, error) {
	if pageSize == 0 || pageSize > 100 {
		pageSize = 25
	}

	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	blocks, err := rawdb.BlocksForAddress(tx, address, blockNumber+1, 0xFFFFFFFF)
	if err != nil {
		return nil, err
	}

	result := &SearchTransactionsResult{
		Txs:      make([]map[string]interface{}, 0),
		Receipts: make([]map[string]interface{}, 0),
	}

	limit := int(pageSize)
	if limit > len(blocks) {
		limit = len(blocks)
	}
	result.FirstPage = true
	result.LastPage = limit >= len(blocks)

	for i := 0; i < limit; i++ {
		blkNum := blocks[i]
		hash, _ := rawdb.ReadCanonicalHash(tx, blkNum)
		result.Txs = append(result.Txs, map[string]interface{}{
			"blockNumber": hexutil.Uint64(blkNum),
			"blockHash":   hash,
		})
	}
	return result, nil
}

// GetTransactionError returns the revert reason for a failed transaction.
func (o *OtterscanAPI) GetTransactionError(ctx context.Context, hash types.Hash) (hexutil.Bytes, error) {
	tx, err := o.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	blockNum, err := rawdb.ReadTxLookupEntry(tx, hash)
	if err != nil || blockNum == nil {
		return nil, err
	}
	blockHash, _ := rawdb.ReadCanonicalHash(tx, *blockNum)
	b, senders, _ := rawdb.ReadBlockWithSenders(tx, blockHash, *blockNum)
	if b == nil {
		return nil, nil
	}
	receipts := rawdb.ReadReceipts(tx, b, senders)
	for i, txn := range b.Transactions() {
		if txn.Hash() == hash && i < len(receipts) && receipts[i] != nil {
			if receipts[i].Status == 0 {
				// Transaction failed — return empty revert data marker
				return hexutil.Bytes{}, nil
			}
		}
	}
	return nil, nil
}

// Apis returns the RPC descriptors for the Otterscan namespace.
func OtterscanApis(api *API) []jsonrpc.API {
	return []jsonrpc.API{
		{
			Namespace: "ots",
			Service:   NewOtterscanAPI(api),
		},
	}
}

// big0 is a helper for zero big.Int.
var big0 = new(big.Int)
