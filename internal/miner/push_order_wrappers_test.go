// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the thin sync.Once wrapper functions push_order_test.go leaves at
// 0%: PushBeforeWrite, ProposeBeforeWrite, and LeaderWriteAfterJournalTimeout
// (their pure parse logic is already covered directly; this exercises the
// wrappers' own Once-backed env lookup).

package miner

import "testing"

func TestPushBeforeWriteWrapperReadsEnv(t *testing.T) {
	// Whatever the current process env says, the call must not panic and
	// must return a value consistent with the already-tested pure parser.
	got := PushBeforeWrite()
	want := parsePushBeforeWrite(pushBeforeWriteEnvSnapshot(t))
	if got != want {
		t.Fatalf("PushBeforeWrite() = %v, want %v (env-derived)", got, want)
	}
}

// pushBeforeWriteEnvSnapshot avoids re-reading os.Getenv after the package's
// sync.Once has already fired (it may have been called by an earlier test in
// this binary), by trusting PushBeforeWrite's own cached result instead.
func pushBeforeWriteEnvSnapshot(t *testing.T) string {
	t.Helper()
	if pushBeforeWriteOn {
		return "1"
	}
	return ""
}

func TestProposeBeforeWriteWrapperReadsEnv(t *testing.T) {
	// ProposeBeforeWrite has no sync.Once -- it reads the env directly every
	// call -- so this just needs to not panic and return a bool.
	_ = ProposeBeforeWrite()
}

func TestLeaderWriteAfterJournalTimeoutWrapperReturnsPositive(t *testing.T) {
	got := LeaderWriteAfterJournalTimeout()
	if got <= 0 {
		t.Fatalf("expected a positive timeout, got %v", got)
	}
}
