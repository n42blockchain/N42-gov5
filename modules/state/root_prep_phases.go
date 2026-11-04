// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import "time"

// RootPrepPhases is the split of the most recent root computation through
// a RootComputer (computeRootViaComputer), for the import phases line: the
// span from IntermediateRoot's entry to the post-state hook (the lag
// pipeline launches the child there) and the root computer after it.
//
//	Pending  pendingIncreaseAddrs (the unfolded credits, sorted)
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
	// Hoisted: Pending and Detach came from the hoisted preparation
	// (N42_FINALIZE_HOIST_DETACH); Detach is then the whole step.
	Hoisted bool
	// The hoisted Detach's parts: HoistWait the root's wait for the hoisted
	// goroutine, HoistRun that goroutine's run (from its arm), HoistGap
	// the time from its arm to the root's take.
	HoistWait, HoistRun, HoistGap time.Duration
	// Objs: the parallel walk's object resolution (part of Loop).
	Objs time.Duration
	// Cut: N42_PREHOOK_CUT moved the detached credits' inserts to their own
	// goroutine; CredIns its run (from the start, before Sort), CredJoin
	// the walk's wait for it (part of Loop).
	Cut               bool
	CredIns, CredJoin time.Duration
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
