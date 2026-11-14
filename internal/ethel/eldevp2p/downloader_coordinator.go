// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader coordinator, missing-ancestor sync, prefetch and header/body ensure rounds.

//go:build n42el

package eldevp2p

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/log"
)

// --- coordinator ---------------------------------------------------------

// coordinator is the single catch-up driver. Each round it fetches a window of
// headers and bodies concurrently across all peers, then executes the
// contiguous prefix in order.
func (d *Downloader) coordinator(ctx context.Context) {
	if err := d.initAdapter(); err != nil {
		log.Error("eldevp2p: cannot init adapter", "err", err)
		return
	}

	// Staged catch-up mode (env-gated). Execution runs writeOnly (no per-block
	// root); the state root + TrieOf* are produced per sub-batch by
	// MerkleStageIncremental. Removes ~82ms/block dRoot (validated ~7-12ms/block
	// amortized). Requires hashed-canonical (the incremental loader path).
	if d.hashedCanonical && os.Getenv("N42_STAGED") == "1" {
		d.staged = true
		d.subBatch = 8192
		if v := os.Getenv("N42_SUBBATCH"); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
				d.subBatch = n
			}
		}
		if dir := os.Getenv("N42_ANCHOR_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err == nil {
				d.anchorDir = dir
				log.Info("eldevp2p: live MPT-anchor emit enabled", "dir", dir, "cadence", d.subBatch)
			} else {
				log.Error("eldevp2p: cannot create anchor dir", "dir", dir, "err", err)
			}
		}
		if v := os.Getenv("N42_MPT_VERIFY_INTERVAL"); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
				d.mptVerifyInterval = n
				log.Info("eldevp2p: live MPT-stateless verify enabled (consumer round-trip)", "interval", n)
			}
		}
		if k := os.Getenv("N42_ATTEST_KEY"); k != "" {
			if key, err := crypto.HexToECDSA(strings.TrimPrefix(k, "0x")); err == nil {
				d.attestKey = key
				d.attestURL = os.Getenv("N42_ATTEST_AGGREGATOR")
				log.Info("eldevp2p: stateless attestation enabled",
					"signer", crypto.PubkeyToAddress(key.PublicKey).Hex(), "aggregator", d.attestURL)
			} else {
				log.Error("eldevp2p: bad N42_ATTEST_KEY", "err", err)
			}
		}
		d.adapter.SetStaged(true)
		d.lastMerkle = d.readMerkleProgress(ctx)
		// Startup reconcile: if execution committed past the last Merkle (a prior
		// kill mid-sub-batch left TrieOf* behind hashed/head), catch TrieOf* up
		// before executing more, so the next Merkle has a consistent base.
		if head, err := d.localHead(ctx); err == nil && head > d.lastMerkle {
			log.Info("eldevp2p: staged startup reconcile", "lastMerkle", d.lastMerkle, "head", head)
			if err := d.runMerkleStage(ctx, d.lastMerkle+1, head); err != nil {
				log.Error("eldevp2p: staged startup reconcile FAILED", "err", err)
				return
			}
		}
		log.Info("eldevp2p: staged catch-up enabled", "subBatch", d.subBatch, "lastMerkle", d.lastMerkle)
		// CS -> freezer (archive data layout): per-block changesets append to
		// acctcs/storcs segment files instead of MDBX, keeping chaindata
		// bounded to the hashed state. Default on; N42_CS_FREEZER=0 reverts.
		// Initialized AFTER the startup reconcile so the alignment head is the
		// reconciled one; the retain-list builder (BuildRetainListMerged) reads
		// freezer blobs where covered and legacy MDBX changesets for older
		// blocks, so the transition point needs no migration.
		if os.Getenv("N42_CS_FREEZER") != "0" && d.hashedCanonical {
			if head, err := d.localHead(ctx); err == nil {
				sink, serr := ethel.NewCSFreezerSink(d.exec.OutFreezer(), head)
				if serr != nil {
					log.Error("eldevp2p: cs freezer sink init failed — falling back to MDBX changesets", "err", serr)
				} else {
					d.csSink = sink
					d.adapter.SetCSFreezerSink(sink)
					// Unwind reads the same freezer blobs (adapter.Reorg →
					// ReorgWithSource); blocks below the sink's start still
					// unwind via the legacy MDBX changesets inside ethel.Reorg
					// only if csSource is nil, so this wiring covers exactly
					// the freezer-covered range going forward.
					d.adapter.WithCSSource(cs.NewFreezerSource(d.exec.OutFreezer()))
					log.Info("eldevp2p: changesets -> freezer enabled (acctcs/storcs)", "from", head+1)
				}
			}
		}
	}

	// Download pipeline (default on; N42_PIPELINE=0 reverts to serial). A
	// background prefetcher keeps the reorder buffer filled ahead of the executed
	// frontier so header+body download (tHdr+tBody — measured at ~55% of wall
	// time under flaky mainnet peers) overlaps execution (tExec) instead of
	// running serially before it. Safe: blockBuffer is mutex-guarded and the
	// prefetcher only appends higher block numbers while the executor drains +
	// prunes lower ones.
	pipeline := os.Getenv("N42_PIPELINE") != "0"
	if pipeline {
		go d.prefetchLoop(ctx)
		log.Info("eldevp2p: download pipeline enabled (async prefetch overlaps execution)",
			"inflightPerPeer", maxInflightPerPeer)
	}
	go d.missingAncestorLoop(ctx)

	// Freezer-direct BLOCKHASH source, installed for the coordinator's whole life
	// (local execution AND the peer transition — the first ~256 peer blocks reach
	// BLOCKHASH below the local head). The adapter reads ancestor hashes straight
	// from headerc's stored h.Hash() and falls back to the MDBX canonical index for
	// blocks imported above the local freezer head. No window seed, no field
	// reconstruction, no GetHashFn ParentHash walk.
	var localHdrReader *ethel.HeaderCompactReader
	if d.freezerDir != "" {
		if hr, herr := ethel.OpenHeaderCompact(d.freezerDir); herr == nil {
			localHdrReader = hr
			defer hr.Close()
			d.adapter.SetHeaderHashReader(hr)
		}
	}

	// Peer-INDEPENDENT local block execution: drain [stateHead+1, localFreezerHead]
	// straight from local headerc+bodyc — no peer download for blocks we already
	// hold on disk ("已经有的数据不要再下载"). Blocks above the local freezer head
	// (the live tip) fall through to the peer-gated loop below for live-follow.
	// No-op when no local headerc/bodyc exists.
	d.localCatchUp(ctx, localHdrReader)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		peers := d.peerSnapshot()
		if len(peers) == 0 {
			time.Sleep(2 * time.Second)
			continue
		}
		var tip uint64
		for _, p := range peers {
			if p.head > tip {
				tip = p.head
			}
		}
		// Confirmation lag: never import within reorgLag of the tip (see reorgLag).
		var target uint64
		if tip > reorgLag {
			target = tip - reorgLag
		}

		local, err := d.localHead(ctx)
		if err != nil {
			log.Warn("eldevp2p: read local head", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if local >= target {
			// Caught up to the lagged tip. Post-merge mainnet EL peers don't push
			// NewBlock/NewBlockHashes (block gossip moved to the CL) and a peer's
			// head in our table is frozen at handshake, so actively probe past the
			// KNOWN tip: a synced peer serves floor+1 even though its advertised head
			// says otherwise. This is what makes 12s live-follow advance for a
			// pure-EL follower.
			//
			// Probe FLOOR = max(tip, local). eth/68 peers carry no head number in
			// their Status (head==0), so when the pool holds only eth/68 peers tip
			// collapses to 0 and probing tip+1 would request ancient block 1 — all
			// below local, zero progress, an infinite "caught up tip=0" spin. Flooring
			// at local makes us probe local+1, so a mainnet peer reveals the block
			// ABOVE our head, its head is bumped to a real value, and the normal fetch
			// path resumes. With a real eth/69 peer present, floor==tip → unchanged.
			if wait := d.liveProbeWait(time.Now()); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			probeFloor := tip
			if local > probeFloor {
				probeFloor = local
			}
			if d.probeForNewTip(ctx, probeFloor) {
				continue // tip advanced; loop to import the now-confirmed block
			}
			if tip < local {
				// Every known peer is behind us (or, eth/68-only, advertised no
				// head) and none served local+1. That is not "caught up": the
				// network head is unknown. Saying so keeps an operator from
				// taking a node held by lagging peers for one at the tip.
				log.Info("eldevp2p: waiting for a peer ahead of the local head", "head", local,
					"bestPeerHead", tip, "peers", len(peers))
			} else {
				log.Info("eldevp2p: caught up", "head", local, "tip", tip, "probeFloor", probeFloor,
					"lag", reorgLag, "peers", len(peers))
			}
			time.Sleep(12 * time.Second)
			continue
		}

		from := local + 1
		window := syncWindow
		if gap := target - local; gap < uint64(window) {
			window = int(gap)
		}
		to := local + uint64(window)

		// reth-style staged download into a persistent reorder buffer: fetch only
		// the still-missing headers/bodies in [from,to] (skipped chunks just retry
		// next round — nothing is discarded), then execute the contiguous prefix.
		// With the pipeline on, the background prefetcher owns fetching; the
		// executor just drains the buffer so download overlaps execution.
		var tHdr, tBody time.Duration
		if !pipeline {
			t0 := time.Now()
			d.ensureHeaders(ctx, from, to)
			tHdr = time.Since(t0)
			t1 := time.Now()
			d.ensureBodies(ctx, from, to)
			tBody = time.Since(t1)
		}

		headers, bodies := d.buffer.assembleContiguous(from, to)
		if len(headers) == 0 {
			// Nothing contiguous yet. Under the pipeline the prefetcher is still
			// filling the buffer, so poll back quickly rather than idling a second.
			if pipeline {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		// Capture reverse-diffs only near the tip — bulk catch-up of old
		// (finalized) blocks never reorgs, so skip the per-write pre-image read
		// there. journalWindow (>> reorgJournalDepth) guarantees any block that
		// could later be reorged was captured.
		d.adapter.SetJournalActive(target >= local && target-local <= journalWindow)

		t2 := time.Now()
		imported, newHead := d.executeRange(ctx, local, headers, bodies)
		tExec := time.Since(t2)
		d.buffer.prune(newHead)
		if d.reorgPending {
			// Our head was orphaned by a reorg — unwind one block via the
			// in-memory reverse-diff journal, then loop to re-fetch the canonical
			// block at that height (depth-1 links immediately; deeper repeats).
			d.handleReorg(ctx)
			if d.reorgFailed {
				// Unrecoverable: re-running the same unwind would spin forever
				// (the 25462234 loop). Stop live-follow so the operator re-syncs.
				log.Error("eldevp2p: live-follow halted after an unrecoverable reorg unwind — re-sync required",
					"reason", d.reorgFailReason)
				return
			}
			continue
		}
		if imported > 0 {
			log.Info("eldevp2p: imported batch", "from", from,
				"imported", imported, "newHead", newHead, "staged", d.staged,
				"remaining", target-newHead, "peers", len(peers),
				"buffered", d.buffer.size(),
				"tHdr", tHdr.Round(time.Millisecond), "tBody", tBody.Round(time.Millisecond),
				"tExec", tExec.Round(time.Millisecond))
			// Staged Merkle stage: once a sub-batch of blocks has executed
			// (writeOnly), compute + verify the root for [lastMerkle+1, newHead]
			// in ONE incremental pass and persist TrieOf*. Also fire when near the
			// tip (so TrieOf* stays fresh for the live 12s handoff).
			// Fire the Merkle stage on a full sub-batch, OR whenever we are at/
			// near the tip with ANY un-merkled debt. The old `target > newHead`
			// guard never held once fully caught up (single-block live rounds
			// end exactly at target), so live mode silently starved the stage —
			// TrieOf* lagged until the next restart's reconcile, and a reorg
			// unwind in that window would have used a stale trie.
			if d.staged && (newHead-d.lastMerkle >= d.subBatch ||
				(target-newHead <= commitInterval && newHead > d.lastMerkle)) {
				// Land the in-flight async window first — its TrieOf* nodes
				// must be committed before the next window's snapshot.
				if err := d.joinMerkleAsync(ctx); err != nil {
					log.Error("eldevp2p: staged Merkle (async) FAILED", "err", err)
					return // state committed but unverified past the failed window
				}
				mFrom, mTo := d.lastMerkle+1, newHead
				if mTo >= mFrom {
					// Async pipeline for big catch-up windows (no anchor/verify
					// side-channels — those need the synchronous path); the root
					// computes on a RoTx snapshot while execution continues.
					mptVerifyDue := d.mptVerifyInterval > 0 && mTo/d.mptVerifyInterval > d.lastMptVerify/d.mptVerifyInterval
					useAsync := asyncMerkleEnabled && d.anchorDir == "" && !mptVerifyDue &&
						mTo-mFrom+1 >= asyncMerkleMinWindow
					if useAsync {
						if err := d.startMerkleAsync(ctx, mFrom, mTo); err != nil {
							log.Warn("eldevp2p: async Merkle start failed — falling back to sync", "err", err)
							useAsync = false
						}
					}
					if !useAsync {
						if err := d.runMerkleStage(ctx, mFrom, mTo); err != nil {
							log.Error("eldevp2p: staged Merkle FAILED", "from", mFrom, "to", mTo, "err", err)
							return // state committed but unverified past lastMerkle; operator investigates
						}
					}
				}
			}
		} else {
			log.Warn("eldevp2p: round imported 0", "from", from, "window", window,
				"buffered", d.buffer.size(),
				"tHdr", tHdr.Round(time.Millisecond), "tBody", tBody.Round(time.Millisecond))
			time.Sleep(3 * time.Second)
		}
	}
}

func (d *Downloader) missingAncestorLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case target := <-d.missingAncestors:
			err := d.syncMissingAncestor(ctx, target)
			d.mu.Lock()
			delete(d.pendingAncestors, target)
			d.mu.Unlock()
			if err != nil {
				log.Warn("eldevp2p: missing-ancestor sync failed", "hash", target.Hex(), "err", err)
			}
		}
	}
}

