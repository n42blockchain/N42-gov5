// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader async merkle stage and anchor attestation.

//go:build n42el

package eldevp2p

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// merkleProgressKey marks the head of the last completed MerkleStageIncremental
// (staged catch-up). It lags the execution head (progressKey) by up to one
// sub-batch; the gap is reconciled on startup.
var merkleProgressKey = []byte("ethel-merkle-block")

func (d *Downloader) readMerkleProgress(ctx context.Context) uint64 {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return 0
	}
	defer tx.Rollback()
	v, _ := tx.GetOne(progressTable, merkleProgressKey)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

// runMerkleStage computes the state root for the post-state of block `to` over the
// touched keys of [from,to] (one incremental FlatDBTrieLoader pass, writing
// TrieOf*), verifies it against block `to`'s trusted wire header.Root, then
// commits (TrieOf* + merkle-progress marker atomic). On mismatch it rolls back the
// whole pass and returns an error (a block in the range produced bad state — the
// caller stops; the sub-batch can be unwound via UnwindHashedCanonical + bisected).
// merkleAsyncJob is one in-flight async Merkle window.
type merkleAsyncJob struct {
	from, to uint64
	started  time.Time
	done     chan struct{}
	root     types.Hash
	upd      *commitment.TrieUpdates
	err      error
}

// asyncMerkleEnabled gates the async pipeline (default on; N42_ASYNC_MERKLE=0
// reverts to the synchronous stage).
var asyncMerkleEnabled = envswitch.Not0("N42_ASYNC_MERKLE", envswitch.Perf, "Default on: async merkle pipeline of the eth-el downloader; 0 reverts to the synchronous stage.")

// asyncMerkleMinWindow: below this the window is cheap enough that pipelining
// buys nothing — run synchronously (also keeps the near-tip/live path, which
// merkles every small delta, semantically identical to before).
const asyncMerkleMinWindow = 1024

// startMerkleAsync kicks off the read-only root computation for [from,to] on
// its own RoTx snapshot and advances the in-memory Merkle frontier. The
// persisted marker moves only when joinMerkleAsync applies the result.
func (d *Downloader) startMerkleAsync(ctx context.Context, from, to uint64) error {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return err
	}
	job := &merkleAsyncJob{from: from, to: to, started: time.Now(), done: make(chan struct{})}
	go func() {
		defer close(job.done)
		defer tx.Rollback()
		rl, nAcc, nSto, err := ethel.BuildRetainListMerged(tx, d.exec.OutFreezer(), from, to)
		if err != nil {
			job.err = fmt.Errorf("retain list [%d,%d]: %w", from, to, err)
			return
		}
		log.Info("MerkleStageIncrementalCollect", "from", from, "to", to, "accKeys", nAcc, "stoKeys", nSto)
		root, upd, err := commitment.CollectTrieUpdates(tx, rl)
		if err != nil {
			job.err = fmt.Errorf("CollectTrieUpdates [%d,%d]: %w", from, to, err)
			return
		}
		// Verify against the wire header on the same snapshot.
		canon, err := rawdb.ReadCanonicalHash(tx, to)
		if err != nil || canon == (types.Hash{}) {
			job.err = fmt.Errorf("no canonical hash at %d", to)
			return
		}
		hdr := rawdb.ReadHeader(tx, canon, to)
		if hdr == nil {
			job.err = fmt.Errorf("no header at %d", to)
			return
		}
		if root != hdr.Root {
			job.err = fmt.Errorf("staged Merkle root mismatch at %d: computed %s wire %s",
				to, root.Hex(), hdr.Root.Hex())
			return
		}
		job.root, job.upd = root, upd
	}()
	d.merkleInflight = job
	d.lastMerkle = to
	log.Info("eldevp2p: staged Merkle started (async)", "from", from, "to", to, "blocks", to-from+1)
	return nil
}

