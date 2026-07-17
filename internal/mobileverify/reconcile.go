// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cross-node index reconciliation — the fix for two real problems:
//
// (1) An amplification vulnerability in "merge already-aggregated certs, drop
// the smaller one on conflict": if a single MobileIndex's signature reaches
// EVERY IDC node's local cohort for a block, that one index pairwise-
// conflicts with every node's cert. Any post-aggregation policy that
// resolves conflicts by keeping one cert and dropping the other can only
// ever keep ONE of them once a single index is universal — discarding every
// other node's honest, non-conflicting signers as collateral damage. A
// single bad index must never be able to nuke N-1 honest nodes' coverage.
//
// (2) Bare, unauthenticated index claims are themselves a censorship vector:
// if reconciliation banned an index merely for being NAMED by more than one
// node's announcement, any single dishonest node could silently censor an
// arbitrary honest device fleet-wide by announcing its index — without ever
// having collected that device's signature. BLS signing is deterministic for
// a fixed key and message, so two nodes that GENUINELY both received the
// identical device's signature always produce byte-identical Keccak256
// commitments to it; a node that never collected that signature cannot
// forge a matching commitment without already knowing the real signature
// bytes. Reconciliation therefore only bans an index when two DISTINCT
// announcements agree on the SAME commitment — proof of a genuine duplicate
// submission — never on a bare, unauthenticated claim.
//
// The fix for (1) moves exclusion BEFORE aggregation, to the one place a
// device's raw individual signature still exists: each node's own Collector,
// prior to Close(). Every node announces its admitted (index, commitment)
// pairs via Collector.Freeze() (no signatures — just index + a hash of the
// signature); ReconcileIndices computes which indices have a
// commitment-matched conflict; every node then calls Collector.ExcludeIndices
// with that list before Close(). Because every node applies the SAME
// exclusion to the SAME conflicting indices before ANY aggregation happens,
// the resulting per-node certs are pairwise signer-disjoint by construction
// — MergeCerts (certmerge.go) never needs to resolve a real conflict in the
// happy path; a conflicting index simply never appears in anyone's final
// cert for that block, at zero cost to any other honest device sharing that
// block.

package mobileverify

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/common/types"
)

// IndexCommitment pairs a registry index with a commitment to the specific
// signature backing it (Keccak256(signature) — see Collector.Freeze). It is
// the cross-node index-announcement's unit of "proof of possession without
// revealing the signature itself".
type IndexCommitment struct {
	Index      MobileIndex
	Commitment types.Hash
}

// ReconcileIndices computes which MobileIndex values were announced, by more
// than one of the given sets, WITH A MATCHING commitment — proof the same
// registered device's signature reached more than one IDC node for this
// block (a violation of the submit-to-fetch-source rule: a misbehaving
// client, a race during a fetch-source switch, or a compromised device key
// deliberately broadcasting to the whole fleet).
//
// An index named by more than one set but with DIFFERING commitments is
// deliberately NOT banned: at most one of those claims can be genuine, and
// without the raw signature there is no way to tell which — banning on a
// bare, unauthenticated disagreement would hand any single dishonest node an
// unconditional censorship tool against devices it never actually observed.
// Only matching commitments (which cannot be forged without already knowing
// the real signature) are treated as evidence.
//
// The caller's own local (index, commitment) set MUST be one of the sets
// passed in (reconciliation is symmetric: every node runs this over the
// identical union of announcements to arrive at the identical banned list,
// and therefore identical exclusions).
//
// A single set repeating an (index, commitment) pair does not by itself
// count as a conflict — only agreement across more than one DISTINCT
// announcement does.
func ReconcileIndices(sets ...[]IndexCommitment) []MobileIndex {
	// index -> commitment -> number of DISTINCT announcing sets reporting it
	seen := make(map[MobileIndex]map[types.Hash]int, 64)
	for _, s := range sets {
		local := make(map[MobileIndex]map[types.Hash]bool, len(s))
		for _, ic := range s {
			if local[ic.Index] == nil {
				local[ic.Index] = make(map[types.Hash]bool, 1)
			}
			if local[ic.Index][ic.Commitment] {
				continue // repeat within one set — not a new witness
			}
			local[ic.Index][ic.Commitment] = true
			if seen[ic.Index] == nil {
				seen[ic.Index] = make(map[types.Hash]int, 2)
			}
			seen[ic.Index][ic.Commitment]++
		}
	}
	var banned []MobileIndex
	for idx, byCommitment := range seen {
		for _, count := range byCommitment {
			if count > 1 {
				banned = append(banned, idx)
				break
			}
		}
	}
	sort.Slice(banned, func(i, j int) bool { return banned[i] < banned[j] })
	return banned
}

// encodeIndexCommitments encodes (index, commitment) pairs for the wire:
// uvarint count, then per entry a delta-varint index (the same
// strictly-ascending, gap-1-delta scheme EncodeMask uses — first delta is
// the index itself, each subsequent is (gap-1)) immediately followed by its
// 32-byte commitment. Self-delimiting; the caller's sorted input must be
// strictly ascending by Index (Collector.Freeze already returns it that
// way).
func encodeIndexCommitments(sorted []IndexCommitment) ([]byte, error) {
	out := make([]byte, 0, 1+len(sorted)*(2+types.HashLength))
	out = binary.AppendUvarint(out, uint64(len(sorted)))
	prev := uint64(0)
	for i, ic := range sorted {
		v := uint64(ic.Index)
		if i == 0 {
			out = binary.AppendUvarint(out, v)
		} else {
			if v <= prev {
				return nil, fmt.Errorf("%w: indices not strictly ascending (%d after %d)", ErrBadMask, v, prev)
			}
			out = binary.AppendUvarint(out, v-prev-1)
		}
		prev = v
		out = append(out, ic.Commitment[:]...)
	}
	return out, nil
}

// decodeIndexCommitments decodes and validates encodeIndexCommitments'
// output. registryCount bounds the largest acceptable index, same
// convention as DecodeMask.
func decodeIndexCommitments(data []byte, registryCount int) ([]IndexCommitment, error) {
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("%w: bad count", ErrBadMask)
	}
	p := data[n:]
	if count > uint64(registryCount) {
		return nil, fmt.Errorf("%w: count %d implausible (registry=%d)", ErrBadMask, count, registryCount)
	}
	out := make([]IndexCommitment, 0, count)
	cur := uint64(0)
	for i := uint64(0); i < count; i++ {
		d, n := binary.Uvarint(p)
		if n <= 0 {
			return nil, fmt.Errorf("%w: truncated index at entry %d", ErrBadMask, i)
		}
		p = p[n:]
		if i == 0 {
			cur = d
		} else {
			cur += d + 1
		}
		if cur >= uint64(registryCount) {
			return nil, fmt.Errorf("%w: index %d beyond registry size %d", ErrBadMask, cur, registryCount)
		}
		if len(p) < types.HashLength {
			return nil, fmt.Errorf("%w: truncated commitment at entry %d", ErrBadMask, i)
		}
		var ic IndexCommitment
		ic.Index = MobileIndex(cur)
		copy(ic.Commitment[:], p[:types.HashLength])
		p = p[types.HashLength:]
		out = append(out, ic)
	}
	if len(p) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrBadMask, len(p))
	}
	return out, nil
}
