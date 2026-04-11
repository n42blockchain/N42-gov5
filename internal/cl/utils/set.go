// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package utils

func IntersectionOfSortedSets(v1, v2 []uint64) []uint64 {
	intersection := []uint64{}
	// keep track of v1 and v2 element iteration
	var i, j int
	// Note that v1 and v2 are both sorted.
	for i < len(v1) && j < len(v2) {
		if v1[i] == v2[j] {
			intersection = append(intersection, v1[i])
			// Change both iterators
			i++
			j++
			continue
		}
		// increase i and j accordingly
		if v1[i] > v2[j] {
			j++
		} else {
			i++
		}
	}
	return intersection
}
