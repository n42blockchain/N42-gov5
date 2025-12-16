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

package api

import (
	"encoding/json"
	"math/big"
	"runtime"
	"testing"

	"github.com/holiman/uint256"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// RPCTransaction Benchmarks
// =============================================================================

func BenchmarkRPCTransactionMarshal(b *testing.B) {
	hash := avmcommon.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	from := avmcommon.HexToAddress("0x1111111111111111111111111111111111111111")
	to := avmcommon.HexToAddress("0x2222222222222222222222222222222222222222")
	blockNum := uint64(100)
	txIndex := hexutil.Uint64(5)

	rpcTx := &RPCTransaction{
		BlockHash:        &hash,
		BlockNumber:      (*hexutil.Big)(big.NewInt(int64(blockNum))),
		From:             from,
		Gas:              hexutil.Uint64(21000),
		GasPrice:         (*hexutil.Big)(big.NewInt(1000000000)),
		Hash:             hash,
		Input:            hexutil.Bytes{},
		Nonce:            hexutil.Uint64(0),
		To:               &to,
		TransactionIndex: &txIndex,
		Value:            (*hexutil.Big)(big.NewInt(1000000000000000000)),
		Type:             hexutil.Uint64(0),
		V:                (*hexutil.Big)(big.NewInt(27)),
		R:                (*hexutil.Big)(big.NewInt(12345)),
		S:                (*hexutil.Big)(big.NewInt(67890)),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(rpcTx)
	}
}

func BenchmarkRPCTransactionUnmarshal(b *testing.B) {
	data := []byte(`{
		"blockHash": "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"blockNumber": "0x64",
		"from": "0x1111111111111111111111111111111111111111",
		"gas": "0x5208",
		"gasPrice": "0x3b9aca00",
		"hash": "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"input": "0x",
		"nonce": "0x0",
		"to": "0x2222222222222222222222222222222222222222",
		"transactionIndex": "0x5",
		"value": "0xde0b6b3a7640000",
		"type": "0x0",
		"v": "0x1b",
		"r": "0x3039",
		"s": "0x10932"
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var tx RPCTransaction
		_ = json.Unmarshal(data, &tx)
	}
}

// =============================================================================
// TransactionArgs Benchmarks
// =============================================================================

func BenchmarkTransactionArgsFrom(b *testing.B) {
	addr := avmcommon.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	args := TransactionArgs{
		From: &addr,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = args.from()
	}
}

func BenchmarkTransactionArgsData(b *testing.B) {
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	args := TransactionArgs{
		Input: (*hexutil.Bytes)(&data),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = args.data()
	}
}

// =============================================================================
// StateOverride Benchmarks
// =============================================================================

func BenchmarkStateOverrideSmall(b *testing.B) {
	balance := (*hexutil.Big)(big.NewInt(1000000000000000000))
	nonce := hexutil.Uint64(10)

	override := make(StateOverride)
	for i := 0; i < 5; i++ {
		addr := avmcommon.Address{}
		addr[0] = byte(i)
		override[addr] = OverrideAccount{
			Balance: &balance,
			Nonce:   &nonce,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(override)
	}
}

func BenchmarkStateOverrideLarge(b *testing.B) {
	balance := (*hexutil.Big)(big.NewInt(1000000000000000000))
	nonce := hexutil.Uint64(10)
	code := hexutil.Bytes(make([]byte, 1024))

	override := make(StateOverride)
	for i := 0; i < 100; i++ {
		addr := avmcommon.Address{}
		addr[0] = byte(i % 256)
		addr[1] = byte(i / 256)
		override[addr] = OverrideAccount{
			Balance: &balance,
			Nonce:   &nonce,
			Code:    &code,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(override)
	}
}

// =============================================================================
// feeHistoryResult Benchmarks
// =============================================================================

func BenchmarkFeeHistoryResultMarshal(b *testing.B) {
	result := &feeHistoryResult{
		OldestBlock:  (*hexutil.Big)(big.NewInt(100)),
		Reward:       make([][]*hexutil.Big, 100),
		BaseFee:      make([]*hexutil.Big, 100),
		GasUsedRatio: make([]float64, 100),
	}

	for i := 0; i < 100; i++ {
		result.BaseFee[i] = (*hexutil.Big)(big.NewInt(1000000000))
		result.GasUsedRatio[i] = 0.5
		result.Reward[i] = []*hexutil.Big{(*hexutil.Big)(big.NewInt(1000000))}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(result)
	}
}

// =============================================================================
// ExecutionResult Benchmarks
// =============================================================================

func BenchmarkExecutionResultSmall(b *testing.B) {
	result := &ExecutionResult{
		Gas:         21000,
		Failed:      false,
		ReturnValue: "0x",
		StructLogs:  []StructLogRes{},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(result)
	}
}

func BenchmarkExecutionResultWithLogs(b *testing.B) {
	result := &ExecutionResult{
		Gas:         50000,
		Failed:      false,
		ReturnValue: "0x0000000000000000000000000000000000000000000000000000000000000001",
		StructLogs:  make([]StructLogRes, 100),
	}

	stack := []string{"0x60", "0x80"}
	memory := []string{}
	storage := map[string]string{}

	for i := 0; i < 100; i++ {
		result.StructLogs[i] = StructLogRes{
			Pc:      uint64(i * 2),
			Op:      "PUSH1",
			Gas:     uint64(50000 - i*3),
			GasCost: 3,
			Depth:   1,
			Stack:   &stack,
			Memory:  &memory,
			Storage: &storage,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(result)
	}
}

// =============================================================================
// hexutil Benchmarks
// =============================================================================

func BenchmarkHexutilBigMarshal(b *testing.B) {
	value := (*hexutil.Big)(big.NewInt(1000000000000000000))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = value.MarshalText()
	}
}

func BenchmarkHexutilBigUnmarshal(b *testing.B) {
	data := []byte("0xde0b6b3a7640000")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var value hexutil.Big
		_ = value.UnmarshalText(data)
	}
}

func BenchmarkHexutilBytesMarshal(b *testing.B) {
	data := make(hexutil.Bytes, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = data.MarshalText()
	}
}

func BenchmarkHexutilBytesUnmarshal(b *testing.B) {
	// 2048 hex chars + "0x" prefix = 1024 bytes
	hexData := make([]byte, 2050)
	hexData[0] = '0'
	hexData[1] = 'x'
	for i := 2; i < 2050; i++ {
		hexData[i] = "0123456789abcdef"[i%16]
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var data hexutil.Bytes
		_ = data.UnmarshalText(hexData)
	}
}

// =============================================================================
// uint256 Benchmarks
// =============================================================================

func BenchmarkUint256ToBig(b *testing.B) {
	u := uint256.NewInt(1000000000000000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = u.ToBig()
	}
}

func BenchmarkUint256FromBig(b *testing.B) {
	big := big.NewInt(1000000000000000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = uint256.FromBig(big)
	}
}

// =============================================================================
// AddrLocker Benchmarks
// =============================================================================

func BenchmarkAddrLockerLockUnlock(b *testing.B) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		locker.LockAddr(addr)
		locker.UnlockAddr(addr)
	}
}

func BenchmarkAddrLockerMultipleAddrs(b *testing.B) {
	locker := &AddrLocker{}
	addrs := make([]types.Address, 10)
	for i := range addrs {
		addrs[i] = types.Address{}
		addrs[i][0] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addr := addrs[i%10]
		locker.LockAddr(addr)
		locker.UnlockAddr(addr)
	}
}

// =============================================================================
// Debug API Benchmarks
// =============================================================================

func BenchmarkMemStats(b *testing.B) {
	debug := &DebugAPI{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = debug.MemStats()
	}
}

func BenchmarkGcStats(b *testing.B) {
	debug := &DebugAPI{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = debug.GcStats()
	}
}

func BenchmarkStacks(b *testing.B) {
	debug := &DebugAPI{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = debug.Stacks()
	}
}

// =============================================================================
// Admin API Benchmarks
// =============================================================================

func BenchmarkNodeInfo(b *testing.B) {
	admin := &AdminAPI{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = admin.NodeInfo()
	}
}

func BenchmarkModules(b *testing.B) {
	rpcAPI := &RPCAPI{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rpcAPI.Modules()
	}
}

// =============================================================================
// Memory Allocation Benchmarks
// =============================================================================

func BenchmarkRPCTransactionAlloc(b *testing.B) {
	b.ReportAllocs()

	hash := avmcommon.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	from := avmcommon.HexToAddress("0x1111111111111111111111111111111111111111")
	to := avmcommon.HexToAddress("0x2222222222222222222222222222222222222222")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = &RPCTransaction{
			BlockHash: &hash,
			From:      from,
			To:        &to,
			Gas:       hexutil.Uint64(21000),
		}
	}
}

func BenchmarkStructLogResAlloc(b *testing.B) {
	b.ReportAllocs()

	stack := []string{"0x1", "0x2"}
	memory := []string{}
	storage := map[string]string{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StructLogRes{
			Pc:      100,
			Op:      "SLOAD",
			Gas:     45000,
			GasCost: 2100,
			Depth:   2,
			Stack:   &stack,
			Memory:  &memory,
			Storage: &storage,
		}
	}
}

// =============================================================================
// Parallel Benchmarks
// =============================================================================

func BenchmarkRPCTransactionMarshalParallel(b *testing.B) {
	hash := avmcommon.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	from := avmcommon.HexToAddress("0x1111111111111111111111111111111111111111")
	to := avmcommon.HexToAddress("0x2222222222222222222222222222222222222222")

	rpcTx := &RPCTransaction{
		BlockHash: &hash,
		From:      from,
		To:        &to,
		Gas:       hexutil.Uint64(21000),
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = json.Marshal(rpcTx)
		}
	})
}

func BenchmarkAddrLockerParallel(b *testing.B) {
	locker := &AddrLocker{}

	b.RunParallel(func(pb *testing.PB) {
		addr := types.Address{}
		addr[0] = byte(runtime.NumCPU())

		for pb.Next() {
			locker.LockAddr(addr)
			locker.UnlockAddr(addr)
		}
	})
}
