// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import "testing"

// The v3 rebuild passes --sched and --sto-sched together. An earlier wiring
// applied --sto-sched first and then let --sched overwrite the whole struct,
// so the storage tries silently ran on the account ladder: a proof at a deep
// storage level then re-folded every child changed in a 16384-block window.
// The banner was the only symptom, and it printed the account ladder.
func TestResolveScheduleKeepsStorageLadder(t *testing.T) {
	const (
		accStr = "1024,16384,1024,4096,4096,4194304"
		stoStr = "1024,1024,1024,1024,4096,4096"
	)
	wantAcc := [maxChgDepth + 1]uint64{1024, 16384, 1024, 4096, 4096, 4194304}
	wantSto := [maxChgDepth + 1]uint64{1024, 1024, 1024, 1024, 4096, 4096}

	s, err := resolveSchedule(16, 20, accStr, stoStr, 1)
	if err != nil {
		t.Fatalf("resolveSchedule: %v", err)
	}
	if s.e != wantAcc {
		t.Fatalf("account ladder = %v, want %v", s.e, wantAcc)
	}
	if s.sto != wantSto {
		t.Fatalf("storage ladder = %v, want %v", s.sto, wantSto)
	}
	if s.accRoot != 1 {
		t.Fatalf("accRoot = %d, want 1", s.accRoot)
	}
	// lenFor is what build and verify actually consult, and what the banner
	// and DatcMeta/stosched are written from.
	for d := 0; d <= maxChgDepth; d++ {
		if got := s.lenFor(true, d); got != wantSto[d] {
			t.Errorf("lenFor(storage, %d) = %d, want %d", d, got, wantSto[d])
		}
	}
	for d := 1; d <= maxChgDepth; d++ {
		if got := s.lenFor(false, d); got != wantAcc[d] {
			t.Errorf("lenFor(account, %d) = %d, want %d", d, got, wantAcc[d])
		}
	}
	if got := s.lenFor(false, 0); got != 1 {
		t.Errorf("lenFor(account, 0) = %d, want the accRoot cadence 1", got)
	}
}

// Without --sto-sched the storage tries keep inheriting the account ladder,
// which is how every format-2 archive was built.
func TestResolveScheduleStorageDefaultsToAccountLadder(t *testing.T) {
	s, err := resolveSchedule(16, 20, "1024,16384,1024,1,4194304,4194304", "", 1)
	if err != nil {
		t.Fatalf("resolveSchedule: %v", err)
	}
	if s.sto != ([maxChgDepth + 1]uint64{}) {
		t.Fatalf("storage ladder = %v, want all zero (inherit)", s.sto)
	}
	for d := 1; d <= maxChgDepth; d++ {
		if got, want := s.lenFor(true, d), s.e[d]; got != want {
			t.Errorf("lenFor(storage, %d) = %d, want the account %d", d, got, want)
		}
	}
}

func TestResolveScheduleRejectsBadLadders(t *testing.T) {
	if _, err := resolveSchedule(16, 20, "1024,16384", "", 1); err == nil {
		t.Error("--sched with too few levels: want an error")
	}
	if _, err := resolveSchedule(16, 20, "", "1024,1024", 1); err == nil {
		t.Error("--sto-sched with too few levels: want an error")
	}
}
