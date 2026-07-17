// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// simFleet wires N CohortCoordinators together via direct in-process
// callbacks (no real p2p) — every node's outbound index/cert announcement
// is delivered synchronously to every other node's inbound methods, the
// same "fully-connected mesh" a healthy small IDC fleet approximates.
type simFleet struct {
	nodes  []*CohortCoordinator
	stores []*CertStore
	addrs  []types.Address
}

func newSimFleet(t *testing.T, reg *Registry, lookup HeaderLookup, n int, cfg CohortConfig) *simFleet {
	t.Helper()
	f := &simFleet{
		nodes:  make([]*CohortCoordinator, n),
		stores: make([]*CertStore, n),
		addrs:  make([]types.Address, n),
	}
	for i := 0; i < n; i++ {
		var addr types.Address
		addr[19] = byte(i + 1) // distinct, stable per-node identity
		f.addrs[i] = addr
		f.stores[i] = NewCertStore(64)
		f.nodes[i] = NewCohortCoordinator(reg, lookup, f.stores[i], addr, cfg)
	}
	// Wire fan-out AFTER all nodes exist, closing over the index so each
	// node's sink reaches every OTHER node (not itself — it already applied
	// its own announcement locally in announceIndex/reconcileAndClose).
	for i := range f.nodes {
		i := i
		f.nodes[i].SetIndexAnnounceSink(func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment) {
			for j := range f.nodes {
				if j != i {
					f.nodes[j].OnPeerIndexSet(blockHash, reporter, indices)
				}
			}
		})
		f.nodes[i].SetCertAnnounceSink(func(reporter types.Address, cert *MobileAttestationCert) {
			for j := range f.nodes {
				if j != i {
					f.nodes[j].OnPeerCert(reporter, cert)
				}
			}
		})
	}
	return f
}

// commitHeight advances every node's shared clock identically — the
// coordination primitive the whole protocol depends on.
func (f *simFleet) commitHeight(number uint64) {
	for _, n := range f.nodes {
		n.OnBlockCommitted(number)
	}
}

// fixedLookupAt returns a HeaderLookup that only knows about one
// (hash, number) pair — enough for these tests, which drive a single block
// through the whole pipeline per test.
func fixedLookupAt(hash types.Hash, number uint64) HeaderLookup {
	return func(h types.Hash) (uint64, bool) {
		if h == hash {
			return number, true
		}
		return 0, false
	}
}

// TestCohortCoordinatorMergesDisjointCohortsAcrossFleet is the end-to-end
// payoff: 5 simulated IDC nodes each receive a DISJOINT slice of the same
// block's phone cohort (design's routing-fragmentation scenario). Driving
// the shared block-height clock through all three checkpoints must leave
// every node holding the IDENTICAL final merged certificate covering every
// phone — not 5 unrelated small certs.
func TestCohortCoordinatorMergesDisjointCohortsAcrossFleet(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}

	const nodes = 5
	const perNode = 8
	fleet := newSimFleet(t, reg, lookup, nodes, cfg)
	stores := fleet.stores

	totalDevices := 0
	for n := 0; n < nodes; n++ {
		for i := 0; i < perNode; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := fleet.nodes[n].Submit(d.receipt(blockHash, number, root)); err != nil {
				t.Fatalf("node %d submit: %v", n, err)
			}
			totalDevices++
		}
	}

	// Drive the shared clock through open+1, open+2, ..., open+4.
	for h := number; h <= number+cfg.MergeDelay; h++ {
		fleet.commitHeight(h)
	}

	var refCert *MobileAttestationCert
	for n := 0; n < nodes; n++ {
		got := stores[n].Get(blockHash)
		if len(got) != 1 {
			t.Fatalf("node %d: got %d certs for the block, want exactly 1 merged cert", n, len(got))
		}
		signers, err := got[0].Verify(reg)
		if err != nil {
			t.Fatalf("node %d: merged cert failed to verify: %v", n, err)
		}
		if len(signers) != totalDevices {
			t.Fatalf("node %d: merged cert covers %d signers, want %d (every node's cohort)", n, len(signers), totalDevices)
		}
		if refCert == nil {
			refCert = got[0]
		} else if refCert.AggregateSig != got[0].AggregateSig {
			t.Fatalf("node %d produced a DIFFERENT merged certificate than the reference node — "+
				"every node must independently converge to the identical result", n)
		}
	}
}

