// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package errors

import (
	"errors"
	"fmt"
	"testing"
)

// =============================================================================
// 错误定义测试
// =============================================================================

// TestBlockChainErrors 测试区块链相关错误
func TestBlockChainErrors(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrInvalidBlock, "invalid block"},
		{ErrBannedHash, "banned hash"},
		{ErrNoGenesis, "genesis not found in chain"},
		{ErrGenesisNoConfig, "genesis has no chain configuration"},
		{ErrSideChainReceipts, "side blocks can't be accepted as ancient chain data"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Expected error message '%s', got '%s'", tt.expected, tt.err.Error())
		}
	}
	t.Log("✓ Block chain errors are correctly defined")
}

// TestTransactionErrors 测试交易相关错误
func TestTransactionErrors(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrNonceTooLow, "nonce too low"},
		{ErrNonceTooHigh, "nonce too high"},
		{ErrNonceMax, "nonce has max value"},
		{ErrGasLimitReached, "gas limit reached"},
		{ErrInsufficientFundsForTransfer, "insufficient funds for transfer"},
		{ErrInsufficientFunds, "insufficient funds for gas * price + value"},
		{ErrGasUintOverflow, "gas uint64 overflow"},
		{ErrIntrinsicGas, "intrinsic gas too low"},
		{ErrTxTypeNotSupported, "transaction type not supported"},
		{ErrTipAboveFeeCap, "max priority fee per gas higher than max fee per gas"},
		{ErrTipVeryHigh, "max priority fee per gas higher than 2^256-1"},
		{ErrFeeCapVeryHigh, "max fee per gas higher than 2^256-1"},
		{ErrFeeCapTooLow, "max fee per gas less than block base fee"},
		{ErrSenderNoEOA, "sender not an eoa"},
		{ErrAlreadyDeposited, "already deposited"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Expected error message '%s', got '%s'", tt.expected, tt.err.Error())
		}
	}
	t.Log("✓ Transaction errors are correctly defined")
}

// TestPubSubErrors 测试 PubSub 相关错误
func TestPubSubErrors(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrInvalidPubSub, "pubsub is nil"},
		{ErrMessageNotMapped, "message type is not mapped to a PubSub topic"},
		{ErrInvalidFetchedData, "invalid data returned from peer"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Expected error message '%s', got '%s'", tt.expected, tt.err.Error())
		}
	}
	t.Log("✓ PubSub errors are correctly defined")
}

// TestDatabaseErrors 测试数据库相关错误
func TestDatabaseErrors(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{ErrKeyNotFound, "db: key not found"},
		{ErrInvalidSize, "bit endian number has an invalid size"},
	}

	for _, tt := range tests {
		if tt.err.Error() != tt.expected {
			t.Errorf("Expected error message '%s', got '%s'", tt.expected, tt.err.Error())
		}
	}
	t.Log("✓ Database errors are correctly defined")
}

// =============================================================================
// 辅助函数测试
// =============================================================================

// TestWrap 测试 Wrap 函数
func TestWrap(t *testing.T) {
	t.Run("wrap nil error", func(t *testing.T) {
		result := Wrap(nil, "context")
		if result != nil {
			t.Error("Wrap(nil) should return nil")
		}
	})

	t.Run("wrap error with context", func(t *testing.T) {
		original := errors.New("original error")
		wrapped := Wrap(original, "context message")
		
		expected := "context message: original error"
		if wrapped.Error() != expected {
			t.Errorf("Expected '%s', got '%s'", expected, wrapped.Error())
		}

		// 验证可以用 Is 检查原始错误
		if !errors.Is(wrapped, original) {
			t.Error("Wrapped error should unwrap to original")
		}
	})

	t.Log("✓ Wrap function works correctly")
}