func (d *Downloader) syncMissingAncestor(ctx context.Context, target types.Hash) error {
	d.mu.Lock()
	known := d.knownEngineBlock
	importer := d.importEngineBlock
	d.mu.Unlock()
	if known == nil || importer == nil {
		return errors.New("Engine sync bridge is not configured")
	}
	if known(target) {
		return nil
	}
	peer := d.pickPeer(0)
	if peer == nil {
		return errors.New("no peer available for missing ancestor")
	}
	defer d.releasePeer(peer)

	rawHeaders, err := d.requestHeadersByHash(ctx, peer.id, peer.rw, target, headersPerBatch, true)
	if err != nil {
		return err
	}
	if len(rawHeaders) == 0 {
		return fmt.Errorf("peer returned no headers for %s", target.Hex())
	}
	branchDescending := make([]*block.Header, 0, len(rawHeaders))
	foundAncestor := false
	for _, raw := range rawHeaders {
		hdr, err := ethel.DecodeUncleHeader(raw)
		if err != nil {
			return fmt.Errorf("decode missing-ancestor header: %w", err)
		}
		if known(hdr.Hash()) {
			foundAncestor = true
			break
		}
		branchDescending = append(branchDescending, hdr)
	}
	if !foundAncestor {
		return fmt.Errorf("no known ancestor within %d headers", len(rawHeaders))
	}
	if len(branchDescending) == 0 {
		return nil
	}
	branch := make([]*block.Header, len(branchDescending))
	for i := range branchDescending {
		branch[len(branchDescending)-1-i] = branchDescending[i]
	}
	hashes := make([]types.Hash, len(branch))
	for i, hdr := range branch {
		hashes[i] = hdr.Hash()
	}
	bodies, err := d.requestBodies(ctx, peer.id, peer.rw, hashes)
	if err != nil {
		return err
	}
	if len(bodies) != len(branch) {
		return fmt.Errorf("missing-ancestor bodies: got %d, want %d", len(bodies), len(branch))
	}
	for i, hdr := range branch {
		blk, withdrawals, err := assembleBlock(hdr, bodies[i])
		if err != nil {
			return fmt.Errorf("assemble missing ancestor %d: %w", hdr.Number64().Uint64(), err)
		}
		if d.exec != nil {
			if _, err := checkWireBlockSize(d.exec.ChainConfig(), hdr, bodies[i]); err != nil {
				return fmt.Errorf("missing ancestor %d: %w", hdr.Number64().Uint64(), err)
			}
		}
		status, latestValid, err := importer(blk, withdrawals)
		if err != nil {
			return fmt.Errorf("import missing ancestor %d: %w", hdr.Number64().Uint64(), err)
		}
		log.Info("eldevp2p: Engine validated missing ancestor",
			"block", hdr.Number64().Uint64(), "hash", hdr.Hash().Hex(),
			"status", status, "latestValid", latestValid)
		if err := d.validateMissingAncestorStatus(branch, i, status, latestValid); err != nil {
			return err
		}
	}
	return nil
}

