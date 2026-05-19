package cs

import (
	"fmt"
	"strings"
)

// TieredSource tries each underlying source in declared order. The
// first source for which Available(blk) is true serves the call.
// If no source covers blk, RetrieveX returns ErrDeepReorg.
//
// Typical composition:
//   tiered := NewTieredSource(warm, freezer)
//   ↑ tries warm (cheap, recent), falls back to freezer (full but
//     potentially absent post-prune)
type TieredSource struct {
	sources []Source
}

func NewTieredSource(sources ...Source) *TieredSource {
	return &TieredSource{sources: sources}
}

func (t *TieredSource) RetrieveAccount(blk uint64) ([]byte, error) {
	for _, s := range t.sources {
		if s.Available(blk) {
			return s.RetrieveAccount(blk)
		}
	}
	return nil, fmt.Errorf("%w: block %d not in any of: %s",
		ErrDeepReorg, blk, t.WindowDescription())
}

func (t *TieredSource) RetrieveStorage(blk uint64) ([]byte, error) {
	for _, s := range t.sources {
		if s.Available(blk) {
			return s.RetrieveStorage(blk)
		}
	}
	return nil, fmt.Errorf("%w: block %d not in any of: %s",
		ErrDeepReorg, blk, t.WindowDescription())
}

func (t *TieredSource) Available(blk uint64) bool {
	for _, s := range t.sources {
		if s.Available(blk) {
			return true
		}
	}
	return false
}

func (t *TieredSource) WindowDescription() string {
	parts := make([]string, len(t.sources))
	for i, s := range t.sources {
		parts[i] = s.WindowDescription()
	}
	return "tiered: " + strings.Join(parts, " + ")
}
