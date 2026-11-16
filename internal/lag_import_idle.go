// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "time"

// The lag import's idle (r94a stamp, always on). Run 80 found 7-11% of
// full blocks bound by an import that started well after the previous one
// had finished while the block was already queued, with nothing logged in
// between. These stamps itemise that gap on the next "blockimport phases"
// line:
//
//	impGapUs      the previous lag import's return -> this import's tIter
//	impWaitUs     of it, the loop asleep in its select (kick / 50 ms tick)
//	impWake       what woke the loop last before the pick: kick, tick, or
//	              loop (no sleep: picked in the pass right after)
//	impPasses     lagStep passes since the previous import that imported
//	              nothing (each one ends in a sleep)
//	impSkipWhy    why the last such pass did not take this block: parent
//	              (its parent not applied yet), queued (not queued yet),
//	              other (a lower block was handled first)
//	impQdAfterUs  this block was queued that long after the previous
//	              import's return (0: queued before)
//	impScanUs     the picking pass, from its start to the import call (the
//	              queue sort and the stored/applied checks)
//	impResolveUs  ResolveFrames before bc.lock
//	impLockUs     the wait for bc.lock
//	impPreUs      bc.lock taken -> tIter (header and body checks of the
//	              iterator; hdr/body also count them)
//	impPrevTailUs the previous import from its own log line to its return
//
// Only the lag loop's goroutine touches lagIdle (one loop runs at a time).
type lagIdle struct {
	prevEnd    time.Time // previous lag import returned
	prevLogged time.Time // previous lag import logged its phases line
	prevTail   time.Duration
	wait       time.Duration
	wake       string
	passes     int
	skipWhy    string
	passStart  time.Time
}

// lagIdleSnap is one import's idle split, handed to insertChainMode through
// lagInsert.
type lagIdleSnap struct {
	prevEnd   time.Time
	prevTail  time.Duration
	wait      time.Duration
	wake      string
	passes    int
	skipWhy   string
	qdAfter   time.Duration
	scan      time.Duration
	resolve   time.Duration
	lockWait  time.Duration
	lockAt    time.Time
	loggedAt  time.Time // set by insertChainMode when it logs the line
	loggedSet bool
}

// slept records a select wait of the loop and what ended it.
func (li *lagIdle) slept(d time.Duration, why string) {
	li.wait += d
	li.wake = why
}

// passBegin marks the start of a lagStep pass.
func (li *lagIdle) passBegin() {
	li.passStart = time.Now()
}

// passEmpty records a pass that imported nothing and why the lowest
// candidate was not taken.
func (li *lagIdle) passEmpty(why string) {
	li.passes++
	li.skipWhy = why
}

// pick returns the idle split of the import starting now for a block queued
// at since.
func (li *lagIdle) pick(since time.Time) *lagIdleSnap {
	now := time.Now()
	s := &lagIdleSnap{prevEnd: li.prevEnd, prevTail: li.prevTail, wait: li.wait, wake: li.wake,
		passes: li.passes, skipWhy: li.skipWhy}
	if s.wake == "" {
		s.wake = "loop"
	}
	if !li.passStart.IsZero() {
		s.scan = now.Sub(li.passStart)
	}
	if !li.prevEnd.IsZero() && since.After(li.prevEnd) {
		s.qdAfter = since.Sub(li.prevEnd)
	}
	return s
}

// done records the end of a lag import and resets the split for the next.
func (li *lagIdle) done(s *lagIdleSnap) {
	now := time.Now()
	li.prevEnd = now
	li.prevTail = 0
	if s != nil && s.loggedSet {
		li.prevTail = now.Sub(s.loggedAt)
	}
	li.wait, li.wake, li.passes, li.skipWhy = 0, "", 0, ""
}

// fields returns the stamps for the "blockimport phases" line; tIter is the
// import's own start.
func (s *lagIdleSnap) fields(tIter time.Time) []interface{} {
	if s == nil {
		return nil
	}
	gap := int64(-1)
	if !s.prevEnd.IsZero() {
		gap = tIter.Sub(s.prevEnd).Microseconds()
	}
	pre := int64(0)
	if !s.lockAt.IsZero() {
		pre = tIter.Sub(s.lockAt).Microseconds()
	}
	return []interface{}{"impGapUs", gap, "impWaitUs", s.wait.Microseconds(), "impWake", s.wake,
		"impPasses", s.passes, "impSkipWhy", s.skipWhy, "impQdAfterUs", s.qdAfter.Microseconds(),
		"impScanUs", s.scan.Microseconds(), "impResolveUs", s.resolve.Microseconds(),
		"impLockUs", s.lockWait.Microseconds(), "impPreUs", pre, "impPrevTailUs", s.prevTail.Microseconds()}
}