func (d *Downloader) validateMissingAncestorStatus(branch []*block.Header, index int, status string, latestValidHash *types.Hash) error {
	if index < 0 || index >= len(branch) || branch[index] == nil {
		return errors.New("missing-ancestor import returned a status for an invalid branch index")
	}
	header := branch[index]
	switch status {
	case api.PayloadStatusValid:
		return nil
	case api.PayloadStatusAccepted, api.PayloadStatusSyncing:
		return fmt.Errorf("Engine still missing parent for block %d", header.Number64().Uint64())
	case api.PayloadStatusInvalid, api.PayloadStatusInvalidBlockHash:
		latestValid := header.ParentHash
		if latestValidHash != nil {
			latestValid = *latestValidHash
		}
		rejectedTip := rejectedBranchTip(branch, index)
		d.reportInvalidAncestor(rejectedTip, latestValid)
		return fmt.Errorf("Engine rejected missing ancestor block %d with status %s", header.Number64().Uint64(), status)
	default:
		return fmt.Errorf("Engine returned unknown payload status %q for block %d", status, header.Number64().Uint64())
	}
}

// prefetchAhead is how far past the executed frontier the pipeline prefetcher
// keeps the reorder buffer filled. Two sync windows give the executor a full
// window to drain while the next is already downloading; the buffer stays
// bounded (~prefetchAhead blocks) because prune() drops executed entries.
const prefetchAhead = 2 * syncWindow

