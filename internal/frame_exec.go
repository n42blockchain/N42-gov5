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
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
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

// FrameSkipReason classifies a skipped frame transaction for the per-block
// skip receipts log (S89).
type FrameSkipReason uint8

const (
	SkipStaleNonce   FrameSkipReason = iota // nonce below the account's
	SkipFutureNonce                         // nonce above the account's (a gap)
	SkipCannotPay                           // balance short of gas * price + value
	SkipIntrinsicGas                        // gas limit under the intrinsic gas
	SkipFeeCap                              // fee cap under the base fee, or tip/cap out of range
	SkipNonEOA                              // sender has code
	SkipBadSig                              // signature or chain id does not recover a sender
	SkipOther                               // nonce max, gas overflow, unsupported type
	numFrameSkipReasons
)

var frameSkipReasonNames = [numFrameSkipReasons]string{
	"staleNonce", "futureNonce", "cannotPay", "intrinsicGas", "feeCap", "nonEOA", "badSig", "other",
}

// ClassifyFrameSkip returns the reason of a skip IsFrameSkippable accepted.
// A skippable error matching none of the listed classes is the sender
// recovery failure, i.e. a bad signature.
func ClassifyFrameSkip(err error) FrameSkipReason {
	switch {
	case errors.Is(err, ErrNonceTooLow):
		return SkipStaleNonce
	case errors.Is(err, ErrNonceTooHigh):
		return SkipFutureNonce
	case errors.Is(err, ErrInsufficientFunds), errors.Is(err, ErrInsufficientFundsForTransfer):
		return SkipCannotPay
	case errors.Is(err, ErrIntrinsicGas):
		return SkipIntrinsicGas
	case errors.Is(err, ErrFeeCapTooLow), errors.Is(err, ErrTipAboveFeeCap),
		errors.Is(err, ErrTipVeryHigh), errors.Is(err, ErrFeeCapVeryHigh):
		return SkipFeeCap
	case errors.Is(err, ErrSenderNoEOA):
		return SkipNonEOA
	case errors.Is(err, transaction.ErrInvalidSig), errors.Is(err, transaction.ErrInvalidChainId):
		return SkipBadSig
	case errors.Is(err, ErrNonceMax), errors.Is(err, ErrGasUintOverflow),
		errors.Is(err, ErrTxTypeNotSupported), errors.Is(err, transaction.ErrTxTypeNotSupported):
		return SkipOther
	}
	return SkipBadSig
}

// FrameSkipCounts counts a block's skipped transactions by reason.
type FrameSkipCounts [numFrameSkipReasons]int

// Add counts one skip with error err.
func (c *FrameSkipCounts) Add(err error) { c[ClassifyFrameSkip(err)]++ }

// Total returns the number of skips counted.
func (c *FrameSkipCounts) Total() int {
	n := 0
	for _, v := range c {
		n += v
	}
	return n
}

// LogFrameSkips writes the per-block skip receipts line of a frame block:
// Info when anything was skipped, Debug otherwise. role is "import" or
// "build".
func LogFrameSkips(role string, number uint64, txs, skipped int, c *FrameSkipCounts) {
	fields := make([]interface{}, 0, 8+2*int(numFrameSkipReasons))
	fields = append(fields, "role", role, "n", number, "txs", txs, "skipped", skipped)
	for i, name := range frameSkipReasonNames {
		fields = append(fields, name, c[i])
	}
	if skipped > 0 {
		log.Info("frame block skips", fields...)
	} else {
		log.Debug("frame block skips", fields...)
	}
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
	r.SetBloom(block.CreateBloom(block.Receipts{r}))
	return r
}

// FrameBuildResult is the builder's view of a frame build run.
type FrameBuildResult struct {
	Included []*transaction.Transaction
	Receipts block.Receipts
	UsedGas  uint64
	Skipped  int
	Reasons  FrameSkipCounts
}

// BuildParallelFrames runs the leader's Block-STM build over the
// transactions of whole frames (StateProcessor.BuildParallelFrames).
// spans (optional, FrameSpansFor) name the frames txs decode from.
func (bc *BlockChain) BuildParallelFrames(header *block.Header, txs []*transaction.Transaction, spans []FrameSpan, ibs *state.IntraBlockState, blockHashFunc func(n uint64) types.Hash) (*FrameBuildResult, error) {
	sp, ok := bc.process.(*StateProcessor)
	if !ok || sp == nil {
		return nil, ErrParallelNotApplicable
	}
	run, err := sp.BuildParallelFrames(header, txs, spans, ibs, blockHashFunc)
	if err != nil {
		return nil, err
	}
	return &FrameBuildResult{Included: run.Included, Receipts: run.Receipts, UsedGas: run.UsedGas, Skipped: run.Skipped, Reasons: run.SkipReasons}, nil
}
