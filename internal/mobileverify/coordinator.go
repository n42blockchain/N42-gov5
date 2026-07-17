// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// CohortCoordinator replaces the per-node wall-clock collection window with
// a block-height-driven, three-phase, cross-node protocol:
//
//  1. Collecting  — same as before: phones submit receipts (design §7's
//     submit-to-fetch-source rule keeps a device's signature at exactly one
//     origin node, so no node ever needs to see a duplicate at admission
//     time — this coordinator does not re-verify that rule; it is the
//     phone-side/SDK behavior it depends on).
//  2. IndexAnnounced — at height openedHeight+IndexAnnounceDelay, this node
//     announces its admitted MobileIndex set (no signatures) and starts
//     collecting peers' announcements.
//  3. Reconciled — at height openedHeight+ReconcileDelay, ReconcileIndices
//     computes which indices were announced by more than one node;
//     Collector.ExcludeIndices removes them BEFORE this node's own local
//     aggregation, so the resulting local cert(s) are pairwise
//     signer-disjoint from every other node's by construction. The local
//     cert(s) are then announced and this node starts collecting peers'.
//  4. Final — at height openedHeight+MergeDelay, MergeCerts combines every
//     same-root cert received (from itself and peers) into one per root,
//     then SelectMajorityBucket keeps only the largest-cohort root bucket if
//     more than one survives — a real receipts-root divergence at that
//     point, not a routing artifact. The result lands in CertStore exactly
//     like today; the divergence alarm fires only for a genuine multi-root
//     outcome (see cross-node reconciliation rationale in reconcile.go).
//
// Every phase transition is driven by block height (OnBlockCommitted), not
// each node's own wall clock — every node computes the identical checkpoint
// for the identical block, which is the coordination primitive the
// multi-node merge depends on (two nodes racing to close on their own timers
// could not consistently agree on the same input set to merge).
package mobileverify

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// CohortConfig sizes the three height-relative checkpoints. Defaults are
// small multiples of one block — generous for a phone's verify+submit round
// trip and one gossip hop, and comfortably faster than the wall-clock window
// this replaces (design's original 45s dwarfs the ~2-3s block cadence this
// pipeline actually runs on).
type CohortConfig struct {
	IndexAnnounceDelay uint64 // blocks after open before announcing the index set
	ReconcileDelay     uint64 // blocks after open before excluding conflicts + closing locally
	MergeDelay         uint64 // blocks after open before merging peer certs + finalizing
}

// DefaultCohortConfig returns conservative defaults (1 / 2 / 4 blocks).
func DefaultCohortConfig() CohortConfig {
	return CohortConfig{IndexAnnounceDelay: 1, ReconcileDelay: 2, MergeDelay: 4}
}

func (c CohortConfig) sane() CohortConfig {
	if c.IndexAnnounceDelay == 0 {
		c.IndexAnnounceDelay = 1
	}
	if c.ReconcileDelay <= c.IndexAnnounceDelay {
		c.ReconcileDelay = c.IndexAnnounceDelay + 1
	}
	if c.MergeDelay <= c.ReconcileDelay {
		c.MergeDelay = c.ReconcileDelay + 2
	}
	return c
}

type cohortPhase int

const (
	phaseCollecting cohortPhase = iota
	phaseIndexAnnounced
	phaseReconciled
	phaseFinal
)