// TestCohortCoordinatorExcludesCrossNodeDuplicate: one device's signature
// (a protocol violation of "submit only to your fetch-source node")
// reaches TWO different nodes for the same block. Reconciliation must
// exclude it fleet-wide; every OTHER honest device across every node must
// still be fully counted in the final merged cert.
func TestCohortCoordinatorExcludesCrossNodeDuplicate(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}

	const nodes = 3
	stores := make([]*CertStore, nodes)
	coords := make([]*CohortCoordinator, nodes)
	addrs := make([]types.Address, nodes)
	for i := 0; i < nodes; i++ {
		var addr types.Address
		addr[19] = byte(i + 1)
		addrs[i] = addr
		stores[i] = NewCertStore(64)
		coords[i] = NewCohortCoordinator(reg, lookup, stores[i], addr, cfg)
	}
	for i := range coords {
		i := i
		coords[i].SetIndexAnnounceSink(func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment) {
			for j := range coords {
				if j != i {
					coords[j].OnPeerIndexSet(blockHash, reporter, indices)
				}
			}
		})
		coords[i].SetCertAnnounceSink(func(reporter types.Address, cert *MobileAttestationCert) {
			for j := range coords {
				if j != i {
					coords[j].OnPeerCert(reporter, cert)
				}
			}
		})
	}

	var honestDevices []*device
	for n := 0; n < nodes; n++ {
		for i := 0; i < 4; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := coords[n].Submit(d.receipt(blockHash, number, root)); err != nil {
				t.Fatalf("node %d submit: %v", n, err)
			}
			honestDevices = append(honestDevices, d)
		}
	}

	// The protocol violation: one device submits to BOTH node 0 and node 1.
	dupDevice := newDevice(t)
	registerCommitted(t, reg, dupDevice.pubkey, dupDevice.pop())
	if _, err := coords[0].Submit(dupDevice.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := coords[1].Submit(dupDevice.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}

	for height := number; height <= number+cfg.MergeDelay; height++ {
		for _, c := range coords {
			c.OnBlockCommitted(height)
		}
	}

	for n := 0; n < nodes; n++ {
		got := stores[n].Get(blockHash)
		if len(got) != 1 {
			t.Fatalf("node %d: got %d certs, want 1", n, len(got))
		}
		signers, err := got[0].Verify(reg)
		if err != nil {
			t.Fatalf("node %d: cert failed to verify: %v", n, err)
		}
		if len(signers) != len(honestDevices) {
			t.Fatalf("node %d: merged cert has %d signers, want exactly %d "+
				"(all honest devices present, duplicated device excluded)", n, len(signers), len(honestDevices))
		}
		dupIdx, _ := reg.Lookup(dupDevice.pubkey)
		for _, idx := range signers {
			if idx == dupIdx {
				t.Fatalf("node %d: the cross-node-duplicated device's signature made it into the final cert", n)
			}
		}
	}
}

// TestCohortCoordinatorGenuineDivergenceKeepsMajority: registered devices
// genuinely split on the receipts root (not a routing artifact — every
// node sees BOTH roots represented). The final cert must be the
// larger-cohort root, and a divergence alarm must fire.
func TestCohortCoordinatorGenuineDivergenceKeepsMajority(t *testing.T) {
	blockHash, number := h(1), uint64(1000)
	majorityRoot, minorityRoot := h(10), h(11)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}

	store := NewCertStore(64)
	var addr types.Address
	addr[19] = 1
	coord := NewCohortCoordinator(reg, lookup, store, addr, cfg)
	var alarms []DivergenceAlarm
	coord.SetDivergenceSink(func(a DivergenceAlarm) { alarms = append(alarms, a) })

	for i := 0; i < 20; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := coord.Submit(d.receipt(blockHash, number, majorityRoot)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := coord.Submit(d.receipt(blockHash, number, minorityRoot)); err != nil {
			t.Fatal(err)
		}
	}

	for height := number; height <= number+cfg.MergeDelay; height++ {
		coord.OnBlockCommitted(height)
	}

	got := store.Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("got %d certs, want exactly 1 (the majority survivor)", len(got))
	}
	if got[0].ReceiptsRoot != majorityRoot {
		t.Fatalf("winning root = %x, want the 20-signer majority %x", got[0].ReceiptsRoot, majorityRoot)
	}
	if len(alarms) != 1 {
		t.Fatalf("divergence alarms fired = %d, want exactly 1", len(alarms))
	}
	if alarms[0].Cohorts != 2 {
		t.Fatalf("alarm reported %d cohorts, want 2", alarms[0].Cohorts)
	}
}

// TestCohortCoordinatorSingleNodeStillWorks: with no peers wired at all
// (onIndexAnnounce/onCertAnnounce left nil), a lone node must still
// converge on its own local cert exactly like WindowManager did before —
// this pipeline must not require a healthy multi-node fleet to function.
func TestCohortCoordinatorSingleNodeStillWorks(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	store := NewCertStore(64)
	var addr types.Address
	addr[19] = 1
	coord := NewCohortCoordinator(reg, lookup, store, addr, DefaultCohortConfig())

	var devices []*device
	for i := 0; i < 7; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		devices = append(devices, d)
		if _, err := coord.Submit(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}

	cfg := DefaultCohortConfig()
	for height := number; height <= number+cfg.MergeDelay; height++ {
		coord.OnBlockCommitted(height)
	}

	got := store.Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("got %d certs, want 1", len(got))
	}
	signers, err := got[0].Verify(reg)
	if err != nil {
		t.Fatalf("cert failed to verify: %v", err)
	}
	if len(signers) != len(devices) {
		t.Fatalf("signer count = %d, want %d", len(signers), len(devices))
	}
}

