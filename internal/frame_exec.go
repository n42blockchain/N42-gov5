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

package internal

import (
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
)

// The skip rule (consensus, frame-referenced blocks only;
// docs/QS_MULTI_MILLION_DESIGN.md P1b).
//
// A frame block's transactions were admitted on the gateway signature
// alone, so nonce order and funding are first checked at execution. In a
// frame block a transaction that fails its pre-execution validity check --
// a stale or future nonce, a sender that cannot pay gas * price + value or
// the intrinsic gas, a fee cap under the base fee, a sender that is not an
// EOA, or a signature that does not recover -- is SKIPPED, not fatal: it
// stays in the block, gets a receipt with status 0, gas used 0 and the
// cumulative gas of the transaction before it, changes no state and pays no
// fee. Leader and followers apply it to the same state, so they agree on
// every receipt and on the root. Any other execution error still fails the
// block. Blocks with transaction bodies keep today's rule (any failure
// fails the block).

var frameSkippableErrs = []error{
	ErrNonceTooLow,
	ErrNonceTooHigh,
	ErrNonceMax,
	ErrInsufficientFunds,
	ErrInsufficientFundsForTransfer,
	ErrIntrinsicGas,
	ErrGasUintOverflow,
	ErrFeeCapTooLow,
	ErrTipAboveFeeCap,
	ErrTipVeryHigh,
	ErrFeeCapVeryHigh,
	ErrSenderNoEOA,
	ErrTxTypeNotSupported,
	transaction.ErrInvalidSig,
	transaction.ErrInvalidChainId,
	transaction.ErrTxTypeNotSupported,
}

// IsFrameSkippable reports whether err, from applying tx in a frame block,
// is a skip under the skip rule. A sender that does not recover under
// signer is a skip whatever error text the recovery produced.
func IsFrameSkippable(err error, tx *transaction.Transaction, signer transaction.Signer) bool {
	if err == nil {
		return false
	}
	for _, e := range frameSkippableErrs {
		if errors.Is(err, e) {
			return true
		}
	}
	if signer != nil && tx != nil && tx.From() == nil {
		if _, serr := transaction.Sender(signer, tx); serr != nil {
			return true
		}
	}
	return false
}

// SkippedTxReceipt is the receipt of a skipped transaction at index in a
// block whose cumulative gas before it is cumGas.
func SkippedTxReceipt(tx *transaction.Transaction, header *block.Header, index int, cumGas uint64) *block.Receipt {
	r := &block.Receipt{
		Type:              tx.Type(),
		Status:            block.ReceiptStatusFailed,
		CumulativeGasUsed: cumGas,
		TxHash:            tx.Hash(),
		GasUsed:           0,
		BlockNumber:       header.Number,
		TransactionIndex:  uint(index), // #nosec G115 -- block index
	}
	r.Bloom = block.CreateBloom(block.Receipts{r})
	return r
}
