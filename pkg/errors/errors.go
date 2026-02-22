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

// Package errors defines common error types used throughout the N42 codebase.
// This package provides a centralized location for error definitions to ensure
// consistency and avoid duplication across modules.
package errors

import (
	"errors"
	"fmt"
)

// =====================
// Block & Chain Errors
// =====================

var (
	// ErrInvalidBlock is returned when a block fails validation.
	ErrInvalidBlock = errors.New("invalid block")

	// ErrBannedHash is returned if a block to import is on the banned list.
	ErrBannedHash = errors.New("banned hash")

	// ErrNoGenesis is returned when there is no Genesis Block.
	ErrNoGenesis = errors.New("genesis not found in chain")

	// ErrGenesisNoConfig is returned when genesis has no chain configuration.
	ErrGenesisNoConfig = errors.New("genesis has no chain configuration")

	// ErrSideChainReceipts is returned when trying to accept side blocks as ancient chain data.
	ErrSideChainReceipts = errors.New("side blocks can't be accepted as ancient chain data")
)

// =====================
// Transaction Errors
// =====================

// Transaction pre-checking errors. All state transition messages will
// be pre-checked before execution. If any invalidation detected, the corresponding
// error should be returned which is defined here.
//
// - If the pre-checking happens in the miner, then the transaction won't be packed.
// - If the pre-checking happens in the block processing procedure, then a "BAD BLOCK"
// error should be emitted.
var (
	// ErrNonceTooLow is returned if the nonce of a transaction is lower than the
	// one present in the local chain.
	ErrNonceTooLow = errors.New("nonce too low")

	// ErrNonceTooHigh is returned if the nonce of a transaction is higher than the
	// next one expected based on the local chain.
	ErrNonceTooHigh = errors.New("nonce too high")

	// ErrNonceMax is returned if the nonce of a transaction sender account has
	// maximum allowed value and would become invalid if incremented.
	ErrNonceMax = errors.New("nonce has max value")

	// ErrGasLimitReached is returned by the gas pool if the amount of gas required
	// by a transaction is higher than what's left in the block.
	ErrGasLimitReached = errors.New("gas limit reached")

	// ErrInsufficientFundsForTransfer is returned if the transaction sender doesn't
	// have enough funds for transfer (topmost call only).
	ErrInsufficientFundsForTransfer = errors.New("insufficient funds for transfer")

	// ErrInsufficientFunds is returned if the total cost of executing a transaction
	// is higher than the balance of the user's account.
	ErrInsufficientFunds = errors.New("insufficient funds for gas * price + value")

	// ErrGasUintOverflow is returned when calculating gas usage.
	ErrGasUintOverflow = errors.New("gas uint64 overflow")

	// ErrIntrinsicGas is returned if the transaction is specified to use less gas
	// than required to start the invocation.
	ErrIntrinsicGas = errors.New("intrinsic gas too low")

	// ErrTxTypeNotSupported is returned if a transaction is not supported in the
	// current network configuration.
	ErrTxTypeNotSupported = errors.New("transaction type not supported")

	// ErrTipAboveFeeCap is a sanity error to ensure no one is able to specify a
	// transaction with a tip higher than the total fee cap.
	ErrTipAboveFeeCap = errors.New("max priority fee per gas higher than max fee per gas")

	// ErrTipVeryHigh is a sanity error to avoid extremely big numbers specified
	// in the tip field.
	ErrTipVeryHigh = errors.New("max priority fee per gas higher than 2^256-1")

	// ErrFeeCapVeryHigh is a sanity error to avoid extremely big numbers specified
	// in the fee cap field.
	ErrFeeCapVeryHigh = errors.New("max fee per gas higher than 2^256-1")

	// ErrFeeCapTooLow is returned if the transaction fee cap is less than the
	// the base fee of the block.
	ErrFeeCapTooLow = errors.New("max fee per gas less than block base fee")

	// ErrSenderNoEOA is returned if the sender of a transaction is a contract.
	ErrSenderNoEOA = errors.New("sender not an eoa")

	// ErrAlreadyDeposited is returned when trying to deposit again.
	ErrAlreadyDeposited = errors.New("already deposited")
)

// =====================
// PubSub & Network Errors
// =====================

var (
	// ErrInvalidPubSub is returned when PubSub is nil.
	ErrInvalidPubSub = errors.New("pubsub is nil")

	// ErrMessageNotMapped is returned when message type is not mapped to a PubSub topic.
	ErrMessageNotMapped = errors.New("message type is not mapped to a PubSub topic")

	// ErrInvalidFetchedData is returned when invalid data is returned from peer.
	ErrInvalidFetchedData = errors.New("invalid data returned from peer")
)