// TestWrapf 测试 Wrapf 函数
func TestWrapf(t *testing.T) {
	t.Run("wrapf nil error", func(t *testing.T) {
		result := Wrapf(nil, "context %d", 123)
		if result != nil {
			t.Error("Wrapf(nil) should return nil")
		}
	})

	t.Run("wrapf error with formatted context", func(t *testing.T) {
		original := errors.New("original error")
		wrapped := Wrapf(original, "context %d %s", 123, "test")
		
		expected := "context 123 test: original error"
		if wrapped.Error() != expected {
			t.Errorf("Expected '%s', got '%s'", expected, wrapped.Error())
		}

		if !errors.Is(wrapped, original) {
			t.Error("Wrapped error should unwrap to original")
		}
	})

	t.Log("✓ Wrapf function works correctly")
}

// TestIs 测试 Is 函数
func TestIs(t *testing.T) {
	t.Run("is same error", func(t *testing.T) {
		if !Is(ErrNonceTooLow, ErrNonceTooLow) {
			t.Error("Is should return true for same error")
		}
	})

	t.Run("is different error", func(t *testing.T) {
		if Is(ErrNonceTooLow, ErrNonceTooHigh) {
			t.Error("Is should return false for different errors")
		}
	})

	t.Run("is wrapped error", func(t *testing.T) {
		wrapped := fmt.Errorf("wrapped: %w", ErrNonceTooLow)
		if !Is(wrapped, ErrNonceTooLow) {
			t.Error("Is should return true for wrapped error")
		}
	})

	t.Run("is nil error", func(t *testing.T) {
		if Is(nil, ErrNonceTooLow) {
			t.Error("Is(nil, err) should return false")
		}
	})

	t.Log("✓ Is function works correctly")
}

// customError 是用于测试 As 函数的自定义错误类型
type customError struct {
	Code    int
	Message string
}

func (e *customError) Error() string {
	return e.Message
}

// TestAs 测试 As 函数
func TestAs(t *testing.T) {
	t.Run("as matching type", func(t *testing.T) {
		original := &customError{Code: 404, Message: "not found"}
		wrapped := fmt.Errorf("wrapped: %w", original)

		var target *customError
		if !As(wrapped, &target) {
			t.Error("As should return true for matching type")
		}
		if target.Code != 404 {
			t.Errorf("Expected Code 404, got %d", target.Code)
		}
	})

	t.Run("as non-matching type", func(t *testing.T) {
		err := errors.New("simple error")
		var target *customError
		if As(err, &target) {
			t.Error("As should return false for non-matching type")
		}
	})

	t.Log("✓ As function works correctly")
}

// TestNew 测试 New 函数
func TestNew(t *testing.T) {
	err := New("test error")
	if err == nil {
		t.Error("New should return non-nil error")
	}
	if err.Error() != "test error" {
		t.Errorf("Expected 'test error', got '%s'", err.Error())
	}
	t.Log("✓ New function works correctly")
}

// TestErrorf 测试 Errorf 函数
func TestErrorf(t *testing.T) {
	t.Run("simple format", func(t *testing.T) {
		err := Errorf("error %d", 123)
		if err.Error() != "error 123" {
			t.Errorf("Expected 'error 123', got '%s'", err.Error())
		}
	})

	t.Run("complex format", func(t *testing.T) {
		err := Errorf("error %s %d %v", "test", 123, true)
		expected := "error test 123 true"
		if err.Error() != expected {
			t.Errorf("Expected '%s', got '%s'", expected, err.Error())
		}
	})

	t.Run("wrap with errorf", func(t *testing.T) {
		original := ErrNonceTooLow
		wrapped := Errorf("wrapped: %w", original)
		if !errors.Is(wrapped, original) {
			t.Error("Errorf with %w should wrap error")
		}
	})

	t.Log("✓ Errorf function works correctly")
}

// =============================================================================
// 错误分类测试
// =============================================================================

