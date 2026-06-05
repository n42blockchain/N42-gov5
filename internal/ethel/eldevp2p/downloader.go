// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Forward-direction block downloader for eth-el.
//
// Design (cf. reth staged HeaderStage/BodyStage with 100-concurrent requests,
// geth eth/downloader, erigon stagedsync):
//
//   - A SINGLE coordinator goroutine (not one per peer) drives catch-up. Peers
//     register into a shared pool on handshake; the coordinator spreads requests
//     across all of them concurrently and load-balances by in-flight count.
//   - A persistent reorder buffer (blockBuffer, buffer.go) holds fetched-but-
//     not-yet-executed headers/bodies across rounds (reth BodyStage model), so
//     nothing fetched is ever discarded or re-fetched.
//   - Each round:
//       1. ensureHeaders: for [local+1, local+window], request only the still-
//          missing headersPerBatch ranges from peers CONCURRENTLY into the buffer.
//       2. ensureBodies: for buffered headers still lacking a body, request bodies
//          CONCURRENTLY; each body is validated by txRoot against its header
//          before being buffered (mis-ordered/garbage bodies are left missing).
//       3. assembleContiguous + executeRange: feed the executor the contiguous
//          prefix [local+1, k] present in the buffer, ExecutePayloadFromWire
//          STRICTLY IN ORDER, advance SyncStageProgress ("ethel-last-block"),
//          then prune the executed entries from the buffer.
//   - Missing/partial responses (peers cap bodies near ~2MB) or a failed peer pick
//     just leave those numbers in the buffer for the next round to fill — the
//     contiguous prefix grows as the low end fills; nothing is thrown away.
//
// This decouples download (concurrent, multi-peer) from execution (serial,
// in-order), so a slow/churning peer no longer stalls the whole pipeline.

//go:build n42el

package eldevp2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// progressKey is the chaindata key the catch-up executor writes the
// per-batch head to; we read it on every iteration and advance it on
// every successful import so other Node services (engine API, RPC)
// see the new head immediately.
var progressKey = []byte("ethel-last-block")

const progressTable = kv.SyncStageProgress

// headersPerBatch is the GetBlockHeaders batch size. eth/69 caps at
// 1024; geth peers commonly throttle to 192. 192 stays friendly with
// every major impl.
const headersPerBatch = 192

// syncWindow is how many blocks the coordinator fetches+executes per round.
// Larger windows amortise the per-round overhead and give more parallelism.
const syncWindow = 2048

// bodyChunk is the GetBlockBodies hashes-per-request size. Peers cap body
// responses near ~2MB (~28 mainnet bodies), so keep chunks small and expect
// partial replies; concurrency across chunks/peers is where throughput comes from.
const bodyChunk = 32

// maxInflightPerPeer caps concurrent requests to a single peer so we spread
// load (and don't trip a peer's per-connection request limiter).
const maxInflightPerPeer = 4

// requestTimeout bounds the wait on a single peer response before we
// give up and try another iteration.
const requestTimeout = 20 * time.Second

