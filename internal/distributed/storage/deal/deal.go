// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package deal implements the storage deal lifecycle (slice 1 of
// docs/distributed-storage-design-2026-07-06.md): a submitter locks escrow
// for size×price×r×epochs; the manager chunks the object, commits to a
// merkle manifest, replicates to r distinct providers, and each epoch issues
// random-spot-check challenges. A provider that misses or fails its challenge
// is slashed via coprocessor.SlashManager (Verify-or-Slash) and its replica
// is marked for repair, which copies the object from a surviving replica onto
// a fresh provider (verified against the manifest root). Providers that pass
// earn size×price for the epoch; on completion or failure the escrow
// remainder refunds to the submitter.
//
// Redundancy in slice 1 is replication (r full copies). Erasure coding
// (RS(k,n)) is a documented later slice; the economic/challenge/repair
// machinery here is redundancy-scheme agnostic.
//
// Deprecated: package deal is not wired into any live path and has zero
// external callers. Its economics overlap almost entirely with the
// now-wired combination of internal/distributed/coprocessor (provider
// stake + Verify-or-Slash settlement) and internal/distributed/storage/
// torrent (content distribution). Rather than maintain a parallel
// stake/challenge/repair stack, fold any genuinely unique storage-deal need
// into coprocessor settlement when one is demonstrated. Retained for
// reference only; do not build new features on it. See
// project_distributed_compute_storage_wiring_plan for the decision.
package deal

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// challengeSamples is the number of distinct chunk indices probed per epoch.
// Production derives the indices from an unpredictable beacon (VRF) so a
// provider cannot pre-drop un-probed chunks; slice 1 derives them from
// keccak(dealID‖epoch‖k) — reproducible, and sufficient against the
// full-replica fault model (a provider that dropped/corrupted the object
// fails whichever index is probed).
const challengeSamples = 4

// Config wires the manager to the shared provider registry and slasher.
type Config struct {
	Registry *coprocessor.ProviderRegistry
	Slasher  *coprocessor.SlashManager
}

// Deal is the manager-side state of a storage deal.
type Deal struct {
	ID            types.Hash
	Submitter     types.Address
	Manifest      Manifest
	Spec          DealSpec
	Status        DealStatus
	Replicas      []Replica
	EpochsElapsed uint64
	chunks        [][]byte // slice-1 retains the object for repair/re-replication
}

// EpochResult summarizes one challenge epoch.
type EpochResult struct {
	Passed int
	Failed int
}

// DealManager orchestrates deals.
type DealManager struct {
	cfg    Config
	escrow *Escrow

	mu        sync.Mutex
	deals     map[types.Hash]*Deal
	providers map[types.Address]StorageProvider
	groups    map[types.Address]string
}

// NewDealManager creates a manager.
func NewDealManager(cfg Config) *DealManager {
	return &DealManager{
		cfg:       cfg,
		escrow:    NewEscrow(),
		deals:     make(map[types.Hash]*Deal),
		providers: make(map[types.Address]StorageProvider),
		groups:    make(map[types.Address]string),
	}
}

// Escrow exposes the settlement ledger.
func (m *DealManager) Escrow() *Escrow { return m.escrow }

