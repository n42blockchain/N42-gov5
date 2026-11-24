// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncanchor

import "testing"

// On a Profile-1 chain the mode is Enforce and nothing can move it back to
// observe or off (standard §3).
func TestEnforceForProfile1PinsEnforce(t *testing.T) {
	prev := current.Load()
	t.Cleanup(func() {
		profile1Enforced.Store(false)
		current.Store(prev)
	})
	current.Store(int32(Observe))
	if Current() != Observe {
		t.Fatalf("precondition: mode %v", Current())
	}
	EnforceForProfile1()
	if Current() != Enforce {
		t.Fatalf("mode %v after EnforceForProfile1, want enforce", Current())
	}
	for _, m := range []Mode{Off, Observe} {
		restore := SetForTest(m)
		if Current() != Enforce {
			t.Fatalf("mode %v reachable on profile 1", m)
		}
		restore()
	}
	current.Store(int32(Off))
	if Current() != Enforce {
		t.Fatal("a direct store moved the profile-1 mode off enforce")
	}
}

// Without the profile call the env-selected mode is untouched.
func TestCurrentUnpinnedWithoutProfile1(t *testing.T) {
	if profile1Enforced.Load() {
		t.Fatal("profile-1 pin set without EnforceForProfile1")
	}
	restore := SetForTest(Off)
	defer restore()
	if Current() != Off {
		t.Fatalf("mode %v, want off on a non-profile chain", Current())
	}
}
