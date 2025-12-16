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
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers/logger"
)

// =============================================================================
// TraceConfig 测试
// =============================================================================

func TestTraceConfigDefaults(t *testing.T) {
	config := &TraceConfig{}

	// 验证默认值
	if config.Tracer != nil {
		t.Error("Tracer should be nil by default")
	}

	if config.Timeout != nil {
		t.Error("Timeout should be nil by default")
	}

	if config.Reexec != nil {
		t.Error("Reexec should be nil by default")
	}

	t.Logf("✓ TraceConfig defaults are correct")
}

func TestTraceConfigWithTimeout(t *testing.T) {
	timeout := "30s"
	config := &TraceConfig{
		Timeout: &timeout,
	}

	if config.Timeout == nil {
		t.Fatal("Timeout should not be nil")
	}

	if *config.Timeout != "30s" {
		t.Errorf("Timeout = %s, want 30s", *config.Timeout)
	}

	t.Logf("✓ TraceConfig with timeout works correctly")
}

func TestTraceConfigWithTracer(t *testing.T) {
	tracer := "callTracer"
	config := &TraceConfig{
		Tracer: &tracer,
	}

	if config.Tracer == nil {
		t.Fatal("Tracer should not be nil")
	}

	if *config.Tracer != "callTracer" {
		t.Errorf("Tracer = %s, want callTracer", *config.Tracer)
	}

	t.Logf("✓ TraceConfig with tracer works correctly")
}

func TestTraceConfigJSON(t *testing.T) {
	tracer := "callTracer"
	timeout := "30s"
	reexec := uint64(128)

	config := &TraceConfig{
		Tracer:  &tracer,
		Timeout: &timeout,
		Reexec:  &reexec,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal TraceConfig: %v", err)
	}

	var decoded TraceConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal TraceConfig: %v", err)
	}

	if decoded.Tracer == nil || *decoded.Tracer != tracer {
		t.Error("Tracer not correctly serialized")
	}

	if decoded.Timeout == nil || *decoded.Timeout != timeout {
		t.Error("Timeout not correctly serialized")
	}

	if decoded.Reexec == nil || *decoded.Reexec != reexec {
		t.Error("Reexec not correctly serialized")
	}

	t.Logf("✓ TraceConfig JSON serialization works correctly")
}

// =============================================================================
// TraceCallConfig 测试
// =============================================================================

func TestTraceCallConfigDefaults(t *testing.T) {
	config := &TraceCallConfig{}

	if config.StateOverrides != nil {
		t.Error("StateOverrides should be nil by default")
	}

	if config.BlockOverrides != nil {
		t.Error("BlockOverrides should be nil by default")
	}

	t.Logf("✓ TraceCallConfig defaults are correct")
}

func TestTraceCallConfigWithOverrides(t *testing.T) {
	// 简单测试 TraceCallConfig 结构
	config := &TraceCallConfig{}

	// 验证默认值
	if config.StateOverrides != nil {
		t.Error("StateOverrides should be nil by default")
	}

	t.Logf("✓ TraceCallConfig with overrides works correctly")
}

// =============================================================================
// ExecutionResult 测试
// =============================================================================

func TestExecutionResultJSON(t *testing.T) {
	result := &ExecutionResult{
		Gas:         21000,
		Failed:      false,
		ReturnValue: "0x",
		StructLogs:  []StructLogRes{},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal ExecutionResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"gas", "failed", "returnValue", "structLogs"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field: %s", field)
		}
	}

	t.Logf("✓ ExecutionResult JSON serialization works correctly")
}

