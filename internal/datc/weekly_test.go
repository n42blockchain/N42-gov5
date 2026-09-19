// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"strings"
	"testing"
)

// TestWeeklyPlan pins the order of the weekly steps and the flags that make
// them about the NEW blocks: the ladder is extended before anything reads it,
// verification comes before the benches, and both derive-ns --from and
// verify-ns --from carry the previous head.
func TestWeeklyPlan(t *testing.T) {
	steps := weeklyPlan("/a", "/h", "/c", "/q.json", 25943311, 32, 1000, "/a/new.txt")
	var got []string
	for _, s := range steps {
		got = append(got, s.args[0])
	}
	if want := "derive-ns derive-plan derive-ns verify-ns verify bench bench"; strings.Join(got, " ") != want {
		t.Fatalf("steps %q, want %q", strings.Join(got, " "), want)
	}
	for _, i := range []int{0, 3, 6} {
		if !strings.Contains(strings.Join(steps[i].args, " "), "--from 25943311") {
			t.Fatalf("step %d (%s) does not carry the previous head: %v", i, steps[i].name, steps[i].args)
		}
	}
	if !steps[2].needsList || steps[0].needsList {
		t.Fatal("only the second derive-ns depends on derive-plan's list")
	}
	if s := weeklyPlan("/a", "/h", "/c", "", 1, 1, 1000, "/n")[5]; s.skip == "" {
		t.Fatal("without --queries the stratified bench must be skipped, not run with an empty file")
	}
}