// prefetchLoop runs concurrently with the coordinator's executor when the
// pipeline is enabled. It continuously fills the reorder buffer with
// headers+bodies in [local+1, local+prefetchAhead] so download (tHdr+tBody)
// overlaps execution (tExec) instead of preceding it each round. It only ever
// appends to the mutex-guarded buffer; the executor drains + prunes it.
func (d *Downloader) prefetchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		peers := d.peerSnapshot()
		if len(peers) == 0 {
			time.Sleep(1 * time.Second)
			continue
		}
		var tip uint64
		for _, p := range peers {
			if p.head > tip {
				tip = p.head
			}
		}
		// Prefetch only up to the same lagged target the executor imports to, so
		// we never buffer (and later import) reorg-prone within-lag tip blocks.
		var target uint64
		if tip > reorgLag {
			target = tip - reorgLag
		}
		local, err := d.localHead(ctx)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		if local >= target {
			time.Sleep(2 * time.Second) // caught up — nothing to prefetch
			continue
		}
		from := local + 1
		to := local + prefetchAhead
		if to > target {
			to = target
		}
		// Near window already fully buffered: let the executor drain before
		// fetching further so the buffer stays bounded and we don't busy-spin.
		if d.bufferedThrough(from, to) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		d.ensureHeaders(ctx, from, to)
		d.ensureBodies(ctx, from, to)
	}
}

