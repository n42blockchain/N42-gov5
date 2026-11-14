// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Block assembly and wire transaction/withdrawal decoding.

//go:build n42el

package eldevp2p

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/devp2p"
)

// --- block assembly ------------------------------------------------------

// assembleBlock decodes the wire transactions + withdrawals and stitches
// them onto the wire header to produce a *block.Block ready for
// EngineStateAdapter.ExecutePayloadFromWire. Withdrawals are returned
// separately because the N42 Body type doesn't carry them.
func assembleBlock(hdr *block.Header, body devp2p.BlockBody) (*block.Block, []*api.Withdrawal, error) {
	if hdr == nil {
		return nil, nil, errors.New("nil header")
	}
	txs, err := decodeTxs(body.Transactions)
	if err != nil {
		return nil, nil, fmt.Errorf("decode txs: %w", err)
	}
	withdrawals, err := decodeWithdrawals(body.Withdrawals)
	if err != nil {
		return nil, nil, fmt.Errorf("decode withdrawals: %w", err)
	}
	ib := block.NewBlock(hdr, txs)
	blk, ok := ib.(*block.Block)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected block type %T", ib)
	}
	return blk, withdrawals, nil
}

// rawTxList feeds raw eth-canonical tx bytes (type||payload for typed, or the
// legacy RLP list) to DeriveShaErigon to reproduce a header's transactionsRoot.
type rawTxList [][]byte

func (l rawTxList) Len() int                           { return len(l) }
func (l rawTxList) EncodeIndex(i int, w *bytes.Buffer) { w.Write(l[i]) }

// unwrapWireTxs strips the outer RLP byte-string wrapper that typed txs carry
// inside a block-bodies message, yielding the canonical `type||payload` bytes
// (legacy txs, which are RLP lists, pass through unchanged).
func unwrapWireTxs(raw []rlp.RawValue) [][]byte {
	out := make([][]byte, 0, len(raw))
	for _, r := range raw {
		data := []byte(r)
		if len(data) > 0 && data[0] >= 0x80 && data[0] < 0xc0 {
			var inner []byte
			if err := rlp.DecodeBytes(data, &inner); err == nil {
				data = inner
			}
		}
		out = append(out, data)
	}
	return out
}

func decodeTxs(raw []rlp.RawValue) ([]*transaction.Transaction, error) {
	out := make([]*transaction.Transaction, 0, len(raw))
	for i, r := range raw {
		if len(r) == 0 {
			continue
		}
		data := []byte(r)
		// Typed transactions (EIP-2718+) arrive as RLP strings wrapping
		// `type || rlp(payload)`. Legacy txs are bare RLP lists. Mirror
		// internal/ethel.DecodeGethBody's unwrap step so both forms feed
		// the same transaction.DecodeEthereumTransaction path.
		if data[0] >= 0x80 && data[0] < 0xc0 {
			var inner []byte
			if err := rlp.DecodeBytes(data, &inner); err != nil {
				return nil, fmt.Errorf("tx %d: unwrap typed: %w", i, err)
			}
			data = inner
		}
		tx, err := transaction.DecodeEthereumTransaction(data)
		if err != nil {
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		out = append(out, tx)
	}
	return out, nil
}

// wireWithdrawal mirrors the eth/69 wire layout for withdrawals
// (EIP-4895): [index, validatorIndex, address, amount].
type wireWithdrawal struct {
	Index          uint64
	ValidatorIndex uint64
	Address        types.Address
	Amount         uint64
}

func decodeWithdrawals(raw []rlp.RawValue) ([]*api.Withdrawal, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]*api.Withdrawal, len(raw))
	for i, r := range raw {
		var w wireWithdrawal
		if err := rlp.DecodeBytes(r, &w); err != nil {
			return nil, fmt.Errorf("withdrawal %d: %w", i, err)
		}
		out[i] = &api.Withdrawal{
			Index:          hexutil.Uint64(w.Index),
			ValidatorIndex: hexutil.Uint64(w.ValidatorIndex),
			Address:        w.Address,
			Amount:         hexutil.Uint64(w.Amount),
		}
	}
	return out, nil
}
