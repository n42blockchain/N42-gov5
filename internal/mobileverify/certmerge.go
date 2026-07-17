// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Cross-node certificate merge (design: replaces "512 nodes -> up to 512
// unrelated small certs per block" with one certificate per genuinely
// distinct receipts-root outcome). BLS aggregate signatures are plain EC
// point addition (crypto/bls.AggregateSignatures), so combining two
// per-node certs for the same (block, receipts root) is exactly as valid as
// combining raw signatures directly — PROVIDED their signer sets are
// disjoint. Disjointness is the reconcile.go/Collector.ExcludeIndices
// pre-aggregation step's job; MergeCerts here still checks for it
// defensively (a degraded-path node may have skipped reconciliation) but
// must never rely on it running.

package mobileverify

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

var (
	// ErrCertMismatch reports an attempt to merge certs for different
	// blocks or different receipts roots — merging across those would be
	// meaningless (they don't attest to the same thing).
	ErrCertMismatch = errors.New("mobileverify: certs are for a different block or receipts root")
	// ErrEmptyCertSet reports an empty input to a merge/select call.
	ErrEmptyCertSet = errors.New("mobileverify: no certificates to combine")
	// ErrAllCertsConflicted reports every input to MergeCerts pairwise
	// conflicting down to nothing acceptable — should be unreachable when
	// reconciliation ran, since it guarantees disjointness up front.
	ErrAllCertsConflicted = errors.New("mobileverify: every certificate conflicted, nothing to merge")
)

// MergeCerts combines multiple certificates for the SAME (BlockHash,
// BlockNumber, ReceiptsRoot) into one covering the union of their signers.
// Returns the merged cert, plus any inputs dropped due to a detected signer
// overlap (should be empty when upstream reconciliation ran — a non-empty
// return here is itself worth alerting on, since it means the disjointness
// invariant was violated somewhere upstream).
//
// Overlap resolution (defense in depth only): certs are processed largest
// first, in descending signer-count order with a deterministic tie-break;
// a cert that shares any index with an already-accepted cert is dropped
// whole, not partially — a BLS aggregate signature cannot be selectively
// decomposed to drop just the conflicting signer without that signer's raw
// individual signature, which is no longer available once a cert has
// already been aggregated. This is exactly why reconciliation excludes
// conflicting indices BEFORE aggregation instead of relying on this
// fallback: a single index present in every input cert would otherwise
// collapse the merge down to just one surviving cert, discarding every
// other node's honest, non-conflicting signers as collateral damage.
func MergeCerts(certs []*MobileAttestationCert, registryBound int) (merged *MobileAttestationCert, droppedForOverlap []*MobileAttestationCert, err error) {
	if len(certs) == 0 {
		return nil, nil, ErrEmptyCertSet
	}
	first := certs[0]
	for _, c := range certs[1:] {
		if !sameCohortKey(first, c) {
			return nil, nil, fmt.Errorf("%w: %x/%d/%x vs %x/%d/%x", ErrCertMismatch,
				first.BlockHash[:8], first.BlockNumber, first.ReceiptsRoot[:8],
				c.BlockHash[:8], c.BlockNumber, c.ReceiptsRoot[:8])
		}
	}
	if len(certs) == 1 {
		return certs[0], nil, nil
	}

	type decoded struct {
		cert    *MobileAttestationCert
		indices []MobileIndex
		sig     common.Signature
	}
	ds := make([]decoded, 0, len(certs))
	for _, c := range certs {
		idxs, derr := DecodeMask(c.SignerMask, registryBound)
		if derr != nil {
			return nil, nil, fmt.Errorf("mobileverify: decode mask: %w", derr)
		}
		sig, serr := bls.SignatureFromBytes(c.AggregateSig[:])
		if serr != nil {
			return nil, nil, fmt.Errorf("mobileverify: decode signature: %w", serr)
		}
		ds = append(ds, decoded{cert: c, indices: idxs, sig: sig})
	}
	// Largest cohort first, so the (should-be-unreachable) overlap fallback
	// deterministically favors keeping more signers; deterministic tie-break
	// keeps the fold order — and thus the eventual result in the disjoint
	// happy path, where order never matters anyway since EC addition
	// commutes — independent of gossip arrival order.
	sort.SliceStable(ds, func(i, j int) bool {
		if len(ds[i].indices) != len(ds[j].indices) {
			return len(ds[i].indices) > len(ds[j].indices)
		}
		return bytes.Compare(ds[i].cert.AggregateSig[:], ds[j].cert.AggregateSig[:]) < 0
	})

	used := make(map[MobileIndex]bool, 256)
	var acceptedIdx []MobileIndex
	var acceptedSig []common.Signature
	for _, d := range ds {
		conflict := false
		for _, idx := range d.indices {
			if used[idx] {
				conflict = true
				break
			}
		}
		if conflict {
			droppedForOverlap = append(droppedForOverlap, d.cert)
			continue
		}
		for _, idx := range d.indices {
			used[idx] = true
		}
		acceptedIdx = append(acceptedIdx, d.indices...)
		acceptedSig = append(acceptedSig, d.sig)
	}
	if len(acceptedSig) == 0 {
		return nil, droppedForOverlap, ErrAllCertsConflicted
	}

	sort.Slice(acceptedIdx, func(i, j int) bool { return acceptedIdx[i] < acceptedIdx[j] })
	mask, merr := EncodeMask(acceptedIdx)
	if merr != nil {
		return nil, droppedForOverlap, merr
	}
	agg := bls.AggregateSignatures(acceptedSig)
	merged = &MobileAttestationCert{
		BlockHash:    first.BlockHash,
		BlockNumber:  first.BlockNumber,
		ReceiptsRoot: first.ReceiptsRoot,
		SignerMask:   mask,
	}
	copy(merged.AggregateSig[:], agg.Marshal())
	merged.WindowClosedAt = NowMs()
	return merged, droppedForOverlap, nil
}

