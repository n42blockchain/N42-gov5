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

package bundler

import (
	"encoding/binary"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"golang.org/x/crypto/sha3"
)

// BundleBuilder constructs handleOps calldata for the EntryPoint contract.
type BundleBuilder struct {
	entryPoint types.Address
}

// NewBundleBuilder creates a new bundle builder.
func NewBundleBuilder(entryPoint types.Address) *BundleBuilder {
	return &BundleBuilder{entryPoint: entryPoint}
}

// BuildCalldata constructs the ABI-encoded calldata for
// EntryPoint.handleOps(UserOperation[], address beneficiary).
func (b *BundleBuilder) BuildCalldata(ops []*vm.UserOperation, beneficiary types.Address) []byte {
	if len(ops) == 0 {
		return nil
	}

	// handleOps(UserOperation[],address) selector: 0x1fad948c
	data := make([]byte, 0, 4+64+len(ops)*352+32)
	data = append(data, vm.HandleOpsSelector...)

	// Offset to UserOperation[] (always 64 bytes after selector for two params).
	data = append(data, padUint256(big.NewInt(64))...)

	// beneficiary address (padded to 32 bytes).
	data = append(data, padAddress(beneficiary)...)

	// UserOperation[] encoding.
	// Length of array.
	data = append(data, padUint256(big.NewInt(int64(len(ops))))...)

	// Offset table for each UserOperation.
	// Each offset points to the start of the tuple from the beginning of the array data.
	// Pre-calculate offsets: we need to know the size of each encoded op.
	encoded := make([][]byte, len(ops))
	for i, op := range ops {
		encoded[i] = encodeUserOp(op)
	}

	// Offsets start after the offset table itself.
	offsetBase := uint64(len(ops)) * 32
	currentOffset := offsetBase
	for i := range ops {
		data = append(data, padUint256(new(big.Int).SetUint64(currentOffset))...)
		currentOffset += uint64(len(encoded[i]))
	}

	// Append encoded operations.
	for _, enc := range encoded {
		data = append(data, enc...)
	}

	return data
}

// EstimateGas estimates the total gas needed for the bundle.
func (b *BundleBuilder) EstimateGas(ops []*vm.UserOperation) *uint256.Int {
	total := uint256.NewInt(0)
	for _, op := range ops {
		opGas := new(uint256.Int).Add(op.CallGasLimit, op.VerificationGasLimit)
		opGas.Add(opGas, op.PreVerificationGas)
		total.Add(total, opGas)
	}
	// Add 10% overhead for EntryPoint bookkeeping.
	overhead := new(uint256.Int).Div(total, uint256.NewInt(10))
	total.Add(total, overhead)
	return total
}

// encodeUserOp ABI-encodes a single UserOperation as a tuple.
// Fields: sender, nonce, initCode, callData, callGasLimit, verificationGasLimit,
//
//	preVerificationGas, maxFeePerGas, maxPriorityFeePerGas, paymasterAndData, signature
func encodeUserOp(op *vm.UserOperation) []byte {
	// Static part: 11 fields × 32 bytes = 352 bytes for static values/offsets.
	// Dynamic fields: initCode, callData, paymasterAndData, signature.
	var buf []byte

	// We use a simplified ABI encoding:
	// - Fixed fields are encoded inline.
	// - Dynamic fields (bytes) use offset/length/data pattern.

	// Calculate dynamic data offsets. Static part is 11 * 32 = 352 bytes.
	staticSize := uint64(11 * 32)
	dynamicOffset := staticSize

	// Pre-encode dynamic fields.
	initCodeEnc := encodeDynamicBytes(op.InitCode)
	callDataEnc := encodeDynamicBytes(op.CallData)
	paymasterEnc := encodeDynamicBytes(op.PaymasterAndData)
	signatureEnc := encodeDynamicBytes(op.Signature)

	// Field 0: sender (address).
	buf = append(buf, padAddress(op.Sender)...)

	// Field 1: nonce (uint256).
	buf = append(buf, padUint256FromU256(op.Nonce)...)

	// Field 2: initCode offset.
	buf = append(buf, padUint256(new(big.Int).SetUint64(dynamicOffset))...)
	dynamicOffset += uint64(len(initCodeEnc))

	// Field 3: callData offset.
	buf = append(buf, padUint256(new(big.Int).SetUint64(dynamicOffset))...)
	dynamicOffset += uint64(len(callDataEnc))

	// Field 4: callGasLimit (uint256).
	buf = append(buf, padUint256FromU256(op.CallGasLimit)...)

	// Field 5: verificationGasLimit (uint256).
	buf = append(buf, padUint256FromU256(op.VerificationGasLimit)...)

	// Field 6: preVerificationGas (uint256).
	buf = append(buf, padUint256FromU256(op.PreVerificationGas)...)

	// Field 7: maxFeePerGas (uint256).
	buf = append(buf, padUint256FromU256(op.MaxFeePerGas)...)

	// Field 8: maxPriorityFeePerGas (uint256).
	buf = append(buf, padUint256FromU256(op.MaxPriorityFeePerGas)...)

	// Field 9: paymasterAndData offset.
	buf = append(buf, padUint256(new(big.Int).SetUint64(dynamicOffset))...)
	dynamicOffset += uint64(len(paymasterEnc))

	// Field 10: signature offset.
	buf = append(buf, padUint256(new(big.Int).SetUint64(dynamicOffset))...)

	// Append dynamic data.
	buf = append(buf, initCodeEnc...)
	buf = append(buf, callDataEnc...)
	buf = append(buf, paymasterEnc...)
	buf = append(buf, signatureEnc...)

	return buf
}

