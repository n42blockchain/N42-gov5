// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "time"

// RootPrepPhases is the split of the most recent root computation through
// a RootComputer (computeRootViaComputer), for the import phases line: the
// span from IntermediateRoot's entry to the post-state hook (the lag
// pipeline launches the child there) and the root computer after it.
//
//	Pending  pendingIncreaseAddrs (the unfolded credits, sorted), or the
//	         fold keys' merge
//	Detach   detachPendingCredits (P4b, parallel prefetch reads)
//	Mat      materialising the credits that were not detached
//	Merge    the journal dirty merge into stateObjectsDirty
//	Sort     sortedAddresses(stateObjectsDirty)
//	Loop     the dirty-object walk (accounts, storage, post-state notes)
//	Tail     the detached accounts appended to the dirty set
//	Hook     the before-root and post-state hooks (the child's launch)
//	Compute  the root computer itself
type RootPrepPhases struct {
	Enter                                   time.Time
	Pending, Detach, Mat, Merge, Sort, Loop time.Duration
	Tail, Hook, Compute                     time.Duration
	Dirty                                   int // dirty objects walked
	// FoldKeys: 1 when both sorted sets came from the fold's keys, -1 when
	// the keys were tracked but a set fell back to its map, 0 untracked
	// (N42_FINALIZE_FOLD_KEYS, finalize_fold_keys.go).
	FoldKeys int
}

// LastRootPrepPhases returns the split of this state's most recent root
// computation through a RootComputer (zero before any).
func (s *IntraBlockState) LastRootPrepPhases() RootPrepPhases {
	return s.rootPrep
}

// stepSince returns the time since t and now, for the next step.
func stepSince(t time.Time) (time.Duration, time.Time) {
	now := time.Now()
	return now.Sub(t), now
}
