// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42 native receipt types and Receipts list codec. Defines the
// ReceiptStatusFailed / ReceiptStatusSuccessful status constants,
// the Marshal / Unmarshal protobuf bridge and EncodeIndex which
// emits the native (Status, CumulativeGasUsed, Logs) tuple via
// storedReceipt + storedLog — intentionally missing Type and
// Bloom, so the derived root is NOT compatible with Ethereum's
// standard receipt trie root.

package block

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/proto/types_pb"
)

const (
	ReceiptStatusFailed     = uint64(0)
	ReceiptStatusSuccessful = uint64(1)
)

type Receipts []*Receipt

// EthereumReceiptRoot computes the canonical Ethereum receipt trie root.
// Ethereum receipts include the logs bloom and, since Byzantium, use a status
// field in place of the pre-Byzantium post-state root. Typed receipts carry
// their EIP-2718 type byte outside the RLP payload. Native N42 receipts use
// Receipts.EncodeIndex instead and deliberately have a different root.
func EthereumReceiptRoot(receipts Receipts, postByzantium bool) types.Hash {
	return hash.DeriveShaErigon(ethereumReceipts{receipts: receipts, postByzantium: postByzantium})
}

type ethereumReceipts struct {
	receipts      Receipts
	postByzantium bool
}

func (rs ethereumReceipts) Len() int { return len(rs.receipts) }

type ethereumReceiptLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

func (rs ethereumReceipts) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs.receipts[i]
	if r.Type > 0 {
		w.WriteByte(r.Type)
	}
	logs := make([]*ethereumReceiptLog, len(r.Logs))
	for j, l := range r.Logs {
		logs[j] = &ethereumReceiptLog{Address: l.Address, Topics: l.Topics, Data: l.Data}
	}
	var bloom Bloom
	copy(bloom[:], LogsBloom(r.Logs))
	if rs.postByzantium {
		rlp.Encode(w, &struct {
			Status            uint64
			CumulativeGasUsed uint64
			Bloom             Bloom
			Logs              []*ethereumReceiptLog
		}{r.Status, r.CumulativeGasUsed, bloom, logs})
		return
	}
	rlp.Encode(w, &struct {
		PostState         []byte
		CumulativeGasUsed uint64
		Bloom             Bloom
		Logs              []*ethereumReceiptLog
	}{r.PostState, r.CumulativeGasUsed, bloom, logs})
}

func (rs *Receipts) Marshal() ([]byte, error) {
	return proto.Marshal(rs.ToProtoMessage())
}

func (rs *Receipts) Unmarshal(data []byte) error {
	// Compact storage format (0xFF marker — never a valid proto first byte,
	// since 0xFF decodes as field 31 / wire type 7 which is illegal).
	if IsCompactReceipts(data) {
		return rs.unmarshalCompact(data)
	}
	pb := new(types_pb.Receipts)
	if err := proto.Unmarshal(data, pb); err != nil {
		return err
	}

	return rs.FromProtoMessage(pb)
}

func (rs Receipts) Len() int { return len(rs) }

// EncodeIndex emits the N42 native receipt encoding (Status,
// CumulativeGasUsed, Logs — no Type, no Bloom). NOT compatible with
// Ethereum's standard receipt trie root.
func (rs Receipts) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs[i]

	logs := make([]*storedLog, len(r.Logs))

	for k, log := range r.Logs {
		logs[k] = &storedLog{
			Address: log.Address,
			Topics:  log.Topics,
			Data:    log.Data,
		}
	}
	data := &storedReceipt{r.Status, r.CumulativeGasUsed, logs}
	rlp.Encode(w, data)
}

func (rs *Receipts) FromProtoMessage(receipts *types_pb.Receipts) error {
	for _, receipt := range receipts.Receipts {
		var rec Receipt
		err := rec.fromProtoMessage(receipt)
		if err == nil {
			*rs = append(*rs, &rec)
		}
	}
	return nil
}