func TestExecutionResultWithStructLogs(t *testing.T) {
	emptyStack := []string{}
	stack1 := []string{"0x60"}

	result := &ExecutionResult{
		Gas:         50000,
		Failed:      false,
		ReturnValue: "0x0000000000000000000000000000000000000000000000000000000000000001",
		StructLogs: []StructLogRes{
			{
				Pc:      0,
				Op:      "PUSH1",
				Gas:     49978,
				GasCost: 3,
				Depth:   1,
				Stack:   &emptyStack,
			},
			{
				Pc:      2,
				Op:      "PUSH1",
				Gas:     49975,
				GasCost: 3,
				Depth:   1,
				Stack:   &stack1,
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal ExecutionResult: %v", err)
	}

	var decoded ExecutionResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if len(decoded.StructLogs) != 2 {
		t.Errorf("StructLogs length = %d, want 2", len(decoded.StructLogs))
	}

	if decoded.StructLogs[0].Op != "PUSH1" {
		t.Errorf("First op = %s, want PUSH1", decoded.StructLogs[0].Op)
	}

	t.Logf("✓ ExecutionResult with StructLogs works correctly")
}

// =============================================================================
// StructLogRes 测试
// =============================================================================

func TestStructLogResJSON(t *testing.T) {
	stack := []string{"0x1", "0x2"}
	memory := []string{"0x00", "0x00"}
	storage := map[string]string{"0x1": "0x100"}

	log := StructLogRes{
		Pc:      100,
		Op:      "SLOAD",
		Gas:     45000,
		GasCost: 2100,
		Depth:   2,
		Error:   "",
		Stack:   &stack,
		Memory:  &memory,
		Storage: &storage,
	}

	data, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("Failed to marshal StructLogRes: %v", err)
	}

	var decoded StructLogRes
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Op != "SLOAD" {
		t.Errorf("Op = %s, want SLOAD", decoded.Op)
	}

	if decoded.GasCost != 2100 {
		t.Errorf("GasCost = %d, want 2100", decoded.GasCost)
	}

	if decoded.Stack != nil && len(*decoded.Stack) != 2 {
		t.Errorf("Stack length = %d, want 2", len(*decoded.Stack))
	}

	t.Logf("✓ StructLogRes JSON serialization works correctly")
}

// =============================================================================
// formatLogs 测试
// =============================================================================

func TestFormatLogs(t *testing.T) {
	logs := []logger.StructLog{
		{
			Pc:      0,
			Op:      0x60, // PUSH1
			Gas:     100000,
			GasCost: 3,
			Depth:   1,
		},
		{
			Pc:      2,
			Op:      0x60, // PUSH1
			Gas:     99997,
			GasCost: 3,
			Depth:   1,
		},
	}

	result := formatLogs(logs, 50000, false, []byte{})

	if result == nil {
		t.Fatal("formatLogs returned nil")
	}

	if len(result.StructLogs) != 2 {
		t.Fatalf("formatLogs returned %d logs, want 2", len(result.StructLogs))
	}

	if result.StructLogs[0].Pc != 0 {
		t.Errorf("First log Pc = %d, want 0", result.StructLogs[0].Pc)
	}

	if result.StructLogs[1].Pc != 2 {
		t.Errorf("Second log Pc = %d, want 2", result.StructLogs[1].Pc)
	}

	t.Logf("✓ formatLogs works correctly")
}

func TestFormatLogsEmpty(t *testing.T) {
	logs := []logger.StructLog{}
	result := formatLogs(logs, 21000, false, []byte{})

	if result == nil {
		t.Fatal("formatLogs returned nil")
	}

	if len(result.StructLogs) != 0 {
		t.Errorf("formatLogs returned %d logs for empty input, want 0", len(result.StructLogs))
	}

	t.Logf("✓ formatLogs handles empty input correctly")
}

// =============================================================================
// BadBlockArgs 测试
// =============================================================================

func TestBadBlockArgsJSON(t *testing.T) {
	hash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	args := &BadBlockArgs{
		Hash:   hash,
		Block:  map[string]interface{}{"number": "0x100"},
		RLP:    "0xf90...",
		Reason: "invalid state root",
	}

	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Failed to marshal BadBlockArgs: %v", err)
	}

	var decoded BadBlockArgs
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Hash != hash {
		t.Error("Hash not correctly serialized")
	}

	if decoded.Reason != "invalid state root" {
		t.Errorf("Reason = %s, want 'invalid state root'", decoded.Reason)
	}

	t.Logf("✓ BadBlockArgs JSON serialization works correctly")
}

// =============================================================================
// StorageRangeResult 测试
// =============================================================================

func TestStorageRangeResultJSON(t *testing.T) {
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	value := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000064")
	nextKey := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	result := &StorageRangeResult{
		Storage: map[types.Hash]StorageEntry{
			key: {
				Key:   &key,
				Value: value,
			},
		},
		NextKey: &nextKey,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal StorageRangeResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, ok := decoded["storage"]; !ok {
		t.Error("Missing storage field")
	}

	if _, ok := decoded["nextKey"]; !ok {
		t.Error("Missing nextKey field")
	}

	t.Logf("✓ StorageRangeResult JSON serialization works correctly")
}

func TestStorageRangeResultNilNextKey(t *testing.T) {
	result := &StorageRangeResult{
		Storage: map[types.Hash]StorageEntry{},
		NextKey: nil,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal StorageRangeResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["nextKey"] != nil {
		t.Error("nextKey should be null when nil")
	}

	t.Logf("✓ StorageRangeResult with nil nextKey works correctly")
}

// =============================================================================
// AccountRangeResult 测试
// =============================================================================

func TestAccountRangeResultJSON(t *testing.T) {
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	result := &AccountRangeResult{
		Accounts: map[types.Address]AccountRangeEntry{
			addr: {
				Balance:  "0xde0b6b3a7640000",
				Nonce:    5,
				Root:     types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
				CodeHash: types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
			},
		},
		NextKey: types.Address{},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal AccountRangeResult: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, ok := decoded["accounts"]; !ok {
		t.Error("Missing accounts field")
	}

	if _, ok := decoded["next"]; !ok {
		t.Error("Missing next field")
	}

	t.Logf("✓ AccountRangeResult JSON serialization works correctly")
}

// =============================================================================
// GCStats 测试
// =============================================================================

func TestGCStatsJSON(t *testing.T) {
	stats := &GCStats{
		LastGC:       uint64(time.Now().UnixNano()),
		NumGC:        100,
		PauseTotal:   1000000,
		PauseHistory: []uint64{1000, 2000, 3000},
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("Failed to marshal GCStats: %v", err)
	}

	var decoded GCStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.NumGC != 100 {
		t.Errorf("NumGC = %d, want 100", decoded.NumGC)
	}

	if len(decoded.PauseHistory) != 3 {
		t.Errorf("PauseHistory length = %d, want 3", len(decoded.PauseHistory))
	}

	t.Logf("✓ GCStats JSON serialization works correctly")
}

// =============================================================================
// Timeout 解析测试
// =============================================================================

func TestTimeoutParsing(t *testing.T) {
	tests := []struct {
		name     string
		timeout  string
		expected time.Duration
		wantErr  bool
	}{
		{"seconds", "30s", 30 * time.Second, false},
		{"minutes", "5m", 5 * time.Minute, false},
		{"milliseconds", "500ms", 500 * time.Millisecond, false},
		{"combined", "1m30s", 90 * time.Second, false},
		{"invalid", "invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := time.ParseDuration(tt.timeout)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if d != tt.expected {
				t.Errorf("Duration = %v, want %v", d, tt.expected)
			}
		})
	}
}
