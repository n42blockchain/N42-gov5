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

// Header represents a block header in the N42 blockchain.
// Fields 1-21 are 100% Ethereum Pectra compatible (same RLP/JSON wire format).
// N42 extensions (LtHashRoot, tree roots, BLS signatures) are encoded in Extra.
type Header struct {
	// --- Standard Ethereum fields (1-15, EL genesis) ---
	ParentHash  types.Hash    `json:"parentHash"       gencodec:"required"` // 1
	UncleHash   types.Hash    `json:"sha3Uncles"       gencodec:"required"` // 2  NEW: always emptyUncleHash post-merge
	Coinbase    types.Address `json:"miner"`                                // 3
	Root        types.Hash    `json:"stateRoot"        gencodec:"required"` // 4  MPT root (standard), BMT/JMT in Extra
	TxHash      types.Hash    `json:"transactionsRoot" gencodec:"required"` // 5
	ReceiptHash types.Hash    `json:"receiptsRoot"     gencodec:"required"` // 6
	Bloom       Bloom         `json:"logsBloom"        gencodec:"required"` // 7
	Difficulty  *uint256.Int  `json:"difficulty"       gencodec:"required"` // 8  always 0 post-merge
	Number      *uint256.Int  `json:"number"           gencodec:"required"` // 9
	GasLimit    uint64        `json:"gasLimit"         gencodec:"required"` // 10
	GasUsed     uint64        `json:"gasUsed"          gencodec:"required"` // 11
	Time        uint64        `json:"timestamp"        gencodec:"required"` // 12
	Extra       []byte        `json:"extraData"        gencodec:"required"` // 13 N42: QC+seal+roots+mobileBLS
	MixDigest   types.Hash    `json:"mixHash"`                              // 14 prevRandao post-merge
	Nonce       BlockNonce    `json:"nonce"`                                // 15 always 0 post-merge

	// --- EIP-1559 (London) ---
	BaseFee *uint256.Int `json:"baseFeePerGas" rlp:"optional"` // 16

	// --- EIP-4895 (Shanghai) ---
	WithdrawalsHash *types.Hash `json:"withdrawalsRoot,omitempty"` // 17 NEW

	// --- EIP-4844 (Cancun) ---
	BlobGasUsed      *uint64     `json:"blobGasUsed,omitempty"`      // 18 CHANGED: pointer
	ExcessBlobGas    *uint64     `json:"excessBlobGas,omitempty"`    // 19 CHANGED: pointer
	ParentBeaconRoot *types.Hash `json:"parentBeaconBlockRoot,omitempty"` // 20 NEW

	// --- EIP-7685 (Prague/Pectra) ---
	RequestsHash *types.Hash `json:"requestsRoot,omitempty"` // 21 NEW

	hash atomic.Value
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
// Post-Shanghai headers have WithdrawalsHash, BlobGasUsed, or other new fields.
func IsLegacyHeader(h *Header) bool {
	if h.WithdrawalsHash != nil || h.BlobGasUsed != nil || h.ExcessBlobGas != nil ||
		h.ParentBeaconRoot != nil || h.RequestsHash != nil {
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
	pbh := &types_pb.Header{
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
		Bloom:         utils.ConvertBytesToH2048(h.Bloom.Bytes()),
		MixDigest:     utils.ConvertHashToH256(h.MixDigest),
		BlobGasUsed:   ptrToUint64(h.BlobGasUsed),
		ExcessBlobGas: ptrToUint64(h.ExcessBlobGas),
		UncleHash:     utils.ConvertHashToH256(h.UncleHash),
	}
	if h.WithdrawalsHash != nil {
		pbh.WithdrawalsHash = utils.ConvertHashToH256(*h.WithdrawalsHash)
	}
	if h.ParentBeaconRoot != nil {
		pbh.ParentBeaconRoot = utils.ConvertHashToH256(*h.ParentBeaconRoot)
	}
	if h.RequestsHash != nil {
		pbh.RequestsHash = utils.ConvertHashToH256(*h.RequestsHash)
	}
	return pbh
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
	// Signature removed from Header — consensus data now in ConsensusEvidence table.
	h.Bloom = utils.ConvertH2048ToBloom(pbHeader.Bloom)
	h.MixDigest = utils.ConvertH256ToHash(pbHeader.MixDigest)
	// Restore BlobGasUsed/ExcessBlobGas pointers when post-Shanghai fields exist.
	// Proto stores 0 as default, so we use WithdrawalsHash presence as indicator.
	if pbHeader.WithdrawalsHash != nil || pbHeader.BlobGasUsed != 0 || pbHeader.ExcessBlobGas != 0 {
		bgu := pbHeader.BlobGasUsed
		h.BlobGasUsed = &bgu
		ebg := pbHeader.ExcessBlobGas
		h.ExcessBlobGas = &ebg
	}
	if pbHeader.UncleHash != nil {
		h.UncleHash = utils.ConvertH256ToHash(pbHeader.UncleHash)
	} else {
		// Default for post-merge: rlpHash([]*Header(nil))
		h.UncleHash = types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")
	}
	if pbHeader.WithdrawalsHash != nil {
		wh := types.Hash(utils.ConvertH256ToHash(pbHeader.WithdrawalsHash))
		h.WithdrawalsHash = &wh
	}
	if pbHeader.ParentBeaconRoot != nil {
		pbr := types.Hash(utils.ConvertH256ToHash(pbHeader.ParentBeaconRoot))
		h.ParentBeaconRoot = &pbr
	}
	if pbHeader.RequestsHash != nil {
		rh := types.Hash(utils.ConvertH256ToHash(pbHeader.RequestsHash))
		h.RequestsHash = &rh
	}
	return nil
}

func ptrToUint64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// headerTrailerMagic identifies the presence of extended fields after proto bytes.
var headerTrailerMagic = [4]byte{'N', '4', '2', 'H'}

func (h *Header) Marshal() ([]byte, error) {
	pbBytes, err := proto.Marshal(h.ToProtoMessage())
	if err != nil {
		return nil, err
	}
	// Append trailer with ETH Pectra fields not in proto.
	// Format: [proto bytes][magic:4B][flags:1B][UncleHash:32B][field data...]
	// flags: bit0=WithdrawalsHash, bit1=ParentBeaconRoot, bit2=RequestsHash,
	//        bit3=BlobGasUsed present, bit4=ExcessBlobGas present
	var flags byte
	trailer := make([]byte, 0, 4+1+32+32*3+8*2)
	trailer = append(trailer, headerTrailerMagic[:]...)
	flagPos := len(trailer)
	trailer = append(trailer, 0) // placeholder for flags
	trailer = append(trailer, h.UncleHash[:]...)
	if h.WithdrawalsHash != nil {
		flags |= 0x01
		trailer = append(trailer, h.WithdrawalsHash[:]...)
	}
	if h.ParentBeaconRoot != nil {
		flags |= 0x02
		trailer = append(trailer, h.ParentBeaconRoot[:]...)
	}
	if h.RequestsHash != nil {
		flags |= 0x04
		trailer = append(trailer, h.RequestsHash[:]...)
	}
	if h.BlobGasUsed != nil {
		flags |= 0x08
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], *h.BlobGasUsed)
		trailer = append(trailer, buf[:]...)
	}
	if h.ExcessBlobGas != nil {
		flags |= 0x10
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], *h.ExcessBlobGas)
		trailer = append(trailer, buf[:]...)
	}
	trailer[flagPos] = flags

	result := make([]byte, len(pbBytes)+len(trailer))
	copy(result, pbBytes)
	copy(result[len(pbBytes):], trailer)
	return result, nil
}

