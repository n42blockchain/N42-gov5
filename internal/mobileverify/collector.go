// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

var (
	// ErrWrongBlock reports a receipt submitted to a collector for a
	// different block.
	ErrWrongBlock = errors.New("mobileverify: receipt is for a different block")
	// ErrNotRegistered reports a receipt from a pubkey absent from the
	// registry — registration (with PoP) must precede attestation.
	ErrNotRegistered = errors.New("mobileverify: verifier not registered")
	// ErrWindowClosed reports a submission after Close.
	ErrWindowClosed = errors.New("mobileverify: collection window closed")
)

// Collector accumulates one block's receipts during its collection
// window (design §5c): verify signature, require registration, dedup
// by MobileIndex (latest wins), bucket by attested receipts root. Close
// aggregates each bucket into a MobileAttestationCert — including
// minority/divergent buckets, which are the alarm signal this pipeline
// exists to surface (§6).
type Collector struct {
	blockHash   types.Hash
	blockNumber uint64
	reg         *Registry

	mu     sync.Mutex
	closed bool
	// latest receipt per device; bucketing happens at Close so a device
	// that re-submits with a different root cleanly moves buckets.
	byIndex map[MobileIndex]*Receipt
}

// NewCollector opens a collection window for one block.
func NewCollector(reg *Registry, blockHash types.Hash, blockNumber uint64) *Collector {
	return &Collector{
		blockHash:   blockHash,
		blockNumber: blockNumber,
		reg:         reg,
		byIndex:     make(map[MobileIndex]*Receipt),
	}
}

// Add validates and admits one receipt: correct block, registered
// pubkey, valid BLS signature. Signature verification is the gate
// before the receipt touches any state (design §8.4). Returns the
// submitting device's index.
func (c *Collector) Add(r *Receipt) (MobileIndex, error) {
	if r == nil {
		return 0, errors.New("mobileverify: nil receipt")
	}
	if r.BlockHash != c.blockHash || r.BlockNumber != c.blockNumber {
		return 0, fmt.Errorf("%w: got %d/%x want %d/%x", ErrWrongBlock,
			r.BlockNumber, r.BlockHash[:8], c.blockNumber, c.blockHash[:8])
	}
	idx, ok := c.reg.Lookup(r.VerifierPubkey)
	if !ok {
		return 0, ErrNotRegistered
	}
	if err := r.VerifySignature(); err != nil {
		return 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, ErrWindowClosed
	}
	cp := *r
	c.byIndex[idx] = &cp
	return idx, nil
}

// Close ends the window and aggregates each receipts-root cohort into
// a certificate, ordered by cohort size descending (majority first —
// callers alarm on any bucket beyond the first). Idempotent-hostile by
// design: a second Close returns ErrWindowClosed.
func (c *Collector) Close(nowMs uint64) ([]*MobileAttestationCert, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrWindowClosed
	}
	c.closed = true
	byIndex := c.byIndex
	c.mu.Unlock()

	type cohort struct {
		indices []MobileIndex
		sigs    map[MobileIndex]common.Signature
	}
	cohorts := make(map[types.Hash]*cohort)
	for idx, r := range byIndex {
		sig, err := bls.SignatureFromBytes(r.Signature[:])
		if err != nil {
			// Admitted receipts already verified; a decode failure here is
			// unreachable in practice — skip rather than poison the window.
			continue
		}
		co := cohorts[r.ComputedReceiptsRoot]
		if co == nil {
			co = &cohort{sigs: make(map[MobileIndex]common.Signature)}
			cohorts[r.ComputedReceiptsRoot] = co
		}
		co.indices = append(co.indices, idx)
		co.sigs[idx] = sig
	}

	certs := make([]*MobileAttestationCert, 0, len(cohorts))
	for root, co := range cohorts {
		sort.Slice(co.indices, func(i, j int) bool { return co.indices[i] < co.indices[j] })
		mask, err := EncodeMask(co.indices)
		if err != nil {
			return nil, err // unreachable: indices are sorted unique by construction
		}
		// Aggregate in mask order so the cert is deterministic for a cohort.
		sigs := make([]common.Signature, len(co.indices))
		for i, idx := range co.indices {
			sigs[i] = co.sigs[idx]
		}
		agg := bls.AggregateSignatures(sigs)
		cert := &MobileAttestationCert{
			BlockHash:      c.blockHash,
			BlockNumber:    c.blockNumber,
			ReceiptsRoot:   root,
			SignerMask:     mask,
			WindowClosedAt: nowMs,
		}
		copy(cert.AggregateSig[:], agg.Marshal())
		certs = append(certs, cert)
	}
	sort.Slice(certs, func(i, j int) bool {
		ci, _ := DecodeMask(certs[i].SignerMask, c.reg.Count())
		cj, _ := DecodeMask(certs[j].SignerMask, c.reg.Count())
		if len(ci) != len(cj) {
			return len(ci) > len(cj)
		}
		// Deterministic tie-break for equal-size cohorts.
		return certs[i].ReceiptsRoot.String() < certs[j].ReceiptsRoot.String()
	})
	return certs, nil
}

// Size returns the number of distinct devices admitted so far.
func (c *Collector) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byIndex)
}

// NowMs is a convenience for callers wiring Close.
func NowMs() uint64 { return uint64(time.Now().UnixMilli()) }
