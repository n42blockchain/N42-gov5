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

package block

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
)

type BlockNonce [8]byte

func EncodeNonce(i uint64) BlockNonce {
	var n BlockNonce
	binary.BigEndian.PutUint64(n[:], i)
	return n
}

func (n BlockNonce) Uint64() uint64 {
	return binary.BigEndian.Uint64(n[:])
}

func (n BlockNonce) MarshalText() ([]byte, error) {
	return hexutil.Bytes(n[:]).MarshalText()
}

func (n *BlockNonce) UnmarshalText(input []byte) error {
	return hexutil.UnmarshalFixedText("BlockNonce", input, n[:])
}

type Header struct {
	ParentHash  types.Hash    `json:"parentHash"       gencodec:"required"`
	Coinbase    types.Address `json:"miner"`
	Root        types.Hash    `json:"stateRoot"        gencodec:"required"`
	TxHash      types.Hash    `json:"transactionsRoot" gencodec:"required"`
	ReceiptHash types.Hash    `json:"receiptsRoot"     gencodec:"required"`
	Bloom       Bloom         `json:"logsBloom"        gencodec:"required"`
	Difficulty  *uint256.Int  `json:"difficulty"       gencodec:"required"`
	Number      *uint256.Int  `json:"number"           gencodec:"required"`
	GasLimit    uint64        `json:"gasLimit"         gencodec:"required"`
	GasUsed     uint64        `json:"gasUsed"          gencodec:"required"`
	Time        uint64        `json:"timestamp"        gencodec:"required"`
	MixDigest   types.Hash    `json:"mixHash"`
	Nonce       BlockNonce    `json:"nonce"`
	Extra       []byte        `json:"extraData"        gencodec:"required"`

	BaseFee *uint256.Int `json:"baseFeePerGas" rlp:"optional"`

	// EIP-4844 blob gas fields (Cancun fork)
	BlobGasUsed   uint64 `json:"blobGasUsed,omitempty"`
	ExcessBlobGas uint64 `json:"excessBlobGas,omitempty"`

	// LtHashRoot is the BLAKE3 summary of the 2048-byte LtHash state digest.
	// Zero before the LtHash fork activation timestamp.
	LtHashRoot types.Hash `json:"ltHashRoot,omitempty"`

	hash atomic.Value

	Signature types.Signature `json:"signature"`
}

func (h *Header) Number64() *uint256.Int {
	return h.Number
}

func (h *Header) StateRoot() types.Hash {
	return h.Root
}

func (h *Header) BaseFee64() *uint256.Int {
	return h.BaseFee
}

// headerHashFieldsLegacy contains the original Header fields for hash computation.
// Used for blocks before the Shanghai fork to maintain backward compatibility.
type headerHashFieldsLegacy struct {
	ParentHash  types.Hash      `json:"parentHash"`
	Coinbase    types.Address   `json:"miner"`
	Root        types.Hash      `json:"stateRoot"`
	TxHash      types.Hash      `json:"transactionsRoot"`
	ReceiptHash types.Hash      `json:"receiptsRoot"`
	Bloom       Bloom           `json:"logsBloom"`
	Difficulty  *uint256.Int    `json:"difficulty"`
	Number      *uint256.Int    `json:"number"`
	GasLimit    uint64          `json:"gasLimit"`
	GasUsed     uint64          `json:"gasUsed"`
	Time        uint64          `json:"timestamp"`
	MixDigest   types.Hash      `json:"mixHash"`
	Nonce       BlockNonce      `json:"nonce"`
	Extra       []byte          `json:"extraData"`
	BaseFee     *uint256.Int    `json:"baseFeePerGas"`
	Signature   types.Signature `json:"signature"`
}

func (h *Header) Hash() types.Hash {
	if hash := h.hash.Load(); hash != nil {
		return hash.(types.Hash)
	}

	var buf []byte
	var err error

	if IsLegacyHeader(h) {
		buf, err = h.legacyHashBytes()
	} else {
		buf, err = h.v2HashBytes()
	}
	if err != nil {
		return types.Hash{}
	}

	hash := types.BytesHash(buf)
	h.hash.Store(hash)
	return hash
}

// IsLegacyHeader returns true for pre-Shanghai headers that use legacy hash.
// Shanghai activation can be block-based or timestamp-based; we check both
// BlobGasUsed (Cancun+ indicator) and LtHashRoot as proxies. If none of the
// new fields are populated AND block number is below the well-known mainnet
// Shanghai activation, use legacy hash.
func IsLegacyHeader(h *Header) bool {
	// If any v2 field is non-zero, this is definitely a v2 header.
	if h.BlobGasUsed != 0 || h.ExcessBlobGas != 0 || (h.LtHashRoot != types.Hash{}) {
		return false
	}
	return true
}

func (h *Header) legacyHashBytes() ([]byte, error) {
	hf := headerHashFieldsLegacy{
		ParentHash:  h.ParentHash,
		Coinbase:    h.Coinbase,
		Root:        h.Root,
		TxHash:      h.TxHash,
		ReceiptHash: h.ReceiptHash,
		Bloom:       h.Bloom,
		Difficulty:  h.Difficulty,
		Number:      h.Number,
		GasLimit:    h.GasLimit,
		GasUsed:     h.GasUsed,
		Time:        h.Time,
		MixDigest:   h.MixDigest,
		Nonce:       h.Nonce,
		Extra:       h.Extra,
		BaseFee:     h.BaseFee,
		Signature:   h.Signature,
	}
	if hf.BaseFee == nil {
		hf.BaseFee = uint256.NewInt(0)
	}
	if hf.Difficulty == nil {
		hf.Difficulty = uint256.NewInt(0)
	}
	return json.Marshal(&hf)
}

