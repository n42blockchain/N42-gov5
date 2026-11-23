// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package consensus

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

// ErrConflictingCommit marks a commit (a CommitQC target handed to
// canonicalization) that does not extend this node's committed chain: it
// names a different block at or below the committed height, or descends from
// one. With a correct quorum this cannot happen; it is evidence of a safety
// failure upstream and must fail closed (refuse, alert, stop participating),
// never rewrite committed history.
var ErrConflictingCommit = errors.New("commit conflicts with the committed chain")

// ConflictingCommitError carries the evidence of a conflicting commit.
type ConflictingCommitError struct {
	// Target is the block the incoming commit names, at TargetNumber.
	Target       types.Hash
	TargetNumber uint64
	// At Height the committed chain holds Committed while the incoming
	// commit's lineage holds Incoming.
	Height    uint64
	Committed types.Hash
	Incoming  types.Hash
	// CommittedHead is the QC-backed committed head (HotStuffCommittedHead).
	CommittedHead       types.Hash
	CommittedHeadNumber uint64
}

func (e *ConflictingCommitError) Error() string {
	return fmt.Sprintf("%v: commit of %d/%x conflicts at height %d (committed %x, incoming lineage %x; committed head %d/%x)",
		ErrConflictingCommit, e.TargetNumber, e.Target[:8], e.Height, e.Committed[:8], e.Incoming[:8],
		e.CommittedHeadNumber, e.CommittedHead[:8])
}

// Unwrap lets errors.Is(err, ErrConflictingCommit) match.
func (e *ConflictingCommitError) Unwrap() error { return ErrConflictingCommit }