// AddProvider registers a storage node (must already be a coprocessor
// provider with stake) plus its dispersal group.
func (m *DealManager) AddProvider(addr types.Address, group string, sp StorageProvider) error {
	if _, ok := m.cfg.Registry.GetSnapshot(addr); !ok {
		return ErrProviderUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[addr] = sp
	m.groups[addr] = group
	return nil
}

// Get returns a snapshot copy of a deal.
func (m *DealManager) Get(id types.Hash) (Deal, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deals[id]
	if !ok {
		return Deal{}, false
	}
	cp := *d
	cp.Replicas = append([]Replica(nil), d.Replicas...)
	cp.chunks = nil // don't leak the payload in snapshots
	return cp, true
}

// Submit validates the spec, builds the manifest, selects r distinct providers
// (dispersed if required), stores replicas, and locks the escrow.
func (m *DealManager) Submit(ctx context.Context, spec *DealSpec, submitter types.Address) (types.Hash, error) {
	if err := spec.Validate(); err != nil {
		return types.Hash{}, err
	}
	chunks := chunkData(spec.Data, spec.ChunkSize)
	tree := buildMerkle(chunkHashes(chunks))
	manifest := Manifest{
		Root:       tree.root(),
		Size:       uint64(len(spec.Data)),
		ChunkSize:  effectiveChunkSize(spec.ChunkSize),
		ChunkCount: len(chunks),
	}
	id := dealID(manifest, submitter)

	picked, err := m.selectProviders(spec.Redundancy, spec.Disperse, nil)
	if err != nil {
		return types.Hash{}, err
	}

	// Store on each selected provider before committing the deal; a provider
	// that rejects the chunks (root mismatch) disqualifies the placement.
	replicas := make([]Replica, 0, len(picked))
	for _, pv := range picked {
		if err := pv.sp.Store(ctx, id, manifest, chunks); err != nil {
			return types.Hash{}, fmt.Errorf("deal: store on %s: %w", pv.addr.Hex()[:8], err)
		}
		replicas = append(replicas, Replica{Provider: pv.addr, Group: pv.group, Healthy: true})
	}

	locked := manifest.Size * spec.EpochPrice * uint64(spec.Redundancy) * spec.Epochs
	m.escrow.Lock(id, locked)

	d := &Deal{
		ID: id, Submitter: submitter, Manifest: manifest, Spec: *spec,
		Status: DealActive, Replicas: replicas, chunks: chunks,
	}
	m.mu.Lock()
	m.deals[id] = d
	m.mu.Unlock()
	return id, nil
}

// RunChallengeEpoch issues this epoch's challenges to every replica, pays the
// providers that pass, slashes the healthy replicas that fail (marking them
// for repair), advances the epoch counter, and finalizes the deal when the
// paid epochs are exhausted.
func (m *DealManager) RunChallengeEpoch(ctx context.Context, id types.Hash) (EpochResult, error) {
	m.mu.Lock()
	d, ok := m.deals[id]
	if !ok {
		m.mu.Unlock()
		return EpochResult{}, ErrDealUnknown
	}
	if d.Status != DealActive {
		m.mu.Unlock()
		return EpochResult{}, ErrDealNotActive
	}
	epoch := d.EpochsElapsed
	manifest := d.Manifest
	price := d.Spec.EpochPrice
	replicas := append([]Replica(nil), d.Replicas...)
	providers := m.snapshotProviders(replicas)
	m.mu.Unlock()

	indices := challengeIndices(id, epoch, manifest.ChunkCount)

	// Challenge phase (no lock): probe each replica and classify the result.
	type outcome struct {
		idx    int
		passed bool
		cond   coprocessor.SlashCondition // meaningful only when !passed
	}
	outcomes := make([]outcome, len(replicas))
	for i, r := range replicas {
		passed, cond := m.challengeOne(ctx, id, manifest, providers[r.Provider], indices)
		outcomes[i] = outcome{idx: i, passed: passed, cond: cond}
	}

	// Settlement phase (locked): pay/slash/advance.
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok = m.deals[id]
	if !ok || d.Status != DealActive || d.EpochsElapsed != epoch {
		return EpochResult{}, nil // another caller advanced this epoch; no-op
	}
	var res EpochResult
	for _, oc := range outcomes {
		r := &d.Replicas[oc.idx]
		if oc.passed {
			res.Passed++
			amount := manifest.Size * price
			if err := m.escrow.Pay(id, r.Provider, amount); err == nil {
				m.cfg.Slasher.Reward(r.Provider, amount, id)
			}
		} else {
			res.Failed++
			if r.Healthy {
				r.Healthy = false // healthy→unhealthy: slash once for the transition
				m.cfg.Slasher.Slash(r.Provider, oc.cond, id)
			}
		}
	}
	d.EpochsElapsed++
	if d.EpochsElapsed >= d.Spec.Epochs {
		d.Status = DealCompleted
		m.escrow.RefundRemainder(id)
		m.dropAll(d)
	}
	return res, nil
}

// challengeOne probes the sampled chunk indices. The provider passes only if
// every sampled chunk verifies against the manifest root. On failure it also
// classifies the cause: an offline/absent provider (Prove errors) is a
// timeout; a provider that answered with a wrong proof is an invalid proof.
func (m *DealManager) challengeOne(ctx context.Context, id types.Hash, manifest Manifest, sp StorageProvider, indices []int) (bool, coprocessor.SlashCondition) {
	if sp == nil {
		return false, coprocessor.SlashTimeout
	}
	for _, idx := range indices {
		chunk, proof, err := sp.Prove(ctx, id, idx)
		if err != nil {
			return false, coprocessor.SlashTimeout // could not answer at all
		}
		if proof.Index != idx || !VerifyChunk(manifest.Root, chunk, proof) {
			return false, coprocessor.SlashInvalidProof // answered, but wrong
		}
	}
	return true, 0
}

// RepairDeal restores full redundancy: for each unhealthy replica, copy the
// object from a surviving replica onto a fresh provider (distinct from all
// current holders) and verify it against the manifest root. Returns the number
// of replicas repaired. Fails if no spare provider is available.
func (m *DealManager) RepairDeal(ctx context.Context, id types.Hash) (int, error) {
	m.mu.Lock()
	d, ok := m.deals[id]
	if !ok {
		m.mu.Unlock()
		return 0, ErrDealUnknown
	}
	if d.Status != DealActive {
		m.mu.Unlock()
		return 0, ErrDealNotActive
	}
	manifest := d.Manifest
	held := make(map[types.Address]bool)
	var survivor StorageProvider
	for _, r := range d.Replicas {
		held[r.Provider] = true
		if r.Healthy {
			if sp := m.providers[r.Provider]; sp != nil {
				survivor = sp
			}
		}
	}
	m.mu.Unlock()

	if survivor == nil {
		return 0, fmt.Errorf("deal: no healthy replica to repair from")
	}
	chunks, err := survivor.Retrieve(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("deal: retrieve from survivor: %w", err)
	}

	repaired := 0
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok = m.deals[id]
	if !ok || d.Status != DealActive {
		return 0, ErrDealNotActive
	}
	for i := range d.Replicas {
		r := &d.Replicas[i]
		if r.Healthy {
			continue
		}
		spare, err := m.pickSpareLocked(held)
		if err != nil {
			return repaired, err
		}
		if err := spare.sp.Store(ctx, id, manifest, chunks); err != nil {
			return repaired, fmt.Errorf("deal: store on spare %s: %w", spare.addr.Hex()[:8], err)
		}
		held[spare.addr] = true
		r.Provider = spare.addr
		r.Group = spare.group
		r.Healthy = true
		repaired++
	}
	return repaired, nil
}

// FailDeal terminates a deal that cannot maintain redundancy and refunds the
// escrow remainder.
func (m *DealManager) FailDeal(id types.Hash, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deals[id]
	if !ok || d.Status != DealActive {
		return
	}
	d.Status = DealFailed
	m.escrow.RefundRemainder(id)
	m.dropAll(d)
}

// ---------------------------------------------------------------------------
// provider selection

type pickedProvider struct {
	addr  types.Address
	group string
	sp    StorageProvider
}

// selectProviders picks n active providers, dispersed across distinct groups
// if required, excluding `exclude`. Sorted by reputation desc for determinism.
func (m *DealManager) selectProviders(n int, disperse bool, exclude map[types.Address]bool) ([]pickedProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selectProvidersLocked(n, disperse, exclude)
}

func (m *DealManager) selectProvidersLocked(n int, disperse bool, exclude map[types.Address]bool) ([]pickedProvider, error) {
	type cand struct {
		addr  types.Address
		group string
		rep   uint64
	}
	var cands []cand
	for addr, sp := range m.providers {
		_ = sp
		if exclude[addr] {
			continue
		}
		p, ok := m.cfg.Registry.GetSnapshot(addr)
		if !ok || p.Status != coprocessor.ProviderActive {
			continue
		}
		cands = append(cands, cand{addr: addr, group: m.groups[addr], rep: p.Reputation})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].rep != cands[j].rep {
			return cands[i].rep > cands[j].rep
		}
		return bytes.Compare(cands[i].addr[:], cands[j].addr[:]) < 0
	})

	var picked []pickedProvider
	seen := make(map[string]bool)
	for _, c := range cands {
		if disperse && seen[c.group] {
			continue
		}
		seen[c.group] = true
		picked = append(picked, pickedProvider{addr: c.addr, group: c.group, sp: m.providers[c.addr]})
		if len(picked) == n {
			return picked, nil
		}
	}
	return nil, ErrInsufficient
}

