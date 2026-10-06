// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"math/big"
	"sort"
	"testing"
)

func TestSortGasAndReward(t *testing.T) {
	s := sortGasAndReward{
		{gasUsed: 1, reward: big.NewInt(3)},
		{gasUsed: 2, reward: big.NewInt(1)},
		{gasUsed: 3, reward: big.NewInt(2)},
	}
	if s.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", s.Len())
	}
	sort.Sort(s)
	if s[0].reward.Int64() != 1 || s[1].reward.Int64() != 2 || s[2].reward.Int64() != 3 {
		t.Fatalf("sort order = %v, want ascending by reward", s)
	}
}
