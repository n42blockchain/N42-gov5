// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Acceptance tests for the storage deal lifecycle (slice 1). These are the
// contract for docs/distributed-storage-design-2026-07-06.md §1-§7:
// upload/manifest, replication redundancy, per-epoch random-spot-check
// challenges, Verify-or-Slash on a failed proof, repair from a surviving
// copy, Byte·Epoch metering + streaming escrow settlement, and conservation.

package deal

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// ---------------------------------------------------------------------------
// harness

type harness struct {
	mgr      *DealManager
	registry *coprocessor.ProviderRegistry
	slasher  *coprocessor.SlashManager
	mem      map[types.Address]*MemProvider
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	registry := coprocessor.NewProviderRegistry(1)
	slasher := coprocessor.NewSlashManager(registry, 20) // slash 20% of stake
	mgr := NewDealManager(Config{Registry: registry, Slasher: slasher})
	return &harness{mgr: mgr, registry: registry, slasher: slasher, mem: map[types.Address]*MemProvider{}}
}

func addr(seed byte) types.Address {
	var a types.Address
	for i := range a {
		a[i] = seed
	}
	return a
}

// addProvider registers a provider + a healthy MemProvider node in `group`.
func (h *harness) addProvider(t *testing.T, a types.Address, group string, stake uint64) *MemProvider {
	t.Helper()
	if err := h.registry.Register(a, stake, []coprocessor.Capability{coprocessor.CapGeneral}); err != nil {
		t.Fatalf("register: %v", err)
	}
	mp := NewMemProvider()
	if err := h.mgr.AddProvider(a, group, mp); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	h.mem[a] = mp
	return mp
}

func baseSpec() *DealSpec {
	return &DealSpec{
		Data:             bytes.Repeat([]byte("N42-storage-payload!"), 5000), // ~100 KB
		Redundancy:       3,
		Disperse:         true,
		ChunkSize:        4096,
		EpochPrice:       1, // wei/byte/epoch/replica
		Epochs:           4,
		ChallengeTimeout: time.Second,
	}
}

// ---------------------------------------------------------------------------
// tests

// D1: happy path — submit, r replicas store, every epoch all pass, providers
// paid Byte·Epoch, deal completes, escrow conserves.
func TestDealHappyPath(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		h.addProvider(t, addr(byte(1+i)), fmt.Sprintf("g%d", i), 1_000_000)
	}
	spec := baseSpec()
	id, err := h.mgr.Submit(context.Background(), spec, addr(0xEE))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	d, _ := h.mgr.Get(id)
	if len(d.Replicas) != 3 {
		t.Fatalf("replicas = %d, want 3", len(d.Replicas))
	}

	size := uint64(len(spec.Data))
	for e := uint64(0); e < spec.Epochs; e++ {
		res, err := h.mgr.RunChallengeEpoch(context.Background(), id)
		if err != nil {
			t.Fatalf("epoch %d: %v", e, err)
		}
		if res.Passed != 3 || res.Failed != 0 {
			t.Fatalf("epoch %d: passed=%d failed=%d", e, res.Passed, res.Failed)
		}
	}
	d, _ = h.mgr.Get(id)
	if d.Status != DealCompleted {
		t.Fatalf("status = %v, want completed", d.Status)
	}
	// Each replica earned size*price per epoch it passed.
	wantPerProvider := size * spec.EpochPrice * spec.Epochs
	for _, r := range d.Replicas {
		if got := h.mgr.Escrow().PaidTo(r.Provider); got != wantPerProvider {
			t.Fatalf("provider %s paid %d, want %d", r.Provider.Hex()[:8], got, wantPerProvider)
		}
	}
	// Conservation.
	assertConserved(t, h, id, spec)
}

