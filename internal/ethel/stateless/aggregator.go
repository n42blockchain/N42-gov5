// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// aggregator.go — the multi-IDC verification counting service (P8 workflow #9).
// N independent verifier nodes each verify a block statelessly and POST a signed
// Attestation; the Aggregator counts distinct allowed signers per (block, roots)
// and reports finalization once a threshold agree. Because counting is per-roots,
// a single lying producer that ships a wrong root cannot inflate the honest
// group — its attestations land in a separate, under-threshold group.

package stateless

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// attestationWireSize is the fixed encoded size: Number(8) + StateRoot(32) +
// ReceiptRoot(32) + Sig(65).
const attestationWireSize = 8 + 32 + 32 + 65

// EncodeAttestation serialises an attestation to its fixed 137-byte wire form.
func EncodeAttestation(a *Attestation) ([]byte, error) {
	if a == nil {
		return nil, fmt.Errorf("attestation: nil")
	}
	if len(a.Sig) != 65 {
		return nil, fmt.Errorf("attestation: bad sig len %d", len(a.Sig))
	}
	buf := make([]byte, attestationWireSize)
	binary.BigEndian.PutUint64(buf[0:8], a.Number)
	copy(buf[8:40], a.StateRoot[:])
	copy(buf[40:72], a.ReceiptRoot[:])
	copy(buf[72:137], a.Sig)
	return buf, nil
}

// DecodeAttestation parses the fixed wire form. The signature is NOT verified
// here (Aggregator.Submit / AttestationPool.Add recovers and validates it).
func DecodeAttestation(b []byte) (*Attestation, error) {
	if len(b) != attestationWireSize {
		return nil, fmt.Errorf("attestation: bad wire len %d, want %d", len(b), attestationWireSize)
	}
	a := &Attestation{Number: binary.BigEndian.Uint64(b[0:8]), Sig: make([]byte, 65)}
	copy(a.StateRoot[:], b[8:40])
	copy(a.ReceiptRoot[:], b[40:72])
	copy(a.Sig, b[72:137])
	return a, nil
}

// Aggregator is a thread-safe verification-count service over an AttestationPool.
// It is the receiving end of the multi-IDC workflow: verifier nodes submit signed
// attestations, the aggregator counts distinct allowed signers per (block, roots)
// and finalises once `threshold` agree. A rolling window bounds memory.
type Aggregator struct {
	mu        sync.Mutex
	pool      *AttestationPool
	threshold int
	window    uint64 // 0 = unbounded; else prune blocks below highest-window
	highest   uint64
}

// NewAggregator creates an aggregator. allow may be nil (accept any signer);
// threshold is the distinct-signer count for finalization; window > 0 enables a
// rolling retention of the most recent `window` blocks.
func NewAggregator(allow map[types.Address]bool, threshold int, window uint64) *Aggregator {
	if threshold < 1 {
		threshold = 1
	}
	return &Aggregator{
		pool:      NewAttestationPool(allow),
		threshold: threshold,
		window:    window,
	}
}

// SubmitResult reports the outcome of recording one attestation.
type SubmitResult struct {
	Signer    types.Address
	Counted   bool // false on duplicate signer or not on the allowlist
	Count     int  // distinct verifiers now backing (block, roots)
	Finalized bool // Count >= threshold
}

// Submit validates and records an attestation, returning the updated count and
// whether the (block, roots) group is finalised. Concurrency-safe.
func (g *Aggregator) Submit(a *Attestation) (SubmitResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	signer, counted, err := g.pool.Add(a)
	if err != nil {
		return SubmitResult{}, err
	}
	if counted && a.Number > g.highest {
		g.highest = a.Number
		g.pruneLocked()
	}
	count := g.pool.Count(a.Number, a.StateRoot, a.ReceiptRoot)
	return SubmitResult{
		Signer:    signer,
		Counted:   counted,
		Count:     count,
		Finalized: count >= g.threshold,
	}, nil
}

// Status returns the current distinct-verifier count and finalisation state for
// (block, roots) without recording anything. Concurrency-safe.
func (g *Aggregator) Status(number uint64, stateRoot, receiptRoot types.Hash) (count int, finalized bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	count = g.pool.Count(number, stateRoot, receiptRoot)
	return count, count >= g.threshold
}

// Prune drops attestations for blocks below keepFrom. Concurrency-safe.
func (g *Aggregator) Prune(keepFrom uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pool.Prune(keepFrom)
}

// pruneLocked applies the rolling window. Caller holds g.mu.
func (g *Aggregator) pruneLocked() {
	if g.window == 0 || g.highest <= g.window {
		return
	}
	g.pool.Prune(g.highest - g.window)
}
