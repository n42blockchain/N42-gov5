package state

import "testing"

func TestParseHistoryIndexDisabled(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
		why  string
	}{
		{"", false, "unset must leave the index ON — the default keeps the query capability"},
		{"1", true, ""},
		{"true", true, ""},
		{"TRUE", true, ""},
		{"yes", true, ""},
		{"on", true, ""},
		{"0", false, "an explicit off must be off"},
		{"false", false, ""},
		{"ture", false, "a typo must not silently disable historical queries"},
		{" 1", false, "no trimming: a stray space must not be read as affirmative either way"},
		{"maybe", false, "anything unrecognised keeps the index"},
	} {
		if got := parseHistoryIndexDisabled(tc.in); got != tc.want {
			t.Errorf("parseHistoryIndexDisabled(%q) = %v, want %v: %s", tc.in, got, tc.want, tc.why)
		}
	}
}

// The latch matters: if the answer could change under a running node, the
// index would grow holes that no reader could detect afterwards.
func TestHistoryIndexDisabledIsStable(t *testing.T) {
	first := HistoryIndexDisabled()
	t.Setenv("N42_NO_HISTORY_INDEX", "1")
	if HistoryIndexDisabled() != first {
		t.Fatal("the flag must latch on first read; a node that changes it mid-run leaves undetectable holes in the index")
	}
	t.Setenv("N42_NO_HISTORY_INDEX", "")
	if HistoryIndexDisabled() != first {
		t.Fatal("latched value must not change when the variable is cleared either")
	}
}
