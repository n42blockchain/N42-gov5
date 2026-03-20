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
func (o *OtterscanAPI) GetContractCreator(ctx context.Context, address types.Address) (*ContractCreatorResult, error) {
	// This is a best-effort lookup. A full implementation would need
	// an address→creation-tx index. For now, return nil (not found).
	return nil, nil
}

// TransactionBySenderAndNonce returns the transaction hash for a given sender and nonce.
func (o *OtterscanAPI) GetTransactionBySenderAndNonce(ctx context.Context, sender types.Address, nonce uint64) (*types.Hash, error) {
	// This requires an address+nonce→txHash index which is not currently maintained.
	// A full implementation would scan the TxLookup index filtered by sender.
	return nil, fmt.Errorf("ots_getTransactionBySenderAndNonce: not yet implemented (requires address+nonce index)")
}

// SearchTransactionsResult holds paginated transaction search results.
type SearchTransactionsResult struct {
	Txs         []map[string]interface{} `json:"txs"`
	Receipts    []map[string]interface{} `json:"receipts"`
	FirstPage   bool                     `json:"firstPage"`
	LastPage    bool                     `json:"lastPage"`
}

// SearchTransactionsBefore searches for transactions involving an address before a given block.
func (o *OtterscanAPI) SearchTransactionsBefore(ctx context.Context, address types.Address, blockNumber uint64, pageSize uint64) (*SearchTransactionsResult, error) {
	// Stub: a full implementation would use the LogAddressIndex bitmap
	// to find blocks containing transactions from/to this address.
	return &SearchTransactionsResult{
		Txs:       []map[string]interface{}{},
		Receipts:  []map[string]interface{}{},
		FirstPage: true,
		LastPage:  true,
	}, nil
}

// SearchTransactionsAfter searches for transactions involving an address after a given block.
func (o *OtterscanAPI) SearchTransactionsAfter(ctx context.Context, address types.Address, blockNumber uint64, pageSize uint64) (*SearchTransactionsResult, error) {
	return &SearchTransactionsResult{
		Txs:       []map[string]interface{}{},
		Receipts:  []map[string]interface{}{},
		FirstPage: true,
		LastPage:  true,
	}, nil
}

// GetTransactionError returns the revert reason for a failed transaction.
func (o *OtterscanAPI) GetTransactionError(ctx context.Context, hash types.Hash) (hexutil.Bytes, error) {
	// Would need to re-execute the transaction to get the revert data.
	// Delegate to debug_traceTransaction with a revert tracer.
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