type cohortWindow struct {
	blockHash    types.Hash
	blockNumber  uint64
	openedHeight uint64
	col          *Collector
	phase        cohortPhase
	// inFlight is true while some goroutine is actively cascading this
	// window through its phase handlers — claimed atomically alongside the
	// phase check (same c.mu section), so at most one goroutine ever runs
	// announceIndex/reconcileAndClose/mergeAndFinalize for a given window at
	// a time. Without this, two concurrent OnBlockCommitted calls (or one
	// racing Stop) could each independently decide a DIFFERENT transition is
	// due and run its handler concurrently — e.g. one claiming and starting
	// announceIndex while another, moments later, already sees the phase it
	// just flipped to and claims+runs reconcileAndClose BEFORE
	// announceIndex's body has actually finished writing peerIndexSets,
	// so the reconcile step reads no announcements at all. See
	// TestOnBlockCommittedConcurrentCallsDoNotDuplicateAlarms.
	inFlight bool

	// peerIndexSets includes this node's own announcement under selfAddr, so
	// ReconcileIndices sees the identical union every reporting node sees.
	// Carries (index, commitment) pairs, not bare indices — see reconcile.go
	// for why an unauthenticated bare index claim is itself a censorship
	// vector this pairing closes.
	peerIndexSets map[types.Address][]IndexCommitment

	// localCerts: this node's own post-exclusion Close() output, keyed by
	// ReceiptsRoot — almost always one entry; more than one only if THIS
	// node itself observed a local receipts-root split.
	localCerts map[types.Hash]*MobileAttestationCert

	// peerCerts: root -> reporter -> cert, including this node's own entries
	// (added under selfAddr at the same time as localCerts).
	peerCerts map[types.Hash]map[types.Address]*MobileAttestationCert
}

// CohortCoordinator owns the full per-block cohort-certificate lifecycle
// (see package doc above). It is the drop-in replacement for WindowManager's
// wall-clock scheme; construct one per node.
type CohortCoordinator struct {
	reg      *Registry
	lookup   HeaderLookup
	certs    *CertStore
	selfAddr types.Address
	cfg      CohortConfig

	// onDivergence fires only for a genuine post-merge multi-root outcome —
	// routing fragmentation across IDC nodes is resolved before this ever
	// sees it, so every firing here is a real receipts-root disagreement.
	onDivergence func(DivergenceAlarm)
	// onIndexAnnounce / onCertAnnounce are the outbound gossip hooks a thin
	// P2P wrapper wires to actual topics; nil is a valid single-node mode
	// (no peers, so every window trivially "merges" to just its own cert).
	onIndexAnnounce func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment)
	onCertAnnounce  func(reporter types.Address, cert *MobileAttestationCert)

	mu            sync.Mutex
	currentHeight uint64
	windows       map[types.Hash]*cohortWindow
	stopped       bool
}

// NewCohortCoordinator creates a coordinator. selfAddr identifies this node
// in index/cert announcements — any stable per-node identity works (the
// validator/etherbase address is what every other N42 subsystem already
// uses for node identity).
func NewCohortCoordinator(reg *Registry, lookup HeaderLookup, certs *CertStore, selfAddr types.Address, cfg CohortConfig) *CohortCoordinator {
	return &CohortCoordinator{
		reg:      reg,
		lookup:   lookup,
		certs:    certs,
		selfAddr: selfAddr,
		cfg:      cfg.sane(),
		windows:  make(map[types.Hash]*cohortWindow),
	}
}

// SetDivergenceSink installs the alarm callback for a genuine post-merge
// multi-root outcome. Set once at startup.
func (c *CohortCoordinator) SetDivergenceSink(fn func(DivergenceAlarm)) {
	c.mu.Lock()
	c.onDivergence = fn
	c.mu.Unlock()
}

// SetIndexAnnounceSink installs the outbound hook for index-set gossip.
func (c *CohortCoordinator) SetIndexAnnounceSink(fn func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment)) {
	c.mu.Lock()
	c.onIndexAnnounce = fn
	c.mu.Unlock()
}

// SetCertAnnounceSink installs the outbound hook for local-cert gossip.
func (c *CohortCoordinator) SetCertAnnounceSink(fn func(reporter types.Address, cert *MobileAttestationCert)) {
	c.mu.Lock()
	c.onCertAnnounce = fn
	c.mu.Unlock()
}