// bufferedThrough reports whether every block in [from,to] already has both a
// header and a body in the buffer (returns false on the first gap).
func (d *Downloader) bufferedThrough(from, to uint64) bool {
	for n := from; n <= to; n++ {
		if !d.buffer.hasHeader(n) || !d.buffer.hasBody(n) {
			return false
		}
	}
	return true
}

// probeForNewTip actively pulls the headers just past local from connected
// peers, bypassing their handshake-frozen advertised head. Post-merge mainnet
// EL peers do NOT gossip NewBlock/NewBlockHashes (block propagation moved to the
// consensus layer), and OnPeerHandshake records a peer's head only once, so a
// pure-EL follower that relies on the advertised head would sit at the tip it
// first reached forever. By requesting local+1 directly, a synced peer serves
// the block it actually has; we buffer the returned headers and bump that peer's
// head so the normal fetch+execute path imports them. Stops at the first peer
// that yields a newer block. Returns true iff a new tip was discovered.
func (d *Downloader) probeForNewTip(ctx context.Context, floor uint64) bool {
	type probePeer struct {
		id string
		rw gethp2p.MsgReadWriter
	}
	d.mu.Lock()
	peers := make([]probePeer, 0, len(d.peers))
	for id, p := range d.peers {
		peers = append(peers, probePeer{id: id, rw: p.rw})
	}
	d.mu.Unlock()
	if len(peers) == 0 {
		return false
	}

	// Probe every peer CONCURRENTLY under one short deadline. Serial probing
	// would stall the coordinator ~20s per unresponsive/wrong-fork peer (mainnet
	// discovery surfaces many), delaying our reaction when a good peer finally
	// appears; concurrent + bounded keeps live-follow snappy under peer churn.
	pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	type probeResult struct {
		id      string
		maxN    uint64
		headers map[uint64]*block.Header
	}
	ch := make(chan probeResult, len(peers))
	for _, pp := range peers {
		go func(pp probePeer) {
			raw, err := d.requestHeaders(pctx, pp.id, pp.rw, floor+1, headersPerBatch)
			if err != nil || len(raw) == 0 {
				ch <- probeResult{}
				return
			}
			hs := make(map[uint64]*block.Header)
			var maxN uint64
			for _, r := range raw {
				h, derr := ethel.DecodeUncleHeader(r)
				if derr != nil {
					continue
				}
				n := h.Number64().Uint64()
				if n > floor {
					hs[n] = h
					if n > maxN {
						maxN = n
					}
				}
			}
			ch <- probeResult{id: pp.id, maxN: maxN, headers: hs}
		}(pp)
	}

	found := false
	for range peers {
		r := <-ch
		if r.maxN <= floor {
			continue
		}
		for n, h := range r.headers {
			d.buffer.putHeader(n, h)
		}
		d.mu.Lock()
		if p, ok := d.peers[r.id]; ok && r.maxN > p.head {
			p.head = r.maxN
		}
		d.mu.Unlock()
		found = true
	}
	return found
}