// encodeDynamicBytes ABI-encodes a dynamic bytes field: length (32 bytes) + data (padded to 32).
func encodeDynamicBytes(data []byte) []byte {
	length := len(data)
	paddedLen := ((length + 31) / 32) * 32

	buf := make([]byte, 32+paddedLen)
	binary.BigEndian.PutUint64(buf[24:32], uint64(length))
	copy(buf[32:], data)
	return buf
}

// padAddress pads an address to 32 bytes (left-padded with zeros).
func padAddress(addr types.Address) []byte {
	var buf [32]byte
	copy(buf[12:], addr.Bytes())
	return buf[:]
}

// padUint256 pads a big.Int to 32 bytes.
func padUint256(v *big.Int) []byte {
	var buf [32]byte
	if v != nil {
		b := v.Bytes()
		copy(buf[32-len(b):], b)
	}
	return buf[:]
}

// padUint256FromU256 pads a uint256.Int to 32 bytes.
func padUint256FromU256(v *uint256.Int) []byte {
	if v == nil {
		return make([]byte, 32)
	}
	b := v.Bytes32()
	return b[:]
}

// UserOpHashForReceipt computes the UserOperation hash for receipt lookup.
// This is the same hash used in UserOperationEvent logs.
func UserOpHashForReceipt(op *vm.UserOperation, entryPoint types.Address, chainID uint64) types.Hash {
	// Pack the UserOperation fields.
	h := sha3.NewLegacyKeccak256()
	h.Write(op.Sender.Bytes())
	if op.Nonce != nil {
		b := op.Nonce.Bytes32()
		h.Write(b[:])
	}
	h.Write(hashBytes(op.InitCode))
	h.Write(hashBytes(op.CallData))
	if op.CallGasLimit != nil {
		b := op.CallGasLimit.Bytes32()
		h.Write(b[:])
	}
	if op.VerificationGasLimit != nil {
		b := op.VerificationGasLimit.Bytes32()
		h.Write(b[:])
	}
	if op.PreVerificationGas != nil {
		b := op.PreVerificationGas.Bytes32()
		h.Write(b[:])
	}
	if op.MaxFeePerGas != nil {
		b := op.MaxFeePerGas.Bytes32()
		h.Write(b[:])
	}
	if op.MaxPriorityFeePerGas != nil {
		b := op.MaxPriorityFeePerGas.Bytes32()
		h.Write(b[:])
	}
	h.Write(hashBytes(op.PaymasterAndData))
	innerHash := h.Sum(nil)

	// Outer hash: keccak256(innerHash, entryPoint, chainId).
	outer := sha3.NewLegacyKeccak256()
	outer.Write(innerHash)
	outer.Write(entryPoint.Bytes())
	var chainBuf [32]byte
	binary.BigEndian.PutUint64(chainBuf[24:], chainID)
	outer.Write(chainBuf[:])

	return types.BytesToHash(outer.Sum(nil))
}
