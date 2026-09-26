// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import "testing"

func TestParseQMDBSingleFold(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", false},
		{"0", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"bogus", false},
	}
	for _, c := range cases {
		if got := parseQMDBSingleFold(c.v); got != c.want {
			t.Errorf("parseQMDBSingleFold(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