// pickSpareLocked selects one active provider not in `exclude`.
func (m *DealManager) pickSpareLocked(exclude map[types.Address]bool) (pickedProvider, error) {
	picks, err := m.selectProvidersLocked(1, false, exclude)
	if err != nil {
		return pickedProvider{}, err
	}
	return picks[0], nil
}

// snapshotProviders resolves the StorageProvider for each replica under lock.
func (m *DealManager) snapshotProviders(replicas []Replica) map[types.Address]StorageProvider {
	out := make(map[types.Address]StorageProvider, len(replicas))
	for _, r := range replicas {
		out[r.Provider] = m.providers[r.Provider]
	}
	return out
}

// dropAll releases every replica's storage (deal end). Caller holds m.mu.
func (m *DealManager) dropAll(d *Deal) {
	for _, r := range d.Replicas {
		if sp := m.providers[r.Provider]; sp != nil {
			sp.Drop(d.ID)
		}
	}
	d.chunks = nil
}

// ---------------------------------------------------------------------------
// helpers

func effectiveChunkSize(cs int) int {
	if cs <= 0 {
		return DefaultChunkSize
	}
	return cs
}

// dealID derives a content-addressed deal id from the manifest + submitter.
func dealID(m Manifest, submitter types.Address) types.Hash {
	var sz [8]byte
	binary.BigEndian.PutUint64(sz[:], m.Size)
	return crypto.Keccak256Hash(m.Root[:], sz[:], submitter[:])
}

// challengeIndices derives this epoch's sampled chunk indices.
func challengeIndices(id types.Hash, epoch uint64, chunkCount int) []int {
	if chunkCount <= 0 {
		return nil
	}
	n := challengeSamples
	if n > chunkCount {
		n = chunkCount
	}
	out := make([]int, 0, n)
	seen := make(map[int]bool, n)
	var eb [8]byte
	binary.BigEndian.PutUint64(eb[:], epoch)
	for k := 0; len(out) < n; k++ {
		var kb [8]byte
		binary.BigEndian.PutUint64(kb[:], uint64(k))
		h := crypto.Keccak256Hash(id[:], eb[:], kb[:])
		idx := int(binary.BigEndian.Uint64(h[:8]) % uint64(chunkCount))
		if seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, idx)
	}
	return out
}