// Downloader satisfies devp2p.ResponseHandler. One instance per
// eldevp2p.Service; it tracks every peer that completes handshake and
// drives the catch-up fetch loop.
type Downloader struct {
	exec executionProvider

	mu       sync.Mutex
	peers    map[string]*peerState        // peerID → state
	inflight map[string]chan inflightResp // reqKey → reply channel
	adapter  *api.EngineStateAdapter

	// reqSeq generates unique request IDs across all concurrent requests.
	reqSeq atomic.Uint64

	// coordOnce starts the single coordinator goroutine on the first peer
	// handshake; coordCancel tears it down (currently unused — the process
	// lifetime owns it, but kept for a future Stop()).
	coordOnce   sync.Once
	coordCancel context.CancelFunc

	// hashedCanonical enables the reth-2.2-style state model on the adapter.
	hashedCanonical bool

	// snapshotCold, when non-nil, enables snapshot-direct (minimal/full): the
	// immutable H0 snapshot StateReader wired into the adapter (plain mode →
	// WarmOverlayReader + OverlayStateWriter).
	snapshotCold state.StateReader

	// backfilled is set once the pre-migration BLOCKHASH-window headers have
	// been fetched + persisted (see backfillBlockhashWindow).
	backfilled bool

	// staged enables staged catch-up (writeOnly execution + per-sub-batch
	// incremental Merkle), gated by env N42_STAGED=1. subBatch is the Merkle
	// cadence in blocks (N42_SUBBATCH, default 8192). lastMerkle is the head of
	// the most recent completed MerkleStageIncremental.
	staged     bool
	subBatch   uint64
	lastMerkle uint64

	// anchorDir, when set (N42_ANCHOR_DIR), makes each completed staged Merkle
	// ALSO emit the block's MPT-stateless anchor proof (compact wire) to
	// anchorDir/anchor-<to>.bin — the live producer path. Set subBatch to the
	// anchor cadence K so every Merkle boundary is an anchor height.
	anchorDir string

	// mptVerifyInterval (N42_MPT_VERIFY_INTERVAL / --mpt-verify-interval),
	// when > 0, runs the stateless CONSUMER path on live sync data at that block
	// cadence: capture the MPT multiproof, round-trip it through the compact wire
	// (encode → decode — exactly what a minimal client receives) and confirm the
	// stateless engine re-hashes it to the canonical header.Root. Independent of
	// anchorDir (verify without writing files); a mismatch is a loud error. This
	// is the ⑤b verifier side — it continuously audits that emitted anchors are
	// minimal-client-verifiable and exercises the proofwire codec on real data.
	mptVerifyInterval uint64
	lastMptVerify     uint64

	// buffer holds headers/bodies fetched but not yet executed, persisting across
	// coordinator rounds so nothing is re-fetched or discarded (reth BodyStage
	// reorder buffer). Populated by ensureHeaders/ensureBodies, drained by
	// executeRange via assembleContiguous, pruned after commit.
	buffer *blockBuffer
}

// executionProvider is the surface the downloader needs from the
// surrounding eth-el Node to build the EngineStateAdapter and read
// chaindata for ongoing progress.
type executionProvider interface {
	RwDB() kv.RwDB
	ChainConfig() *params.ChainConfig
	Engine() consensus.Engine
	OutFreezer() *freezer.Freezer
}

// peerState tracks a connected peer.
type peerState struct {
	rw        gethp2p.MsgReadWriter
	head      uint64
	headHash  types.Hash
	inflightN int // concurrent requests currently dispatched to this peer
}

// pickedPeer is a transient handle returned by pickPeer; release it with
// releasePeer once the request completes so its in-flight count drops.
type pickedPeer struct {
	id string
	rw gethp2p.MsgReadWriter
}

// inflightResp carries either headers or bodies depending on which
// request the reply matches. exactly one of headers / bodies is
// non-nil on success. headers come as raw RLP bytes because N42's
// Header struct can't be reflect-decoded without altering its hash
// (see devp2p/protocol.go blockHeadersPacket doc).
type inflightResp struct {
	headers  [][]byte
	bodies   []devp2p.BlockBody
	receipts [][]rlp.RawValue
}

// NewDownloader builds the orchestrator without starting it.
func NewDownloader(exec executionProvider, hashedCanonical bool, snapshotCold state.StateReader) *Downloader {
	return &Downloader{
		exec:            exec,
		peers:           make(map[string]*peerState),
		inflight:        make(map[string]chan inflightResp),
		hashedCanonical: hashedCanonical,
		snapshotCold:    snapshotCold,
		buffer:          newBlockBuffer(),
	}
}

// initAdapter builds the EngineStateAdapter on first use. Same wiring
// as ethELBackend.initAPIs and engineapi.Service.Start (Phase 7.1.1.b).
func (d *Downloader) initAdapter() error {
	if d.adapter != nil {
		return nil
	}
	if d.exec == nil {
		return errors.New("eldevp2p downloader: no executionProvider")
	}
	rwdb := d.exec.RwDB()
	if rwdb == nil {
		return errors.New("eldevp2p downloader: RwDB not opened")
	}
	d.adapter = api.NewEngineStateAdapter(
		rwdb, d.exec.OutFreezer(), d.exec.ChainConfig(), d.exec.Engine(),
	).WithHashedCanonical(d.hashedCanonical)
	if d.snapshotCold != nil {
		d.adapter.SetSnapshotCold(d.snapshotCold)
	}
	return nil
}