// D2: merkle proof soundness — a valid proof verifies; tampering the chunk or
// a sibling breaks verification.
func TestMerkleProofSoundness(t *testing.T) {
	data := bytes.Repeat([]byte("abcd"), 4000)
	chunks := chunkData(data, 1024)
	tree := buildMerkle(chunkHashes(chunks))
	root := tree.root()

	for i := range chunks {
		p, err := tree.proof(i)
		if err != nil {
			t.Fatalf("proof %d: %v", i, err)
		}
		if !VerifyChunk(root, chunks[i], p) {
			t.Fatalf("valid proof for chunk %d rejected", i)
		}
		// Tamper chunk.
		bad := append([]byte(nil), chunks[i]...)
		bad[0] ^= 0xFF
		if VerifyChunk(root, bad, p) {
			t.Fatalf("tampered chunk %d accepted", i)
		}
		// Tamper a sibling.
		if len(p.Siblings) > 0 {
			p2 := p
			p2.Siblings = append([]types.Hash(nil), p.Siblings...)
			p2.Siblings[0][0] ^= 0xFF
			if VerifyChunk(root, chunks[i], p2) {
				t.Fatalf("tampered proof for chunk %d accepted", i)
			}
		}
	}
}

// D3: missed challenge — one provider goes offline; it fails its challenge, is
// slashed for timeout, and its replica is scheduled for repair.
func TestMissedChallengeSlash(t *testing.T) {
	h := newHarness(t)
	a1, a2, a3 := addr(1), addr(2), addr(3)
	h.addProvider(t, a1, "g0", 1_000_000)
	h.addProvider(t, a2, "g1", 1_000_000)
	h.addProvider(t, a3, "g2", 1_000_000)
	// A 4th provider exists for repair to land on.
	h.addProvider(t, addr(4), "g3", 1_000_000)

	spec := baseSpec()
	id, err := h.mgr.Submit(context.Background(), spec, addr(0xEE))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Find which provider actually holds a replica, then make it fail.
	d, _ := h.mgr.Get(id)
	victim := d.Replicas[0].Provider
	h.mem[victim].SetOffline(true)

	res, err := h.mgr.RunChallengeEpoch(context.Background(), id)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	if res.Failed != 1 || res.Passed != 2 {
		t.Fatalf("passed=%d failed=%d, want 2/1", res.Passed, res.Failed)
	}
	// Victim slashed for timeout; not paid this epoch.
	p, _ := h.registry.GetSnapshot(victim)
	if p.Stake >= 1_000_000 {
		t.Fatalf("victim stake not deducted: %d", p.Stake)
	}
	evts := h.slasher.SlashHistory()
	if len(evts) != 1 || evts[0].Provider != victim || evts[0].Condition != coprocessor.SlashTimeout {
		t.Fatalf("slash events = %+v", evts)
	}
	if got := h.mgr.Escrow().PaidTo(victim); got != 0 {
		t.Fatalf("offline victim was paid %d", got)
	}
}

// D4: repair — after a failure, RepairDeal restores full redundancy by copying
// from a surviving replica onto a fresh provider, verified against the root.
func TestRepairRestoresRedundancy(t *testing.T) {
	h := newHarness(t)
	held := []types.Address{addr(1), addr(2), addr(3)}
	for i, a := range held {
		h.addProvider(t, a, fmt.Sprintf("g%d", i), 1_000_000)
	}
	spare := addr(9)
	h.addProvider(t, spare, "g9", 1_000_000)

	spec := baseSpec()
	id, _ := h.mgr.Submit(context.Background(), spec, addr(0xEE))
	d, _ := h.mgr.Get(id)
	victim := d.Replicas[0].Provider
	h.mem[victim].SetOffline(true)

	if _, err := h.mgr.RunChallengeEpoch(context.Background(), id); err != nil {
		t.Fatalf("epoch: %v", err)
	}
	// One replica is now unhealthy.
	d, _ = h.mgr.Get(id)
	unhealthy := 0
	for _, r := range d.Replicas {
		if !r.Healthy {
			unhealthy++
		}
	}
	if unhealthy != 1 {
		t.Fatalf("unhealthy replicas = %d, want 1", unhealthy)
	}

	n, err := h.mgr.RepairDeal(context.Background(), id)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if n != 1 {
		t.Fatalf("repaired %d replicas, want 1", n)
	}
	// Redundancy restored, all healthy, the fresh provider now holds the data
	// and can prove it.
	d, _ = h.mgr.Get(id)
	if len(d.Replicas) != 3 {
		t.Fatalf("replicas after repair = %d, want 3", len(d.Replicas))
	}
	for _, r := range d.Replicas {
		if !r.Healthy {
			t.Fatalf("replica %s still unhealthy after repair", r.Provider.Hex()[:8])
		}
	}
	// The spare must now serve a valid proof for a random chunk.
	if _, ok := h.mem[spare]; ok {
		_, proof, err := h.mem[spare].Prove(context.Background(), id, 0)
		if err != nil {
			t.Fatalf("spare cannot prove after repair: %v", err)
		}
		chunk, _, _ := h.mem[spare].Prove(context.Background(), id, 0)
		if !VerifyChunk(d.Manifest.Root, chunk, proof) {
			t.Fatalf("spare's proof does not verify against the manifest root")
		}
	}
}