// Submit routes one receipt into its block's window, opening it (at the
// CURRENT height) if this is the block's first receipt. Rejected once the
// window has left the collecting phase — mirroring WindowManager's
// ErrWindowClosed for a submission that arrives too late to matter.
func (c *CohortCoordinator) Submit(r *Receipt) (MobileIndex, error) {
	if r == nil {
		return 0, errors.New("mobileverify: nil receipt")
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return 0, ErrWindowClosed
	}
	w, ok := c.windows[r.BlockHash]
	if !ok {
		number, known := c.lookup(r.BlockHash)
		if !known {
			c.mu.Unlock()
			return 0, ErrUnknownBlock
		}
		if number != r.BlockNumber {
			c.mu.Unlock()
			return 0, fmt.Errorf("%w: receipt number %d, block is %d", ErrWrongBlock, r.BlockNumber, number)
		}
		if len(c.windows) >= maxOpenWindows {
			c.mu.Unlock()
			return 0, ErrTooManyWindows
		}
		w = &cohortWindow{
			blockHash:   r.BlockHash,
			blockNumber: r.BlockNumber,
			// Anchor checkpoints to the RECEIPT's own (already lookup-
			// verified, so already-committed) block height, not whatever
			// c.currentHeight this node happens to know at Submit time.
			// c.currentHeight can be 0 (no OnBlockCommitted has run yet — a
			// window opened before this node's own clock ever advanced would
			// otherwise measure its delays from the wrong origin entirely)
			// or, for a late-arriving receipt about an old block, already far
			// ahead of the block's real height (which would silently strand
			// the window's checkpoints in the future relative to when the
			// block actually happened, rather than bounding staleness the
			// way N+delay is meant to). r.BlockNumber is real and trustworthy
			// here precisely because lookup() already confirmed it matches a
			// block this node's chain actually has.
			openedHeight:  r.BlockNumber,
			col:           NewCollector(c.reg, r.BlockHash, r.BlockNumber),
			peerIndexSets: make(map[types.Address][]IndexCommitment),
			localCerts:    make(map[types.Hash]*MobileAttestationCert),
			peerCerts:     make(map[types.Hash]map[types.Address]*MobileAttestationCert),
		}
		c.windows[r.BlockHash] = w
	}
	if w.phase != phaseCollecting {
		c.mu.Unlock()
		return 0, ErrWindowClosed
	}
	col := w.col
	c.mu.Unlock()

	idx, err := col.Add(r)
	if err == nil {
		c.reg.MarkActive(r.VerifierPubkey)
	}
	return idx, err
}

// OnPeerIndexSet admits a peer's index-set announcement for a block this
// node also has (or will have) a window for. Safe to call before this
// node's own window exists yet — the announcement is buffered under a
// synthetic placeholder window if needed... actually: a peer's index
// announcement for a block THIS node has zero receipts for is simply
// irrelevant (this node has nothing to reconcile against), so unknown
// blocks are dropped rather than buffered, keeping memory bounded.
func (c *CohortCoordinator) OnPeerIndexSet(blockHash types.Hash, reporter types.Address, indices []IndexCommitment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[blockHash]
	if !ok {
		return
	}
	cp := make([]IndexCommitment, len(indices))
	copy(cp, indices)
	w.peerIndexSets[reporter] = cp
}

// OnPeerCert admits a peer's local (already exclusion-cleaned) certificate.
func (c *CohortCoordinator) OnPeerCert(reporter types.Address, cert *MobileAttestationCert) {
	if cert == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[cert.BlockHash]
	if !ok {
		return
	}
	byReporter, ok := w.peerCerts[cert.ReceiptsRoot]
	if !ok {
		byReporter = make(map[types.Address]*MobileAttestationCert)
		w.peerCerts[cert.ReceiptsRoot] = byReporter
	}
	byReporter[reporter] = cert
}