// TestOnBlockCommittedConcurrentCallsDoNotDuplicateAlarms is the regression
// test for a real concurrency bug an audit caught: OnBlockCommitted's phase
// transitions were originally decided (read w.phase) and CLAIMED (write
// w.phase) in two separate lock sections, so two concurrent calls could both
// observe the same pre-transition phase and both invoke the same handler —
// most visibly, mergeAndFinalize running twice for one window and firing
// the divergence alarm twice. The fix claims each transition atomically with
// the decision (single lock section). Drive many concurrent
// OnBlockCommitted calls for the same heights and assert the alarm and the
// final cert store each reflect exactly one merge, not N.
func TestOnBlockCommittedConcurrentCallsDoNotDuplicateAlarms(t *testing.T) {
	blockHash, number := h(1), uint64(1000)
	majorityRoot, minorityRoot := h(10), h(11)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}

	store := NewCertStore(64)
	var addr types.Address
	addr[19] = 1
	coord := NewCohortCoordinator(reg, lookup, store, addr, cfg)
	var mu sync.Mutex
	var alarms []DivergenceAlarm
	coord.SetDivergenceSink(func(a DivergenceAlarm) {
		mu.Lock()
		alarms = append(alarms, a)
		mu.Unlock()
	})

	for i := 0; i < 20; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := coord.Submit(d.receipt(blockHash, number, majorityRoot)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := coord.Submit(d.receipt(blockHash, number, minorityRoot)); err != nil {
			t.Fatal(err)
		}
	}

	// Fire many concurrent, overlapping OnBlockCommitted calls across every
	// checkpoint height — the exact race the fix closes.
	var wg sync.WaitGroup
	for round := 0; round < 8; round++ {
		for height := number; height <= number+cfg.MergeDelay; height++ {
			height := height
			wg.Add(1)
			go func() {
				defer wg.Done()
				coord.OnBlockCommitted(height)
			}()
		}
	}
	wg.Wait()
	// A couple of settling calls in case any goroutine's height arrived out
	// of scheduling order relative to another.
	coord.OnBlockCommitted(number + cfg.MergeDelay)
	coord.OnBlockCommitted(number + cfg.MergeDelay)

	got := store.Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("cert store has %d entries for the block, want exactly 1 (no duplicate merge)", len(got))
	}
	if got[0].ReceiptsRoot != majorityRoot {
		t.Fatalf("winning root = %x, want the majority %x", got[0].ReceiptsRoot, majorityRoot)
	}
	mu.Lock()
	n := len(alarms)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("divergence alarms fired = %d, want exactly 1 (concurrent OnBlockCommitted calls must not double-finalize)", n)
	}
}

// TestSubmitAnchorsCheckpointsToReceiptHeightNotSubmitTimeHeight is the
// regression test for a related bug an audit caught: Submit originally
// stamped a window's openedHeight from c.currentHeight (whatever height this
// node happened to know about AT SUBMIT TIME — 0 if no OnBlockCommitted had
// run yet), not from the receipt's own (already lookup-verified) block
// height. A receipt for an OLD, already-well-past block would then measure
// its checkpoints from "now" instead of the block's real height, letting an
// attacker (or just a slow phone) hold a window open for MergeDelay MORE
// blocks into the future purely by referencing a stale-but-known block hash
// — compounding the maxOpenWindows cap into a resource-exhaustion vector.
// Anchoring to the receipt's real height means a stale submission's
// checkpoints are already in the past relative to the current height, so it
// flushes on the very next OnBlockCommitted call instead of staying open.
func TestSubmitAnchorsCheckpointsToReceiptHeightNotSubmitTimeHeight(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	lookup := fixedLookupAt(blockHash, number)
	cfg := CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}
	store := NewCertStore(64)
	var addr types.Address
	addr[19] = 1
	coord := NewCohortCoordinator(reg, lookup, store, addr, cfg)

	// Advance this node's own clock WAY past the block's height BEFORE the
	// receipt ever arrives — simulating a phone submitting a receipt for an
	// old block long after the fact.
	coord.OnBlockCommitted(number + 500)

	d := newDevice(t)
	registerCommitted(t, reg, d.pubkey, d.pop())
	if _, err := coord.Submit(d.receipt(blockHash, number, root)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if coord.OpenWindows() != 1 {
		t.Fatalf("open windows = %d, want 1 right after submit", coord.OpenWindows())
	}

	// A single further height notification (this node's clock is already
	// far past every checkpoint relative to the block's REAL height) must
	// flush the window all the way to finalization in one shot — not stay
	// open waiting MergeDelay MORE blocks from "now".
	coord.OnBlockCommitted(number + 501)

	if coord.OpenWindows() != 0 {
		t.Fatalf("open windows = %d, want 0 — a stale receipt must flush promptly, not linger", coord.OpenWindows())
	}
	got := store.Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("cert store has %d entries, want 1", len(got))
	}
}
