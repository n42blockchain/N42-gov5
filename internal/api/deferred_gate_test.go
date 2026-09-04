package api

import "testing"

// The row that matters is "latest". Leaving the head comparison out of this
// rule refused blockNr == head as if it were historical, which took every node
// in a benchmark round off the air for present-tense queries — eth_getBalance
// at latest returned null and the faucet preflight died on "invalid hex
// quantity". The index is only needed for the past; `latest` reads PlainState.
func TestDeferredRefusesQuery(t *testing.T) {
	const head = 1000
	for _, tc := range []struct {
		name             string
		blockNr, indexed uint64
		markerPresent    bool
		headOverride     uint64
		want             bool
		why              string
	}{
		{name: "latest is never refused", blockNr: head, indexed: 5, markerPresent: true,
			want: false, why: "latest reads PlainState and does not consult the index"},
		{name: "above head is never refused", blockNr: head + 10, indexed: 5, markerPresent: true,
			want: false, why: "a future height is not a historical query"},
		{name: "historical below the marker is served", blockNr: 100, indexed: 500, markerPresent: true,
			want: false, why: "the index covers it"},
		{name: "historical above the marker is refused", blockNr: 900, indexed: 500, markerPresent: true,
			want: true, why: "the gap reads as untouched and resolves to the current value"},
		{name: "historical exactly at the marker is served", blockNr: 500, indexed: 500, markerPresent: true,
			want: false, why: "the marker is inclusive of the block it names"},
		{name: "no marker refuses historical", blockNr: 100, indexed: 0, markerPresent: false,
			want: true, why: "absent must mean nothing covered, never everything"},
		{name: "no marker still serves latest", blockNr: head, indexed: 0, markerPresent: false,
			want: false, why: "a missing index must not break present-tense queries"},
		{name: "unknown head fails open", blockNr: 100, indexed: 0, markerPresent: false,
			headOverride: 1, want: false,
			why: "refusing everything on a momentarily unreadable head is worse than the failure prevented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := uint64(head)
			if tc.headOverride != 0 {
				h = 0
			}
			if got := deferredRefusesQuery(tc.blockNr, h, tc.indexed, tc.markerPresent); got != tc.want {
				t.Errorf("deferredRefusesQuery(blockNr=%d, head=%d, indexed=%d, present=%v) = %v, want %v: %s",
					tc.blockNr, h, tc.indexed, tc.markerPresent, got, tc.want, tc.why)
			}
		})
	}
}
