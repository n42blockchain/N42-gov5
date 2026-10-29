// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// block_size.go — EIP-7934 RLP block size cap for eth-el imports.
//
// geth enforces the cap in BlockValidator.ValidateBody on Block.Size(): the
// length of rlp([header, transactions, uncles, withdrawals?]), with typed
// transactions as RLP strings wrapping type||payload and withdrawals present
// whenever the header commits to them (Shanghai on). The helpers here
// measure exactly that without materialising the encoding of the body.

package ethel

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// RLPBlockSize returns the size of an eth-el block's RLP encoding as geth's
// Block.Size measures it. The body lists are given by the summed encoded size
// of their items; the withdrawals list is counted only when the header
// carries a withdrawals root.
func RLPBlockSize(header *block.Header, txsContent, unclesContent, withdrawalsContent uint64) (uint64, error) {
	enc, err := rlp.EncodeToBytes(header)
	if err != nil {
		return 0, fmt.Errorf("encode header: %w", err)
	}
	content := uint64(len(enc)) + rlp.ListSize(txsContent) + rlp.ListSize(unclesContent)
	if header.WithdrawalsHash != nil {
		content += rlp.ListSize(withdrawalsContent)
	}
	return rlp.ListSize(content), nil
}

// TxBlockItemSize returns the size a transaction takes inside a block's
// transaction list: a legacy transaction is embedded as its RLP list, a typed
// one as an RLP string wrapping type||payload (a string header has the same
// size as a list header for the same content length).
func TxBlockItemSize(enc []byte) uint64 {
	if len(enc) > 0 && enc[0] >= 0xc0 {
		return uint64(len(enc))
	}
	return rlp.ListSize(uint64(len(enc)))
}

func withdrawalItemSize(w *Withdrawal) uint64 {
	const addressSize = 21 // 0x94 || 20 bytes
	return rlp.ListSize(uint64(rlp.IntSize(w.Index) + rlp.IntSize(w.Validator) + addressSize + rlp.IntSize(w.Amount)))
}

// BlockRLPSize measures a decoded eth-el block as geth's Block.Size does.
func BlockRLPSize(header *block.Header, txs []*transaction.Transaction, uncles []block.IHeader, withdrawals []*Withdrawal) (uint64, error) {
	var txsContent, unclesContent, withdrawalsContent uint64
	for i, tx := range txs {
		enc, err := tx.EthEncoded()
		if err != nil {
			return 0, fmt.Errorf("encode tx %d: %w", i, err)
		}
		txsContent += TxBlockItemSize(enc)
	}
	for i, u := range uncles {
		enc, err := rlp.EncodeToBytes(u)
		if err != nil {
			return 0, fmt.Errorf("encode uncle %d: %w", i, err)
		}
		unclesContent += uint64(len(enc))
	}
	for _, w := range withdrawals {
		if w != nil {
			withdrawalsContent += withdrawalItemSize(w)
		}
	}
	return RLPBlockSize(header, txsContent, unclesContent, withdrawalsContent)
}

// validateBlockRLPSize enforces EIP-7934 on a decoded eth-el block from Osaka.
func validateBlockRLPSize(chainCfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction, uncles []block.IHeader, withdrawals []*Withdrawal) error {
	if chainCfg == nil || !chainCfg.IsOsaka(header.Time) {
		return nil
	}
	size, err := BlockRLPSize(header, txs, uncles, withdrawals)
	if err != nil {
		return err
	}
	if err := vm2.ValidateRLPBlockSize(size, true); err != nil {
		return fmt.Errorf("%w: %d bytes (max %d)", err, size, vm2.MaxRLPBlockSizeFusaka)
	}
	return nil
}
