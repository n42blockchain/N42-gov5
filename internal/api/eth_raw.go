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

package api

// =============================================================================
// eth_* Raw Transaction & Signing APIs
// =============================================================================
//
// This file contains eth_* RPC methods for:
// - Message signing (eth_sign)
// - Transaction signing (eth_signTransaction)
// - Raw transaction data retrieval (eth_getRawTransaction*)
//
// Reference: https://ethereum.github.io/execution-apis/api-documentation/

import (
	"context"
	"errors"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/accounts"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"

	"github.com/ledgerwatch/erigon-lib/kv"
)

// =============================================================================
// Message Signing
// =============================================================================

// SignedTransactionResult represents a signed but not submitted transaction.
type SignedTransactionResult struct {
	Raw hexutil.Bytes   `json:"raw"`
	Tx  *RPCTransaction `json:"tx"`
}

// Sign calculates an Ethereum specific signature with:
// sign(keccak256("\x19Ethereum Signed Message:\n" + len(message) + message)))
//
// Note: the address must be an unlocked account managed by this node.
//
// Parameters:
//   - address: The address to sign with
//   - data: The data to sign
//
// Returns:
//   - The signature
func (s *TransactionAPI) Sign(ctx context.Context, address avmcommon.Address, data hexutil.Bytes) (hexutil.Bytes, error) {
	// Look up the wallet containing the requested address
	account := accounts.Account{Address: types.Address(address)}

	if s.api.accountManager == nil {
		return nil, errors.New("account manager not available")
	}

	wallet, err := s.api.accountManager.Find(account)
	if err != nil {
		return nil, fmt.Errorf("account not found: %v", err)
	}

	// Sign the data with the wallet
	signature, err := wallet.SignData(account, accounts.MimetypeTextPlain, data)
	if err != nil {
		return nil, err
	}

	return signature, nil
}

// SignTransaction signs a transaction without submitting it to the network.
// This allows offline transaction creation.
//
// Parameters:
//   - args: The transaction arguments
//
// Returns:
//   - The signed transaction in both raw and decoded format
func (s *TransactionAPI) SignTransaction(ctx context.Context, args TransactionArgs) (*SignedTransactionResult, error) {
	// Look up the wallet containing the requested signer
	if args.From == nil {
		return nil, errors.New("from address is required")
	}

	account := accounts.Account{Address: args.from()}

	if s.api.accountManager == nil {
		return nil, errors.New("account manager not available")
	}

	wallet, err := s.api.accountManager.Find(account)
	if err != nil {
		return nil, fmt.Errorf("account not found: %v", err)
	}

	// Set defaults for the transaction
	if err := args.setDefaults(ctx, s.api); err != nil {
		return nil, err
	}

	// Create the transaction
	tx := args.toTransaction()

	// Sign the transaction
	signed, err := wallet.SignTx(account, tx, s.api.GetChainConfig().ChainID)
	if err != nil {
		return nil, err
	}

	// Encode the signed transaction
	raw, err := signed.Marshal()
	if err != nil {
		return nil, err
	}

	// Get the current header for RPC transaction formatting
	header := s.api.BlockChain().CurrentBlock().Header()

	return &SignedTransactionResult{
		Raw: raw,
		Tx:  newRPCPendingTransaction(signed, header),
	}, nil
}

// =============================================================================
// Raw Transaction Retrieval
// =============================================================================

// GetRawTransactionByHash returns the raw transaction for the given transaction hash.
//
// Parameters:
//   - hash: The transaction hash
//
// Returns:
//   - The RLP-encoded transaction bytes
func (s *TransactionAPI) GetRawTransactionByHash(ctx context.Context, hash avmcommon.Hash) (hexutil.Bytes, error) {
	// Try to find the transaction in the database
	var rawTx hexutil.Bytes

	err := s.api.Database().View(ctx, func(t kv.Tx) error {
		tx, _, _, _, err := rawdb.ReadTransactionByHash(t, avmtypes.ToastHash(hash))
		if err != nil {
			return err
		}
		if tx == nil {
			return nil
		}

		raw, err := tx.Marshal()
		if err != nil {
			return err
		}
		rawTx = raw
		return nil
	})

	if err != nil {
		return nil, err
	}

	if rawTx != nil {
		return rawTx, nil
	}

	// Check the transaction pool
	if poolTx := s.api.TxsPool().GetTx(avmtypes.ToastHash(hash)); poolTx != nil {
		raw, err := poolTx.Marshal()
		if err != nil {
			return nil, err
		}
		return raw, nil
	}

	return nil, nil
}