// SelectMajorityBucket picks the single largest-cohort cert among certs for
// the SAME block that disagree on ReceiptsRoot. Call this AFTER MergeCerts
// has already consolidated same-root certs across nodes — routing
// fragmentation (the same honest phones split across many collecting IDC
// nodes) is resolved by MergeCerts already, so if more than one bucket
// reaches SelectMajorityBucket it means registered devices genuinely
// recomputed different roots: a real divergence, not a coordination
// artifact. Ties break on the lexicographically smaller ReceiptsRoot hex
// string — the same tie-break Collector.Close already uses for its own
// cohort ordering, so every node picks identically.
func SelectMajorityBucket(buckets []*MobileAttestationCert, registryBound int) (winner *MobileAttestationCert, discarded []*MobileAttestationCert, err error) {
	if len(buckets) == 0 {
		return nil, nil, ErrEmptyCertSet
	}
	if len(buckets) == 1 {
		return buckets[0], nil, nil
	}
	sizes := make([]int, len(buckets))
	for i, b := range buckets {
		idxs, derr := DecodeMask(b.SignerMask, registryBound)
		if derr != nil {
			return nil, nil, fmt.Errorf("mobileverify: decode mask: %w", derr)
		}
		sizes[i] = len(idxs)
	}
	best := 0
	for i := 1; i < len(buckets); i++ {
		switch {
		case sizes[i] > sizes[best]:
			best = i
		case sizes[i] == sizes[best] && buckets[i].ReceiptsRoot.String() < buckets[best].ReceiptsRoot.String():
			best = i
		}
	}
	for i, b := range buckets {
		if i != best {
			discarded = append(discarded, b)
		}
	}
	return buckets[best], discarded, nil
}

func sameCohortKey(a, b *MobileAttestationCert) bool {
	return a.BlockHash == b.BlockHash && a.BlockNumber == b.BlockNumber && a.ReceiptsRoot == b.ReceiptsRoot
}