// OnBlockCommitted advances every open window whose next checkpoint has now
// been reached. Call this on every newly-committed canonical block, on
// every node (leader or follower) — it is the shared clock the whole
// cross-node protocol is built on.
//
// Claims each window's cascade via inFlight (see cohortWindow's doc
// comment) so at most one goroutine ever runs a given window's handlers at
// a time, and hands the whole cascade — through as many transitions as the
// CURRENT height allows, not just the one this call happened to observe —
// to whichever goroutine wins the claim. Two mistakes this specifically
// avoids, both caught by TestOnBlockCommittedConcurrentCallsDoNotDuplicateAlarms:
// (1) claiming a transition without also serializing against a second
// concurrent claim of the SAME window's NEXT transition, which could let
// reconcileAndClose run before announceIndex's body has finished writing
// peerIndexSets; (2) reading the request's own `number` parameter deep
// inside the cascade instead of the shared c.currentHeight, which would
// lose height information from a concurrent caller that saw a higher number
// but skipped this window because it was already in flight.
func (c *CohortCoordinator) OnBlockCommitted(number uint64) {
	c.mu.Lock()
	if number > c.currentHeight {
		c.currentHeight = number
	}
	var toProcess []*cohortWindow
	for _, w := range c.windows {
		if !w.inFlight && c.windowHasPendingTransitionLocked(w) {
			w.inFlight = true
			toProcess = append(toProcess, w)
		}
	}
	c.mu.Unlock()

	for _, w := range toProcess {
		c.cascadeWindow(w, false)
	}
}

// windowHasPendingTransitionLocked reports whether w's current phase has a
// height-gated transition due at c.currentHeight. Caller must hold c.mu.
func (c *CohortCoordinator) windowHasPendingTransitionLocked(w *cohortWindow) bool {
	switch w.phase {
	case phaseCollecting:
		return c.currentHeight >= w.openedHeight+c.cfg.IndexAnnounceDelay
	case phaseIndexAnnounced:
		return c.currentHeight >= w.openedHeight+c.cfg.ReconcileDelay
	case phaseReconciled:
		return c.currentHeight >= w.openedHeight+c.cfg.MergeDelay
	}
	return false
}

