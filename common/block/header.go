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
// N42 block Header and BlockNonce. Fields 1-21 match Ethereum Pectra
// exactly (same RLP / JSON wire format); N42-specific extensions
// (LtHashRoot, tree roots, BLS signatures) live inside Extra. Also
// defines BlockNonce (8-byte nonce) with Encode/Decode and
// hexutil-backed MarshalText / UnmarshalText for JSON-RPC output.

package block

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/proto/types_pb"
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
	Extra       []byte        `json:"extraData"        gencodec:"required"` // 13 vanity (32B), ETH standard
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

// Hash returns the Keccak256 hash of the RLP-encoded header.
// This matches the standard Ethereum header hash computation.
func (h *Header) Hash() types.Hash {
	if hash := h.hash.Load(); hash != nil {
		return hash.(types.Hash)
	}
	hash := h.rlpHash()
	h.hash.Store(hash)
	return hash
}

// ResetHashCache clears the cached Hash() result. Required after
// mutating any field that contributes to the canonical hash —
// notably ParentHash and Bloom on a header reconstructed from a
// columnar source.
func (h *Header) ResetHashCache() { h.hash = atomic.Value{} }

// SetHash forces Hash() to return v without recomputing. Used by
// columnar readers that store the canonical hash alongside the
// stripped fields (ParentHash, Bloom) — calling SetHash skips the
// reconstruction work entirely. Caller is responsible for ensuring
// v actually equals keccak256(rlp(header)) for the canonical form.
func (h *Header) SetHash(v types.Hash) { h.hash.Store(v) }

// rlpHash computes keccak256(rlp(header)) following Ethereum Pectra field order.
func (h *Header) rlpHash() types.Hash {
	// Pre-London: 15 fields, London: +baseFee, Shanghai: +withdrawalsRoot,
	// Cancun: +blobGasUsed +excessBlobGas +parentBeaconBlockRoot,
	// Pectra: +requestsRoot
	enc := []interface{}{
		h.ParentHash,
		h.UncleHash,
		h.Coinbase,
		h.Root,
		h.TxHash,
		h.ReceiptHash,
		h.Bloom,
		h.Difficulty,
		h.Number,
		h.GasLimit,
		h.GasUsed,
		h.Time,
		h.Extra,
		h.MixDigest,
		h.Nonce,
	}
	if h.BaseFee != nil {
		enc = append(enc, h.BaseFee)
	}
	if h.WithdrawalsHash != nil {
		enc = append(enc, h.WithdrawalsHash)
	}
	if h.BlobGasUsed != nil {
		enc = append(enc, h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		enc = append(enc, h.ExcessBlobGas)
	}
	if h.ParentBeaconRoot != nil {
		enc = append(enc, h.ParentBeaconRoot)
	}
	if h.RequestsHash != nil {
		enc = append(enc, h.RequestsHash)
	}
	return hash.RlpHash(enc)
}

// IsLegacyHeader returns true for pre-Shanghai headers (no post-Shanghai fields set).
func IsLegacyHeader(h *Header) bool {
	if h.WithdrawalsHash != nil || h.BlobGasUsed != nil || h.ExcessBlobGas != nil ||
		h.ParentBeaconRoot != nil || h.RequestsHash != nil {
		return false
	}
	return true
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
		BlobGasUsed:      ptrToUint64(h.BlobGasUsed),
		ExcessBlobGas:    ptrToUint64(h.ExcessBlobGas),
		HasBlobGasUsed:   h.BlobGasUsed != nil,
		HasExcessBlobGas: h.ExcessBlobGas != nil,
		UncleHash:        utils.ConvertHashToH256(h.UncleHash),
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
	if pbHeader.HasBlobGasUsed || pbHeader.BlobGasUsed != 0 {
		bgu := pbHeader.BlobGasUsed
		h.BlobGasUsed = &bgu
	}
	if pbHeader.HasExcessBlobGas || pbHeader.ExcessBlobGas != 0 {
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

	// Append 4-byte proto length at end of trailer for precise split on read.
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(pbBytes)))
	trailer = append(trailer, lenBuf[:]...)

	result := make([]byte, len(pbBytes)+len(trailer))
	copy(result, pbBytes)
	copy(result[len(pbBytes):], trailer)
	return result, nil
}

func (h *Header) Unmarshal(data []byte) error {
	// Split proto bytes from trailer using the 4-byte proto length at the end.
	// Format: [proto bytes][magic(4)+flags(1)+fields...+protoLen(4)]
	protoData := data
	if len(data) > 41 { // magic(4)+flags(1)+UncleHash(32)+protoLen(4) = 41 minimum
		protoLen := binary.LittleEndian.Uint32(data[len(data)-4:])
		if int(protoLen) < len(data) && protoLen > 0 {
			trailerStart := int(protoLen)
			if trailerStart+4 <= len(data) &&
				data[trailerStart] == 'N' && data[trailerStart+1] == '4' &&
				data[trailerStart+2] == '2' && data[trailerStart+3] == 'H' {
				protoData = data[:trailerStart]
				h.parseTrailer(data[trailerStart : len(data)-4]) // exclude trailing protoLen
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
	cpy.hash = atomic.Value{}

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
	cpy.WithdrawalsHash = cloneHashPtr(h.WithdrawalsHash)
	cpy.BlobGasUsed = cloneUint64Ptr(h.BlobGasUsed)
	cpy.ExcessBlobGas = cloneUint64Ptr(h.ExcessBlobGas)
	cpy.ParentBeaconRoot = cloneHashPtr(h.ParentBeaconRoot)
	cpy.RequestsHash = cloneHashPtr(h.RequestsHash)
	return &cpy
}

func cloneHashPtr(src *types.Hash) *types.Hash {
	if src == nil {
		return nil
	}
	dst := new(types.Hash)
	*dst = *src
	return dst
}

func cloneUint64Ptr(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	dst := new(uint64)
	*dst = *src
	return dst
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
