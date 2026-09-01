// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// schedule.go — the DATC epoch schedule, a build/verify shared contract.
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths. build
// writes records keyed by epochOf(d, block); verify resolves them with the
// same schedule loaded from DatcMeta. Keep this the single definition so the
// two sides never drift.

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// epochSchedule holds per-depth epoch lengths. e[d] applies to storage level
// d and account levels d >= 1; the account-trie ROOT (level 0, which has no
// TrieOfAccounts row) is recorded every accRoot blocks from the loader's
// dense slots (0 = not recorded: the reader synthesizes it from the 16
// depth-1 children). A per-block root record (accRoot = 1) costs ~16 hashes
// per block and removes the whole depth-1..3 fan-out from every proof.
type epochSchedule struct {
	e       [maxChgDepth + 1]uint64
	accRoot uint64
}

// lenFor is the epoch length of one level in one trie (0 = level not
// recorded).
func (s epochSchedule) lenFor(storage bool, d int) uint64 {
	if !storage && d == 0 {
		return s.accRoot
	}
	return s.e[d]
}

// epochOfFor is epochOf for a level that may be the account root.
func (s epochSchedule) epochOfFor(storage bool, d int, block uint64) uint64 {
	l := s.lenFor(storage, d)
	if l == 0 {
		return 0
	}
	return block / l
}

func newSchedule(alpha, cbar float64) epochSchedule {
	var s epochSchedule
	for d := 0; d <= maxChgDepth; d++ {
		e := alpha * pow16(d) / cbar
		if e < 1 {
			e = 1
		}
		if e > 1<<22 {
			e = 1 << 22
		}
		s.e[d] = uint64(e)
	}
	return s
}

func pow16(d int) float64 {
	v := 1.0
	for i := 0; i < d; i++ {
		v *= 16
	}
	return v
}

func (s epochSchedule) epochOf(d int, block uint64) uint64 { return block / s.e[d] }

// parseSchedule parses "e0,e1,...,e5" (exactly maxChgDepth+1 values ≥ 1).
func parseSchedule(str string) (epochSchedule, error) {
	var s epochSchedule
	parts := strings.Split(str, ",")
	if len(parts) != maxChgDepth+1 {
		return s, fmt.Errorf("need %d comma-separated epoch lengths, got %d", maxChgDepth+1, len(parts))
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err != nil || v == 0 {
			return s, fmt.Errorf("epoch %d: %q is not a positive integer", i, p)
		}
		s.e[i] = v
	}
	return s, nil
}
