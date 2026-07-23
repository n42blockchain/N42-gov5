package main

import "testing"

func TestResolveFinalizedRangeDistinguishesExplicitGenesis(t *testing.T) {
	tests := []struct {
		name                     string
		head, requestedFrom, to  uint64
		fromProvided, toProvided bool
		expectedFrom, expectedTo uint64
	}{
		{
			name: "default range below cap starts at genesis", head: 898,
			expectedFrom: 0, expectedTo: 898,
		},
		{
			name: "default range above cap is bounded", head: 1500,
			expectedFrom: 477, expectedTo: 1500,
		},
		{
			name: "explicit genesis only", head: 898,
			requestedFrom: 0, to: 0, fromProvided: true, toProvided: true,
			expectedFrom: 0, expectedTo: 0,
		},
		{
			name: "explicit range starting at genesis", head: 898,
			requestedFrom: 0, to: 29, fromProvided: true, toProvided: true,
			expectedFrom: 0, expectedTo: 29,
		},
		{
			name: "default start with explicit short end", head: 898,
			to: 29, toProvided: true,
			expectedFrom: 0, expectedTo: 29,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			from, to := resolveFinalizedRange(
				test.head,
				test.requestedFrom,
				test.to,
				test.fromProvided,
				test.toProvided,
			)
			if from != test.expectedFrom || to != test.expectedTo {
				t.Fatalf("got %d-%d, want %d-%d", from, to, test.expectedFrom, test.expectedTo)
			}
		})
	}
}