// ensureHeaders fetches any headers in [from,to] still missing from the buffer,
// in headersPerBatch chunks across peers concurrently. Chunks already fully
// buffered are skipped; chunks whose peer pick fails are simply retried next
// round (the buffer keeps what already arrived).
func (d *Downloader) ensureHeaders(ctx context.Context, from, to uint64) {
	var wg sync.WaitGroup
	for start := from; start <= to; start += headersPerBatch {
		amount := uint64(headersPerBatch)
		if to-start+1 < amount {
			amount = to - start + 1
		}
		end := start + amount - 1
		allHave := true
		for n := start; n <= end; n++ {
			if !d.buffer.hasHeader(n) {
				allHave = false
				break
			}
		}
		if allHave {
			continue
		}
		wg.Add(1)
		go func(start, end, amount uint64) {
			defer wg.Done()
			pp := d.pickPeer(end)
			if pp == nil {
				return
			}
			raw, err := d.requestHeaders(ctx, pp.id, pp.rw, start, amount)
			d.releasePeer(pp)
			if err != nil {
				return
			}
			var maxN uint64
			for _, r := range raw {
				h, err := ethel.DecodeUncleHeader(r)
				if err != nil {
					continue
				}
				n := h.Number64().Uint64()
				if n >= start && n <= end {
					d.buffer.putHeader(n, h)
				}
				if n > maxN {
					maxN = n
				}
			}
			// A peer that served headers up to maxN clearly has at least that head;
			// bump our record so tip reflects reality. eth/68 peers carry no head
			// number in their Status (head==0), so without this the pool's tip can
			// collapse to 0 under eth/68-only churn and stall the fetch path.
			if maxN > 0 {
				d.mu.Lock()
				if p, ok := d.peers[pp.id]; ok && maxN > p.head {
					p.head = maxN
				}
				d.mu.Unlock()
			}
		}(start, end, amount)
	}
	wg.Wait()
}

