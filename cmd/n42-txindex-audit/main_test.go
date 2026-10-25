package main

import "testing"

func TestAuditOptionsArbitraryPositiveHead(t *testing.T) {
	for _, head := range []uint64{1, 25, 31, 256} {
		if err := validateAuditOptions(0, "/stopped-owner", head, "98765", 2, 1, 4075000); err != nil {
			t.Fatalf("head %d: %v", head, err)
		}
	}
	cases := []struct {
		name        string
		extra       int
		dir         string
		head        uint64
		chainID     string
		segments    int
		tail, total uint64
	}{
		{"zero head", 0, "/owner", 0, "1", 2, 1, 1},
		{"relative directory", 0, "owner", 25, "1", 2, 1, 1},
		{"missing chain ID", 0, "/owner", 25, "", 2, 1, 1},
		{"one segment", 0, "/owner", 25, "1", 1, 1, 1},
		{"no tail", 0, "/owner", 25, "1", 2, 0, 1},
		{"no exact total", 0, "/owner", 25, "1", 2, 1, 0},
		{"extra argument", 1, "/owner", 25, "1", 2, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAuditOptions(tc.extra, tc.dir, tc.head, tc.chainID, tc.segments, tc.tail, tc.total); err == nil {
				t.Fatal("invalid audit shape accepted")
			}
		})
	}
}

func TestBlockTransactionExpectation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		number    uint64
		count     int
		expected  uint64
		wantError bool
	}{
		{"strict full block", 25, 163000, 163000, false},
		{"strict partial rejected", 25, 162999, 163000, true},
		{"strict excess rejected", 25, 163001, 163000, true},
		{"strict empty rejected", 25, 0, 163000, true},
		{"genesis excluded", 0, 0, 163000, false},
		{"variable full block", 31, 163000, 0, false},
		{"variable partial block", 31, 500, 0, false},
		{"variable empty block", 31, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkBlockTransactionCount(tc.number, tc.count, tc.expected); (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}
