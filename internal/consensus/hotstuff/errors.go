// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

// Consensus error types for the HotStuff-2 protocol.
var (
	ErrOutputChannelClosed = fmt.Errorf("consensus output channel full or closed")
	ErrInvalidMessage      = fmt.Errorf("invalid or corrupted consensus message")
	ErrValidatorRemoved    = fmt.Errorf("validator has been removed from the active set")
	ErrEpochScheduleEmpty  = fmt.Errorf("epoch schedule must not be empty")
)

// InvalidSignatureError indicates a BLS signature verification failure.
type InvalidSignatureError struct {
	View           ViewNumber
	ValidatorIndex ValidatorIndex
}

func (e *InvalidSignatureError) Error() string {
	return fmt.Sprintf("invalid BLS signature from validator %d in view %d", e.ValidatorIndex, e.View)
}

// InvalidProposerError indicates the message sender is not the expected leader.
type InvalidProposerError struct {
	View     ViewNumber
	Expected ValidatorIndex
	Actual   ValidatorIndex
}

func (e *InvalidProposerError) Error() string {
	return fmt.Sprintf("invalid proposer: expected validator %d, got %d in view %d", e.Expected, e.Actual, e.View)
}

// InsufficientVotesError indicates not enough votes to form a quorum.
type InsufficientVotesError struct {
	View ViewNumber
	Have int
	Need int
}

func (e *InsufficientVotesError) Error() string {
	return fmt.Sprintf("insufficient votes: have %d, need %d in view %d", e.Have, e.Need, e.View)
}

// ViewMismatchError indicates the message view doesn't match the current view.
type ViewMismatchError struct {
	Current  ViewNumber
	Received ViewNumber
}

func (e *ViewMismatchError) Error() string {
	return fmt.Sprintf("view mismatch: current view is %d, message view is %d", e.Current, e.Received)
}

// DuplicateVoteError indicates a duplicate vote from the same validator.
type DuplicateVoteError struct {
	View           ViewNumber
	ValidatorIndex ValidatorIndex
}

func (e *DuplicateVoteError) Error() string {
	return fmt.Sprintf("duplicate vote from validator %d in view %d", e.ValidatorIndex, e.View)
}

// InvalidQCError indicates an invalid quorum certificate.
type InvalidQCError struct {
	View   ViewNumber
	Reason string
}

func (e *InvalidQCError) Error() string {
	return fmt.Sprintf("invalid quorum certificate for view %d: %s", e.View, e.Reason)
}

// InvalidTCError indicates an invalid timeout certificate.
type InvalidTCError struct {
	View   ViewNumber
	Reason string
}

func (e *InvalidTCError) Error() string {
	return fmt.Sprintf("invalid timeout certificate for view %d: %s", e.View, e.Reason)
}

// UnknownValidatorError indicates a validator index is out of bounds.
type UnknownValidatorError struct {
	Index   ValidatorIndex
	SetSize uint32
}

func (e *UnknownValidatorError) Error() string {
	return fmt.Sprintf("unknown validator index %d, set size is %d", e.Index, e.SetSize)
}

// SafetyViolationError indicates a proposal doesn't extend the locked QC.
type SafetyViolationError struct {
	QCView     ViewNumber
	LockedView ViewNumber
}

func (e *SafetyViolationError) Error() string {
	return fmt.Sprintf("safety violation: proposal QC view %d does not extend locked QC view %d", e.QCView, e.LockedView)
}

// BlockHashMismatchError indicates a block hash mismatch in QC.
type BlockHashMismatchError struct {
	Expected types.Hash
	Got      types.Hash
}

func (e *BlockHashMismatchError) Error() string {
	return fmt.Sprintf("block hash mismatch: expected %s, got %s", e.Expected.Hex(), e.Got.Hex())
}

// DAVerificationError is returned when a block's actual transaction root does not
// match the TxRootHash committed in the proposal (Baby Raptr DA check).
type DAVerificationError struct {
	BlockHash    types.Hash
	ExpectedRoot types.Hash
	ActualRoot   types.Hash
}

func (e *DAVerificationError) Error() string {
	return fmt.Sprintf("DA verification failed for block %s: expected tx root %s, actual %s",
		e.BlockHash.Hex(), e.ExpectedRoot.Hex(), e.ActualRoot.Hex())
}