// joinMerkleAsync waits for the in-flight window (if any), lands its TrieOf*
// nodes + progress marker, and clears the slot. Called before every new
// Merkle boundary so windows chain: window N's nodes are committed before
// window N+1's snapshot is taken.
func (d *Downloader) joinMerkleAsync(ctx context.Context) error {
	job := d.merkleInflight
	if job == nil {
		return nil
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	d.merkleInflight = nil
	if job.err != nil {
		// Execution has run past the failed window (the async tradeoff) —
		// state is committed but unverified from job.from onward.
		return job.err
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t0 := time.Now()
	if err := job.upd.Apply(tx); err != nil {
		return fmt.Errorf("apply TrieOf* updates [%d,%d]: %w", job.from, job.to, err)
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], job.to)
	if err := tx.Put(progressTable, merkleProgressKey, v[:]); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Info("eldevp2p: staged Merkle OK (async)", "from", job.from, "to", job.to,
		"blocks", job.to-job.from+1, "root", job.root.Hex(),
		"compute", time.Since(job.started).Round(time.Millisecond),
		"apply", time.Since(t0).Round(time.Millisecond))
	return nil
}

func (d *Downloader) runMerkleStage(ctx context.Context, from, to uint64) error {
	if to < from {
		return nil
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t0 := time.Now()
	// Live producer/verifier path: when anchor emit OR stateless verify is due,
	// capture the MPT-stateless multiproof as a byproduct of the same Merkle
	// (one walk); else plain root.
	mptVerifyDue := d.mptVerifyInterval > 0 && to/d.mptVerifyInterval > d.lastMptVerify/d.mptVerifyInterval
	// Retain list from acctcs/storcs freezer blobs where covered, legacy MDBX
	// changesets otherwise (transition-safe).
	rl, nAcc, nSto, err := ethel.BuildRetainListMerged(tx, d.exec.OutFreezer(), from, to)
	if err != nil {
		return fmt.Errorf("retain list [%d,%d]: %w", from, to, err)
	}
	log.Info("MerkleStageIncremental", "from", from, "to", to, "accKeys", nAcc, "stoKeys", nSto)
	var root types.Hash
	var anchorProof [][]byte
	if d.anchorDir != "" || mptVerifyDue {
		root, anchorProof, err = commitment.MerkleStageIncrementalWithProofRL(tx, rl)
	} else {
		root, err = commitment.MerkleStageIncrementalRL(tx, rl)
	}
	if err != nil {
		return fmt.Errorf("MerkleStageIncremental [%d,%d]: %w", from, to, err)
	}
	canon, err := rawdb.ReadCanonicalHash(tx, to)
	if err != nil || canon == (types.Hash{}) {
		return fmt.Errorf("no canonical hash at %d", to)
	}
	hdr := rawdb.ReadHeader(tx, canon, to)
	if hdr == nil {
		return fmt.Errorf("no header at %d", to)
	}
	if root != hdr.Root {
		return fmt.Errorf("staged Merkle root mismatch at %d: computed %s wire %s", to, root.Hex(), hdr.Root.Hex())
	}
	if d.anchorDir != "" {
		if err := d.emitAnchor(to, root, anchorProof); err != nil {
			log.Error("eldevp2p: anchor emit failed", "block", to, "err", err)
		}
	}
	if mptVerifyDue {
		if err := verifyAnchorRoundTrip(to, root, anchorProof); err != nil {
			return fmt.Errorf("MPT-stateless verify [%d,%d]: %w", from, to, err)
		}
		d.lastMptVerify = to
		// Closed multi-IDC loop (#9): having verified block `to` statelessly
		// (state root) and via execution (receipt root, full-node path), sign and
		// submit an attestation. Best-effort — a down aggregator must not halt sync.
		if d.attestKey != nil && d.attestURL != "" {
			if err := signAndPostAttestation(d.attestKey, d.attestURL, to, hdr.Root, hdr.ReceiptHash); err != nil {
				log.Warn("eldevp2p: attestation submit failed (non-fatal)", "block", to, "err", err)
			} else {
				log.Info("eldevp2p: attestation submitted", "block", to)
			}
		}
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], to)
	if err := tx.Put(progressTable, merkleProgressKey, v[:]); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.lastMerkle = to
	log.Info("eldevp2p: staged Merkle OK", "from", from, "to", to, "blocks", to-from+1,
		"root", root.Hex(), "dur", time.Since(t0).Round(time.Millisecond))
	return nil
}

// emitAnchor writes block N's compact MPT-stateless anchor proof to
// anchorDir/anchor-<N>.bin, self-checking it reconstructs to the verified root.
// Best-effort: a failure is logged by the caller, not fatal to sync.
func (d *Downloader) emitAnchor(blockNum uint64, root types.Hash, proof [][]byte) error {
	if len(proof) == 0 {
		return fmt.Errorf("empty proof")
	}
	if err := stateless.VerifyProofAnchors(root[:], proof); err != nil {
		return fmt.Errorf("anchor self-check: %w", err)
	}
	wire, err := stateless.CompactProofFromNodes(root[:], proof)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.anchorDir, fmt.Sprintf("anchor-%010d.bin", blockNum)), wire, 0o644)
}