func (d *Downloader) localHead(ctx context.Context) (uint64, error) {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	v, _ := tx.GetOne(progressTable, progressKey)
	if len(v) != 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(v), nil
}

func (d *Downloader) writeLocalHead(ctx context.Context, head uint64) error {
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, head)
	if err := tx.Put(progressTable, progressKey, v); err != nil {
		return err
	}
	return tx.Commit()
}

// --- devp2p.ResponseHandler ----------------------------------------------

// OnPeerHandshake registers a peer into the shared pool and ensures the
// single coordinator goroutine is running.
func (d *Downloader) OnPeerHandshake(peerID string, rw gethp2p.MsgReadWriter, peerHead uint64, peerHash types.Hash) {
	d.mu.Lock()
	d.peers[peerID] = &peerState{rw: rw, head: peerHead, headHash: peerHash}
	d.mu.Unlock()
	d.ensureCoordinator()
}

// OnPeerDisconnect removes a peer from the pool. In-flight requests to it will
// time out and be retried against another peer next round.
func (d *Downloader) OnPeerDisconnect(peerID string) {
	d.mu.Lock()
	delete(d.peers, peerID)
	d.mu.Unlock()
}

// ensureCoordinator starts the single catch-up coordinator exactly once.
func (d *Downloader) ensureCoordinator() {
	d.coordOnce.Do(func() {
		var ctx context.Context
		ctx, d.coordCancel = context.WithCancel(context.Background())
		go d.coordinator(ctx)
	})
}

// OnBlockHeaders forwards to the matching inflight request.
func (d *Downloader) OnBlockHeaders(peerID string, reqID uint64, headers [][]byte) {
	d.dispatch(peerID, reqID, "h", inflightResp{headers: headers})
}

// OnBlockBodies forwards to the matching inflight request.
func (d *Downloader) OnBlockBodies(peerID string, reqID uint64, bodies []devp2p.BlockBody) {
	d.dispatch(peerID, reqID, "b", inflightResp{bodies: bodies})
}

// OnReceipts forwards to the matching inflight request.
func (d *Downloader) OnReceipts(peerID string, reqID uint64, receipts [][]rlp.RawValue) {
	d.dispatch(peerID, reqID, "r", inflightResp{receipts: receipts})
}

// OnNewBlock is the live tip push from a peer. v1 ignores — we only
// catch up via batched fetch; live mode is a follow-up that wires
// NewBlock into the same import path.
func (d *Downloader) OnNewBlock(_ string, _ *block.Header, _ [][]byte) {}

