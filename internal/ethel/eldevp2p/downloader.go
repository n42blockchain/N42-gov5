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
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/cs"
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
// load (and don't trip a peer's per-connection request limiter). Overridable
// via N42_INFLIGHT: when only a few peers are retained (mainnet churn often
// leaves ~7), raising it pulls more headers/bodies per held peer to keep the
// prefetch pipeline fed — at the cost of leaning harder on each peer.
var maxInflightPerPeer = envInt("N42_INFLIGHT", 4)

// reorgLag is the confirmation depth for live-follow: the coordinator imports
// only blocks that are at least this many below the peers' tip, so a shallow
// tip reorg (post-merge mainnet reorgs are almost always depth-1, resolved
// within one slot) settles BEFORE we import the block. A pure-EL follower in
// minimal mode has no changesets to unwind an orphaned block, so NOT importing
// reorg-prone tip blocks is the only clean way to stay reorg-safe. Overridable
// via N42_REORG_LAG (0 = follow the bare tip, reorg-unsafe). Trades ~lag×12s of
// tip latency for reorg safety.
var reorgLag = func() uint64 {
	if v := os.Getenv("N42_REORG_LAG"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return uint64(n)
		}
	}
	// Default 0: the in-memory reverse-diff journal (reorgJournalDepth) unwinds
	// shallow tip reorgs, so we follow the bare tip with zero added latency. Set
	// N42_REORG_LAG>0 to additionally hold back K blocks (belt-and-suspenders).
	return 0
}()

// reorgJournalDepth is how many recent committed blocks keep an in-memory
// reverse-diff for shallow-reorg unwind (geth uses 128). Overridable via
// N42_REORG_JOURNAL. Post-merge reorgs are depth-1 and never below finality
// (~64 slots), so 128 is comfortably deep.
var reorgJournalDepth = func() int {
	if v := os.Getenv("N42_REORG_JOURNAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 128
}()

// journalWindow is how close to the tip (in blocks) reverse-diff capture turns
// on. Must exceed reorgJournalDepth so every block that could be reorged is
// captured; kept well above it (bulk catch-up below this runs capture-free).
const journalWindow = 1024

// envInt reads a positive int from env, falling back to def when unset/invalid.
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

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

	// freezerDir is the local input freezer (<datadir>/chain/freezer). When it
	// holds a headerc.cidx + bodyc set, localCatchUp executes those blocks
	// directly from disk (freezer-direct BLOCKHASH) instead of downloading them.
	freezerDir string

	// reorgPending is set by executeRange when it detects an orphaned head
	// (parent mismatch) so the coordinator unwinds one block via the reverse-diff
	// journal before the next fetch round.
	reorgPending bool

	// reorgFailed latches an UNRECOVERABLE reorg unwind (journal exhausted,
	// changeset unwind error, or a recomputed root that does not match the
	// parent header). Without it the coordinator re-detected the identical
	// reorg every round and re-ran the same failing unwind forever — the
	// ~1400x spin observed stuck at block 25462234, where the "halting
	// (re-sync required)" log was a misnomer because nothing actually halted.
	// Set here, checked at the top of the coordinator's reorg branch, which
	// then stops live-follow cleanly (one operator-facing error) instead of
	// spinning. reorgFailReason carries the cause for that log.
	reorgFailed     bool
	reorgFailReason string

	// staged enables staged catch-up (writeOnly execution + per-sub-batch
	// incremental Merkle), gated by env N42_STAGED=1. subBatch is the Merkle
	// cadence in blocks (N42_SUBBATCH, default 8192). lastMerkle is the head of
	// the most recent completed MerkleStageIncremental.
	staged     bool
	subBatch   uint64
	lastMerkle uint64

	// merkleInflight is the async Merkle window in flight (N42_ASYNC_MERKLE,
	// default on for large catch-up windows): the window's root is computed on
	// an MDBX RoTx snapshot in a background goroutine while execution proceeds
	// into the next window; the collected TrieOf* nodes + progress marker land
	// at the next boundary's join. lastMerkle advances at START (in-memory
	// frontier for boundary math); the persisted marker only at join, so a
	// crash mid-flight just recomputes the window in the startup reconcile.
	merkleInflight *merkleAsyncJob

	// csSink, when non-nil, routes per-block changesets to acctcs/storcs
	// freezer tables (archive data layout) instead of MDBX; commitBatch
	// flushes+fsyncs it before every MDBX commit. N42_CS_FREEZER=0 disables.
	csSink *ethel.CSFreezerSink

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

	// attestKey / attestURL (N42_ATTEST_KEY hex + N42_ATTEST_AGGREGATOR url):
	// after a successful ⑤b stateless verify, this node signs an Attestation over
	// (block, header.Root, header.ReceiptHash) and POSTs it to the aggregator,
	// closing the multi-IDC loop (#9). Best-effort: a failed POST warns, never
	// halts sync. Set both to enable.
	attestKey *ecdsa.PrivateKey
	attestURL string

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
	bals     [][]byte
}

