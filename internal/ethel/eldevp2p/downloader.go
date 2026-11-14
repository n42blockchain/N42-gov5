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
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
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
var maxInflightPerPeer = envswitch.Int("N42_INFLIGHT", 4, envswitch.Perf, "Concurrent requests per peer of the eth-el downloader.")

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

// peerProbeGrace leaves the base-protocol handshake quiet long enough for
// ping/pong and capability checks before active live-tip probing starts. A
// newly connected peer may legitimately advertise the same head and have no
// next header yet; requesting localHead+1 immediately breaks ForkID-only peers
// and needlessly races their post-handshake checks.
const peerProbeGrace = 10 * time.Second

// Downloader satisfies devp2p.ResponseHandler. One instance per
// eldevp2p.Service; it tracks every peer that completes handshake and
// drives the catch-up fetch loop.
type Downloader struct {
	exec executionProvider

	mu       sync.Mutex
	peers    map[string]*peerState        // peerID → state
	inflight map[string]chan inflightResp // reqKey → reply channel
	adapter  *api.EngineStateAdapter
	// invalidAncestorObserver reports the advertised peer head when execution
	// proves an ancestor invalid. The Engine API uses this to reject a payload
	// whose missing parent chain was learned through devp2p.
	invalidAncestorObserver func(rejectedHead, latestValidHash types.Hash)
	knownEngineBlock        EngineBlockKnownFunc
	importEngineBlock       EngineBlockImporter
	missingAncestors        chan types.Hash
	pendingAncestors        map[types.Hash]struct{}

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

// SetInvalidAncestorObserver installs the bridge to the Engine API rejected
// payload cache. It should be configured before the first peer handshake.
func (d *Downloader) SetInvalidAncestorObserver(observer func(rejectedHead, latestValidHash types.Hash)) {
	d.invalidAncestorObserver = observer
}

func (d *Downloader) reportInvalidAncestor(rejectedHead, latestValidHash types.Hash) {
	if d.invalidAncestorObserver == nil || rejectedHead == (types.Hash{}) {
		return
	}
	d.invalidAncestorObserver(rejectedHead, latestValidHash)
}

func rejectedBranchTip(headers []*block.Header, invalidIndex int) types.Hash {
	if invalidIndex < 0 || invalidIndex >= len(headers) || headers[invalidIndex] == nil {
		return types.Hash{}
	}
	tip := headers[invalidIndex]
	for _, next := range headers[invalidIndex+1:] {
		if next == nil || next.Number64().Uint64() != tip.Number64().Uint64()+1 || next.ParentHash != tip.Hash() {
			break
		}
		tip = next
	}
	return tip.Hash()
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
	rw          gethp2p.MsgReadWriter
	head        uint64
	headHash    types.Hash
	version     uint
	connectedAt time.Time
	inflightN   int // concurrent requests currently dispatched to this peer
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
		exec:             exec,
		peers:            make(map[string]*peerState),
		inflight:         make(map[string]chan inflightResp),
		hashedCanonical:  hashedCanonical,
		snapshotCold:     snapshotCold,
		freezerDir:       freezerDir,
		buffer:           newBlockBuffer(),
		missingAncestors: make(chan types.Hash, 16),
		pendingAncestors: make(map[types.Hash]struct{}),
	}
}

// SetEngineSyncBridge installs the Engine-side known/import callbacks used by
// hash-directed missing-ancestor retrieval.
func (d *Downloader) SetEngineSyncBridge(known EngineBlockKnownFunc, importer EngineBlockImporter) {
	d.mu.Lock()
	d.knownEngineBlock = known
	d.importEngineBlock = importer
	d.mu.Unlock()
}

// RequestMissingAncestor queues one parent hash without blocking Engine RPC.
func (d *Downloader) RequestMissingAncestor(hash types.Hash) {
	if hash == (types.Hash{}) {
		return
	}
	d.mu.Lock()
	if _, exists := d.pendingAncestors[hash]; exists {
		d.mu.Unlock()
		return
	}
	d.pendingAncestors[hash] = struct{}{}
	d.mu.Unlock()
	select {
	case d.missingAncestors <- hash:
	default:
		d.mu.Lock()
		delete(d.pendingAncestors, hash)
		d.mu.Unlock()
		log.Warn("eldevp2p: missing-ancestor queue full", "hash", hash.Hex())
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

func init() {
	envswitch.Declare("N42_REORG_LAG", envswitch.Config, "Confirmation depth of live-follow in the eth-el downloader.")
	envswitch.Declare("N42_REORG_JOURNAL", envswitch.Perf, "Recent committed blocks that keep an in-memory reverse diff.")
	envswitch.Declare("N42_STAGED", envswitch.Config, "Staged catch-up mode of the eth-el downloader.")
	envswitch.Declare("N42_SUBBATCH", envswitch.Perf, "Sub-batch size of the staged eth-el catch-up.")
	envswitch.Declare("N42_ANCHOR_DIR", envswitch.Config, "Directory of consensus anchors an eth-el catch-up verifies against.")
	envswitch.Declare("N42_MPT_VERIFY_INTERVAL", envswitch.Perf, "Interval of the MPT verification of the eth-el downloader.")
	envswitch.Declare("N42_ATTEST_KEY", envswitch.Config, "Attestation signing key of an eth-el node.")
	envswitch.Declare("N42_ATTEST_AGGREGATOR", envswitch.Config, "URL of the attestation aggregator an eth-el node posts to.")
	envswitch.Declare("N42_CS_FREEZER", envswitch.Config, "Default on: per-block changesets append to freezer segment files instead of MDBX.")
	envswitch.Declare("N42_PIPELINE", envswitch.Perf, "Default on: download pipeline of the eth-el downloader; 0 reverts to serial.")
	envswitch.Declare("N42_COMMIT_INTERVAL", envswitch.Config, "Commit interval of the eth-el downloader.")
	envswitch.Declare("N42_RCPTDIFF", envswitch.Diag, "Diff receipts against the reference.")
}