// GetRawTransactionByBlockHashAndIndex returns the raw transaction for the given
// block hash and index.
//
// Parameters:
//   - blockHash: The block hash
//   - index: The transaction index within the block
//
// Returns:
//   - The RLP-encoded transaction bytes
func (s *TransactionAPI) GetRawTransactionByBlockHashAndIndex(ctx context.Context, blockHash avmcommon.Hash, index hexutil.Uint) (hexutil.Bytes, error) {
	block, err := s.api.BlockChain().GetBlockByHash(avmtypes.ToastHash(blockHash))
	if err != nil || block == nil {
		return nil, err
	}

	txs := block.Transactions()
	if int(index) >= len(txs) {
		return nil, nil
	}

	raw, err := txs[index].Marshal()
	if err != nil {
		return nil, err
	}

	return raw, nil
}

// GetRawTransactionByBlockNumberAndIndex returns the raw transaction for the given
// block number and index.
//
// Parameters:
//   - blockNr: The block number
//   - index: The transaction index within the block
//
// Returns:
//   - The RLP-encoded transaction bytes
func (s *TransactionAPI) GetRawTransactionByBlockNumberAndIndex(ctx context.Context, blockNr jsonrpc.BlockNumber, index hexutil.Uint) (hexutil.Bytes, error) {
	var block interface {
		Transactions() interface{ Len() int }
	}

	if blockNr == jsonrpc.PendingBlockNumber || blockNr == jsonrpc.LatestBlockNumber {
		blk := s.api.BlockChain().CurrentBlock()
		if blk == nil {
			return nil, nil
		}
		txs := blk.Transactions()
		if int(index) >= len(txs) {
			return nil, nil
		}
		raw, err := txs[index].Marshal()
		if err != nil {
			return nil, err
		}
		return raw, nil
	}

	blk, err := s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
	if err != nil || blk == nil {
		return nil, err
	}
	_ = block // silence unused variable

	txs := blk.Transactions()
	if int(index) >= len(txs) {
		return nil, nil
	}

	raw, err := txs[index].Marshal()
	if err != nil {
		return nil, err
	}

	return raw, nil
}

// =============================================================================
// Additional Transaction APIs
// =============================================================================

// PendingTransactions returns the transactions that are in the transaction pool
// and have a from address that is one of the accounts this node manages.
func (s *TransactionAPI) PendingTransactions() ([]*RPCTransaction, error) {
	pending, _ := s.api.TxsPool().Content()

	// Get managed accounts
	var accounts []types.Address
	if s.api.accountManager != nil {
		accounts = s.api.accountManager.Accounts()
	}

	// Create a map for fast lookup
	accountSet := make(map[types.Address]struct{})
	for _, acc := range accounts {
		accountSet[acc] = struct{}{}
	}

	var transactions []*RPCTransaction
	curHeader := s.api.BlockChain().CurrentBlock().Header()

	for account, txs := range pending {
		// Only include transactions from managed accounts
		if _, ok := accountSet[account]; ok {
			for _, tx := range txs {
				transactions = append(transactions, newRPCPendingTransaction(tx, curHeader))
			}
		}
	}

	return transactions, nil
}

// Resend accepts an existing transaction and replaces it with a new one.
// This is used for bumping gas price on stuck transactions.
//
// Parameters:
//   - sendArgs: The original transaction arguments
//   - gasPrice: New gas price (optional)
//   - gasLimit: New gas limit (optional)
//
// Returns:
//   - The hash of the new transaction
func (s *TransactionAPI) Resend(ctx context.Context, sendArgs TransactionArgs, gasPrice *hexutil.Big, gasLimit *hexutil.Uint64) (avmcommon.Hash, error) {
	// Override gas price if provided
	if gasPrice != nil {
		sendArgs.GasPrice = gasPrice
	}

	// Override gas limit if provided
	if gasLimit != nil {
		sendArgs.Gas = gasLimit
	}

	// Send the transaction (this will replace the old one in the pool)
	return s.SendTransaction(ctx, sendArgs)
}
