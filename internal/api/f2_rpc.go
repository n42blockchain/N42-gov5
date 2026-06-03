// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// f2_rpc.go — serve position-addressed tx RPCs from an F2 (no-signature) ledger
// store when the full body is unavailable (an EIP-4444 Full node that keeps only
// recent full bodies, or an F2-only node). The returned RPCTransaction carries
// the ledger fields (from/to/value/nonce/gas/input/type/accessList) but leaves
// hash/v/r/s empty — F2 cannot reproduce signatures or the canonical tx hash
// (those are served via the MPHF hash index / F1.5). See
// docs/ethel/body-compression-design.md §7.

package api

import (
	"math/big"

	"github.com/holiman/uint256"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
)

func bigOrZero(v *uint256.Int) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	return v.ToBig()
}

// newRPCTransactionFromF2 builds an RPCTransaction from an F2 ledger tx. Hash,
// V, R, S are deliberately left zero/nil (F2 drops signatures and cannot
// recompute the canonical hash).
func newRPCTransactionFromF2(ftx *bodyf2.F2Tx, blockHash types.Hash, blockNumber, index uint64) *RPCTransaction {
	r := &RPCTransaction{
		Type:  hexutil.Uint64(ftx.Type),
		From:  *avmtypes.FromastAddress(&ftx.From),
		Gas:   hexutil.Uint64(ftx.Gas),
		Input: hexutil.Bytes(ftx.Data),
		Nonce: hexutil.Uint64(ftx.Nonce),
		To:    avmtypes.FromastAddress(ftx.To),
		Value: (*hexutil.Big)(bigOrZero(ftx.Value)),
	}
	if ftx.GasFeeCap != nil {
		r.GasPrice = (*hexutil.Big)(ftx.GasFeeCap.ToBig())
	}
	if ftx.Type >= 2 {
		if ftx.GasFeeCap != nil {
			r.GasFeeCap = (*hexutil.Big)(ftx.GasFeeCap.ToBig())
		}
		if ftx.GasTipCap != nil {
			r.GasTipCap = (*hexutil.Big)(ftx.GasTipCap.ToBig())
		}
	}
	if len(ftx.Access) > 0 {
		var tal transaction.AccessList
		for _, at := range ftx.Access {
			tt := transaction.AccessTuple{Address: at.Address}
			for _, k := range at.StorageKeys {
				var h types.Hash
				copy(h[:], k[:])
				tt.StorageKeys = append(tt.StorageKeys, h)
			}
			tal = append(tal, tt)
		}
		al := avmtypes.FromastAccessList(tal)
		r.Accesses = &al
	}
	if blockHash != (types.Hash{}) {
		h := avmtypes.FromastHash(blockHash)
		r.BlockHash = &h
		r.BlockNumber = (*hexutil.Big)(new(big.Int).SetUint64(blockNumber))
		r.TransactionIndex = (*hexutil.Uint64)(&index)
	}
	return r
}

// f2TxByNumberAndIndex serves a tx by (blockNumber, index) from the F2 ledger
// store, or nil if no F2 store is configured / the block or index is absent.
// blockHash is best-effort (zero if the header hash is unknown here).
func f2TxByNumberAndIndex(blockNumber uint64, index uint64, blockHash types.Hash) *RPCTransaction {
	if ethel.F2Reader() == nil {
		return nil
	}
	fb, err := ethel.F2LedgerBody(blockNumber)
	if err != nil || index >= uint64(len(fb.Txs)) {
		return nil
	}
	return newRPCTransactionFromF2(&fb.Txs[index], blockHash, blockNumber, index)
}

// f2TxByHash resolves a tx hash via the MPHF index (F1.5) and serves its F2
// ledger view. The response Hash is the queried hash (echo — the caller
// supplied it), so getTransactionByHash returns the right hash even though F2
// does not store it. Returns nil if no index is configured or the hash is absent.
func (s *TransactionAPI) f2TxByHash(h types.Hash) *RPCTransaction {
	block, index, ok := ethel.F2TxLocByHash(h)
	if !ok {
		return nil
	}
	fb, err := ethel.F2LedgerBody(block)
	if err != nil || index >= uint64(len(fb.Txs)) {
		return nil
	}
	var bh types.Hash
	if hdr := s.api.BlockChain().GetHeaderByNumber(uint256.NewInt(block)); hdr != nil {
		bh = hdr.Hash()
	}
	r := newRPCTransactionFromF2(&fb.Txs[index], bh, block, index)
	r.Hash = avmtypes.FromastHash(h) // echo the queried canonical hash
	return r
}
