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
func Wrapf(err error, format string, args ...interface{}) error {
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
func As(err error, target interface{}) bool {
	return errors.As(err, target)
}

// New returns an error that formats as the given text.
func New(text string) error {
	return errors.New(text)
}

// Errorf formats according to a format specifier and returns the string as a value that satisfies error.
func Errorf(format string, a ...interface{}) error {
	return fmt.Errorf(format, a...)
}