// D5: proof forgery — a provider that never stored the deal cannot answer a
// challenge; it fails and is slashed for an invalid proof.
func TestProofForgeryRejected(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		h.addProvider(t, addr(byte(1+i)), fmt.Sprintf("g%d", i), 1_000_000)
	}
	spec := baseSpec()
	id, _ := h.mgr.Submit(context.Background(), spec, addr(0xEE))
	d, _ := h.mgr.Get(id)

	// Make one provider return a wrong (well-formed but false) proof.
	victim := d.Replicas[0].Provider
	h.mem[victim].SetCorrupt(true)

	res, err := h.mgr.RunChallengeEpoch(context.Background(), id)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("failed=%d, want 1", res.Failed)
	}
	evts := h.slasher.SlashHistory()
	if len(evts) != 1 || evts[0].Condition != coprocessor.SlashInvalidProof {
		t.Fatalf("slash events = %+v, want one InvalidProof", evts)
	}
}

// D6: metering — Byte·Epoch accrues per passed epoch and the deal completes
// exactly at spec.Epochs; running an extra epoch is a no-op error.
func TestMeteringAndCompletion(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		h.addProvider(t, addr(byte(1+i)), fmt.Sprintf("g%d", i), 1_000_000)
	}
	spec := baseSpec()
	spec.Epochs = 2
	id, _ := h.mgr.Submit(context.Background(), spec, addr(0xEE))

	for e := 0; e < 2; e++ {
		if _, err := h.mgr.RunChallengeEpoch(context.Background(), id); err != nil {
			t.Fatalf("epoch %d: %v", e, err)
		}
	}
	if _, err := h.mgr.RunChallengeEpoch(context.Background(), id); err == nil {
		t.Fatalf("challenge past completion must error")
	}
	d, _ := h.mgr.Get(id)
	if d.Status != DealCompleted || d.EpochsElapsed != 2 {
		t.Fatalf("status=%v elapsed=%d", d.Status, d.EpochsElapsed)
	}
	assertConserved(t, h, id, spec)
}

// D7: insufficient distinct groups with Disperse fails at submit and locks no
// escrow (fail-fast, no silent weakening).
func TestInsufficientProviders(t *testing.T) {
	h := newHarness(t)
	// 3 providers but only 2 distinct groups, Disperse requires 3.
	h.addProvider(t, addr(1), "g0", 1_000_000)
	h.addProvider(t, addr(2), "g0", 1_000_000)
	h.addProvider(t, addr(3), "g1", 1_000_000)

	spec := baseSpec()
	spec.Disperse = true
	if _, err := h.mgr.Submit(context.Background(), spec, addr(0xEE)); err == nil {
		t.Fatalf("submit must fail with insufficient distinct groups")
	}
	if h.mgr.Escrow().TotalLocked() != 0 {
		t.Fatalf("escrow locked despite failed submit")
	}
}