func (h *Header) Unmarshal(data []byte) error {
	// Check for trailer magic at the end of data.
	protoData := data
	if len(data) > 37 { // at least magic(4)+flags(1)+UncleHash(32)
		// Scan backwards for magic. Trailer is at the end after proto bytes.
		// Proto bytes are variable length, so we search for magic signature.
		for i := len(data) - 37; i >= 0; i-- {
			if data[i] == 'N' && data[i+1] == '4' && data[i+2] == '2' && data[i+3] == 'H' {
				protoData = data[:i]
				h.parseTrailer(data[i:])
				break
			}
		}
	}

	var pbHeader types_pb.Header
	if err := proto.Unmarshal(protoData, &pbHeader); err != nil {
		return err
	}
	return h.FromProtoMessage(&pbHeader)
}

func (h *Header) parseTrailer(t []byte) {
	if len(t) < 37 { return } // magic(4)+flags(1)+UncleHash(32)
	pos := 4 // skip magic
	flags := t[pos]; pos++
	copy(h.UncleHash[:], t[pos:]); pos += 32
	if flags&0x01 != 0 && pos+32 <= len(t) {
		var wh types.Hash
		copy(wh[:], t[pos:]); pos += 32
		h.WithdrawalsHash = &wh
	}
	if flags&0x02 != 0 && pos+32 <= len(t) {
		var pbr types.Hash
		copy(pbr[:], t[pos:]); pos += 32
		h.ParentBeaconRoot = &pbr
	}
	if flags&0x04 != 0 && pos+32 <= len(t) {
		var rh types.Hash
		copy(rh[:], t[pos:]); pos += 32
		h.RequestsHash = &rh
	}
	if flags&0x08 != 0 && pos+8 <= len(t) {
		v := binary.LittleEndian.Uint64(t[pos:]); pos += 8
		h.BlobGasUsed = &v
	}
	if flags&0x10 != 0 && pos+8 <= len(t) {
		v := binary.LittleEndian.Uint64(t[pos:])
		h.ExcessBlobGas = &v
	}
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
