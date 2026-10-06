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

package tracers

import "testing"

func TestStateTrackerCallReleasesInvokesAndClears(t *testing.T) {
	tracker := newStateTracker(3, 0)
	var calls int
	tracker.releaseState(0, func() { calls++ })
	tracker.releaseState(1, func() { calls++ })

	tracker.callReleases()
	if calls != 2 {
		t.Fatalf("expected 2 release functions invoked, got %d", calls)
	}
	if len(tracker.releases) != 0 {
		t.Fatalf("expected releases cleared after callReleases, got %d", len(tracker.releases))
	}

	// Calling again with no new releases should be a no-op.
	tracker.callReleases()
	if calls != 2 {
		t.Fatalf("expected no additional calls, got %d", calls)
	}
}

func TestStateTrackerReleaseStateIgnoresBelowOldest(t *testing.T) {
	tracker := newStateTracker(3, 5)
	tracker.releaseState(2, func() { t.Fatal("release should not be called for stale number") })
	if len(tracker.releases) != 0 {
		t.Fatalf("expected no release recorded for number below oldest")
	}
}

func TestStateTrackerReleaseStateIgnoresOutOfRange(t *testing.T) {
	tracker := newStateTracker(3, 0)
	tracker.releaseState(10, func() { t.Fatal("release should not be called for out-of-range number") })
	if len(tracker.releases) != 0 {
		t.Fatalf("expected no release recorded for out-of-range number")
	}
}

func TestStateTrackerWaitErrorsBelowOldest(t *testing.T) {
	tracker := newStateTracker(3, 5)
	if err := tracker.wait(2); err == nil {
		t.Fatalf("expected error for number below oldest")
	}
}