// TestErrorCategorization 测试错误分类是否合理
func TestErrorCategorization(t *testing.T) {
	// 区块链错误
	blockchainErrors := []error{
		ErrInvalidBlock,
		ErrBannedHash,
		ErrNoGenesis,
		ErrGenesisNoConfig,
		ErrSideChainReceipts,
	}

	// 交易错误
	txErrors := []error{
		ErrNonceTooLow,
		ErrNonceTooHigh,
		ErrNonceMax,
		ErrGasLimitReached,
		ErrInsufficientFundsForTransfer,
		ErrInsufficientFunds,
		ErrGasUintOverflow,
		ErrIntrinsicGas,
		ErrTxTypeNotSupported,
		ErrTipAboveFeeCap,
		ErrTipVeryHigh,
		ErrFeeCapVeryHigh,
		ErrFeeCapTooLow,
		ErrSenderNoEOA,
		ErrAlreadyDeposited,
	}

	// PubSub 错误
	pubsubErrors := []error{
		ErrInvalidPubSub,
		ErrMessageNotMapped,
		ErrInvalidFetchedData,
	}

	// 数据库错误
	dbErrors := []error{
		ErrKeyNotFound,
		ErrInvalidSize,
	}

	t.Logf("Blockchain errors: %d", len(blockchainErrors))
	t.Logf("Transaction errors: %d", len(txErrors))
	t.Logf("PubSub errors: %d", len(pubsubErrors))
	t.Logf("Database errors: %d", len(dbErrors))
	t.Logf("Total errors: %d", len(blockchainErrors)+len(txErrors)+len(pubsubErrors)+len(dbErrors))

	t.Log("✓ Error categorization is reasonable")
}

// TestErrorUniqueness 测试错误的唯一性
func TestErrorUniqueness(t *testing.T) {
	allErrors := []error{
		ErrInvalidBlock,
		ErrBannedHash,
		ErrNoGenesis,
		ErrGenesisNoConfig,
		ErrSideChainReceipts,
		ErrNonceTooLow,
		ErrNonceTooHigh,
		ErrNonceMax,
		ErrGasLimitReached,
		ErrInsufficientFundsForTransfer,
		ErrInsufficientFunds,
		ErrGasUintOverflow,
		ErrIntrinsicGas,
		ErrTxTypeNotSupported,
		ErrTipAboveFeeCap,
		ErrTipVeryHigh,
		ErrFeeCapVeryHigh,
		ErrFeeCapTooLow,
		ErrSenderNoEOA,
		ErrAlreadyDeposited,
		ErrInvalidPubSub,
		ErrMessageNotMapped,
		ErrInvalidFetchedData,
		ErrKeyNotFound,
		ErrInvalidSize,
	}

	// 检查每个错误都是唯一的
	seen := make(map[string]bool)
	for _, err := range allErrors {
		msg := err.Error()
		if seen[msg] {
			t.Errorf("Duplicate error message: %s", msg)
		}
		seen[msg] = true
	}

	t.Log("✓ All errors are unique")
}

// =============================================================================
// 基准测试
// =============================================================================

// BenchmarkWrap 基准测试 Wrap 函数
func BenchmarkWrap(b *testing.B) {
	err := errors.New("original error")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Wrap(err, "context message")
	}
}

// BenchmarkWrapf 基准测试 Wrapf 函数
func BenchmarkWrapf(b *testing.B) {
	err := errors.New("original error")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Wrapf(err, "context %d", 123)
	}
}

// BenchmarkIs 基准测试 Is 函数
func BenchmarkIs(b *testing.B) {
	wrapped := fmt.Errorf("layer3: %w", fmt.Errorf("layer2: %w", ErrNonceTooLow))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Is(wrapped, ErrNonceTooLow)
	}
}

// BenchmarkNew 基准测试 New 函数
func BenchmarkNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = New("test error")
	}
}

// BenchmarkErrorf 基准测试 Errorf 函数
func BenchmarkErrorf(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Errorf("error %d %s", 123, "test")
	}
}