// D8: deal fails permanently if redundancy cannot be repaired (no spare
// providers), and the remaining escrow is refunded to the submitter.
func TestUnrepairableDealRefunds(t *testing.T) {
	h := newHarness(t)
	held := []types.Address{addr(1), addr(2), addr(3)}
	for i, a := range held {
		h.addProvider(t, a, fmt.Sprintf("g%d", i), 1_000_000)
	}
	spec := baseSpec()
	id, _ := h.mgr.Submit(context.Background(), spec, addr(0xEE))
	d, _ := h.mgr.Get(id)

	// Two of three fail: below the point where repair is possible (no spares).
	h.mem[d.Replicas[0].Provider].SetOffline(true)
	h.mem[d.Replicas[1].Provider].SetOffline(true)
	if _, err := h.mgr.RunChallengeEpoch(context.Background(), id); err != nil {
		t.Fatalf("epoch: %v", err)
	}
	if _, err := h.mgr.RepairDeal(context.Background(), id); err == nil {
		t.Fatalf("repair should fail with no spare providers")
	}
	h.mgr.FailDeal(id, "unrepairable")
	d, _ = h.mgr.Get(id)
	if d.Status != DealFailed {
		t.Fatalf("status = %v, want failed", d.Status)
	}
	assertConserved(t, h, id, spec)
}

// D9: concurrent challenge epochs across many deals stay isolated and conserve.
// Run with -race.
func TestConcurrentDeals(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		h.addProvider(t, addr(byte(1+i)), fmt.Sprintf("g%d", i), 100_000_000)
	}
	const n = 12
	ids := make([]types.Hash, n)
	specs := make([]*DealSpec, n)
	for i := 0; i < n; i++ {
		spec := baseSpec()
		spec.Data = bytes.Repeat([]byte{byte(i)}, 8192)
		spec.Epochs = 2
		id, err := h.mgr.Submit(context.Background(), spec, addr(0xEE))
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		ids[i] = id
		specs[i] = spec
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for e := 0; e < 2; e++ {
				_, _ = h.mgr.RunChallengeEpoch(context.Background(), ids[i])
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		d, _ := h.mgr.Get(ids[i])
		if d.Status != DealCompleted {
			t.Fatalf("deal %d status = %v", i, d.Status)
		}
		assertConserved(t, h, ids[i], specs[i])
	}
}

// D10: spec validation happens before any escrow is locked.
func TestSpecValidation(t *testing.T) {
	h := newHarness(t)
	h.addProvider(t, addr(1), "g0", 1_000_000)

	for _, bad := range []*DealSpec{
		{Data: []byte("x"), Redundancy: 0, Epochs: 1, EpochPrice: 1},
		{Data: []byte("x"), Redundancy: 1, Epochs: 0, EpochPrice: 1},
		{Data: []byte("x"), Redundancy: 1, Epochs: 1, EpochPrice: 0},
	} {
		if _, err := h.mgr.Submit(context.Background(), bad, addr(0xEE)); err == nil {
			t.Fatalf("invalid spec accepted: %+v", bad)
		}
	}
	if h.mgr.Escrow().TotalLocked() != 0 {
		t.Fatalf("escrow locked for invalid specs")
	}
}

// assertConserved verifies locked == paid + refunded for a settled deal,
// attributing payouts to this deal (a provider may earn across many deals).
func assertConserved(t *testing.T, h *harness, id types.Hash, spec *DealSpec) {
	t.Helper()
	locked := uint64(len(spec.Data)) * spec.EpochPrice * uint64(spec.Redundancy) * spec.Epochs
	paid := h.mgr.Escrow().PaidForDeal(id)
	refunded := h.mgr.Escrow().Refunded(id)
	if paid+refunded != locked {
		t.Fatalf("deal %s escrow leak: paid %d + refunded %d != locked %d",
			id.Hex()[:8], paid, refunded, locked)
	}
}
