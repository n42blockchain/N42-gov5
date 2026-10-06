package main

import "testing"

// TestWaitForBarrierPollsUntilExists: exists() returning false a fixed number
// of times before true must produce exactly that many sleeps, and return
// only once exists() finally reports true.
func TestWaitForBarrierPollsUntilExists(t *testing.T) {
	existsCalls, sleepCalls, onWaitCalls := 0, 0, 0
	falseUntil := 4

	waitForBarrier(
		func() bool {
			existsCalls++
			return existsCalls > falseUntil
		},
		func() { sleepCalls++ },
		func() { onWaitCalls++ },
	)

	if onWaitCalls != 1 {
		t.Fatalf("onWait called %d times, want exactly 1", onWaitCalls)
	}
	if sleepCalls != falseUntil {
		t.Fatalf("sleep called %d times, want %d (one per false exists() before the true)", sleepCalls, falseUntil)
	}
	if existsCalls != falseUntil+1 {
		t.Fatalf("exists called %d times, want %d", existsCalls, falseUntil+1)
	}
}

// TestWaitForBarrierAlreadyExists: if the file already exists on the first
// check, waitForBarrier must still call onWait exactly once (so "waiting for
// barrier" is always printed, even when the wait turns out to be instant) but
// must sleep zero times.
func TestWaitForBarrierAlreadyExists(t *testing.T) {
	sleepCalls, onWaitCalls := 0, 0

	waitForBarrier(
		func() bool { return true },
		func() { sleepCalls++ },
		func() { onWaitCalls++ },
	)

	if onWaitCalls != 1 {
		t.Fatalf("onWait called %d times, want exactly 1", onWaitCalls)
	}
	if sleepCalls != 0 {
		t.Fatalf("sleep called %d times, want 0 (barrier already up)", sleepCalls)
	}
}

// TestWaitForBarrierOnWaitNotRepeated confirms onWait is called before the
// polling loop, not once per iteration, across a longer poll.
func TestWaitForBarrierOnWaitNotRepeated(t *testing.T) {
	existsCalls, onWaitCalls := 0, 0

	waitForBarrier(
		func() bool {
			existsCalls++
			return existsCalls > 50
		},
		func() {},
		func() { onWaitCalls++ },
	)

	if onWaitCalls != 1 {
		t.Fatalf("onWait called %d times over 50 poll iterations, want exactly 1", onWaitCalls)
	}
}