func (rs *Receipts) ToProtoMessage() proto.Message {
	receipts := make([]*types_pb.Receipt, len(*rs))
	for i, receipt := range *rs {
		receipts[i] = receipt.toProtoMessage().(*types_pb.Receipt)
	}
	return &types_pb.Receipts{Receipts: receipts}
}

type Receipt struct {
	Type              uint8  `json:"type,omitempty"`
	PostState         []byte `json:"root"`
	Status            uint64 `json:"status"`
	CumulativeGasUsed uint64 `json:"cumulativeGasUsed" gencodec:"required"`
	Bloom             Bloom  `json:"logsBloom"         gencodec:"required"`
	Logs              []*Log `json:"logs"              gencodec:"required"`

	TxHash          types.Hash    `json:"transactionHash" gencodec:"required"`
	ContractAddress types.Address `json:"contractAddress"`
	GasUsed         uint64        `json:"gasUsed" gencodec:"required"`

	BlockHash        types.Hash   `json:"blockHash,omitempty"`
	BlockNumber      *uint256.Int `json:"blockNumber,omitempty"`
	TransactionIndex uint         `json:"transactionIndex"`
}

func (r *Receipt) Marshal() ([]byte, error) {
	return proto.Marshal(r.toProtoMessage())
}

func (r *Receipt) Unmarshal(data []byte) error {
	var pReceipt types_pb.Receipt
	if err := proto.Unmarshal(data, &pReceipt); err != nil {
		return err
	}
	return r.fromProtoMessage(&pReceipt)
}

func (r *Receipt) toProtoMessage() proto.Message {
	logs := make([]*types_pb.Log, len(r.Logs))
	for i, log := range r.Logs {
		logs[i] = log.ToProtoMessage().(*types_pb.Log)
	}
	return &types_pb.Receipt{
		Type:              uint32(r.Type),
		PostState:         r.PostState,
		Status:            r.Status,
		CumulativeGasUsed: r.CumulativeGasUsed,
		Logs:              logs,
		TxHash:            utils.ConvertHashToH256(r.TxHash),
		ContractAddress:   utils.ConvertAddressToH160(r.ContractAddress),
		GasUsed:           r.GasUsed,
		BlockHash:         utils.ConvertHashToH256(r.BlockHash),
		BlockNumber:       utils.ConvertUint256IntToH256(r.BlockNumber),
		TransactionIndex:  uint64(r.TransactionIndex),
		Bloom:             utils.ConvertBytesToH2048(r.Bloom[:]),
	}
}

func (r *Receipt) fromProtoMessage(message proto.Message) error {
	pReceipt, ok := message.(*types_pb.Receipt)
	if !ok {
		return fmt.Errorf("type conversion failure")
	}

	logs := make([]*Log, 0, len(pReceipt.Logs))
	for _, logMessage := range pReceipt.Logs {
		log := new(Log)
		if err := log.FromProtoMessage(logMessage); err != nil {
			return fmt.Errorf("type conversion failure log %s", err)
		}
		logs = append(logs, log)
	}

	r.Type = uint8(pReceipt.Type)
	r.PostState = pReceipt.PostState
	r.Status = pReceipt.Status
	r.CumulativeGasUsed = pReceipt.CumulativeGasUsed
	r.Bloom = utils.ConvertH2048ToBloom(pReceipt.Bloom)
	r.Logs = logs
	r.TxHash = utils.ConvertH256ToHash(pReceipt.TxHash)
	r.ContractAddress = utils.ConvertH160toAddress(pReceipt.ContractAddress)
	r.GasUsed = pReceipt.GasUsed
	r.BlockHash = utils.ConvertH256ToHash(pReceipt.BlockHash)
	r.BlockNumber = utils.ConvertH256ToUint256Int(pReceipt.BlockNumber)
	r.TransactionIndex = uint(pReceipt.TransactionIndex)

	return nil
}

type storedReceipt struct {
	PostStateOrStatus uint64
	CumulativeGasUsed uint64
	Logs              []*storedLog
}

type storedLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}