func (h *Header) v2HashBytes() ([]byte, error) {
	// V2: marshal ALL fields including new ones.
	cpy := *h
	if cpy.BaseFee == nil {
		cpy.BaseFee = uint256.NewInt(0)
	}
	if cpy.Difficulty == nil {
		cpy.Difficulty = uint256.NewInt(0)
	}
	type headerForHash Header // alias to prevent recursive MarshalJSON
	return json.Marshal((*headerForHash)(&cpy))
}

func (h *Header) ToProtoMessage() proto.Message {
	return &types_pb.Header{
		ParentHash:    utils.ConvertHashToH256(h.ParentHash),
		Coinbase:      utils.ConvertAddressToH160(h.Coinbase),
		Root:          utils.ConvertHashToH256(h.Root),
		TxHash:        utils.ConvertHashToH256(h.TxHash),
		ReceiptHash:   utils.ConvertHashToH256(h.ReceiptHash),
		Difficulty:    utils.ConvertUint256IntToH256(h.Difficulty),
		Number:        utils.ConvertUint256IntToH256(h.Number),
		GasLimit:      h.GasLimit,
		GasUsed:       h.GasUsed,
		Time:          h.Time,
		Nonce:         h.Nonce.Uint64(),
		BaseFee:       utils.ConvertUint256IntToH256(h.BaseFee),
		Extra:         h.Extra,
		Signature:     utils.ConvertSignatureToH768(h.Signature),
		Bloom:         utils.ConvertBytesToH2048(h.Bloom.Bytes()),
		MixDigest:     utils.ConvertHashToH256(h.MixDigest),
		BlobGasUsed:   h.BlobGasUsed,
		ExcessBlobGas: h.ExcessBlobGas,
		LtHashRoot:    utils.ConvertHashToH256(h.LtHashRoot),
	}
}

func (h *Header) FromProtoMessage(message proto.Message) error {
	pbHeader, ok := message.(*types_pb.Header)
	if !ok {
		return fmt.Errorf("type conversion failure")
	}

	h.ParentHash = utils.ConvertH256ToHash(pbHeader.ParentHash)
	h.Coinbase = utils.ConvertH160toAddress(pbHeader.Coinbase)
	h.Root = utils.ConvertH256ToHash(pbHeader.Root)
	h.TxHash = utils.ConvertH256ToHash(pbHeader.TxHash)
	h.ReceiptHash = utils.ConvertH256ToHash(pbHeader.ReceiptHash)
	h.Difficulty = utils.ConvertH256ToUint256Int(pbHeader.Difficulty)
	h.Number = utils.ConvertH256ToUint256Int(pbHeader.Number)
	h.GasLimit = pbHeader.GasLimit
	h.GasUsed = pbHeader.GasUsed
	h.Time = pbHeader.Time
	h.Nonce = EncodeNonce(pbHeader.Nonce)
	h.BaseFee = utils.ConvertH256ToUint256Int(pbHeader.BaseFee)
	h.Extra = pbHeader.Extra
	h.Signature = utils.ConvertH768ToSignature(pbHeader.Signature)
	h.Bloom = utils.ConvertH2048ToBloom(pbHeader.Bloom)
	h.MixDigest = utils.ConvertH256ToHash(pbHeader.MixDigest)
	h.BlobGasUsed = pbHeader.BlobGasUsed
	h.ExcessBlobGas = pbHeader.ExcessBlobGas
	h.LtHashRoot = utils.ConvertH256ToHash(pbHeader.LtHashRoot)
	return nil
}

func (h *Header) Marshal() ([]byte, error) {
	return proto.Marshal(h.ToProtoMessage())
}

func (h *Header) Unmarshal(data []byte) error {
	var pbHeader types_pb.Header
	if err := proto.Unmarshal(data, &pbHeader); err != nil {
		return err
	}
	return h.FromProtoMessage(&pbHeader)
}

func CopyHeader(h *Header) *Header {
	cpy := *h

	cpy.Difficulty = uint256.NewInt(0)
	if h.Difficulty != nil {
		cpy.Difficulty.Set(h.Difficulty)
	}

	cpy.Number = uint256.NewInt(0)
	if h.Number != nil {
		cpy.Number.Set(h.Number)
	}

	if h.BaseFee != nil {
		cpy.BaseFee = new(uint256.Int).Set(h.BaseFee)
	}

	if len(h.Extra) > 0 {
		cpy.Extra = make([]byte, len(h.Extra))
		copy(cpy.Extra, h.Extra)
	}
	return &cpy
}

func CopyReward(rewards []*Reward) []*Reward {
	cpyReward := make([]*Reward, len(rewards))
	for i, reward := range rewards {
		cpyReward[i] = &Reward{
			Address: reward.Address,
			Amount:  new(uint256.Int).Set(reward.Amount),
		}
	}
	return cpyReward
}
