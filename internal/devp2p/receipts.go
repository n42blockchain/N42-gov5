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

package devp2p

import (
	"fmt"
	"math/big"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

// GetReceipts serving limits, as geth's: at most maxReceiptsServe blocks per
// response, and no further block once the response passes
// softResponseLimit bytes.
const (
	maxReceiptsServe  = 1024
	softResponseLimit = 2 * 1024 * 1024
)

// receiptServer is the optional capability a BlockProvider implements to
// serve GetReceipts (0x0f): the receipts of a canonical block with each
// receipt's transaction type set, or nil when it does not hold them.
type receiptServer interface {
	GetReceiptsByHash(hash types.Hash) (n42block.Receipts, error)
}

// getReceiptsPacket is the eth/68-69 GetReceipts (0x0f) request.
type getReceiptsPacket struct {
	RequestID uint64
	Hashes    []types.Hash
}

// wireReceiptLog is a log as the receipt encodings carry it.
type wireReceiptLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

func wireReceiptLogs(logs []*n42block.Log) []wireReceiptLog {
	out := make([]wireReceiptLog, len(logs))
	for i, l := range logs {
		out[i] = wireReceiptLog{Address: l.Address, Topics: l.Topics, Data: l.Data}
	}
	return out
}

// postStateOrStatus is the receipt's pre-Byzantium post-state root, or its
// status as a minimal big-endian integer (success 0x01, failure empty).
func postStateOrStatus(r *n42block.Receipt) []byte {
	if len(r.PostState) > 0 {
		return r.PostState
	}
	return new(big.Int).SetUint64(r.Status).Bytes()
}

// encodeReceipt68 returns the eth/68 network form of a receipt: its consensus
// encoding, with the logs bloom, a typed receipt as a string holding
// type || payload.
func encodeReceipt68(r *n42block.Receipt) (rlp.RawValue, error) {
	var bloom n42block.Bloom
	copy(bloom[:], n42block.LogsBloom(r.Logs))
	payload, err := rlp.EncodeToBytes(&struct {
		PostStateOrStatus []byte
		CumulativeGasUsed uint64
		Bloom             n42block.Bloom
		Logs              []wireReceiptLog
	}{postStateOrStatus(r), r.CumulativeGasUsed, bloom, wireReceiptLogs(r.Logs)})
	if err != nil {
		return nil, err
	}
	if r.Type == 0 {
		return payload, nil
	}
	return rlp.EncodeToBytes(append([]byte{r.Type}, payload...))
}

// encodeReceipt69 returns the eth/69 network form of a receipt: the list
// [tx-type, post-state-or-status, cumulative-gas, logs], without the bloom.
func encodeReceipt69(r *n42block.Receipt) (rlp.RawValue, error) {
	return rlp.EncodeToBytes(&struct {
		TxType            byte
		PostStateOrStatus []byte
		CumulativeGasUsed uint64
		Logs              []wireReceiptLog
	}{r.Type, postStateOrStatus(r), r.CumulativeGasUsed, wireReceiptLogs(r.Logs)})
}

// handleGetReceipts serves the receipts of the requested blocks in the
// negotiated version's form, in request order, stopping at the first block
// whose receipts the provider does not hold.
func (h *EthHandler) handleGetReceipts(rw gethp2p.MsgReadWriter, msg gethp2p.Msg, version uint) error {
	var req getReceiptsPacket
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("decode GetReceipts: %w", err)
	}
	resp := blockReceiptsPacket{RequestID: req.RequestID, Receipts: [][]rlp.RawValue{}}
	if srv, ok := h.provider.(receiptServer); ok {
		encode := encodeReceipt68
		if version >= 69 {
			encode = encodeReceipt69
		}
		size := 0
		for _, hash := range req.Hashes {
			if len(resp.Receipts) >= maxReceiptsServe || size >= softResponseLimit {
				break
			}
			receipts, err := srv.GetReceiptsByHash(hash)
			if err != nil {
				return err
			}
			if receipts == nil {
				break
			}
			list := make([]rlp.RawValue, len(receipts))
			for i, r := range receipts {
				if list[i], err = encode(r); err != nil {
					return fmt.Errorf("encode receipt %d of %x: %w", i, hash, err)
				}
				size += len(list[i])
			}
			resp.Receipts = append(resp.Receipts, list)
		}
	}
	return gethp2p.Send(rw, eth69.ReceiptsMsg, &resp)
}
