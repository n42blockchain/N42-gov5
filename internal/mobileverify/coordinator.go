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

	// peerIndexSets includes this node's own announcement under selfAddr, so
	// ReconcileIndices sees the identical union every reporting node sees.
	peerIndexSets map[types.Address][]MobileIndex

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
	onIndexAnnounce func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []MobileIndex)
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
func (c *CohortCoordinator) SetIndexAnnounceSink(fn func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []MobileIndex)) {
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
			blockHash:     r.BlockHash,
			blockNumber:   r.BlockNumber,
			openedHeight:  c.currentHeight,
			col:           NewCollector(c.reg, r.BlockHash, r.BlockNumber),
			peerIndexSets: make(map[types.Address][]MobileIndex),
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
func (c *CohortCoordinator) OnPeerIndexSet(blockHash types.Hash, reporter types.Address, indices []MobileIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[blockHash]
	if !ok {
		return
	}
	cp := make([]MobileIndex, len(indices))
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
func (c *CohortCoordinator) OnBlockCommitted(number uint64) {
	c.mu.Lock()
	if number > c.currentHeight {
		c.currentHeight = number
	}
	var toAnnounceIdx, toReconcile, toMerge []*cohortWindow
	for _, w := range c.windows {
		switch {
		case w.phase == phaseCollecting && number >= w.openedHeight+c.cfg.IndexAnnounceDelay:
			toAnnounceIdx = append(toAnnounceIdx, w)
		case w.phase == phaseIndexAnnounced && number >= w.openedHeight+c.cfg.ReconcileDelay:
			toReconcile = append(toReconcile, w)
		case w.phase == phaseReconciled && number >= w.openedHeight+c.cfg.MergeDelay:
			toMerge = append(toMerge, w)
		}
	}
	c.mu.Unlock()

	// Never hold the lock across a callback: an in-process test (or a
	// same-process fan-out gossip shim) may re-enter another coordinator's
	// OnPeer* synchronously from within these sinks.
	for _, w := range toAnnounceIdx {
		c.announceIndex(w)
	}
	for _, w := range toReconcile {
		c.reconcileAndClose(w)
	}
	for _, w := range toMerge {
		c.mergeAndFinalize(w)
	}
}

func (c *CohortCoordinator) announceIndex(w *cohortWindow) {
	mine := w.col.Indices()
	c.mu.Lock()
	w.phase = phaseIndexAnnounced
	w.peerIndexSets[c.selfAddr] = mine
	sink := c.onIndexAnnounce
	c.mu.Unlock()
	if sink != nil {
		sink(w.blockHash, w.blockNumber, c.selfAddr, mine)
	}
}

func (c *CohortCoordinator) reconcileAndClose(w *cohortWindow) {
	c.mu.Lock()
	sets := make([][]MobileIndex, 0, len(w.peerIndexSets))
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
	w.phase = phaseReconciled
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

func (c *CohortCoordinator) mergeAndFinalize(w *cohortWindow) {
	c.mu.Lock()
	w.phase = phaseFinal
	registryBound := c.reg.IndexBound()
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
		m, dropped, err := MergeCerts(perRoot[root], registryBound)
		if err != nil {
			log.Warn("mobileverify: merge failed for a receipts-root bucket",
				"block", blockNumber, "root", root.Hex()[:12], "err", err)
			continue
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

	winner, discarded, err := SelectMajorityBucket(merged, registryBound)
	if err != nil {
		log.Warn("mobileverify: majority-bucket selection failed", "block", blockNumber, "err", err)
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
// merge over whatever has been collected so far.
func (c *CohortCoordinator) Stop() {
	c.mu.Lock()
	c.stopped = true
	var pending []*cohortWindow
	for _, w := range c.windows {
		pending = append(pending, w)
	}
	c.mu.Unlock()

	for _, w := range pending {
		c.mu.Lock()
		phase := w.phase
		c.mu.Unlock()
		if phase == phaseCollecting {
			c.announceIndex(w)
		}
		c.mu.Lock()
		phase = w.phase
		c.mu.Unlock()
		if phase == phaseIndexAnnounced {
			c.reconcileAndClose(w)
		}
		c.mergeAndFinalize(w)
	}
}

// OpenWindows returns the number of currently-active (not yet finalized)
// blocks — same meaning as WindowManager.OpenWindows.
func (c *CohortCoordinator) OpenWindows() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.windows)
}
