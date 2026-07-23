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
// Block and transaction JSON marshalling helpers for RPC responses.
// RPCMarshalBlock formats a block.IBlock into the canonical eth_getBlock*
// map representation, optionally inlining transactions in either hash-only
// or full-object form, and honours a blockHashOverride for engine API
// payload responses where the canonical hash differs from the computed one.

package api

import (
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/params"
)

func RPCMarshalBlock(block block.IBlock, chain common.IBlockChain, cfg *params.ChainConfig, inclTx bool, fullTx bool, blockHashOverride *types.Hash) (map[string]interface{}, error) {
	fields := RPCMarshalHeader(block.Header(), cfg)
	if blockHashOverride != nil {
		fields["hash"] = avmtypes.FromastHash(*blockHashOverride)
	}
	size, err := rpcBlockRLPSize(block, cfg)
	if err != nil {
		return nil, err
	}
	fields["size"] = hexutil.Uint64(size)

	if inclTx {
		num := uint256ToUint64OrZero(block.Number64())
		txs := block.Transactions()
		var transactions []interface{}

		// F2 ledger fallback: the full body is unavailable (an EIP-4444 Full
		// node past its window, or an F2-only node) but the ledger view is in
		// the F2 store. fullTx serves the ledger objects; a hash-only listing
		// needs canonical hashes from the optional tx-hash sidecar (§7), so it
		// is only served when that sidecar is configured.
		if len(txs) == 0 && ethel.F2Reader() != nil && (fullTx || ethel.F2Hashes() != nil) {
			if fb, ferr := ethel.F2LedgerBody(num); ferr == nil && len(fb.Txs) > 0 {
				bh := block.Hash()
				if blockHashOverride != nil {
					bh = *blockHashOverride
				}
				hashes, _ := ethel.F2BlockHashes(num) // optional sidecar; nil if absent
				transactions = make([]interface{}, len(fb.Txs))
				for i := range fb.Txs {
					var th types.Hash
					if i < len(hashes) {
						copy(th[:], hashes[i][:])
					}
					if fullTx {
						rt := newRPCTransactionFromF2(&fb.Txs[i], bh, num, uint64(i))
						if th != (types.Hash{}) {
							rt.Hash = avmtypes.FromastHash(th)
						}
						transactions[i] = rt
					} else {
						transactions[i] = avmtypes.FromastHash(th)
					}
				}
			}
		}

		if transactions == nil {
			formatTx := func(tx *transaction.Transaction) (interface{}, error) {
				hash := tx.Hash()
				return avmtypes.FromastHash(hash), nil
			}
			if fullTx {
				formatTx = func(tx *transaction.Transaction) (interface{}, error) {
					hash := tx.Hash()
					return newRPCTransactionFromBlockHash(block, hash, cfg, blockHashOverride), nil
				}
			}
			transactions = make([]interface{}, len(txs))
			var err error
			for i, tx := range txs {
				if transactions[i], err = formatTx(tx); err != nil {
					return nil, err
				}
			}
		}
		fields["transactions"] = transactions

		if !isHotStuffChain(cfg) {
			// Legacy N42 RPC extensions are intentionally absent from HotStuff
			// responses. HotStuff exposes the Ethereum-compatible block schema,
			// which is also what the Rust/reth participant serves.
			body := block.Body()
			var verifiers []interface{}
			if body != nil {
				verifiers = make([]interface{}, len(body.Verifier()))
				for i, verifier := range body.Verifier() {
					verifiers[i] = verifier
				}
			}
			fields["verifier"] = verifiers

			type RPCReward struct {
				Address types.Address
				Amount  *uint256.Int
			}
			var rewards []*RPCReward
			if body != nil {
				rewards = make([]*RPCReward, len(body.Reward()))
				for i, reward := range body.Reward() {
					rewards[i] = &RPCReward{
						reward.Address,
						reward.Amount,
					}
				}
			}
			fields["rewards"] = rewards
		}
	}

	if !isHotStuffChain(cfg) {
		td := chain.GetTd(block.Hash(), block.Number64())
		if td == nil {
			td = new(uint256.Int)
		}
		fields["totalDifficulty"] = (*hexutil.Big)(td.ToBig())
	}
	// POA
	uncleHashes := make([]types.Hash, 0)
	fields["uncles"] = uncleHashes

	return fields, nil
}

func isHotStuffChain(cfg *params.ChainConfig) bool {
	return cfg != nil && (cfg.Consensus == params.HotStuffConsensus || cfg.HotStuff != nil)
}

// rpcBlockRLPSize returns the canonical Ethereum block-body encoding length
// expected by eth_getBlock*. Header.Size reports approximate in-memory cache
// usage and must never be exposed as the JSON-RPC block size.
func rpcBlockRLPSize(blk block.IBlock, cfg *params.ChainConfig) (uint64, error) {
	rawTxs := make([]hexutil.Bytes, len(blk.Transactions()))
	for i, tx := range blk.Transactions() {
		encoded, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			return 0, err
		}
		rawTxs[i] = encoded
	}
	return executionPayloadBlockRLPSize(blk, rawTxs, cfg, enginePayloadHashOptions{})
}

