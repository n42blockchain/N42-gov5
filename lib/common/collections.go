package common

import (
	"math/rand"
	"slices"
)

func SliceReverse[T any](s []T) {
	slices.Reverse(s)
}

func SliceMap[T any, U any](s []T, mapFunc func(T) U) []U {
	out := make([]U, 0, len(s))
	for _, x := range s {
		out = append(out, mapFunc(x))
	}
	return out
}

func SliceShuffle[T any](s []T) {
	rand.Shuffle(len(s), func(i, j int) {
		s[i], s[j] = s[j], s[i]
	})
}
