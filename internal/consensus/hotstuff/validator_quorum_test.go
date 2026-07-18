// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import "testing"

// TestQuorumSizeSafetyAcrossSizes asserts QuorumSize = n - f and that the
// resulting quorum preserves the BFT intersection property (two quorums share
// more than f nodes, i.e. 2q - n > f) for every n >= 3f+1. The old 2f+1 formula
// violated this for n not of the form 3f+1 (e.g. n=5,f=1 gave q=3, and two
// size-3 quorums in a 5-set can meet in only 1 = f node).
func TestQuorumSizeSafetyAcrossSizes(t *testing.T) {
	cases := []struct {
		n, f, wantQuorum int
	}{
		{1, 0, 1},
		{4, 1, 3},
		{5, 1, 4},
		{6, 1, 5},
		{7, 2, 5},
		{8, 2, 6},
		{10, 3, 7},
		{100, 33, 67},
	}
	for _, c := range cases {
		vs := &ValidatorSet{
			validators:     make([]ValidatorInfo, c.n),
			faultTolerance: uint32(c.f),
		}
		q := vs.QuorumSize()
		if q != c.wantQuorum {
			t.Errorf("n=%d f=%d: QuorumSize=%d, want %d", c.n, c.f, q, c.wantQuorum)
		}
		// Intersection of two quorums must leave an honest node in common.
		if c.n >= 3*c.f+1 && 2*q-c.n <= c.f {
			t.Errorf("n=%d f=%d q=%d: quorum intersection %d <= f, unsafe", c.n, c.f, q, 2*q-c.n)
		}
	}
}