// ensureBodies fetches bodies for buffered headers in [from,to] that still lack
// one, in bodyChunk groups across peers concurrently. Each returned body is
// validated by txRoot against its header BEFORE buffering, so a mis-ordered or
// garbage body is left missing (re-requested next round) instead of being cached
// and then rejected forever by executeRange.
func (d *Downloader) ensureBodies(ctx context.Context, from, to uint64) {
	var need []*block.Header
	for n := from; n <= to; n++ {
		if h, ok := d.buffer.headerAt(n); ok && !d.buffer.hasBody(n) {
			need = append(need, h)
		}
	}
	if len(need) == 0 {
		return
	}
	var wg sync.WaitGroup
	for off := 0; off < len(need); off += bodyChunk {
		end := off + bodyChunk
		if end > len(need) {
			end = len(need)
		}
		chunk := need[off:end]
		wg.Add(1)
		go func(chunk []*block.Header) {
			defer wg.Done()
			needHead := chunk[len(chunk)-1].Number64().Uint64()
			pp := d.pickPeer(needHead)
			if pp == nil {
				return
			}
			hashes := make([]types.Hash, len(chunk))
			for i, h := range chunk {
				hashes[i] = h.Hash()
			}
			bodies, err := d.requestBodies(ctx, pp.id, pp.rw, hashes)
			d.releasePeer(pp)
			if err != nil {
				return
			}
			// Peers reply in request order (a prefix on partial replies), so
			// body[j] is the body for chunk[j]. Confirm with the txRoot before
			// trusting it.
			for j := 0; j < len(bodies) && j < len(chunk); j++ {
				h := chunk[j]
				if got := hash.DeriveShaErigon(rawTxList(unwrapWireTxs(bodies[j].Transactions))); got != h.TxHash {
					continue
				}
				d.buffer.putBody(h.Number64().Uint64(), bodies[j])
			}
		}(chunk)
	}
	wg.Wait()
}