// =====================
// Database Errors
// =====================

var (
	// ErrKeyNotFound is returned when a key is not found in the database.
	ErrKeyNotFound = errors.New("db: key not found")

	// ErrInvalidSize is returned when a number has an invalid size.
	ErrInvalidSize = errors.New("bit endian number has an invalid size")
)

// =====================
// Helper Functions
// =====================

// Wrap wraps an error with additional context.
func Wrap(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

// Wrapf wraps an error with a formatted message.
func Wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

// Is reports whether any error in err's chain matches target.
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As finds the first error in err's chain that matches target.
func As(err error, target any) bool {
	return errors.As(err, target)
}

// New returns an error that formats as the given text.
func New(text string) error {
	return errors.New(text)
}

// Errorf formats according to a format specifier and returns the string as a value that satisfies error.
func Errorf(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}

// =====================
// Error Code System
// =====================

// ErrorCode represents a categorized error code for structured error handling.
type ErrorCode int

const (
	// General errors (0-999)
	CodeUnknown    ErrorCode = 0
	CodeInternal   ErrorCode = 1
	CodeInvalidArg ErrorCode = 2
	CodeNotFound   ErrorCode = 3
	CodeTimeout    ErrorCode = 4
	CodeCancelled  ErrorCode = 5

	// Validation errors (1000-1999)
	CodeValidation       ErrorCode = 1000
	CodeInvalidBlock     ErrorCode = 1001
	CodeInvalidHeader    ErrorCode = 1002
	CodeInvalidTx        ErrorCode = 1003
	CodeInvalidSignature ErrorCode = 1004

	// Consensus errors (2000-2999)
	CodeConsensus        ErrorCode = 2000
	CodeUnknownAncestor  ErrorCode = 2001
	CodePrunedAncestor   ErrorCode = 2002
	CodeFutureBlock      ErrorCode = 2003

	// Execution errors (3000-3999)
	CodeExecution         ErrorCode = 3000
	CodeOutOfGas          ErrorCode = 3001
	CodeDepth             ErrorCode = 3002
	CodeInsufficientFunds ErrorCode = 3003

	// Network errors (4000-4999)
	CodeNetwork        ErrorCode = 4000
	CodePeerDisconnect ErrorCode = 4001
	CodePeerNotFound   ErrorCode = 4002

	// Database errors (5000-5999)
	CodeDatabase    ErrorCode = 5000
	CodeDBRead      ErrorCode = 5001
	CodeDBWrite     ErrorCode = 5002
	CodeDBNotFound  ErrorCode = 5003
)

// String returns the string representation of the error code.
func (c ErrorCode) String() string {
	switch c {
	case CodeUnknown:
		return "UNKNOWN"
	case CodeInternal:
		return "INTERNAL"
	case CodeInvalidArg:
		return "INVALID_ARG"
	case CodeNotFound:
		return "NOT_FOUND"
	case CodeTimeout:
		return "TIMEOUT"
	case CodeCancelled:
		return "CANCELLED"
	case CodeValidation:
		return "VALIDATION"
	case CodeConsensus:
		return "CONSENSUS"
	case CodeExecution:
		return "EXECUTION"
	case CodeNetwork:
		return "NETWORK"
	case CodeDatabase:
		return "DATABASE"
	default:
		return fmt.Sprintf("CODE_%d", int(c))
	}
}

// CodedError represents an error with an associated error code.
type CodedError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Context map[string]any
}

// NewCoded creates a new CodedError with the given code and message.
func NewCoded(code ErrorCode, msg string) *CodedError {
	return &CodedError{
		Code:    code,
		Message: msg,
	}
}

// WrapCoded wraps an existing error with a code.
func WrapCoded(err error, code ErrorCode, msg string) *CodedError {
	if err == nil {
		return nil
	}
	return &CodedError{
		Code:    code,
		Message: msg,
		Cause:   err,
	}
}

// Error implements the error interface.
func (e *CodedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code.String(), e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code.String(), e.Message)
}

// Unwrap returns the underlying cause for use with errors.Is and errors.As.
func (e *CodedError) Unwrap() error {
	return e.Cause
}

// WithContext adds context information to the error.
func (e *CodedError) WithContext(key string, value any) *CodedError {
	if e.Context == nil {
		e.Context = make(map[string]any)
	}
	e.Context[key] = value
	return e
}

// GetCode extracts the error code from an error if it's a CodedError.
// Returns CodeUnknown for other error types.
func GetCode(err error) ErrorCode {
	var e *CodedError
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeUnknown
}

// HasCode checks if an error has a specific error code.
func HasCode(err error, code ErrorCode) bool {
	return GetCode(err) == code
}