// verifyAnchorRoundTrip is the ⑤b verifier side: it runs the stateless CONSUMER
// path on the live block's captured multiproof, exactly as a minimal client
// would. First the raw proof is re-hashed by the stateless engine to confirm it
// anchors to the production root (cross-checks the two root engines agree). Then
// the proof is round-tripped through the compact wire (encode → decode — the form
// a minimal client actually receives) and re-verified, which continuously
// exercises the proofwire codec on real data. Any failure means an emitted anchor
// would NOT verify on a minimal client, so it is a loud, sync-halting error.
func verifyAnchorRoundTrip(blockNum uint64, root types.Hash, proof [][]byte) error {
	if len(proof) == 0 {
		return fmt.Errorf("empty proof at %d", blockNum)
	}
	if err := stateless.VerifyProofAnchors(root[:], proof); err != nil {
		return fmt.Errorf("raw proof anchor: %w", err)
	}
	wire, err := stateless.CompactProofFromNodes(root[:], proof)
	if err != nil {
		return fmt.Errorf("compact encode: %w", err)
	}
	decoded, err := stateless.DecodeCompactToNodes(wire)
	if err != nil {
		return fmt.Errorf("compact decode: %w", err)
	}
	if err := stateless.VerifyProofAnchors(root[:], decoded); err != nil {
		return fmt.Errorf("decoded-wire anchor: %w", err)
	}
	log.Info("eldevp2p: MPT-stateless verify OK (minimal-client round-trip)",
		"block", blockNum, "root", root.Hex(), "nodes", len(proof), "wireBytes", len(wire))
	return nil
}

// signAndPostAttestation signs an Attestation over (blockNum, stateRoot,
// receiptRoot) with key and POSTs its wire form to the aggregator's /attest
// endpoint. Used to close the multi-IDC loop after a successful stateless verify.
// Bounded by a short timeout so a slow/down aggregator cannot stall sync.
func signAndPostAttestation(key *ecdsa.PrivateKey, aggregatorURL string, blockNum uint64, stateRoot, receiptRoot types.Hash) error {
	a, err := stateless.SignAttestation(key, blockNum, stateRoot, receiptRoot)
	if err != nil {
		return err
	}
	wire, err := stateless.EncodeAttestation(a)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(strings.TrimRight(aggregatorURL, "/")+"/attest", "application/octet-stream", bytes.NewReader(wire))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aggregator status %d", resp.StatusCode)
	}
	return nil
}

// commitBatch writes the head marker INTO the batch tx and commits it, so the
// block state and the head advance persist atomically. The cs freezer sink is
// flushed+fsynced FIRST, keeping freezer-head >= MDBX-head: after a crash the
// importer re-executes at most one commit interval and the re-appends
// overwrite the freezer tail.
func (d *Downloader) commitBatch(tx kv.RwTx, head uint64) error {
	if d.csSink != nil {
		if err := d.csSink.Flush(); err != nil {
			return fmt.Errorf("cs freezer flush: %w", err)
		}
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], head)
	if err := tx.Put(progressTable, progressKey, v[:]); err != nil {
		return err
	}
	return tx.Commit()
}