// newRPCTransactionFromBlockHash returns a transaction that will serialize to the RPC representation.
func newRPCTransactionFromBlockHash(b block.IBlock, findHash types.Hash, cfg *params.ChainConfig, blockHashOverride *types.Hash) *RPCTransaction {
	for idx, tx := range b.Transactions() {
		hash := tx.Hash()
		if hash == findHash {
			return newRPCTransactionFromBlockIndex(b, uint64(idx), cfg, blockHashOverride)
		}
	}
	return nil
}

// newRPCTransactionFromBlockIndex returns a transaction that will serialize to the RPC representation.
func newRPCTransactionFromBlockIndex(b block.IBlock, index uint64, cfg *params.ChainConfig, blockHashOverride *types.Hash) *RPCTransaction {
	txs := b.Transactions()
	if index >= uint64(len(txs)) {
		return nil
	}
	blockHash := rpcBlockHash(b, cfg)
	if blockHashOverride != nil {
		blockHash = *blockHashOverride
	}
	return newRPCTransaction(txs[index], blockHash, uint256ToUint64OrZero(b.Number64()), index, big.NewInt(baseFee))
}

// RPCMarshalHeader converts the given header to the RPC output .
func RPCMarshalHeader(head block.IHeader, cfg *params.ChainConfig) map[string]interface{} {
	header, ok := head.(*block.Header)
	if !ok || header == nil {
		return nil
	}
	ethHeader := avmtypes.FromN42Header(head)
	uncleHash := hash.EmptyUncleHash
	if cfg != nil && cfg.HotStuff != nil {
		// H2's compact header codec intentionally preserves a zero ommers hash.
		// Returning Ethereum's synthetic EmptyUncleHash here made
		// eth_getBlock* disagree with the byte-identical Rust client even though
		// both addressed the same native header hash.
		uncleHash = header.UncleHash
	}

	result := map[string]interface{}{
		"number":           (*hexutil.Big)(uint256ToBigOrZero(head.Number64())),
		"hash":             avmtypes.FromastHash(rpcHeaderHash(head, cfg)),
		"parentHash":       avmtypes.FromastHash(header.ParentHash),
		"nonce":            header.Nonce,
		"mixHash":          avmtypes.FromastHash(header.MixDigest),
		"sha3Uncles":       avmtypes.FromastHash(uncleHash),
		"miner":            avmtypes.FromastAddress(&header.Coinbase),
		"difficulty":       (*hexutil.Big)(uint256ToBigOrZero(header.Difficulty)),
		"extraData":        hexutil.Bytes(header.Extra),
		"size":             hexutil.Uint64(ethHeader.Size()),
		"gasLimit":         hexutil.Uint64(header.GasLimit),
		"gasUsed":          hexutil.Uint64(header.GasUsed),
		"timestamp":        hexutil.Uint64(header.Time),
		"transactionsRoot": avmtypes.FromastHash(header.TxHash),
		"receiptsRoot":     avmtypes.FromastHash(header.ReceiptHash),
		"logsBloom":        ethHeader.Bloom,
		"stateRoot":        avmtypes.FromastHash(header.Root),
	}

	if header.BaseFee != nil {
		result["baseFeePerGas"] = (*hexutil.Big)(header.BaseFee.ToBig())
	}

	number := uint64FromUint256OrZero(header.Number)
	if cfg != nil && cfg.IsShanghaiAt(number, header.Time) {
		withdrawalsHash := withdrawalsRoot(nil)
		if header.WithdrawalsHash != nil {
			withdrawalsHash = *header.WithdrawalsHash
		}
		result["withdrawalsRoot"] = avmtypes.FromastHash(withdrawalsHash)
		result["withdrawals"] = []interface{}{}
	}
	if cfg != nil && cfg.IsCancunAt(number, header.Time) {
		if _, ok := result["withdrawalsRoot"]; !ok {
			result["withdrawalsRoot"] = avmtypes.FromastHash(withdrawalsRoot(nil))
		}
		if _, ok := result["withdrawals"]; !ok {
			result["withdrawals"] = []interface{}{}
		}
		var bgu, ebg uint64
		if header.BlobGasUsed != nil {
			bgu = *header.BlobGasUsed
		}
		if header.ExcessBlobGas != nil {
			ebg = *header.ExcessBlobGas
		}
		result["blobGasUsed"] = hexutil.Uint64(bgu)
		result["excessBlobGas"] = hexutil.Uint64(ebg)
		parentBeaconRoot := types.Hash{}
		if header.ParentBeaconRoot != nil {
			parentBeaconRoot = *header.ParentBeaconRoot
		}
		result["parentBeaconBlockRoot"] = avmtypes.FromastHash(parentBeaconRoot)
	}
	if cfg != nil && (cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time)) {
		requestsHash := executionRequestsHash(nil)
		if header.RequestsHash != nil {
			requestsHash = *header.RequestsHash
		}
		result["requestsHash"] = avmtypes.FromastHash(requestsHash)
	}

	return result
}
