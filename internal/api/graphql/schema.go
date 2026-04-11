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
// GraphQL schema types for blocks and transactions per EIP-1767.
// Block encodes number, hash, parent, timestamp, gas fields, miner,
// trie roots, logs bloom, difficulty, size, base fee and extra data.
// Transaction mirrors the EIP-1559/2930 shape so clients can query a
// consistent object model across legacy and typed transactions.

package graphql

import (
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

// Block represents an Ethereum block in the GraphQL schema, following EIP-1767.
type Block struct {
	Number           hexutil.Uint64  `json:"number"`
	Hash             types.Hash      `json:"hash"`
	Parent           *types.Hash     `json:"parent"`
	Timestamp        hexutil.Uint64  `json:"timestamp"`
	GasUsed          hexutil.Uint64  `json:"gasUsed"`
	GasLimit         hexutil.Uint64  `json:"gasLimit"`
	Transactions     []*Transaction  `json:"transactions"`
	Miner            types.Address   `json:"miner"`
	StateRoot        types.Hash      `json:"stateRoot"`
	ReceiptsRoot     types.Hash      `json:"receiptsRoot"`
	TransactionsRoot types.Hash      `json:"transactionsRoot"`
	LogsBloom        hexutil.Bytes   `json:"logsBloom"`
	Difficulty       *hexutil.Big    `json:"difficulty"`
	TotalDifficulty  *hexutil.Big    `json:"totalDifficulty"`
	Size             hexutil.Uint64  `json:"size"`
	BaseFeePerGas    *hexutil.Big    `json:"baseFeePerGas"`
	ExtraData        hexutil.Bytes   `json:"extraData"`
}

// Transaction represents an Ethereum transaction in the GraphQL schema, following EIP-1767.
type Transaction struct {
	Hash                 types.Hash      `json:"hash"`
	Nonce                hexutil.Uint64  `json:"nonce"`
	From                 types.Address   `json:"from"`
	To                   *types.Address  `json:"to"`
	Value                *hexutil.Big    `json:"value"`
	Gas                  hexutil.Uint64  `json:"gas"`
	GasPrice             *hexutil.Big    `json:"gasPrice"`
	Input                hexutil.Bytes   `json:"input"`
	BlockNumber          *hexutil.Uint64 `json:"blockNumber"`
	BlockHash            *types.Hash     `json:"blockHash"`
	TransactionIndex     *hexutil.Uint64 `json:"transactionIndex"`
	Type                 hexutil.Uint64  `json:"type"`
	MaxFeePerGas         *hexutil.Big    `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas *hexutil.Big    `json:"maxPriorityFeePerGas,omitempty"`
}

// Log represents an Ethereum log entry in the GraphQL schema, following EIP-1767.
type Log struct {
	Address          types.Address  `json:"address"`
	Topics           []types.Hash   `json:"topics"`
	Data             hexutil.Bytes  `json:"data"`
	BlockNumber      hexutil.Uint64 `json:"blockNumber"`
	BlockHash        types.Hash     `json:"blockHash"`
	TransactionHash  types.Hash     `json:"transactionHash"`
	TransactionIndex hexutil.Uint64 `json:"transactionIndex"`
	LogIndex         hexutil.Uint64 `json:"logIndex"`
	Removed          bool           `json:"removed"`
}

// Account represents an Ethereum account in the GraphQL schema, following EIP-1767.
type Account struct {
	Address          types.Address  `json:"address"`
	Balance          *hexutil.Big   `json:"balance"`
	Nonce            hexutil.Uint64 `json:"nonce"`
	Code             hexutil.Bytes  `json:"code"`
	TransactionCount hexutil.Uint64 `json:"transactionCount"`
}

// BlockArgs contains the arguments for block queries.
type BlockArgs struct {
	Number *hexutil.Uint64 `json:"number"`
	Hash   *types.Hash     `json:"hash"`
}

// LogFilter contains the arguments for log queries.
type LogFilter struct {
	FromBlock *hexutil.Uint64 `json:"fromBlock"`
	ToBlock   *hexutil.Uint64 `json:"toBlock"`
	Addresses []types.Address `json:"addresses"`
	Topics    [][]types.Hash  `json:"topics"`
}

// GraphQLRequest represents a GraphQL HTTP request body.
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a GraphQL HTTP response body.
type GraphQLResponse struct {
	Data   interface{}      `json:"data,omitempty"`
	Errors []*GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error in the response.
type GraphQLError struct {
	Message string `json:"message"`
}
