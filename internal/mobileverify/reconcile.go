// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cross-node index reconciliation — the fix for a real amplification
// vulnerability in "merge already-aggregated certs, drop the smaller one on
// conflict": if a single MobileIndex's signature reaches EVERY IDC node's
// local cohort for a block (a misbehaving client that ignores the
// submit-to-fetch-source rule, a race during a fetch-source switch, or a
// malicious/compromised device key deliberately broadcasting to the whole
// fleet), that one index pairwise-conflicts with every node's cert. Any
// post-aggregation policy that resolves conflicts by keeping one cert and
// dropping the other can only ever keep ONE of them once a single index is
// universal — a "star" conflict graph collapses to one surviving cert no
// matter how the tie-break is defined, discarding every other node's honest,
// non-conflicting signers as collateral damage. A single bad index must
// never be able to nuke N-1 honest nodes' coverage.
//
// The fix moves exclusion BEFORE aggregation, to the one place a device's
// raw individual signature still exists: each node's own Collector, prior
// to Close(). Every node announces its admitted index set (Collector.Indices,
// no signatures — just the sparse index list); ReconcileIndices computes
// which indices appear in more than one announcement; every node then calls
// Collector.ExcludeIndices with that list before Close(). Because every node
// applies the SAME exclusion to the SAME conflicting indices before ANY
// aggregation happens, the resulting per-node certs are pairwise
// signer-disjoint by construction — MergeCerts (certmerge.go) never needs to
// resolve a real conflict in the happy path; a conflicting index simply
// never appears in anyone's final cert for that block, at zero cost to any
// other honest device sharing that block.

package mobileverify

import "sort"

// ReconcileIndices computes which MobileIndex values were announced by more
// than one of the given sets — the same registered device's signature
// reached more than one IDC node for this block. The caller's own local
// index set MUST be one of the sets passed in (reconciliation is symmetric:
// every node runs this over the identical union of announcements to arrive
// at the identical banned list, and therefore identical exclusions).
//
// A single set repeating an index (e.g. a caller passing an already-sorted
// slice with an accidental duplicate) does not by itself count as a
// conflict — only presence in more than one DISTINCT announcement does.
func ReconcileIndices(sets ...[]MobileIndex) []MobileIndex {
	count := make(map[MobileIndex]int, 64)
	for _, s := range sets {
		seenInSet := make(map[MobileIndex]struct{}, len(s))
		for _, idx := range s {
			if _, dup := seenInSet[idx]; dup {
				continue
			}
			seenInSet[idx] = struct{}{}
			count[idx]++
		}
	}
	var banned []MobileIndex
	for idx, c := range count {
		if c > 1 {
			banned = append(banned, idx)
		}
	}
	sort.Slice(banned, func(i, j int) bool { return banned[i] < banned[j] })
	return banned
}