// getBlockAccessListsRequest is the eth/71-style GetBlockAccessLists (0x12)
// request; its RLP shape matches the server-side getBlockAccessListsPacket so a
// plain gethp2p.Send round-trips.
type getBlockAccessListsRequest struct {
	RequestID uint64
	Hashes    []types.Hash
}

const maxBlockAccessListsRequest = 256

// NewDownloader builds the orchestrator without starting it.
func NewDownloader(exec executionProvider, hashedCanonical bool, snapshotCold state.StateReader, freezerDir string) *Downloader {
	return &Downloader{
		exec:            exec,
		peers:           make(map[string]*peerState),
		inflight:        make(map[string]chan inflightResp),
		hashedCanonical: hashedCanonical,
		snapshotCold:    snapshotCold,
		freezerDir:      freezerDir,
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
		// In-memory reverse-diff ring: lets a shallow tip reorg be unwound
		// without on-disk changesets (minimal mode has none). geth-style, N=128.
		d.adapter.EnableReorgJournal(reorgJournalDepth)
		log.Info("eldevp2p: reorg journal enabled (in-memory reverse-diff unwind)", "depth", reorgJournalDepth)
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

// OnBlockAccessLists routes an out-of-band BlockAccessLists (0x13) response to the
// matching in-flight request. Satisfies the devp2p balResponseHandler interface.
func (d *Downloader) OnBlockAccessLists(peerID string, reqID uint64, bals [][]byte) {
	d.dispatch(peerID, reqID, "a", inflightResp{bals: bals})
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
			// KNOWN tip: a synced peer serves tip+1 even though its advertised head
			// says otherwise. This is what makes 12s live-follow advance for a
			// pure-EL follower.
			if d.probeForNewTip(ctx, tip) {
				continue // tip advanced; loop to import the now-confirmed block
			}
			log.Info("eldevp2p: caught up", "head", local, "tip", tip, "lag", reorgLag, "peers", len(peers))
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
func (d *Downloader) probeForNewTip(ctx context.Context, knownTip uint64) bool {
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
			raw, err := d.requestHeaders(pctx, pp.id, pp.rw, knownTip+1, headersPerBatch)
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
				if n > knownTip {
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
		if r.maxN <= knownTip {
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
		// head==0 means the peer's tip number is unknown — an eth/68 peer, whose
		// Status carries the head hash but no number. Treat it as pickable: a
		// mainnet full node serves any historical range, and the first header
		// response bumps its head to the real value (see OnBlockHeaders). Only skip
		// peers KNOWN to sit below the requested block.
		if (p.head != 0 && p.head < needHead) || p.inflightN >= maxInflightPerPeer {
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
// localCatchUp executes [stateHead+1, localFreezerHead] DIRECTLY from local
// headerc+bodyc — NO peer download, NO MDBX header seeding, NO field
// reconstruction — the "already have the data, don't re-download" fast path for a
// state-height datadir (hashed-canonical / minimal / full) catching up to its own
// frozen tip. Blocks above the local freezer head (the live tip) are left to the
// peer loop.
//
// BLOCKHASH resolves FREEZER-DIRECT: the adapter's headerHashReader returns each
// ancestor's stored h.Hash() straight from headerc (the ethexec/witness-block-
// trace model), so the columnar-stripped ParentHash/UncleHash/Bloom fields are
// irrelevant to BLOCKHASH and no window seed / recompute is needed. The canonical
// index is written straight from the headerc header's stored hash (its stripped
// fields make a recompute wrong; h.Hash() is authoritative). Old finalized blocks
// never reorg → journal disabled. No local bodyc (leaves / archive-cs) → no-op.
func (d *Downloader) localCatchUp(ctx context.Context, hr *ethel.HeaderCompactReader) {
	if hr == nil {
		return // no headerc → no freezer-direct source, nothing to execute locally
	}
	br, err := ethel.OpenBodyCompact(d.freezerDir)
	if err != nil {
		log.Info("eldevp2p: local catch-up skipped — no local bodyc", "dir", d.freezerDir, "err", err)
		return
	}
	defer br.Close()

	local, err := d.localHead(ctx)
	if err != nil {
		return
	}
	start := local

	// The freezer-direct BLOCKHASH source (hr) is installed on the adapter by the
	// coordinator and stays there for the peer path too. Old finalized blocks
	// never reorg — no reverse-diff journal needed.
	d.adapter.SetJournalActive(false)

	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: local catch-up begin tx", "err", err)
		return
	}
	d.adapter.SetBatchTx(tx)
	txOpen := true
	defer func() {
		d.adapter.SetBatchTx(nil)
		if txOpen {
			tx.Rollback()
		}
	}()

	// EIP-2935 (ProcessExecutionBlockStart → vm.StoreParentBlockHash) writes
	// header.ParentHash into STATE, so it's a direct execution input — unlike
	// BLOCKHASH (freezer-direct) it can't be sourced externally. headerc strips
	// ParentHash, so reconstruct it from the prior block's canonical hash (same
	// freezer-direct source: ReadHeader(n-1).Hash()). This is the ONLY field
	// execution needs: UncleHash isn't an execution input, Bloom is filled by
	// execution, and canonical[n] is written from h.Hash() (SetHash) below.
	prevHash := types.Hash{}
	if p, perr := hr.ReadHeader(local); perr == nil && p != nil {
		prevHash = p.Hash()
	}

	committed := local
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n := local + 1
		hdr, herr := hr.ReadHeader(n)
		if herr != nil || hdr == nil {
			break // reached local header head
		}
		db, berr := br.ReadBody(n)
		if berr != nil || db == nil {
			break // reached local body head — hand off to peers
		}
		canonical := hdr.Hash() // headerc stored canonical hash (SetHash)
		hdr.ParentHash = prevHash // EIP-2935 execution input (see above)
		ws := make([]*api.Withdrawal, len(db.Withdrawals))
		for i, w := range db.Withdrawals {
			ws[i] = &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.Validator),
				Address:        w.Address,
				Amount:         hexutil.Uint64(w.Amount),
			}
		}
		blk, ok := block.NewBlock(hdr, db.Txs).(*block.Block)
		if !ok {
			log.Warn("eldevp2p: local catch-up assemble failed", "block", n)
			break
		}
		okRes, root, xerr := d.adapter.ExecutePayloadFromWire(blk, ws)
		if xerr != nil {
			log.Warn("eldevp2p: local catch-up exec error — handing to peers", "block", n, "err", xerr)
			break
		}
		if !okRes {
			log.Warn("eldevp2p: local catch-up payload invalid — handing to peers",
				"block", n, "computedRoot", root.Hex(), "declaredRoot", hdr.Root.Hex())
			break
		}
		// Write canonical[n] straight from the stored headerc hash — the columnar
		// header's fields are stripped so a recompute would be wrong; h.Hash() is
		// the authoritative canonical value. This is what the peer handoff's
		// parent-link check reads for canonical[localFreezerHead].
		if werr := rawdb.WriteCanonicalHash(tx, canonical, n); werr != nil {
			log.Warn("eldevp2p: local catch-up canonical write failed", "block", n, "err", werr)
			break
		}
		local = n
		prevHash = canonical // next block's EIP-2935 ParentHash input
		if local-committed >= uint64(commitInterval) {
			if err := d.commitBatch(tx, local); err != nil {
				log.Error("eldevp2p: local catch-up commit failed", "head", local, "err", err)
				tx.Rollback()
				txOpen = false
				return
			}
			committed = local
			if tx, err = d.exec.RwDB().BeginRw(ctx); err != nil {
				log.Warn("eldevp2p: local catch-up reopen tx", "err", err)
				txOpen = false
				return
			}
			d.adapter.SetBatchTx(tx)
		}
	}
	// Flush the tail (or roll back an empty tx).
	if local > committed {
		if err := d.commitBatch(tx, local); err != nil {
			log.Error("eldevp2p: local catch-up tail commit failed", "head", local, "err", err)
			tx.Rollback()
			txOpen = false
			return
		}
		txOpen = false
	} else {
		tx.Rollback()
		txOpen = false
	}
	if local > start {
		log.Info("eldevp2p: local catch-up complete (freezer-direct BLOCKHASH, no peer download)",
			"from", start+1, "head", local)
	}
}

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
		if err := d.validateHeaderLink(tx, hdr, n); err != nil {
			// Parent mismatch at n (canonical(n-1) present but != hdr.parent) means
			// our head n-1 was orphaned by a reorg. Commit the valid work done so
			// far, flush its diffs, and signal the coordinator to unwind the
			// orphaned head via the in-memory reverse-diff journal (minimal mode
			// has no on-disk changesets). This is the depth-1 post-merge case; a
			// deeper reorg unwinds one block per round until the branch links.
			if d.isReorg(tx, hdr, n) {
				if newHead > committed {
					if cerr := d.commitBatch(tx, newHead); cerr == nil {
						committed = newHead
						d.adapter.FlushReorgJournal()
					} else {
						tx.Rollback()
						d.adapter.DiscardReorgJournal()
					}
				} else {
					tx.Rollback()
					d.adapter.DiscardReorgJournal()
				}
				d.reorgPending = true
				log.Warn("eldevp2p: reorg detected — unwinding orphaned head",
					"block", n, "parent", hdr.ParentHash.Hex(), "ourHead", committed)
				return committed - local, committed
			}
			log.Warn("eldevp2p: header link invalid — refetch",
				"block", n, "hash", hdr.Hash().Hex(), "parent", hdr.ParentHash.Hex(), "err", err)
			d.buffer.drop(n)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		blk, withdrawals, aerr := assembleBlock(hdr, body)
		if aerr != nil {
			log.Warn("eldevp2p: assemble block failed", "block", n, "err", aerr)
			d.buffer.drop(n)
			break
		}
		if got := hash.DeriveShaErigon(rawTxList(unwrapWireTxs(body.Transactions))); got != hdr.TxHash {
			log.Warn("eldevp2p: body txRoot mismatch — refetch",
				"block", n, "got", got.Hex(), "want", hdr.TxHash.Hex())
			d.buffer.drop(n)
			break
		}
		// Per-block root verification (ExecutePayloadFromWire computes the
		// incremental root and checks it against the wire header.Root). State
		// commits into the shared batch tx with NO per-block commit.
		ok, root, xerr := d.adapter.ExecutePayloadFromWire(blk, withdrawals)
		if xerr != nil {
			log.Warn("eldevp2p: ExecutePayloadFromWire error", "block", n, "err", xerr)
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		if !ok {
			log.Warn("eldevp2p: payload invalid", "block", n,
				"computedRoot", root.Hex(), "declaredRoot", hdr.Root.Hex(),
				"parentHash", hdr.ParentHash.Hex(), "uncleHash", hdr.UncleHash.Hex(),
				"bloomZero", hdr.Bloom == (block.Bloom{}), "hdrHash", hdr.Hash().Hex())
			if os.Getenv("N42_RCPTDIFF") != "" {
				d.dumpMainnetReceipts(ctx, n, hdr.Hash())
			}
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		newHead = n
		want++

		if newHead-committed >= ci {
			if err := d.commitBatch(tx, newHead); err != nil {
				log.Error("eldevp2p: batch commit failed", "head", newHead, "err", err)
				tx.Rollback() // never leak the write tx (BeginRw would be MDBX_BUSY forever)
				d.adapter.DiscardReorgJournal()
				return committed - local, committed
			}
			committed = newHead
			d.adapter.FlushReorgJournal() // promote this batch's diffs into the ring
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
			// commitBatch can fail BEFORE tx.Commit (cs freezer flush) — the
			// write tx must not leak or every later BeginRw is MDBX_BUSY.
			tx.Rollback()
			d.adapter.DiscardReorgJournal()
			return committed - local, committed
		}
		committed = newHead
		d.adapter.FlushReorgJournal()
	} else {
		tx.Rollback()
		d.adapter.DiscardReorgJournal()
	}
	return committed - local, committed
}

// isReorg reports whether the link failure at block n is a REORG (our committed
// head n-1 is orphaned) rather than a gap/missing-parent. True iff canonical(n-1)
// exists and differs from hdr's parent — i.e. a competing block at n builds on a
// different n-1 than the one we committed.
func (d *Downloader) isReorg(tx kv.Tx, hdr *block.Header, n uint64) bool {
	if n == 0 {
		return false
	}
	parentCanon, err := rawdb.ReadCanonicalHash(tx, n-1)
	if err != nil || parentCanon == (types.Hash{}) {
		return false // no committed n-1 → a gap, not a reorg
	}
	return hdr.ParentHash != parentCanon
}

// handleReorg unwinds exactly ONE orphaned head block using its in-memory
// reverse-diff (restoring the warm overlay to the block's pre-state), clears its
// canonical mapping, and rolls the progress marker back by one. The coordinator
// then re-fetches the canonical block at that height; a depth-1 reorg links
// immediately, a deeper one re-enters here and unwinds another block, bounded by
// the journal depth (N=128, far below finality). This is the minimal-mode
// substitute for erigon-style changeset unwind (geth's in-memory-diff model).
func (d *Downloader) handleReorg(ctx context.Context) {
	d.reorgPending = false
	if d.hashedCanonical {
		// Archive (hashed-canonical) nodes carry per-block changesets — unwind
		// via those instead of the (restart-volatile) in-memory journal.
		d.handleReorgHashed(ctx)
		return
	}
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: reorg begin tx failed", "err", err)
		return
	}
	defer tx.Rollback()

	num, hash, ok, uerr := d.adapter.UnwindOverlayBlock(tx)
	if uerr != nil {
		log.Error("eldevp2p: reorg reverse-diff restore failed", "err", uerr)
		return
	}
	if !ok {
		// Journal exhausted: the reorg is deeper than reorgJournalDepth, i.e.
		// below finality — this does not happen on mainnet. Halt live-follow
		// rather than corrupt state; the operator re-syncs.
		log.Error("eldevp2p: reorg deeper than reverse-diff journal — cannot unwind; halting (re-sync required)",
			"journalDepth", d.adapter.ReorgJournalDepth())
		d.reorgFailed = true
		d.reorgFailReason = "reorg deeper than reverse-diff journal"
		return
	}
	if terr := rawdb.TruncateCanonicalHash(tx, num, true); terr != nil {
		log.Error("eldevp2p: reorg truncate canonical failed", "num", num, "err", terr)
		return
	}
	// commitBatch writes the progress marker (= num-1) and commits the tx, which
	// carries the restored warm state + cleared canonical atomically.
	if cerr := d.commitBatch(tx, num-1); cerr != nil {
		log.Error("eldevp2p: reorg commit failed", "num", num, "err", cerr)
		return
	}
	// The DB is now durable — only now finalize the journal pop. A failure in
	// TruncateCanonicalHash or commitBatch above rolled the tx back with the
	// journal entry intact, so the next attempt restores from it cleanly.
	d.adapter.CommitOverlayUnwind(num)
	d.buffer.prune(num - 1) // drop any stale buffered blocks at/below the unwound head
	log.Info("eldevp2p: reorg unwound one block", "orphan", num, "orphanHash", hash.Hex(),
		"newHead", num-1, "journalDepth", d.adapter.ReorgJournalDepth())
}

// handleReorgHashed unwinds exactly ONE orphaned head block on a
// hashed-canonical (archive) node via its recorded changeset — the freezer
// blob when the cs sink covers it, the legacy MDBX changesets otherwise. The
// unwind rewrites HashedAccounts/HashedStorage + TrieOf* incrementally and
// the recomputed root is verified against the parent header before anything
// commits. Unlike the minimal-mode reverse-diff journal this survives
// restarts, so a tip reorg that lands while the node is down (journalDepth=0
// on boot) no longer halts sync.
func (d *Downloader) handleReorgHashed(ctx context.Context) {
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		log.Warn("eldevp2p: reorg begin tx failed", "err", err)
		return
	}
	defer tx.Rollback()

	head := ethel.ReadProgress(tx)
	if head == 0 {
		log.Error("eldevp2p: reorg with no head marker — halting")
		d.reorgFailed = true
		d.reorgFailReason = "reorg with no head marker"
		return
	}
	var provider commitment.BlockChangesProvider
	if d.csSink != nil {
		provider = d.csSink.BlockChangesProvider()
	}
	root, err := commitment.UnwindHashedCanonicalWith(tx, head, head-1, provider)
	if err != nil {
		log.Error("eldevp2p: changeset unwind failed — halting (re-sync required)",
			"block", head, "err", err)
		d.reorgFailed = true
		d.reorgFailReason = fmt.Sprintf("changeset unwind failed at block %d: %v", head, err)
		return
	}
	// Verify the unwound state against the parent header's root — an unwind
	// that does not land exactly on the parent state must not commit.
	parentCanon, err := rawdb.ReadCanonicalHash(tx, head-1)
	if err != nil || parentCanon == (types.Hash{}) {
		log.Error("eldevp2p: reorg unwind: no canonical parent", "block", head-1)
		return
	}
	if hdr := rawdb.ReadHeader(tx, parentCanon, head-1); hdr == nil || root != hdr.Root {
		var want string
		if hdr != nil {
			want = hdr.Root.Hex()
		}
		log.Error("eldevp2p: reorg unwind root mismatch — halting",
			"block", head-1, "computed", root.Hex(), "want", want)
		d.reorgFailed = true
		d.reorgFailReason = fmt.Sprintf("reorg unwind root mismatch at block %d", head-1)
		return
	}
	if terr := rawdb.TruncateCanonicalHash(tx, head, true); terr != nil {
		log.Error("eldevp2p: reorg truncate canonical failed", "num", head, "err", terr)
		return
	}
	// TrieOf* is now at head-1 (the unwind maintained it); rewind the staged
	// Merkle frontier with it so boundary math never underflows.
	if d.staged && d.lastMerkle >= head {
		var v [8]byte
		binary.BigEndian.PutUint64(v[:], head-1)
		if err := tx.Put(progressTable, merkleProgressKey, v[:]); err != nil {
			log.Error("eldevp2p: reorg merkle marker rewind failed", "err", err)
			return
		}
		d.lastMerkle = head - 1
	}
	// The unwind rewrote hashed state outside the read cache's invalidation
	// hooks — drop it before any re-execution reads through it.
	d.adapter.PurgeHashedReadCache()
	// Re-align the cs freezer sink to the unwound head: the replacement block
	// appends at head, which may sit below the tables' previously adopted
	// origin (SetStartItem re-declares it on the emptied tables).
	if d.csSink != nil {
		if rerr := d.csSink.Rewind(head - 1); rerr != nil {
			log.Error("eldevp2p: cs sink rewind failed — halting", "err", rerr)
			return
		}
	}
	if cerr := d.commitBatch(tx, head-1); cerr != nil {
		log.Error("eldevp2p: reorg commit failed", "num", head, "err", cerr)
		return
	}
	d.buffer.prune(head - 1)
	log.Info("eldevp2p: reorg unwound one block via changesets", "orphan", head,
		"newHead", head-1, "root", root.Hex())
}

func (d *Downloader) validateHeaderLink(tx kv.Tx, hdr *block.Header, n uint64) error {
	if hdr == nil {
		return errors.New("nil header")
	}
	if n == 0 {
		return nil
	}
	parentHash, err := rawdb.ReadCanonicalHash(tx, n-1)
	if err != nil {
		return fmt.Errorf("read parent canonical hash %d: %w", n-1, err)
	}
	if parentHash == (types.Hash{}) {
		return fmt.Errorf("missing canonical parent hash at %d", n-1)
	}
	if hdr.ParentHash != parentHash {
		return fmt.Errorf("parent mismatch: got %s want %s", hdr.ParentHash.Hex(), parentHash.Hex())
	}
	if existing, err := rawdb.ReadCanonicalHash(tx, n); err != nil {
		return fmt.Errorf("read existing canonical hash %d: %w", n, err)
	} else if existing != (types.Hash{}) && existing != hdr.Hash() {
		return fmt.Errorf("canonical hash collision at %d: existing %s new %s",
			n, existing.Hex(), hdr.Hash().Hex())
	}
	return nil
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
var asyncMerkleEnabled = os.Getenv("N42_ASYNC_MERKLE") != "0"

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

// requestBlockAccessLists fetches the out-of-band EIP-7928 BALs for the given
// block hashes from a peer (eth/71-style GetBlockAccessLists 0x12). Returns the
// raw BAL RLP per requested hash, in request order (nil where the peer has none),
// so a consumer can decode + verify against the header hash and drive prefetch.
func (d *Downloader) requestBlockAccessLists(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([][]byte, error) {
	if len(hashes) > maxBlockAccessListsRequest {
		return nil, fmt.Errorf("too many block access lists requested: %d > %d", len(hashes), maxBlockAccessListsRequest)
	}
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:a:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := getBlockAccessListsRequest{RequestID: reqID, Hashes: hashes}
	if err := gethp2p.Send(rw, 0x12, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockAccessLists: %w", err)
	}
	select {
	case resp := <-ch:
		if len(resp.bals) > len(hashes) {
			return nil, fmt.Errorf("block access lists response has %d entries for %d requested hashes", len(resp.bals), len(hashes))
		}
		out := make([][]byte, len(resp.bals))
		for i, b := range resp.bals {
			out[i] = []byte(b)
		}
		return out, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("block access lists timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// FetchBlockAccessLists fetches the out-of-band EIP-7928 BALs for the given block
// hashes from any available peer at atHeight. Public entry for the BAL
// prefetch/verify path: the caller decodes each raw BAL (bal.DecodeBAL), verifies
// it against the header's BlockAccessListHash, and can prewarm state from it.
func (d *Downloader) FetchBlockAccessLists(ctx context.Context, atHeight uint64, hashes []types.Hash) ([][]byte, error) {
	pp := d.pickPeer(atHeight)
	if pp == nil {
		return nil, errors.New("no peer for block access lists")
	}
	defer d.releasePeer(pp)
	return d.requestBlockAccessLists(ctx, pp.id, pp.rw, hashes)
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