// cascadeWindow assumes the caller already claimed w.inFlight (so no other
// goroutine will concurrently process this SAME window) and advances it
// through every transition currently due, one at a time, re-checking the
// SHARED c.currentHeight (force ignores height gating entirely, for Stop's
// shutdown flush) on each iteration rather than a stale snapshot — so a
// higher height observed by a different, concurrently-skipped caller is
// never lost, it just gets picked up on this goroutine's next loop turn.
// Releases inFlight (without ever holding c.mu across a handler call) once
// no further transition is due.
func (c *CohortCoordinator) cascadeWindow(w *cohortWindow, force bool) {
	for {
		c.mu.Lock()
		var next cohortPhase
		claimed := false
		if force {
			switch w.phase {
			case phaseCollecting:
				w.phase, next, claimed = phaseIndexAnnounced, phaseIndexAnnounced, true
			case phaseIndexAnnounced:
				w.phase, next, claimed = phaseReconciled, phaseReconciled, true
			case phaseReconciled:
				w.phase, next, claimed = phaseFinal, phaseFinal, true
			}
		} else {
			switch {
			case w.phase == phaseCollecting && c.currentHeight >= w.openedHeight+c.cfg.IndexAnnounceDelay:
				w.phase, next, claimed = phaseIndexAnnounced, phaseIndexAnnounced, true
			case w.phase == phaseIndexAnnounced && c.currentHeight >= w.openedHeight+c.cfg.ReconcileDelay:
				w.phase, next, claimed = phaseReconciled, phaseReconciled, true
			case w.phase == phaseReconciled && c.currentHeight >= w.openedHeight+c.cfg.MergeDelay:
				w.phase, next, claimed = phaseFinal, phaseFinal, true
			}
		}
		if !claimed {
			w.inFlight = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		// Never hold the lock across a handler: an in-process test (or a
		// same-process fan-out gossip shim) may re-enter another
		// coordinator's OnPeer* synchronously from within these sinks.
		switch next {
		case phaseIndexAnnounced:
			c.announceIndex(w)
		case phaseReconciled:
			c.reconcileAndClose(w)
		case phaseFinal:
			c.mergeAndFinalize(w) // deletes w from c.windows; nothing left to loop on
			return
		}
	}
}

// announceIndex assumes the caller already claimed this window's transition
// into phaseIndexAnnounced — it never touches w.phase itself.
func (c *CohortCoordinator) announceIndex(w *cohortWindow) {
	// Freeze (not Indices) atomically snapshots AND seals this window
	// against further Add calls, inside Collector's own lock — closing the
	// gap a plain read-then-flip-phase-later would leave open for a
	// concurrent Submit to slip an unreconciled receipt into the local
	// aggregate (see Collector.Freeze's doc comment).
	mine := w.col.Freeze()
	c.mu.Lock()
	w.peerIndexSets[c.selfAddr] = mine
	sink := c.onIndexAnnounce
	c.mu.Unlock()
	if sink != nil {
		sink(w.blockHash, w.blockNumber, c.selfAddr, mine)
	}
}

// reconcileAndClose assumes the caller already claimed this window's
// transition into phaseReconciled.
func (c *CohortCoordinator) reconcileAndClose(w *cohortWindow) {
	c.mu.Lock()
	sets := make([][]IndexCommitment, 0, len(w.peerIndexSets))
	for _, s := range w.peerIndexSets {
		sets = append(sets, s)
	}
	c.mu.Unlock()

	banned := ReconcileIndices(sets...)
	if len(banned) > 0 {
		removed := w.col.ExcludeIndices(banned)
		log.Debug("mobileverify: excluded cross-node conflicting devices before local aggregation",
			"block", w.blockNumber, "banned", len(banned), "removed", removed)
	}
	localCerts, err := w.col.Close(NowMs())
	if err != nil {
		log.Warn("mobileverify: local close failed after reconciliation", "block", w.blockNumber, "err", err)
		localCerts = nil
	}

	c.mu.Lock()
	for _, cert := range localCerts {
		w.localCerts[cert.ReceiptsRoot] = cert
		byReporter, ok := w.peerCerts[cert.ReceiptsRoot]
		if !ok {
			byReporter = make(map[types.Address]*MobileAttestationCert)
			w.peerCerts[cert.ReceiptsRoot] = byReporter
		}
		byReporter[c.selfAddr] = cert
	}
	sink := c.onCertAnnounce
	c.mu.Unlock()

	if sink != nil {
		for _, cert := range localCerts {
			sink(c.selfAddr, cert)
		}
	}
}

// mergeAndFinalize assumes the caller already claimed this window's
// transition into phaseFinal (see OnBlockCommitted's doc comment); the
// delete(c.windows, ...) below is what makes that claim permanent — no
// other call can still be holding a reference to this window by the time
// another goroutine's OnBlockCommitted/Stop next scans c.windows.
func (c *CohortCoordinator) mergeAndFinalize(w *cohortWindow) {
	c.mu.Lock()
	reg := c.reg
	roots := make([]types.Hash, 0, len(w.peerCerts))
	for root := range w.peerCerts {
		roots = append(roots, root)
	}
	perRoot := make(map[types.Hash][]*MobileAttestationCert, len(roots))
	for _, root := range roots {
		byReporter := w.peerCerts[root]
		list := make([]*MobileAttestationCert, 0, len(byReporter))
		for _, cert := range byReporter {
			list = append(list, cert)
		}
		perRoot[root] = list
	}
	sink := c.onDivergence
	blockHash, blockNumber := w.blockHash, w.blockNumber
	delete(c.windows, w.blockHash)
	c.mu.Unlock()

	if len(roots) == 0 {
		return
	}

	merged := make([]*MobileAttestationCert, 0, len(roots))
	for _, root := range roots {
		m, dropped, invalid, err := MergeCerts(perRoot[root], reg)
		if err != nil {
			log.Warn("mobileverify: merge failed for a receipts-root bucket",
				"block", blockNumber, "root", root.Hex()[:12], "err", err)
			continue
		}
		if len(invalid) > 0 {
			// A cert that fails Verify() before ever influencing the merge —
			// a forged/corrupted announcement (see certmerge.go's doc
			// comment on why MergeCerts authenticates every input). Not an
			// "unreachable" anomaly like dropped below: an adversarial peer
			// can trigger this at will, so it's expected background noise
			// on an open gossip topic, not necessarily a local bug.
			log.Debug("mobileverify: rejected unauthenticated/invalid peer cert during merge",
				"block", blockNumber, "root", root.Hex()[:12], "invalid", len(invalid))
		}
		if len(dropped) > 0 {
			// Should be unreachable when reconciliation ran cleanly — a
			// non-empty drop list here means the disjointness invariant was
			// violated somewhere upstream (a degraded node skipped
			// reconciliation, a late-arriving cert missed the reconcile
			// round, etc.). Worth its own signal, not silence.
			log.Warn("mobileverify: certs conflicted at merge time despite reconciliation",
				"block", blockNumber, "root", root.Hex()[:12], "dropped", len(dropped))
		}
		merged = append(merged, m)
	}
	if len(merged) == 0 {
		return
	}

	sort.Slice(merged, func(i, j int) bool { return merged[i].ReceiptsRoot.Hex() < merged[j].ReceiptsRoot.Hex() })

	winner, discarded, err := SelectMajorityBucket(merged, reg.IndexBound())
	if err != nil {
		log.Warn("mobileverify: majority-bucket selection failed", "block", blockNumber, "err", err)
		return
	}
	// Cheap sanity check on the merge math itself: every input to MergeCerts
	// was individually authenticated, so the freshly-built aggregate should
	// always verify by construction. A failure here would mean a bug in the
	// merge arithmetic, not an attacker (those are already filtered out
	// above) — worth refusing to serve rather than silently storing a
	// broken certificate.
	if _, verr := winner.Verify(reg); verr != nil {
		log.Error("mobileverify: merged certificate failed self-verification, discarding",
			"block", blockNumber, "root", winner.ReceiptsRoot.Hex()[:12], "err", verr)
		return
	}
	c.certs.Put([]*MobileAttestationCert{winner})

	if len(discarded) > 0 {
		log.Warn("mobileverify: divergent attestation cohorts after cross-node merge",
			"block", blockNumber, "hash", blockHash.Hex()[:12],
			"winnerRoot", winner.ReceiptsRoot.Hex()[:12], "winnerSigners", maskCount(winner),
			"discardedBuckets", len(discarded))
		if sink != nil {
			sink(DivergenceAlarm{
				BlockHash:   blockHash,
				BlockNumber: blockNumber,
				Cohorts:     len(discarded) + 1,
				AtMs:        NowMs(),
			})
		}
	}
}

func maskCount(cert *MobileAttestationCert) int {
	// Best-effort diagnostic count; a decode failure here is a logging
	// nicety only, never load-bearing.
	idxs, err := DecodeMask(cert.SignerMask, 1<<31)
	if err != nil {
		return -1
	}
	return len(idxs)
}

// Stop finalizes every open window immediately, skipping straight to a
// merge over whatever has been collected so far — pushing each window
// through every remaining phase in one shutdown flush rather than waiting
// for future height checkpoints that will now never arrive. Uses the same
// inFlight claim as OnBlockCommitted (see cohortWindow's doc comment) so a
// window mid-cascade from a concurrent OnBlockCommitted call is left alone
// rather than double-processed — that goroutine's own cascade will still
// carry it to phaseFinal once it next checks (force bypasses height gating
// entirely, so it needs no height information from this call to finish).
func (c *CohortCoordinator) Stop() {
	c.mu.Lock()
	c.stopped = true
	var toProcess []*cohortWindow
	for _, w := range c.windows {
		if !w.inFlight {
			w.inFlight = true
			toProcess = append(toProcess, w)
		}
	}
	c.mu.Unlock()

	for _, w := range toProcess {
		c.cascadeWindow(w, true)
	}
}

// OpenWindows returns the number of currently-active (not yet finalized)
// blocks — same meaning as WindowManager.OpenWindows.
func (c *CohortCoordinator) OpenWindows() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.windows)
}
