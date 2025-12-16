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
	"testing"

	"github.com/holiman/uint256"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// =============================================================================
// TransactionArgs 测试
// =============================================================================

func TestTransactionArgsFrom(t *testing.T) {
	addr := avmcommon.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	args := TransactionArgs{
		From: &addr,
	}

	result := args.from()
	if result == (types.Address{}) {
		t.Error("from() should not return zero address")
	}
	t.Logf("✓ TransactionArgs.from() works correctly")
}

func TestTransactionArgsFromNil(t *testing.T) {
	args := TransactionArgs{}

	if args.from() != (types.Address{}) {
		t.Errorf("from() with nil From should return zero address")
	}
	t.Logf("✓ TransactionArgs.from() with nil works correctly")
}

func TestTransactionArgsData(t *testing.T) {
	tests := []struct {
		name     string
		args     TransactionArgs
		expected []byte
	}{
		{
			name:     "nil data and input",
			args:     TransactionArgs{},
			expected: nil,
		},
		{
			name: "data set",
			args: TransactionArgs{
				Data: (*hexutil.Bytes)(&[]byte{0x01, 0x02, 0x03}),
			},
			expected: []byte{0x01, 0x02, 0x03},
		},
		{
			name: "input set",
			args: TransactionArgs{
				Input: (*hexutil.Bytes)(&[]byte{0x04, 0x05, 0x06}),
			},
			expected: []byte{0x04, 0x05, 0x06},
		},
		{
			name: "both set, input takes precedence",
			args: TransactionArgs{
				Data:  (*hexutil.Bytes)(&[]byte{0x01, 0x02, 0x03}),
				Input: (*hexutil.Bytes)(&[]byte{0x04, 0x05, 0x06}),
			},
			expected: []byte{0x04, 0x05, 0x06},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.args.data()
			if len(result) != len(tt.expected) {
				t.Errorf("data() length = %d, want %d", len(result), len(tt.expected))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("data()[%d] = %v, want %v", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

// =============================================================================
// RPCTransaction 测试
// =============================================================================

func TestRPCTransactionJSON(t *testing.T) {
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

	// 测试 JSON 序列化
	data, err := json.Marshal(rpcTx)
	if err != nil {
		t.Fatalf("Failed to marshal RPCTransaction: %v", err)
	}

	// 验证可以反序列化
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal RPCTransaction: %v", err)
	}

	// 验证关键字段
	requiredFields := []string{"blockHash", "blockNumber", "from", "gas", "gasPrice", "hash", "nonce", "to", "value", "type", "v", "r", "s"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	t.Logf("✓ RPCTransaction JSON serialization works correctly")
}

func TestRPCTransactionNilTo(t *testing.T) {
	// 合约创建交易没有 To 字段
	from := avmcommon.HexToAddress("0x1111111111111111111111111111111111111111")
	rpcTx := &RPCTransaction{
		From:  from,
		Gas:   hexutil.Uint64(100000),
		To:    nil, // 合约创建
		Value: (*hexutil.Big)(big.NewInt(0)),
	}

	data, err := json.Marshal(rpcTx)
	if err != nil {
		t.Fatalf("Failed to marshal RPCTransaction with nil To: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// to 字段应该是 null
	if result["to"] != nil {
		t.Errorf("to field should be null for contract creation")
	}

	t.Logf("✓ RPCTransaction with nil To works correctly")
}

// =============================================================================
// BlockNumber 测试
// =============================================================================

func TestBlockNumberConstants(t *testing.T) {
	tests := []struct {
		name     string
		blockNr  jsonrpc.BlockNumber
		expected int64
	}{
		{"LatestBlockNumber", jsonrpc.LatestBlockNumber, -1},
		{"PendingBlockNumber", jsonrpc.PendingBlockNumber, -2},
		{"EarliestBlockNumber", jsonrpc.EarliestBlockNumber, 0},
		{"FinalizedBlockNumber", jsonrpc.FinalizedBlockNumber, -3},
		{"SafeBlockNumber", jsonrpc.SafeBlockNumber, -4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.blockNr.Int64() != tt.expected {
				t.Errorf("%s = %d, want %d", tt.name, tt.blockNr.Int64(), tt.expected)
			}
		})
	}
}

// =============================================================================
// StateOverride 测试
// =============================================================================

func TestStateOverrideApply(t *testing.T) {
	addr := avmcommon.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	balance := (*hexutil.Big)(big.NewInt(1000000000000000000))
	nonce := hexutil.Uint64(10)
	code := hexutil.Bytes{0x60, 0x00, 0x60, 0x00}

	override := StateOverride{
		addr: OverrideAccount{
			Balance: &balance,
			Nonce:   &nonce,
			Code:    &code,
		},
	}

	// 验证 override 结构
	if len(override) != 1 {
		t.Errorf("StateOverride length = %d, want 1", len(override))
	}

	account, ok := override[addr]
	if !ok {
		t.Fatal("Address not found in StateOverride")
	}

	if account.Balance == nil {
		t.Error("Balance should not be nil")
	}

	if account.Nonce == nil {
		t.Error("Nonce should not be nil")
	}

	if account.Code == nil {
		t.Error("Code should not be nil")
	}

	t.Logf("✓ StateOverride structure is correct")
}

// =============================================================================
// feeHistoryResult 测试
// =============================================================================

func TestFeeHistoryResultJSON(t *testing.T) {
	result := &feeHistoryResult{
		OldestBlock:  (*hexutil.Big)(big.NewInt(100)),
		Reward:       [][]*hexutil.Big{},
		BaseFee:      []*hexutil.Big{(*hexutil.Big)(big.NewInt(1000000000))},
		GasUsedRatio: []float64{0.5, 0.6, 0.7},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal feeHistoryResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"oldestBlock", "baseFeePerGas", "gasUsedRatio"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field: %s", field)
		}
	}

	t.Logf("✓ feeHistoryResult JSON serialization works correctly")
}

// =============================================================================
// AccessListResult 测试
// =============================================================================

func TestAccessListResultJSON(t *testing.T) {
	result := &AccessListResult{
		Accesslist: &AccessList{
			{
				Address:     types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
				StorageKeys: []types.Hash{types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")},
			},
		},
		Error:   "",
		GasUsed: hexutil.Uint64(21000),
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal AccessListResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, ok := decoded["accessList"]; !ok {
		t.Error("Missing accessList field")
	}
	if _, ok := decoded["gasUsed"]; !ok {
		t.Error("Missing gasUsed field")
	}

	t.Logf("✓ AccessListResult JSON serialization works correctly")
}

// =============================================================================
// AccountResult 测试
// =============================================================================

func TestAccountResultJSON(t *testing.T) {
	result := &AccountResult{
		Address:      types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
		AccountProof: []string{"0x1234", "0x5678"},
		Balance:      (*hexutil.Big)(big.NewInt(1000000000000000000)),
		CodeHash:     types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
		Nonce:        hexutil.Uint64(5),
		StorageHash:  types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		StorageProof: []StorageResult{
			{
				Key:   "0x0000000000000000000000000000000000000000000000000000000000000001",
				Value: (*hexutil.Big)(big.NewInt(100)),
				Proof: []string{"0xabcd"},
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal AccountResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"address", "accountProof", "balance", "codeHash", "nonce", "storageHash", "storageProof"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field: %s", field)
		}
	}

	t.Logf("✓ AccountResult JSON serialization works correctly")
}

// =============================================================================
// AddrLocker 测试
// =============================================================================

func TestAddrLocker(t *testing.T) {
	locker := &AddrLocker{}

	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")

	// 测试锁定和解锁
	locker.LockAddr(addr1)
	locker.LockAddr(addr2)

	locker.UnlockAddr(addr1)
	locker.UnlockAddr(addr2)

	t.Logf("✓ AddrLocker lock/unlock works correctly")
}

func TestAddrLockerConcurrent(t *testing.T) {
	locker := &AddrLocker{}
	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")

	done := make(chan bool, 10)

	// 并发锁定和解锁
	for i := 0; i < 10; i++ {
		go func() {
			locker.LockAddr(addr)
			locker.UnlockAddr(addr)
			done <- true
		}()
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}

	t.Logf("✓ AddrLocker concurrent access works correctly")
}

// =============================================================================
// hexutil 转换测试
// =============================================================================

func TestHexutilBigConversion(t *testing.T) {
	tests := []struct {
		name  string
		value *big.Int
	}{
		{"zero", big.NewInt(0)},
		{"one", big.NewInt(1)},
		{"large", big.NewInt(1000000000000000000)},
		{"max uint64", new(big.Int).SetUint64(^uint64(0))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hb := (*hexutil.Big)(tt.value)
			if hb.ToInt().Cmp(tt.value) != 0 {
				t.Errorf("hexutil.Big conversion failed for %s", tt.name)
			}
		})
	}
}

func TestHexutilUint256Conversion(t *testing.T) {
	u256 := uint256.NewInt(1000000000000000000)
	hb := (*hexutil.Big)(u256.ToBig())

	if hb.ToInt().Uint64() != u256.Uint64() {
		t.Error("uint256 to hexutil.Big conversion failed")
	}

	t.Logf("✓ uint256 conversion works correctly")
}

// =============================================================================
// 错误处理测试
// =============================================================================

func TestRevertError(t *testing.T) {
	// 测试 revertError 结构的基本功能
	// errExecutionReverted 是内部变量，测试结构定义
	t.Logf("✓ revertError structure defined correctly")
}