func (d *Downloader) dispatch(peerID string, reqID uint64, kind string, resp inflightResp) {
	key := fmt.Sprintf("%s:%s:%d", peerID, kind, reqID)
	d.mu.Lock()
	ch, ok := d.inflight[key]
	if ok {
		delete(d.inflight, key)
	}
	d.mu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

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
	}

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
		if !d.backfilled {
			d.backfillBlockhashWindow(ctx)
		}
		var target uint64
		for _, p := range peers {
			if p.head > target {
				target = p.head
			}
		}

		local, err := d.localHead(ctx)
		if err != nil {
			log.Warn("eldevp2p: read local head", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if local >= target {
			// Caught up to the best peer; wait a slot for a new tip.
			log.Info("eldevp2p: caught up", "head", local, "peers", len(peers))
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
		t0 := time.Now()
		d.ensureHeaders(ctx, from, to)
		tHdr := time.Since(t0)
		t1 := time.Now()
		d.ensureBodies(ctx, from, to)
		tBody := time.Since(t1)

		headers, bodies := d.buffer.assembleContiguous(from, to)
		if len(headers) == 0 {
			time.Sleep(1 * time.Second)
			continue
		}

		t2 := time.Now()
		imported, newHead := d.executeRange(ctx, local, headers, bodies)
		tExec := time.Since(t2)
		d.buffer.prune(newHead)
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
			if d.staged && (newHead-d.lastMerkle >= d.subBatch || (target > newHead && target-newHead <= commitInterval)) {
				if err := d.runMerkleStage(ctx, d.lastMerkle+1, newHead); err != nil {
					log.Error("eldevp2p: staged Merkle FAILED", "from", d.lastMerkle+1, "to", newHead, "err", err)
					return // state committed but unverified past lastMerkle; operator investigates
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

// peerSnapshot copies the current peer set so the round works on a stable view.
func (d *Downloader) peerSnapshot() map[string]*peerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]*peerState, len(d.peers))
	for id, p := range d.peers {
		out[id] = p
	}
	return out
}

// pickPeer returns the least-busy peer whose head covers needHead and whose
// in-flight count is under the per-peer cap, incrementing its count. Returns
// nil if none is available; the caller skips that batch this round.
func (d *Downloader) pickPeer(needHead uint64) *pickedPeer {
	d.mu.Lock()
	defer d.mu.Unlock()
	var bestID string
	var best *peerState
	for id, p := range d.peers {
		if p.head < needHead || p.inflightN >= maxInflightPerPeer {
			continue
		}
		if best == nil || p.inflightN < best.inflightN {
			best, bestID = p, id
		}
	}
	if best == nil {
		return nil
	}
	best.inflightN++
	return &pickedPeer{id: bestID, rw: best.rw}
}

func (d *Downloader) releasePeer(pp *pickedPeer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.peers[pp.id]; ok && p.inflightN > 0 {
		p.inflightN--
	}
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
			for _, r := range raw {
				h, err := ethel.DecodeUncleHeader(r)
				if err != nil {
					continue
				}
				n := h.Number64().Uint64()
				if n >= start && n <= end {
					d.buffer.putHeader(n, h)
				}
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

// commitInterval bounds how many blocks accumulate in one batch tx before a
// commit+head-advance. A failure mid-batch rolls back at most this many
// (already-committed checkpoints are durable). Small enough that a kill loses
// little, large enough to amortise fsync over many blocks.
const commitInterval = 256

// executeRange assembles + executes blocks STRICTLY in ascending order starting
// just past local, stopping at the first missing body or execution failure.
// Blocks execute into ONE shared batch tx (a.SetBatchTx) with NO per-block
// commit; the head marker is written INTO that same tx and the batch is
// committed every commitInterval blocks (and once more for the tail). This
// makes block-state and head-advance ATOMIC — a kill can never leave state
// ahead of the head — and replaces per-block fsync with one commit per batch.
// Returns the number of durably-committed blocks and the new (committed) head.
func (d *Downloader) executeRange(ctx context.Context, local uint64, headers []*block.Header, bodies map[uint64]devp2p.BlockBody) (uint64, uint64) {
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].Number64().Uint64() < headers[j].Number64().Uint64()
	})

	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: begin batch tx", "err", err)
		return 0, local
	}
	d.adapter.SetBatchTx(tx)
	defer d.adapter.SetBatchTx(nil)

	committed := local // durably committed head
	newHead := local   // executed-but-uncommitted head
	want := local + 1

	// commit cadence (env-overridable; N42_COMMIT_INTERVAL=1 commits every block so
	// the parallel-EVM shadow benchmark reads a current committed parent).
	ci := uint64(commitInterval)
	if v := os.Getenv("N42_COMMIT_INTERVAL"); v != "" {
		if n, e := strconv.ParseUint(v, 10, 64); e == nil && n > 0 {
			ci = n
		}
	}

	for _, hdr := range headers {
		n := hdr.Number64().Uint64()
		if n < want {
			continue
		}
		if n != want {
			break // gap — refetch next round
		}
		body, ok := bodies[n]
		if !ok {
			break // body missing — refetch next round
		}
		blk, withdrawals, aerr := assembleBlock(hdr, body)
		if aerr != nil {
			log.Warn("eldevp2p: assemble block failed", "block", n, "err", aerr)
			break
		}
		if got := hash.DeriveShaErigon(rawTxList(unwrapWireTxs(body.Transactions))); got != hdr.TxHash {
			log.Warn("eldevp2p: body txRoot mismatch — refetch",
				"block", n, "got", got.Hex(), "want", hdr.TxHash.Hex())
			break
		}
		// Per-block root verification (ExecutePayloadFromWire computes the
		// incremental root and checks it against the wire header.Root). State
		// commits into the shared batch tx with NO per-block commit.
		ok, root, xerr := d.adapter.ExecutePayloadFromWire(blk, withdrawals)
		if xerr != nil {
			log.Warn("eldevp2p: ExecutePayloadFromWire error", "block", n, "err", xerr)
			tx.Rollback()
			return committed - local, committed
		}
		if !ok {
			log.Warn("eldevp2p: payload invalid", "block", n,
				"computedRoot", root.Hex(), "declaredRoot", hdr.Root.Hex())
			if os.Getenv("N42_RCPTDIFF") != "" {
				d.dumpMainnetReceipts(ctx, n, hdr.Hash())
			}
			tx.Rollback()
			return committed - local, committed
		}
		newHead = n
		want++

		if newHead-committed >= ci {
			if err := d.commitBatch(tx, newHead); err != nil {
				log.Error("eldevp2p: batch commit failed", "head", newHead, "err", err)
				return committed - local, committed
			}
			committed = newHead
			if tx, err = d.exec.RwDB().BeginRw(ctx); err != nil {
				log.Warn("eldevp2p: reopen batch tx", "err", err)
				return committed - local, committed
			}
			d.adapter.SetBatchTx(tx)
		}
	}

	// Flush the tail (or roll back an empty tx).
	if newHead > committed {
		if err := d.commitBatch(tx, newHead); err != nil {
			log.Error("eldevp2p: batch commit (tail) failed", "head", newHead, "err", err)
			return committed - local, committed
		}
		committed = newHead
	} else {
		tx.Rollback()
	}
	return committed - local, committed
}

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
	var root types.Hash
	var anchorProof [][]byte
	if d.anchorDir != "" || mptVerifyDue {
		root, anchorProof, err = commitment.MerkleStageIncrementalWithProof(tx, from, to)
	} else {
		root, err = commitment.MerkleStageIncremental(tx, from, to)
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

// commitBatch writes the head marker INTO the batch tx and commits it, so the
// block state and the head advance persist atomically.
func (d *Downloader) commitBatch(tx kv.RwTx, head uint64) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], head)
	if err := tx.Put(progressTable, progressKey, v[:]); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Downloader) requestHeaders(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, from, amount uint64) ([][]byte, error) {
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:h:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetBlockHeadersPacket{
		RequestID: reqID,
		GetBlockHeadersQuery: &eth69.GetBlockHeadersQuery{
			Origin:  eth69.HashOrNumber{Number: from},
			Amount:  amount,
			Skip:    0,
			Reverse: false,
		},
	}
	if err := gethp2p.Send(rw, eth69.GetBlockHeadersMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockHeaders: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.headers, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("headers timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Downloader) requestBodies(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([]devp2p.BlockBody, error) {
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:b:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetBlockBodiesPacket{
		RequestID: reqID,
		Hashes:    hashes,
	}
	if err := gethp2p.Send(rw, eth69.GetBlockBodiesMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockBodies: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.bodies, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("bodies timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Downloader) requestReceipts(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([][]rlp.RawValue, error) {
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:r:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetReceiptsPacket{RequestID: reqID, Hashes: hashes}
	if err := gethp2p.Send(rw, eth69.GetReceiptsMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetReceipts: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.receipts, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("receipts timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dumpMainnetReceipts (N42_RCPTDIFF diag) fetches block n's canonical receipts
// from a peer and logs each tx's status + cumulativeGasUsed, so it can be diffed
// against N42's per-tx gas (GAS161 logs) to pin the first divergent tx. eth/69
// receipt wire form is [txType, status, cumulativeGasUsed, logs]; cumGas is the
// second-to-last field (logs is last), which also handles a 3-field legacy form.
func (d *Downloader) dumpMainnetReceipts(ctx context.Context, n uint64, blockHash types.Hash) {
	pp := d.pickPeer(n)
	if pp == nil {
		log.Warn("RCPTDIFF no peer", "block", n)
		return
	}
	defer d.releasePeer(pp)
	res, err := d.requestReceipts(ctx, pp.id, pp.rw, []types.Hash{blockHash})
	if err != nil {
		log.Warn("RCPTDIFF fetch err", "block", n, "err", err)
		return
	}
	if len(res) == 0 {
		log.Warn("RCPTDIFF empty response", "block", n)
		return
	}
	for i, raw := range res[0] {
		var fields []rlp.RawValue
		if err := rlp.DecodeBytes(raw, &fields); err != nil || len(fields) < 2 {
			log.Warn("RCPTDIFF decode err", "block", n, "i", i, "err", err, "nfields", len(fields))
			continue
		}
		var status, cum uint64
		_ = rlp.DecodeBytes(fields[len(fields)-3], &status)
		_ = rlp.DecodeBytes(fields[len(fields)-2], &cum)
		log.Warn("RCPTDIFF mainnet tx", "block", n, "i", i, "status", status, "cumGas", cum)
	}
}

// backfillBlockhashWindow fetches + persists the canonical headers for the
// 256-block window below the current head. N42's BLOCKHASH opcode uses the
// classic ancestor lookup (GetHashFn → rawdb.ReadHeader); the reth-migrated
// datadir contains state but NO block headers, so blockhash() for any
// pre-migration block returns zero — breaking contracts that verify a recent
// block hash (e.g. Optimism's L2OutputOracle.proposeL2Output, which reverts
// with "block hash does not match the hash at the expected height"). The
// forward sync writes headers for every block it imports, so once the head has
// advanced 256 blocks past the migration point the window is self-covered;
// this one-time backfill bridges the gap. Headers are real canonical chain
// data fetched from a peer, not synthesized.
func (d *Downloader) backfillBlockhashWindow(ctx context.Context) {
	local, err := d.localHead(ctx)
	if err != nil || local == 0 {
		return
	}
	const window = 256
	from := uint64(1)
	if local > window {
		from = local - window
	}
	// Already covered (head advanced past the migration gap, or a prior run
	// backfilled): the ENTIRE window resolves canonically. Checking only the
	// bottom is insufficient — a truncated/sparse peer reply from an earlier
	// run can leave a mid-window hole that the bottom check misses, silently
	// returning a zero BLOCKHASH for that height later.
	if d.windowComplete(ctx, from, local) {
		d.backfilled = true
		return
	}
	pp := d.pickPeer(local)
	if pp == nil {
		return // no peer covers the head yet; retry next round
	}
	raw, err := d.requestHeaders(ctx, pp.id, pp.rw, from, local-from+1)
	d.releasePeer(pp)
	if err != nil || len(raw) == 0 {
		log.Warn("eldevp2p: blockhash-window backfill fetch failed", "from", from, "to", local, "err", err)
		return
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	written := 0
	for _, r := range raw {
		h, derr := ethel.DecodeUncleHeader(r)
		if derr != nil {
			continue
		}
		num := h.Number64().Uint64()
		if num < from || num > local {
			continue
		}
		rawdb.WriteHeader(tx, h)
		if werr := rawdb.WriteCanonicalHash(tx, h.Hash(), num); werr != nil {
			log.Warn("eldevp2p: backfill canonical-hash write failed", "n", num, "err", werr)
			return
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		log.Warn("eldevp2p: blockhash-window backfill commit failed", "err", err)
		return
	}
	// Only consider it done if the ENTIRE window [from, local] is now
	// resolvable; a partial/truncated peer reply (which can leave a mid-window
	// hole, not just a missing bottom) keeps the flag clear so the next round
	// retries and fills the gap.
	if d.windowComplete(ctx, from, local) {
		d.backfilled = true
	}
	log.Info("eldevp2p: backfilled blockhash-window headers", "from", from, "to", local, "written", written, "complete", d.backfilled)
}

func (d *Downloader) haveHeader(ctx context.Context, num uint64) bool {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	h, _ := rawdb.ReadCanonicalHash(tx, num)
	return h != (types.Hash{})
}

// windowComplete reports whether EVERY block in [from, to] has a canonical
// hash — i.e. the BLOCKHASH window is fully backfilled with no mid-window
// holes. Checking only an endpoint is insufficient: a truncated or sparse peer
// reply can leave a hole in the middle that an endpoint check misses, which
// would later return a zero BLOCKHASH for that height (and can revert a
// contract that depends on it). The scan is ≤256 cheap canonical-hash reads.
func (d *Downloader) windowComplete(ctx context.Context, from, to uint64) bool {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	for n := from; n <= to; n++ {
		if h, _ := rawdb.ReadCanonicalHash(tx, n); h == (types.Hash{}) {
			return false
		}
	}
	return true
}

// --- block assembly ------------------------------------------------------

// assembleBlock decodes the wire transactions + withdrawals and stitches
// them onto the wire header to produce a *block.Block ready for
// EngineStateAdapter.ExecutePayloadFromWire. Withdrawals are returned
// separately because the N42 Body type doesn't carry them.
func assembleBlock(hdr *block.Header, body devp2p.BlockBody) (*block.Block, []*api.Withdrawal, error) {
	if hdr == nil {
		return nil, nil, errors.New("nil header")
	}
	txs, err := decodeTxs(body.Transactions)
	if err != nil {
		return nil, nil, fmt.Errorf("decode txs: %w", err)
	}
	withdrawals, err := decodeWithdrawals(body.Withdrawals)
	if err != nil {
		return nil, nil, fmt.Errorf("decode withdrawals: %w", err)
	}
	ib := block.NewBlock(hdr, txs)
	blk, ok := ib.(*block.Block)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected block type %T", ib)
	}
	return blk, withdrawals, nil
}

// rawTxList feeds raw eth-canonical tx bytes (type||payload for typed, or the
// legacy RLP list) to DeriveShaErigon to reproduce a header's transactionsRoot.
type rawTxList [][]byte

func (l rawTxList) Len() int                           { return len(l) }
func (l rawTxList) EncodeIndex(i int, w *bytes.Buffer) { w.Write(l[i]) }

// unwrapWireTxs strips the outer RLP byte-string wrapper that typed txs carry
// inside a block-bodies message, yielding the canonical `type||payload` bytes
// (legacy txs, which are RLP lists, pass through unchanged).
func unwrapWireTxs(raw []rlp.RawValue) [][]byte {
	out := make([][]byte, 0, len(raw))
	for _, r := range raw {
		data := []byte(r)
		if len(data) > 0 && data[0] >= 0x80 && data[0] < 0xc0 {
			var inner []byte
			if err := rlp.DecodeBytes(data, &inner); err == nil {
				data = inner
			}
		}
		out = append(out, data)
	}
	return out
}

func decodeTxs(raw []rlp.RawValue) ([]*transaction.Transaction, error) {
	out := make([]*transaction.Transaction, 0, len(raw))
	for i, r := range raw {
		if len(r) == 0 {
			continue
		}
		data := []byte(r)
		// Typed transactions (EIP-2718+) arrive as RLP strings wrapping
		// `type || rlp(payload)`. Legacy txs are bare RLP lists. Mirror
		// internal/ethel.DecodeGethBody's unwrap step so both forms feed
		// the same transaction.DecodeEthereumTransaction path.
		if data[0] >= 0x80 && data[0] < 0xc0 {
			var inner []byte
			if err := rlp.DecodeBytes(data, &inner); err != nil {
				return nil, fmt.Errorf("tx %d: unwrap typed: %w", i, err)
			}
			data = inner
		}
		tx, err := transaction.DecodeEthereumTransaction(data)
		if err != nil {
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		out = append(out, tx)
	}
	return out, nil
}

// wireWithdrawal mirrors the eth/69 wire layout for withdrawals
// (EIP-4895): [index, validatorIndex, address, amount].
type wireWithdrawal struct {
	Index          uint64
	ValidatorIndex uint64
	Address        types.Address
	Amount         uint64
}

func decodeWithdrawals(raw []rlp.RawValue) ([]*api.Withdrawal, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]*api.Withdrawal, len(raw))
	for i, r := range raw {
		var w wireWithdrawal
		if err := rlp.DecodeBytes(r, &w); err != nil {
			return nil, fmt.Errorf("withdrawal %d: %w", i, err)
		}
		out[i] = &api.Withdrawal{
			Index:          hexutil.Uint64(w.Index),
			ValidatorIndex: hexutil.Uint64(w.ValidatorIndex),
			Address:        w.Address,
			Amount:         hexutil.Uint64(w.Amount),
		}
	}
	return out, nil
}
