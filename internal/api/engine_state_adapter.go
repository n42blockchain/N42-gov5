// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// EngineStateAdapter bridges the Engine API with persistent state execution.
// Wraps a kv.RwDB, freezer, chain configuration and consensus engine so
// that ExecutePayload can run a CL-supplied block through the EVM,
// commit the resulting state and verify the declared state root. Provides
// the thin glue layer between newPayload/forkchoiceUpdated RPC handlers
// and the internal ethel execution pipeline.

package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// gas161WarnOnce guards the one-time loud warning emitted when the
// N42_GAS161 consensus-bypass diagnostic is active (see executeBlock).
var gas161WarnOnce sync.Once

// noReadCacheLogOnce guards the one-time log emitted when the N42_NO_READCACHE
// diagnostic disables the cross-block hashed read cache (see executePayloadDetailed).
var noReadCacheLogOnce sync.Once

// EngineStateAdapter bridges Engine API with persistent state execution.
type EngineStateAdapter struct {
	db       kv.RwDB
	freezer  *freezer.Freezer
	chainCfg *params.ChainConfig
	engine   consensus.Engine

	// csSource overrides Reorg's data source. nil falls back to a.freezer.
	csSource cs.Source

	// balPrefetch, when set, returns the raw EIP-7928 BAL for a block hash so
	// executePayloadDetailed can prewarm state before executing (out-of-band BAL
	// consume side). nil = no prewarm. Wire it to eldevp2p.FetchBlockAccessLists on
	// the sync path, or leave nil and fall back to the locally stored BAL.
	balPrefetch func(hash types.Hash) []byte

	// fastVerify skips the full-MPT VerifyStateRoot pass at end of
	// executePayloadDetailed and trusts the HPH IntermediateRoot
	// computed during execution. Set true for the wire-driven sync
	// path (ExecutePayloadFromWire); leave false for CL/test paths
	// where state-root reconstruction is the canonical verifier.
	fastVerify bool

	// hashedCanonical selects the reth-2.2-style hashed-canonical state
	// model: EVM reads come from HashedAccounts/HashedStorage (keccak the
	// key on access) instead of PlainState, and the state-root computer
	// runs incrementally over the migrated TrieOfAccounts/TrieOfStorage.
	// Set true when the datadir was populated by n42-migrate-reth-hashed
	// (no PlainState). Leave false for the legacy plain-state datadir.
	hashedCanonical bool

	// batchTx, when non-nil, makes executePayloadDetailed run against this
	// caller-owned tx and NOT open/commit its own. The batch importer
	// (eldevp2p executeRange) sets it so many blocks execute into one tx and
	// the head advance commits ATOMICALLY with the block state — a kill then
	// leaves state and head consistent (no "state ahead of head" split), and
	// per-block fsync is replaced by one commit per batch. Single-coordinator
	// use only (the CL/NewPayload path leaves it nil → unchanged behavior).
	batchTx kv.RwTx

	// headerHashReader, when set, is the freezer-direct BLOCKHASH source (see
	// SetHeaderHashReader). Preferred over internalcore.GetHashFn's ParentHash walk.
	headerHashReader headerHashReader

	// trackHeadMarker keeps the eth-el SyncStageProgress marker aligned with
	// Engine forkchoice. Generic Engine adapters leave this disabled because
	// their database schema does not necessarily include that table.
	trackHeadMarker bool

	// staged: staged catch-up execution. The trc runs writeOnly (writes
	// HashedAccounts/HashedStorage + changesets, SKIPS per-block CalcTrieRoot),
	// the trusted wire header.Root is kept as-is, and the per-block root compare
	// is skipped. The state root + TrieOf* are produced/verified per sub-batch by
	// commitment.MerkleStageIncremental, driven by the importer. Removes ~82ms/block
	// dRoot during catch-up (validated ~7-12ms/block amortized at 1k-10k sub-batches).
	staged bool

	// snapshotCold, when non-nil, is the immutable H0 snapshot StateReader
	// (a snapshotreader.StateReader). In plain (non-hashed-canonical) mode it
	// turns on snapshot-direct (minimal/full): reads go through a
	// WarmOverlayReader(warm MDBX + this cold snapshot) and writes use
	// OverlayStateWriter (empty-value tombstones on delete). Set by the node
	// when --bootstrap.mode=snapshot.
	snapshotCold state.StateReader

	// reorgJournal, when non-nil (snapshot-direct only), captures each executed
	// block's warm-table reverse-diff into an in-memory ring so a shallow tip
	// reorg can be unwound without on-disk changesets (minimal mode has none).
	// journalActive gates capture to blocks near the tip — bulk catch-up of old
	// (finalized) blocks never reorgs, so the per-write pre-image read is skipped
	// there to keep catch-up at full speed.
	reorgJournal  *overlayReorgJournal
	journalActive bool

	// hashedReadCache: cross-block LRU over HashedAccounts/HashedStorage/Code
	// reads (hashed-canonical mode). Created lazily on first block; write
	// invalidation is wired into the TrieRootComputer each block. Purged on
	// any state rewind (see Reorg).
	hashedReadCache *state.HashedReadCache

	// csFreezerSink, when non-nil, routes hashed-canonical per-block
	// changesets to acctcs/storcs freezer tables (ethel.CSFreezerWriter)
	// instead of MDBX AccountChangeSet/StorageChangeSet, and drops MDBX
	// history-index writes (archive data layout). The importer owns
	// flush+fsync at its commit boundary.
	csFreezerSink *ethel.CSFreezerSink
}

// SetCSFreezerSink switches hashed-canonical changeset persistence to the
// freezer sink (nil restores MDBX changesets + history).
func (a *EngineStateAdapter) SetCSFreezerSink(s *ethel.CSFreezerSink) { a.csFreezerSink = s }

// SetBALPrefetch wires the out-of-band BAL source used to prewarm state before
// executing a block (EIP-7928 consume side). The func returns the raw BAL for a
// block hash, or nil if unavailable; typically eldevp2p.FetchBlockAccessLists on
// the sync path. nil disables the fetch-based prefetch (the locally stored BAL is
// still used when present).
func (a *EngineStateAdapter) SetBALPrefetch(fn func(hash types.Hash) []byte) { a.balPrefetch = fn }

// headerHashReader reads a block header carrying its stored canonical hash — the
// columnar headerc reader (h.Hash() returns the SetHash-stored value directly,
// no ParentHash/Bloom reconstruction, no recompute).
type headerHashReader interface {
	ReadHeader(blockNum uint64) (*block.Header, error)
}

// SetHeaderHashReader installs a freezer-direct BLOCKHASH source. When set,
// BLOCKHASH resolves block n's hash straight from this reader's stored
// h.Hash() (falling back to the MDBX canonical index for blocks the reader
// doesn't hold — i.e. blocks imported above the local freezer head). This is the
// ethexec/witness-block-trace model: it does NOT walk MDBX header.ParentHash
// fields (internalcore.GetHashFn), so a state-height datadir whose header chain
// is absent/columnar-stripped resolves BLOCKHASH correctly with zero seeding or
// field reconstruction. Pass nil to restore the default GetHashFn walk.
func (a *EngineStateAdapter) SetHeaderHashReader(r headerHashReader) { a.headerHashReader = r }

// PurgeHashedReadCache drops the cross-block read cache. Callers that rewrite
// hashed state outside the TrieRootComputer's invalidation hooks (e.g. the
// downloader's changeset reorg unwind) must call this before reads resume.
func (a *EngineStateAdapter) PurgeHashedReadCache() { a.hashedReadCache.PurgeAll() }

// SetBatchTx routes executePayloadDetailed onto a caller-owned tx (no internal
// commit). Pass nil to restore standalone (open+commit-per-call) behavior.
//
// The hashed read cache is purged here: reads inside a batch memoize values
// the batch tx has written but not yet committed, so if the caller ROLLED the
// previous batch back those entries are stale. Purging at every batch
// boundary (success or failure — we can't tell which) costs one extra MDBX
// read per hot key per batch, which is noise against the within-batch reuse.
func (a *EngineStateAdapter) SetBatchTx(tx kv.RwTx) {
	a.batchTx = tx
	if tx != nil {
		// Mid-range rotation (commit → new tx): everything previously cached
		// is either committed or visible in the new tx — keep the cache warm.
		// Purging here (every commitInterval=256) capped hit rates at ~30%.
		return
	}
	// Batch teardown (deferred SetBatchTx(nil) runs on BOTH success and error
	// exits of executeRange): an errored range rolled its tx back, so entries
	// memoized from that tx's uncommitted writes are stale — drop everything.
	// Once per multi-thousand-block range, the cold restart is noise.
	if a.hashedReadCache != nil {
		ah, am, sh, sm, ch, cm := a.hashedReadCache.StatsAndReset()
		if ah+am+sh+sm > 0 {
			log.Info("hashed read cache range stats",
				"accHit", ah, "accMiss", am,
				"stoHit", sh, "stoMiss", sm,
				"codeHit", ch, "codeMiss", cm)
		}
	}
	a.hashedReadCache.PurgeAll()
}

// EnableReorgJournal turns on in-memory reverse-diff capture for shallow-reorg
// unwind (snapshot-direct minimal/full mode; no-op semantics for other modes
// since only OverlayStateWriter writes are journaled). n is the ring depth
// (blocks retained); geth uses 128.
func (a *EngineStateAdapter) EnableReorgJournal(n int) {
	a.reorgJournal = newOverlayReorgJournal(n)
}

// SetJournalActive toggles reverse-diff capture. The downloader turns it on only
// when the executed head is within the journal window of the tip, so bulk
// catch-up runs capture-free at full speed. No-op when journaling is off.
func (a *EngineStateAdapter) SetJournalActive(v bool) { a.journalActive = v }

// FlushReorgJournal promotes the in-flight batch's captured diffs into the ring.
// Call after a successful commitBatch. No-op when journaling is off.
func (a *EngineStateAdapter) FlushReorgJournal() {
	if a.reorgJournal != nil {
		a.reorgJournal.flush()
	}
}

// DiscardReorgJournal drops the in-flight batch's captured diffs. Call when a
// batch tx is rolled back. No-op when journaling is off.
func (a *EngineStateAdapter) DiscardReorgJournal() {
	if a.reorgJournal != nil {
		a.reorgJournal.discard()
	}
}

// UnwindOverlayBlock restores the highest journaled block's warm state into tx
// and pops it, returning its number+hash. ok=false when journaling is off or the
// ring is empty (reorg deeper than the journal). The caller clears the canonical
// mapping for the returned number and rolls the progress marker back to num-1 in
// the SAME tx.
func (a *EngineStateAdapter) UnwindOverlayBlock(tx kv.RwTx) (uint64, types.Hash, bool, error) {
	if a.reorgJournal == nil {
		return 0, types.Hash{}, false, nil
	}
	return a.reorgJournal.unwindOne(tx)
}

// CommitOverlayUnwind finalizes an UnwindOverlayBlock (pops the journal entry)
// after the caller's tx has durably committed. Skipping this — or calling it
// before the commit — either leaks the entry or loses the undo on a failed
// commit; see overlayReorgJournal.unwindOne.
func (a *EngineStateAdapter) CommitOverlayUnwind(num uint64) {
	if a.reorgJournal != nil {
		a.reorgJournal.commitUnwind(num)
	}
}

// ReorgJournalDepth reports how many blocks are currently unwindable.
func (a *EngineStateAdapter) ReorgJournalDepth() int {
	if a.reorgJournal == nil {
		return 0
	}
	return a.reorgJournal.depth()
}

// SetStaged toggles staged catch-up execution (writeOnly root, per-sub-batch Merkle).
func (a *EngineStateAdapter) SetStaged(v bool) { a.staged = v }

// SetSnapshotCold enables snapshot-direct (minimal/full): in plain mode, reads
// overlay the warm MDBX on this immutable H0 snapshot reader and writes use
// tombstone-on-delete. Pass nil to disable (legacy plain-state behavior).
func (a *EngineStateAdapter) SetSnapshotCold(r state.StateReader) { a.snapshotCold = r }

type enginePayloadExecutionResult struct {
	stateRoot       types.Hash
	receipts        block.Receipts
	stateOverlay    *engineStateOverlay
	validationError error
}

// NewEngineStateAdapter creates a new adapter.
func NewEngineStateAdapter(db kv.RwDB, f *freezer.Freezer, cfg *params.ChainConfig, engine consensus.Engine) *EngineStateAdapter {
	return &EngineStateAdapter{db: db, freezer: f, chainCfg: cfg, engine: engine}
}

// WithCSSource overrides Reorg's data source (e.g. cs.TieredSource
// over warm + freezer). Pass nil to revert to direct freezer reads.
func (a *EngineStateAdapter) WithCSSource(src cs.Source) *EngineStateAdapter {
	a.csSource = src
	return a
}

// WithHashedCanonical enables the reth-2.2-style hashed-canonical state
// model (read/execute against HashedAccounts/HashedStorage, incremental
// root over migrated TrieOf*). Use for datadirs built by
// n42-migrate-reth-hashed.
func (a *EngineStateAdapter) WithHashedCanonical(v bool) *EngineStateAdapter {
	a.hashedCanonical = v
	return a
}

// WithHeadMarker enables the eth-el execution-head marker.
func (a *EngineStateAdapter) WithHeadMarker(v bool) *EngineStateAdapter {
	a.trackHeadMarker = v
	return a
}

// ExecutePayload executes a block from the CL, persists state, verifies root.
// Returns (valid bool, stateRoot, error).
func (a *EngineStateAdapter) ExecutePayload(blk *block.Block) (bool, types.Hash, error) {
	result, err := a.executePayloadDetailed(blk, nil, nil, nil, false)
	if err != nil {
		return false, types.Hash{}, err
	}
	if result.validationError != nil {
		return false, result.stateRoot, nil
	}
	return true, result.stateRoot, nil
}

// ExecutePayloadFromWire executes a block decoded directly from the
// eth/68-69 devp2p wire form: header + raw transactions + withdrawals.
// Unlike the CL path, the Pectra+ execution requests hash is anchored
// to header.RequestsHash carried on the wire (no separate CL-supplied
// requests list), and withdrawals are sourced from the body rather than
// engine_newPayloadV4 args. Returns (valid, computed state root, err).
//
// State-root verification uses the HPH IntermediateRoot already computed
// during execution (catchup-grade ~3000 blk/s); the full-MPT rebuild
// VerifyStateRoot is skipped because it OOMs at 25M-state scale.
func (a *EngineStateAdapter) ExecutePayloadFromWire(blk *block.Block, withdrawals []*Withdrawal) (bool, types.Hash, error) {
	return a.executePayloadFromWire(blk, withdrawals, false)
}

// ExecutePayloadFromWireWithFullVerification executes a wire block with the
// complete state-root path. This is intended for isolated RLP fixture imports,
// where the database starts at a small synthetic genesis and correctness is
// more important than the catch-up shortcut used by the live wire sync path.
func (a *EngineStateAdapter) ExecutePayloadFromWireWithFullVerification(blk *block.Block, withdrawals []*Withdrawal) (bool, types.Hash, error) {
	return a.executePayloadFromWireMode(blk, withdrawals, false, false)
}

// ExecutePayloadFromTrustedColumnar executes a block read from the local
// columnar freezer. That codec deliberately strips logsBloom while retaining the
// canonical header hash, so this trusted local-only path may reconstruct a
// missing all-zero bloom from the already-validated receipts.
func (a *EngineStateAdapter) ExecutePayloadFromTrustedColumnar(blk *block.Block, withdrawals []*Withdrawal) (bool, types.Hash, error) {
	return a.executePayloadFromWire(blk, withdrawals, true)
}

func (a *EngineStateAdapter) executePayloadFromWire(blk *block.Block, withdrawals []*Withdrawal, allowMissingExpectedBloom bool) (bool, types.Hash, error) {
	return a.executePayloadFromWireMode(blk, withdrawals, allowMissingExpectedBloom, true)
}

func (a *EngineStateAdapter) executePayloadFromWireMode(blk *block.Block, withdrawals []*Withdrawal, allowMissingExpectedBloom, fastVerify bool) (bool, types.Hash, error) {
	var parentBeaconRoot *types.Hash
	if hdr, _ := blk.Header().(*block.Header); hdr != nil && hdr.ParentBeaconRoot != nil {
		v := *hdr.ParentBeaconRoot
		parentBeaconRoot = &v
	}
	prevFast := a.fastVerify
	a.fastVerify = fastVerify
	defer func() { a.fastVerify = prevFast }()
	result, err := a.executePayloadDetailed(blk, parentBeaconRoot, nil, withdrawals, allowMissingExpectedBloom)
	if err != nil {
		return false, types.Hash{}, err
	}
	if result.validationError != nil {
		log.Warn("ExecutePayloadFromWire: validation failed",
			"block", blk.Number64().Uint64(), "err", result.validationError)
		return false, result.stateRoot, nil
	}
	return true, result.stateRoot, nil
}

// decodeRevert renders a tx's revert payload: solidity Error(string),
// Panic(uint256), or a raw 4-byte custom-error selector. Diagnostic only.
func decodeRevert(data []byte) string {
	if len(data) == 0 {
		return "(empty)"
	}
	if len(data) < 4 {
		return fmt.Sprintf("(short 0x%x)", data)
	}
	sel := data[:4]
	if bytes.Equal(sel, []byte{0x08, 0xc3, 0x79, 0xa0}) && len(data) >= 68 { // Error(string)
		strLen := 0
		for _, b := range data[60:68] {
			strLen = strLen<<8 | int(b)
		}
		if strLen >= 0 && 68+strLen <= len(data) {
			return "Error(" + string(data[68:68+strLen]) + ")"
		}
		return "Error(string?)"
	}
	if bytes.Equal(sel, []byte{0x4e, 0x48, 0x7b, 0x71}) && len(data) >= 36 { // Panic(uint256)
		return fmt.Sprintf("Panic(0x%x)", data[4:36])
	}
	return fmt.Sprintf("customErr sel=0x%x", sel)
}

// recoverSenders runs ecrecover on every transaction in parallel, caching each
// recovered sender on the tx via SetFrom. A worker per CPU pulls indices off a
// shared atomic counter. Errors are left uncached so the serial apply loop's
// AsMessage surfaces them exactly as before (no behavior change, only timing).
func (a *EngineStateAdapter) recoverSenders(txns []*transaction.Transaction, signer transaction.Signer) {
	workers := runtime.NumCPU()
	if workers > len(txns) {
		workers = len(txns)
	}
	var next int64 = -1
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&next, 1))
				if i >= len(txns) {
					return
				}
				if txns[i].From() != nil {
					continue
				}
				if from, err := transaction.Sender(signer, txns[i]); err == nil {
					txns[i].SetFrom(from)
				}
			}
		}()
	}
	wg.Wait()
}

// isAllZeroBytes reports whether b is empty or all zero.
func isAllZeroBytes(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func validateLogsBloom(actual, expected []byte, allowMissingExpected bool) error {
	if allowMissingExpected && isAllZeroBytes(expected) {
		return nil
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("logs bloom mismatch")
	}
	return nil
}

func (a *EngineStateAdapter) executePayloadDetailed(blk *block.Block, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal, allowMissingExpectedBloom bool) (_ *enginePayloadExecutionResult, retErr error) {
	return a.executePayloadDetailedMode(blk, parentBeaconRoot, expectedRequests, withdrawals, allowMissingExpectedBloom, true, nil)
}

func (a *EngineStateAdapter) validatePayloadDetailed(blk *block.Block, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal, parentState *engineStateOverlay) (*enginePayloadExecutionResult, error) {
	return a.executePayloadDetailedModeWithParent(blk, parentHash, parentBeaconRoot, expectedRequests, withdrawals, false, false, parentState)
}

func (a *EngineStateAdapter) executePayloadDetailedMode(blk *block.Block, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal, allowMissingExpectedBloom, persist bool, parentState *engineStateOverlay) (_ *enginePayloadExecutionResult, retErr error) {
	return a.executePayloadDetailedModeWithParent(blk, types.Hash{}, parentBeaconRoot, expectedRequests, withdrawals, allowMissingExpectedBloom, persist, parentState)
}

func (a *EngineStateAdapter) executePayloadDetailedModeWithParent(blk *block.Block, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal, allowMissingExpectedBloom, persist bool, parentState *engineStateOverlay) (_ *enginePayloadExecutionResult, retErr error) {
	// A failed block leaves the read cache holding values memoized from this
	// (about to be rolled back) tx — drop them. The batch path additionally
	// purges on every SetBatchTx.
	defer func() {
		if retErr != nil {
			a.hashedReadCache.PurgeAll()
		}
	}()
	header := blk.Header().(*block.Header)
	if header == nil || header.Number == nil {
		return nil, fmt.Errorf("payload header missing block number")
	}
	if parentBeaconRoot == nil {
		parentBeaconRoot = header.ParentBeaconRoot
	}
	blockNum := header.Number.Uint64()
	// Keep the staged parent's cumulative lineage for the next overlay even
	// when execution can read this parent directly from canonical storage.
	// Dropping it here would reduce every newly staged overlay to a one-block
	// delta, which is insufficient after a later deep reorg unwinds that parent.
	overlayParentState := parentState

	// When a batch tx is supplied, run against it and let the caller commit
	// (atomic state+head per batch). Otherwise open + commit our own tx.
	tx := a.batchTx
	ownTx := tx == nil
	var err error
	if ownTx {
		tx, err = a.db.BeginRw(context.Background())
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
	}
	validationStateRebased := false
	validationRewindBlockNum := blockNum
	if !persist {
		// A persisted canonical parent is represented directly by database
		// state. Overlay state is only needed when the parent itself is a
		// staged side-chain payload. Resolve this before choosing the rewind
		// point: a cumulative side-chain overlay is based at its fork ancestor,
		// not necessarily at parent block number minus one.
		if parentHash != (types.Hash{}) {
			storedParentHash, err := a.resolveCanonicalStoredHash(tx, parentHash)
			if err != nil {
				return nil, err
			}
			if storedParentHash != (types.Hash{}) {
				parentState = nil
			}
		}
		if parentState != nil && parentState.hasBaseBlockNumber {
			validationRewindBlockNum = parentState.baseBlockNumber + 1
		}
		validationStateRebased, err = rewindEngineValidationState(tx, validationRewindBlockNum)
		if err != nil {
			return nil, err
		}
	}
	if !persist && parentState != nil {
		if err := applyEngineStateOverlayToTx(tx, parentState); err != nil {
			return nil, err
		}
	}
	if !persist && (validationStateRebased || parentState != nil) {
		if err := tx.ClearBucket(kv.TrieOfAccounts); err != nil {
			return nil, fmt.Errorf("clear validation account trie: %w", err)
		}
		if err := tx.ClearBucket(kv.TrieOfStorage); err != nil {
			return nil, fmt.Errorf("clear validation storage trie: %w", err)
		}
		if err := ethel.RebuildHashedState(tx); err != nil {
			return nil, fmt.Errorf("rebuild validation hashed state: %w", err)
		}
	}

	// Execute against the parent snapshot, i.e. the state at the start of this block.
	// This matches the stateful Engine payload builder path and preserves historical
	// account metadata such as code hashes reconstructed from change history.
	var reader state.StateReader
	var writer state.WriterWithChangeSets
	var revDiff *state.BlockRevDiff // non-nil only in journaled snapshot-direct mode
	// N42_NO_READCACHE (DIAGNOSTIC): bypass the cross-block hashed read cache so
	// every account/slot/code read hits the underlying hashed tables directly. Used
	// to A/B whether a stale cached read causes a divergence. Nil-cache reads are
	// safe (the reader's memoized-cache calls are nil-guarded → plain keccak +
	// always-miss; the TrieRootComputer's invalidations are simply not wired below).
	// Default (unset) = cache on, behavior unchanged. Hoisted to function scope so
	// both a.hashedCanonical blocks (reader setup + TrieRootComputer) can see it.
	noReadCache := os.Getenv("N42_NO_READCACHE") == "1"
	if a.hashedCanonical {
		// reth-2.2-style: read/execute against hashed state; the dirty hashed
		// account/storage leaves are persisted by the incremental
		// TrieRootComputer during IntermediateRoot. CODE is NOT part of the
		// trie, so it needs an explicit writer — newly deployed contracts and
		// EIP-7702 delegation designators must reach modules.Code, else a later
		// block CALLing them reads empty code and diverges (block-160 bug).
		hr := state.NewHashedStateReader(tx)
		if noReadCache {
			noReadCacheLogOnce.Do(func() {
				log.Warn("hashedReadCache disabled (N42_NO_READCACHE) — every read hits the underlying hashed tables directly. DIAGNOSTIC ONLY.")
			})
			hr.SetCache(nil)
		} else {
			if a.hashedReadCache == nil {
				// Cross-block read cache: hot accounts/slots/code hit the Go-heap
				// LRU instead of a keccak + MDBX cgocall on every access (~35% of
				// archive catch-up CPU was cursor_get_val). Write invalidation is
				// wired into the TrieRootComputer below.
				a.hashedReadCache = state.NewHashedReadCache()
			}
			hr.SetCache(a.hashedReadCache)
		}
		reader = hr
		if a.csFreezerSink != nil {
			writer = ethel.NewCSFreezerWriter(tx, blockNum, a.csFreezerSink)
		} else {
			writer = state.NewHashedCanonicalWriter(tx, blockNum)
		}
	} else if a.snapshotCold != nil {
		// snapshot-direct (minimal/full): warm MDBX delta overlaid on the
		// immutable H0 snapshot; tombstone-aware writer so SSTORE slot->0 does
		// not fall through to a stale snapshot value.
		wor := state.NewWarmOverlayReader(tx, a.snapshotCold)
		defer wor.Close() // release the block's cached warm cursors
		reader = wor
		if a.reorgJournal != nil && a.journalActive {
			// Capture this block's warm-table pre-images so a shallow reorg can
			// unwind it from memory (no on-disk changesets in minimal mode).
			revDiff = state.NewBlockRevDiff()
			writer = state.NewOverlayStateWriter(state.NewCapturingTx(tx, revDiff))
		} else {
			writer = state.NewOverlayStateWriter(tx)
		}
	} else {
		// Payload execution always starts from the transaction's current plain
		// state. Normal canonical execution has the parent at the DB tip; a
		// forkchoice reorg explicitly unwinds the DB to the common ancestor
		// first. A historical PlainState reader is incorrect after that unwind
		// because consumed orphan changesets no longer back its history index.
		reader = state.NewPlainStateReader(tx)
		if persist {
			writer = state.NewPlainStateWriter(tx, tx, blockNum)
		} else {
			writer = state.NewPlainStateWriterNoHistory(tx)
		}
	}
	ibs := state.New(reader)
	ibs.BeginWriteCodes()
	trc := ethel.SetupStateRootComputer(tx, ibs)
	if a.hashedCanonical {
		// Reuse the migrated TrieOfAccounts/TrieOfStorage: O(dirty) per block.
		trc.SetIncremental(true)
		// Skip wiring the read-cache invalidator when the cache is disabled
		// (N42_NO_READCACHE) — leaving t.readCache as an untyped nil so its
		// invalidation guards short-circuit; passing a typed-nil pointer would
		// make the guards fire on a nil receiver.
		if !noReadCache {
			trc.SetReadCache(a.hashedReadCache)
		}
		if a.staged {
			// Staged catch-up: write hashed state but defer the root + TrieOf*
			// to the per-sub-batch MerkleStageIncremental.
			trc.SetWriteOnly(true)
		}
	}
	// InitHashState populates HashedAccounts/HashedStorage from PlainState
	// for the FlatDBTrieLoader-based CalcStateRoot path. HPH itself uses
	// CommitmentBranches (see lib/commitment/persistent_context.go) and
	// reads Account/Storage by plain key, so it does NOT need this
	// pre-population. The catchup executor mirrors this: it only calls
	// InitHashState when VerifyInterval > 0 (see executor.go:314).
	//
	// In the wire path the one-time cost is catastrophic — 280M+ account
	// hashes for a 25M-state chain takes 30-60min and produces a ~14 GB
	// MDBX table we won't read. Skip it entirely on fastVerify; only the
	// legacy CL/test path that may fall through to VerifyStateRoot's
	// FlatDBTrieLoader sibling needs it. (VerifyStateRoot itself doesn't
	// use HashedAccounts — but other tests / paths that ARE exercised
	// from the same adapter might, so we keep the legacy behavior off
	// the wire path only.)
	// snapshot-direct (minimal) keeps no local trie — HashedAccounts/Storage are
	// never scanned for a root, so skip the (30-60min / ~14GB) PlainState→Hashed
	// init entirely. State is served from the warm overlay + snapshot cold tier.
	if !a.fastVerify && a.snapshotCold == nil {
		if err := ethel.InitHashState(tx); err != nil {
			return nil, err
		}
	}

	expected := captureExpectedExecutionPayloadOutputs(header)
	getHeader := func(_ types.Hash, number uint64) *block.Header {
		canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil || canonicalHash == (types.Hash{}) {
			return nil
		}
		return rawdb.ReadHeader(tx, canonicalHash, number)
	}
	blockHashFunc := internalcore.GetHashFn(header, getHeader)
	if a.headerHashReader != nil {
		// Freezer-direct BLOCKHASH (ethexec/witness-block-trace model): return the
		// stored canonical hash straight from headerc (h.Hash() = SetHash value),
		// and fall back to the MDBX canonical index for blocks the reader doesn't
		// hold (imported above the local freezer head). No GetHashFn ParentHash
		// walk, so a columnar-stripped / seed-less datadir resolves correctly.
		reader := a.headerHashReader
		blockHashFunc = func(m uint64) types.Hash {
			if h, herr := reader.ReadHeader(m); herr == nil && h != nil {
				return h.Hash()
			}
			ch, _ := rawdb.ReadCanonicalHash(tx, m)
			return ch
		}
	}

	gasPool := new(common.GasPool)
	gasPool.AddGas(blk.GasLimit())
	var usedGas uint64
	receipts := make(block.Receipts, 0, len(blk.Transactions()))
	if err := internalcore.ProcessExecutionBlockStart(parentBeaconRoot, a.chainCfg, ibs, header, a.engine); err != nil {
		return nil, err
	}
	gasDiag := os.Getenv("N42_GAS161") != ""
	if gasDiag {
		// N42_GAS161 downgrades gas/receipts/bloom mismatches to non-fatal so an
		// operator can push a block import past a known mismatch while debugging.
		// That is a fail-OPEN consensus bypass — warn loudly (once) so it can't be
		// silently left on in a production / validator node. Behaviour is kept
		// (diagnostic-continue) rather than flipped to fail-closed, which would
		// break that ad-hoc debugging use.
		gas161WarnOnce.Do(func() {
			log.Warn("N42_GAS161 SET — gas/receipts/bloom mismatches are NON-FATAL; this node will ACCEPT consensus-invalid blocks. DIAGNOSTIC ONLY — never set on a production or validator node.")
		})
	}
	execProf := os.Getenv("N42_EXECPROF") != ""
	var dEVM, dRoot, dCommit, dCS time.Duration
	tPhase := time.Now()
	// Phase 2: recover all tx senders concurrently before the serial apply loop.
	// ecrecover (~100µs/tx) otherwise runs inline inside AsMessage and dominates
	// execution on tx-dense blocks. SetFrom caches the result so AsMessage hits
	// the cache (message.go:106) instead of re-recovering.
	if txns := blk.Transactions(); len(txns) > 1 {
		a.recoverSenders(txns, transaction.MakeSignerWithTimestamp(a.chainCfg, header.Number.ToBig(), header.Time))
	}
	// EIP-7928 consume: if a full BAL is available out of band (fetched from a peer
	// via balPrefetch, or stored locally from a prior execution), verify it against
	// the header hash and prewarm state before executing so reads overlap execution.
	// A bad/mismatched BAL is skipped (execution proceeds without prefetch).
	if a.chainCfg.IsBAL(header.Time) {
		var raw []byte
		if a.balPrefetch != nil {
			raw = a.balPrefetch(blk.Hash())
		}
		if len(raw) == 0 {
			raw = rawdb.ReadBlockAccessList(tx, blk.Hash())
		}
		if len(raw) > 0 {
			if err := ethel.VerifyAndPrewarmBAL(reader, raw, header.BlockAccessListHash); err != nil {
				log.Debug("BAL prewarm skipped", "block", blockNum, "err", err)
			}
		}
	}

	// EIP-7928 (eth-el live path): harvest each tx's post-value writes so the block
	// access list hash can be verified against the header and the raw BAL stored for
	// the out-of-band service. Pure observer wrapping the per-tx noop; nil and
	// zero-overhead when the fork is off. The parallel EVM below is a discarded
	// shadow benchmark, so the serial path is the only one to capture.
	var balCap *internalcore.BALCapture
	if a.chainCfg.IsBAL(header.Time) {
		balCap = internalcore.NewBALCapture(state.NewNoopWriter())
	}
	for i, txn := range blk.Transactions() {
		ibs.Prepare(txn.Hash(), blk.Hash(), i)
		prevUsed := usedGas
		var perTxWriter state.StateWriter = state.NewNoopWriter()
		if balCap != nil {
			balCap.BeginTx(txn.Hash())
			perTxWriter = balCap
		}
		receipt, retData, err := internalcore.ApplyTransaction(a.chainCfg, blockHashFunc, a.engine, nil, gasPool, ibs, perTxWriter, header, txn, &usedGas, vm2.Config{})
		if err != nil {
			if os.Getenv("N42_TXERR") != "" {
				enc, _ := transaction.EncodeEthereumTransaction(txn)
				log.Warn("TXERR", "block", blockNum, "i", i, "type", txn.Type(), "decodedNonce", txn.Nonce(),
					"to", fmt.Sprintf("%v", txn.To()), "hash", txn.Hash().Hex(), "err", err,
					"raw", fmt.Sprintf("%x", enc))
			}
			return nil, err
		}
		if gasDiag {
			st := uint64(99)
			if receipt != nil {
				st = receipt.Status
			}
			log.Warn("GAS161 tx", "i", i, "type", txn.Type(), "txGas", usedGas-prevUsed,
				"cum", usedGas, "status", st, "to", fmt.Sprintf("%v", txn.To()), "dataLen", len(txn.Data()))
			if receipt != nil && receipt.Status == 0 {
				log.Warn("GAS161 REVERT", "i", i, "retLen", len(retData),
					"reason", decodeRevert(retData), "input", fmt.Sprintf("%x", txn.Data()))
			}
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
	}
	if balCap != nil {
		b := balCap.BALFor(internalcore.TxHashes(blk.Transactions()))
		got, gerr := b.Hash()
		if gerr != nil {
			return nil, fmt.Errorf("compute block access list hash: %w", gerr)
		}
		// Verify against the hash bound in the header (producer computed the same
		// BAL from the same execution); reject on mismatch. Then store the raw BAL so
		// the out-of-band BAL service can serve it. System-call writes are not yet
		// harvested; producer and verifier omit them identically.
		if want := header.BlockAccessListHash; want == nil || *want != got {
			return nil, fmt.Errorf("block access list hash mismatch: header %v, computed %s", want, got.Hex())
		}
		if raw, eerr := b.EncodeRLP(); eerr == nil {
			_ = rawdb.WriteBlockAccessList(tx, blk.Hash(), raw)
		}
	}
	dEVM = time.Since(tPhase)
	// N42_PEVM_BENCH: shadow-run the Block-STM parallel EVM on this block's txs and
	// time it against the serial dEVM. Results are DISCARDED (the serial path above
	// already produced the real state) — this only measures the parallel speedup on
	// our real 25M tx-dense workload before committing to the full integration.
	// Workers read a per-worker RoTx at the committed parent, so run with
	// N42_COMMIT_INTERVAL=1 (each block committed) for an un-stale read view.
	if a.hashedCanonical && os.Getenv("N42_PEVM_BENCH") != "" {
		if txns := blk.Transactions(); len(txns) > 1 {
			senders := make([]types.Address, len(txns))
			for i, t := range txns {
				if f := t.From(); f != nil {
					senders[i] = *f
				}
			}
			pevm := ethel.NewRealParallelEVM(a.chainCfg, a.engine, vm2.Config{}, header, blockHashFunc, nil)
			if os.Getenv("N42_PEVM_LAZYCB") != "" {
				pevm.SetSkipCoinbase(header.Coinbase)
			}
			outs := make([]state.TxOutput, len(txns))
			runner := state.ParallelTxRunner(pevm, txns, senders, &state.BlockContext{}, outs)
			factory := func() (state.MVBaseReader, func()) {
				rotx, err := a.db.BeginRo(context.Background())
				if err != nil {
					return nil, nil
				}
				return state.NewMVBaseFromStateReader(state.NewHashedStateReader(rotx)), func() { rotx.Rollback() }
			}
			workers := 8
			if v := os.Getenv("N42_PEVM_WORKERS"); v != "" {
				if n, e := strconv.Atoi(v); e == nil && n > 0 {
					workers = n
				}
			}
			tp := time.Now()
			_, mv, perr := state.ExecuteBlockParallelF(len(txns), workers, factory, runner)
			dPar := time.Since(tp)
			fails := 0
			var firstErr error
			firstErrTx := -1
			for i, o := range outs {
				if o.Err != nil {
					fails++
					if firstErr == nil {
						firstErr = o.Err
						firstErrTx = i
					}
				}
			}
			if firstErr != nil {
				log.Warn("PEVMERR", "block", blockNum, "tx", firstErrTx, "err", firstErr.Error())
			}
			// Correctness check: assemble receipts from the parallel BlockCommit and
			// compare receiptHash + total gas to the trusted wire header. Validates
			// FinalizeBlock + lazy-coinbase + receipt assembly on real blocks (no
			// commit). The receipt-root RLP needs only Type/Status/CumGas/Bloom/Logs.
			if mv != nil {
				if bc, ferr := state.FinalizeBlock(mv, outs, header.Coinbase); ferr == nil {
					pr := make([]*block.Receipt, 0, len(bc.Receipts))
					for _, tr := range bc.Receipts {
						lg := make([]*block.Log, 0, tr.LastLogIndex-tr.FirstLogIndex)
						for j := tr.FirstLogIndex; j < tr.LastLogIndex; j++ {
							sl := bc.Logs[j]
							lg = append(lg, &block.Log{Address: sl.Address, Topics: sl.Topics, Data: sl.Data})
						}
						r := &block.Receipt{Type: txns[tr.TxIdx].Type(), Status: uint64(tr.Status),
							CumulativeGasUsed: tr.CumulativeGasUsed, Logs: lg}
						r.Bloom = block.CreateBloom(block.Receipts{r})
						pr = append(pr, r)
					}
					prHash := ethel.EthReceiptHash(pr)
					log.Warn("PEVMCHK", "block", blockNum,
						"rcptMatch", prHash == header.ReceiptHash, "gasMatch", bc.GasUsed == usedGas,
						"pGas", bc.GasUsed, "sGas", usedGas, "fails", fails)
				}
			}
			speedup := 0.0
			if dPar > 0 {
				speedup = float64(dEVM) / float64(dPar)
			}
			log.Warn("PEVMBENCH", "block", blockNum, "ntx", len(txns), "workers", workers,
				"dEVMserial", dEVM.Round(time.Millisecond), "dParallel", dPar.Round(time.Millisecond),
				"speedup", fmt.Sprintf("%.2fx", speedup), "txFails", fails, "perr", perr)
		}
	}
	if usedGas != header.GasUsed {
		if gasDiag {
			log.Warn("GAS161 mismatch", "block", blockNum, "got", usedGas, "want", header.GasUsed, "diff", int64(header.GasUsed)-int64(usedGas))
		}
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("gas mismatch: got %d, want %d", usedGas, header.GasUsed),
		}, nil
	}
	if err := finalizeExecutionStateChanges(a.chainCfg, header, ibs); err != nil {
		return nil, err
	}
	applyExecutionWithdrawals(ibs, withdrawals)
	var actualRequests []hexutil.Bytes
	if actualRequests, err = internalcore.ProcessExecutionBlockEnd(receipts, a.chainCfg, ibs, header, a.engine); err != nil {
		return nil, err
	}
	rules := a.chainCfg.RulesWithTimestamp(blockNum, header.Time)
	if rules != nil && rules.IsPrague {
		actualHash := executionRequestsHash(actualRequests)
		// Pick the trust anchor for the requests hash. Three cases:
		//   1. CL-driven (NewPayloadV4): expectedRequests is the
		//      authoritative list from the beacon block — compare
		//      against its hash.
		//   2. Wire-driven (devp2p sync): the caller didn't pass a
		//      list, but the wire header carries header.RequestsHash
		//      — that's the anchor.
		//   3. Neither: pre-existing tests with toy blocks that have
		//      no requests anywhere. Hash empty list and compare.
		var expectedHash types.Hash
		switch {
		case expectedRequests != nil:
			expectedHash = executionRequestsHash(expectedRequests)
		case header.RequestsHash != nil:
			expectedHash = *header.RequestsHash
		default:
			expectedHash = executionRequestsHash(nil)
		}
		if actualHash != expectedHash {
			return &enginePayloadExecutionResult{
				validationError: fmt.Errorf("invalid requests hash"),
			}, nil
		}
		header.RequestsHash = &actualHash
	}
	actualReceiptHash := ethel.EthReceiptHash(receipts)
	actualBloom := block.CreateBloom(receipts)
	if blockNum == 25096160 {
		log.Warn("RCPT160", "computed", actualReceiptHash.Hex(), "header", header.ReceiptHash.Hex(),
			"match", actualReceiptHash == header.ReceiptHash, "nrcpt", len(receipts), "ntx", len(blk.Transactions()))
	}
	if expected != nil && actualReceiptHash != expected.receiptsRoot {
		if gasDiag {
			log.Warn("GAS161 receipts root mismatch", "block", blockNum,
				"computed", actualReceiptHash.Hex(), "expected", expected.receiptsRoot.Hex())
		}
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("receipts root mismatch"),
		}, nil
	}
	// Only the explicit trusted-columnar path may reconstruct a bloom omitted by
	// headerc. Peer and Engine API payloads must match even when they claim zero.
	if expected != nil && validateLogsBloom(actualBloom.Bytes(), expected.logsBloom, allowMissingExpectedBloom) != nil {
		if gasDiag {
			log.Warn("GAS161 logs bloom mismatch", "block", blockNum)
		}
		return &enginePayloadExecutionResult{
			validationError: fmt.Errorf("logs bloom mismatch"),
		}, nil
	}
	header.ReceiptHash = actualReceiptHash
	header.Bloom = actualBloom
	if os.Getenv("N42_DUMP_DIRTY") == "1" && os.Getenv("N42_DUMP_BLOCK") == fmt.Sprintf("%d", blockNum) {
		dumpStorage := os.Getenv("N42_DUMP_DIRTY_STORAGE") == "1"
		for _, addr := range ibs.DirtyAddresses() {
			bal := ibs.GetBalance(addr)
			non := ibs.GetNonce(addr)
			ch := ibs.GetCodeHash(addr)
			log.Warn("DUMPDIRTY", "block", blockNum, "addr", fmt.Sprintf("%x", addr[:]),
				"balance", bal.Hex(), "nonce", non, "codeHash", ch.Hex())
			if dumpStorage {
				for _, slot := range ibs.DirtyStorageSlots(addr) {
					var val uint256.Int
					ibs.GetState(addr, &slot, &val)
					log.Warn("DUMPDIRTYSTOR", "block", blockNum, "addr", fmt.Sprintf("%x", addr[:]),
						"slot", slot.Hex(), "value", val.Hex())
				}
			}
		}
	}
	if dumpAddrs := os.Getenv("N42_DUMP_ADDR"); dumpAddrs != "" && os.Getenv("N42_DUMP_BLOCK") == fmt.Sprintf("%d", blockNum) {
		for _, raw := range strings.Split(dumpAddrs, ",") {
			raw = strings.TrimSpace(strings.TrimPrefix(raw, "0x"))
			if len(raw) != 40 {
				continue
			}
			b, derr := hex.DecodeString(raw)
			if derr != nil {
				continue
			}
			var a types.Address
			copy(a[:], b)
			bal := ibs.GetBalance(a)
			non := ibs.GetNonce(a)
			ch := ibs.GetCodeHash(a)
			csz := ibs.GetCodeSize(a)
			log.Warn("DUMPSTATE", "block", blockNum, "addr", fmt.Sprintf("%x", a),
				"balance", bal.String(), "nonce", non, "codeHash", ch.Hex(), "codeSize", csz)
			for _, slotHex := range strings.Split(os.Getenv("N42_DUMP_SLOTS"), ",") {
				slotHex = strings.TrimSpace(strings.TrimPrefix(slotHex, "0x"))
				if len(slotHex) == 0 {
					continue
				}
				var slot types.Hash
				sb, _ := hex.DecodeString(strings.Repeat("0", 64-len(slotHex)) + slotHex)
				copy(slot[:], sb)
				var val uint256.Int
				ibs.GetState(a, &slot, &val)
				log.Warn("DUMPSLOT", "block", blockNum, "addr", fmt.Sprintf("%x", a[:4][:]),
					"slot", slot.Hex(), "value", val.Hex())
			}
		}
	}
	tPhase = time.Now()
	if a.staged {
		// Staged catch-up: IntermediateRoot still runs (writeOnly trc writes the
		// dirty hashed state in Phase 1/2) but returns zero (root deferred to the
		// per-sub-batch Merkle stage). Keep the trusted wire header.Root unchanged.
		ibs.IntermediateRoot()
	} else if a.snapshotCold != nil {
		// snapshot-direct (minimal): no local trie. Trust the wire header.Root
		// (the block was already validated by the network/CL); receipts/gas/bloom
		// were verified above. Skip the root compute entirely — keep header.Root.
	} else {
		header.Root = ibs.IntermediateRoot()
	}
	dRoot = time.Since(tPhase)
	blk.WithSeal(header)

	// Commit block state into the transaction, then verify the canonical
	// Ethereum MPT root before exposing any changes to the database.
	tPhase = time.Now()
	if err := ibs.CommitBlock(rules, writer); err != nil {
		return nil, err
	}
	dCommit = time.Since(tPhase)
	tPhase = time.Now()
	if err := writer.WriteChangeSets(); err != nil {
		return nil, err
	}
	if err := writer.WriteHistory(); err != nil {
		return nil, err
	}
	dCS = time.Since(tPhase)
	if execProf {
		log.Warn("EXECPROF", "block", blockNum, "ntx", len(blk.Transactions()),
			"dEVM", dEVM.Round(time.Millisecond), "dRoot", dRoot.Round(time.Millisecond),
			"dCommit", dCommit.Round(time.Millisecond), "dCS", dCS.Round(time.Millisecond))
	}

	var computedRoot types.Hash
	if a.staged {
		// Staged catch-up: root not computed per block (writeOnly). Trust the wire
		// header.Root; commitment.MerkleStageIncremental verifies the whole sub-batch.
		computedRoot = header.Root
	} else if a.snapshotCold != nil {
		// snapshot-direct (minimal): trust the wire root — there is no local trie
		// to verify against. Execution correctness is still bounded by the
		// receipts-root / gas / logs-bloom checks above and the E2E-verified
		// snapshot cold values feeding the warm overlay reader.
		computedRoot = header.Root
	} else if a.fastVerify {
		// Wire-driven sync path (ExecutePayloadFromWire). Trust the HPH
		// IntermediateRoot we already computed above — that's the same
		// incremental algorithm the catchup executor uses at ~3000 blk/s
		// for 25M-state chains. The full-MPT-rebuild VerifyStateRoot
		// allocates ~30 GB transient at 25M state and times out before
		// the per-block CommitBlock finishes (observed 10+ min per block
		// on 2026-05-24). HPH is correct as long as its commitment
		// state is warm from prior catchup activity.
		computedRoot = header.Root
	} else {
		var verifyErr error
		computedRoot, verifyErr = ethel.VerifyStateRoot(tx)
		if verifyErr != nil {
			return nil, fmt.Errorf("verify state root: %w", verifyErr)
		}
	}
	if expected != nil && computedRoot != expected.stateRoot {
		var overlayBase uint64
		var overlayHasBase bool
		var overlayAccounts, overlayStorage int
		if overlayParentState != nil {
			overlayBase = overlayParentState.baseBlockNumber
			overlayHasBase = overlayParentState.hasBaseBlockNumber
			overlayAccounts = len(overlayParentState.accounts)
			overlayStorage = len(overlayParentState.storage)
		}
		log.Error("State root mismatch", "block", blockNum,
			"computed", computedRoot.Hex(), "expected", expected.stateRoot.Hex(),
			"fastVerify", a.fastVerify, "hashedCanonical", a.hashedCanonical,
			"envFULLROOT", os.Getenv("N42_FULLROOT"),
			"parentOverlay", overlayParentState != nil,
			"executionOverlay", parentState != nil,
			"overlayBase", overlayBase,
			"overlayHasBase", overlayHasBase,
			"overlayAccounts", overlayAccounts,
			"overlayStorage", overlayStorage,
			"rewindBlock", validationRewindBlockNum,
			"rewound", validationStateRebased)
		if a.hashedCanonical && os.Getenv("N42_FULLROOT") == "1" {
			// Diagnostic: recompute the root by FULL descent from the
			// post-block HashedAccounts/HashedStorage leaves (retain
			// everything → ignore cached TrieOf*). If this equals the
			// expected root, execution is correct and only the incremental
			// path is wrong; otherwise execution produced wrong state.
			rl := trie.NewRetainList(1 << 30)
			ldr := trie.NewFlatDBTrieLoader("dbg-fullroot", rl, nil, nil, false)
			if fr, ferr := ldr.CalcTrieRoot(tx, nil); ferr == nil {
				log.Error("DEBUG full-rebuild root", "block", blockNum,
					"incremental", computedRoot.Hex(), "full", fr.Hex(),
					"expected", expected.stateRoot.Hex())
			} else {
				log.Error("DEBUG full-rebuild failed", "err", ferr)
			}
		}
		return &enginePayloadExecutionResult{
			stateRoot:       computedRoot,
			validationError: fmt.Errorf("state root mismatch"),
		}, nil
	}
	header.Root = computedRoot
	blk.WithSeal(header)
	accounts, storage := ibs.DirtyAccountData()
	stateOverlay := mergeEngineStateOverlay(overlayParentState, blockNum-1, accounts, storage, ibs.CodeHashes())

	if persist && os.Getenv("N42_NOCOMMIT") == "1" {
		log.Warn("NOCOMMIT: root MATCHED, rolling back (no persist)", "block", blockNum, "root", computedRoot.Hex())
		return &enginePayloadExecutionResult{stateRoot: computedRoot,
			validationError: fmt.Errorf("nocommit test")}, nil
	}
	if !persist {
		return &enginePayloadExecutionResult{
			stateRoot:    computedRoot,
			receipts:     receipts,
			stateOverlay: stateOverlay,
		}, nil
	}

	// Persist the executed payload so subsequent head/header lookups can
	// resolve Engine eth-compatible hashes back to the underlying block.
	storedHash, err := writeEnginePayloadBlock(tx, blk)
	if err != nil {
		return nil, err
	}
	if err := rawdb.WriteCanonicalHash(tx, storedHash, blockNum); err != nil {
		return nil, err
	}
	// Journal this block's reverse-diff (pending until the batch commits) so a
	// later shallow reorg can unwind it. storedHash is what canonical(blockNum)
	// now resolves to; header.ParentHash links to blockNum-1.
	if revDiff != nil {
		a.reorgJournal.addPending(blockNum, storedHash, header.ParentHash, revDiff)
	}

	// Standalone: commit our own tx. Batch: leave it to the caller, who
	// writes the head into the SAME tx and commits the batch atomically.
	if ownTx {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}

	return &enginePayloadExecutionResult{stateRoot: computedRoot, receipts: receipts, stateOverlay: stateOverlay}, nil
}

// rewindEngineValidationState restores the persisted canonical state to the
// payload parent's height inside the caller's rollback-only transaction. A CL
// may submit a side chain whose parent is several blocks behind the current
// head; applying the staged side-chain overlay directly on top of tip state
// would retain changes made after the fork point and invalidate execution.
func rewindEngineValidationState(tx kv.RwTx, blockNum uint64) (bool, error) {
	if tx == nil || blockNum == 0 {
		return false, nil
	}
	var headNum uint64
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash != (types.Hash{}) {
		if number := rawdb.ReadHeaderNumber(tx, headHash); number != nil {
			headNum = *number
		}
	}
	// Peer catch-up commits state and ethel-last-block atomically, but its
	// rawdb head pointer can lag until Engine forkchoice adopts that chain.
	// Validation must unwind from the state that is actually in PlainState,
	// otherwise a sibling branch is executed on top of the peer branch's state.
	if progress := ethel.ReadProgress(tx); progress > headNum {
		headNum = progress
	}
	parentNum := blockNum - 1
	if headNum <= parentNum {
		return false, nil
	}
	for number := headNum; number > parentNum; number-- {
		if err := commitment.UnwindPlainStateBlock(tx, number); err != nil {
			return false, fmt.Errorf("rewind validation state at block %d: %w", number, err)
		}
	}
	return true, nil
}

// canonicalBlockNumber reports whether engineHash is currently indexed on the
// persisted canonical chain. Payload hashes can differ from their stored N42
// header hashes, so the lookup resolves both representations before checking
// the number-to-hash canonical mapping.
func (a *EngineStateAdapter) canonicalBlockNumber(engineHash types.Hash) (uint64, bool, error) {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	storedHash, err := a.resolveCanonicalStoredHash(tx, engineHash)
	if err != nil || storedHash == (types.Hash{}) {
		return 0, false, err
	}
	number := rawdb.ReadHeaderNumber(tx, storedHash)
	if number == nil {
		return 0, false, nil
	}
	canonicalHash, err := rawdb.ReadCanonicalHash(tx, *number)
	if err != nil {
		return 0, false, err
	}
	return *number, canonicalHash == storedHash, nil
}

// reorgCanonicalTo unwinds the persisted world state and canonical index to a
// known ancestor. The caller can then execute a previously validated staged
// branch forward from exactly that ancestor.
func (a *EngineStateAdapter) reorgCanonicalTo(engineHash types.Hash) error {
	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	storedHash, err := a.resolveCanonicalStoredHash(tx, engineHash)
	if err != nil {
		return err
	}
	if storedHash == (types.Hash{}) {
		return fmt.Errorf("reorg ancestor not found: %s", engineHash.Hex())
	}
	targetNum := rawdb.ReadHeaderNumber(tx, storedHash)
	if targetNum == nil {
		return fmt.Errorf("reorg ancestor number not found: %s", engineHash.Hex())
	}
	canonicalHash, err := rawdb.ReadCanonicalHash(tx, *targetNum)
	if err != nil {
		return err
	}
	if canonicalHash != storedHash {
		return fmt.Errorf("reorg target is not canonical: %s", engineHash.Hex())
	}

	currentHash := rawdb.ReadHeadBlockHash(tx)
	currentNum := rawdb.ReadHeaderNumber(tx, currentHash)
	if currentNum == nil {
		return fmt.Errorf("current canonical head number not found: %s", currentHash.Hex())
	}
	if *targetNum > *currentNum {
		return fmt.Errorf("cannot reorg forward from block %d to %d", *currentNum, *targetNum)
	}
	for number := *currentNum; number > *targetNum; number-- {
		if err := commitment.UnwindPlainStateBlock(tx, number); err != nil {
			return fmt.Errorf("unwind canonical state at block %d: %w", number, err)
		}
	}
	if err := rawdb.TruncateCanonicalHash(tx, *targetNum+1, false); err != nil {
		return err
	}
	if err := tx.ClearBucket(kv.TrieOfAccounts); err != nil {
		return fmt.Errorf("clear account trie after reorg: %w", err)
	}
	if err := tx.ClearBucket(kv.TrieOfStorage); err != nil {
		return fmt.Errorf("clear storage trie after reorg: %w", err)
	}
	if err := ethel.RebuildHashedState(tx); err != nil {
		return fmt.Errorf("rebuild hashed state after reorg: %w", err)
	}
	rawdb.WriteHeadBlockHash(tx, storedHash)
	if err := rawdb.WriteHeadHeaderHash(tx, storedHash); err != nil {
		return err
	}
	if a.trackHeadMarker {
		if err := ethel.WriteHeadMarker(tx, *targetNum); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.hashedReadCache.PurgeAll()
	return nil
}

// ForkchoiceUpdated updates the canonical chain head.
func (a *EngineStateAdapter) ForkchoiceUpdated(headHash, safeHash, finalizedHash types.Hash) error {
	tx, err := a.db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	storedHeadHash, err := a.resolveCanonicalStoredHash(tx, headHash)
	if err != nil {
		return err
	}
	if storedHeadHash == (types.Hash{}) {
		return fmt.Errorf("head block not found: %s", headHash.Hex())
	}
	if safeHash != (types.Hash{}) {
		if _, err := a.resolveCanonicalStoredHash(tx, safeHash); err != nil {
			return err
		}
	}
	if finalizedHash != (types.Hash{}) {
		if _, err := a.resolveCanonicalStoredHash(tx, finalizedHash); err != nil {
			return err
		}
	}

	rawdb.WriteHeadBlockHash(tx, storedHeadHash)
	if err := rawdb.WriteHeadHeaderHash(tx, storedHeadHash); err != nil {
		return err
	}
	headerNum := rawdb.ReadHeaderNumber(tx, storedHeadHash)
	if a.trackHeadMarker && headerNum != nil {
		if err := ethel.WriteHeadMarker(tx, *headerNum); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	if headerNum != nil {
		log.Info("Forkchoice updated", "head", headHash.Hex()[:10], "block", *headerNum)
	}
	return nil
}

// Reorg rolls back state to the given block number using changesets.
func (a *EngineStateAdapter) Reorg(targetBlock uint64) error {
	// Changeset unwind rewrites hashed state outside the TrieRootComputer's
	// invalidation hooks — drop the whole read cache.
	a.hashedReadCache.PurgeAll()
	if a.csSource != nil {
		return ethel.ReorgWithSource(a.db, a.csSource, targetBlock)
	}
	return ethel.Reorg(a.db, a.freezer, targetBlock)
}

// CurrentHead returns the current chain tip block as recorded in chaindata.
// It is used by the EngineAPIV1 nil-bc fallback so that
// engine_forkchoiceUpdated calls report VALID (not SYNCING) once the
// EthEL adapter has executed up to the head. Returns nil when chaindata
// has no head pointer yet.
func (a *EngineStateAdapter) CurrentHead() *block.Block {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return nil
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err != nil || blk == nil {
		return nil
	}
	return blk
}

// CurrentHeadHash returns the current head block hash without materialising
// the body. Cheaper than CurrentHead when the caller only needs the hash.
func (a *EngineStateAdapter) CurrentHeadHash() types.Hash {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return types.Hash{}
	}
	defer tx.Rollback()
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return types.Hash{}
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err == nil && blk != nil {
		return ethCompatibleBlockHash(blk, a.chainCfg)
	}
	hdr, err := rawdb.ReadHeaderByHash(tx, headHash)
	if err != nil || hdr == nil {
		return types.Hash{}
	}
	return ethCompatibleHeaderHash(hdr, a.chainCfg)
}

// HeaderByHash returns the EL block header for the given hash, or nil
// if chaindata does not have it. Used by the EngineAPIV1 nil-bc
// fallback path for parentHeader lookups: without it, the EL side of
// NewPayload cannot resolve the parent when the CL asks whether a
// received payload extends the current head.
func (a *EngineStateAdapter) HeaderByHash(hash types.Hash) *block.Header {
	tx, err := a.db.BeginRo(context.Background())
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	hdr, err := rawdb.ReadHeaderByHash(tx, hash)
	if err == nil && hdr != nil {
		return hdr
	}
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		return nil
	}
	blk, err := rawdb.ReadBlockByHash(tx, headHash)
	if err != nil || blk == nil {
		return nil
	}
	if ethCompatibleBlockHash(blk, a.chainCfg) != hash {
		return nil
	}
	headHdr, ok := blk.Header().(*block.Header)
	if !ok {
		return nil
	}
	return headHdr
}

func (a *EngineStateAdapter) resolveCanonicalStoredHash(tx kv.Tx, engineHash types.Hash) (types.Hash, error) {
	if engineHash == (types.Hash{}) {
		return types.Hash{}, nil
	}
	if hdr, err := rawdb.ReadHeaderByHash(tx, engineHash); err == nil && hdr != nil {
		number := uint64FromUint256OrZero(hdr.Number)
		canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return types.Hash{}, err
		}
		if canonicalHash == engineHash {
			return engineHash, nil
		}
	}
	for number := uint64(0); ; number++ {
		storedHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return types.Hash{}, err
		}
		if storedHash == (types.Hash{}) {
			return types.Hash{}, nil
		}
		blk, err := rawdb.ReadBlockByHash(tx, storedHash)
		if err != nil {
			return types.Hash{}, err
		}
		if blk != nil && ethCompatibleBlockHash(blk, a.chainCfg) == engineHash {
			return storedHash, nil
		}
		hdr, err := rawdb.ReadHeaderByHash(tx, storedHash)
		if err != nil {
			return types.Hash{}, err
		}
		if hdr != nil && ethCompatibleHeaderHash(hdr, a.chainCfg) == engineHash {
			return storedHash, nil
		}
	}
}

func writeEnginePayloadBlock(tx kv.RwTx, blk *block.Block) (types.Hash, error) {
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return types.Hash{}, fmt.Errorf("unexpected header type")
	}
	hash := header.Hash()
	rawBody := &block.RawBody{Transactions: make([][]byte, 0, len(blk.Transactions()))}
	for _, txn := range blk.Transactions() {
		encoded, err := transaction.EncodeEthereumTransaction(txn)
		if err != nil {
			return types.Hash{}, err
		}
		rawBody.Transactions = append(rawBody.Transactions, encoded)
	}
	if _, _, err := rawdb.WriteRawBody(tx, hash, header.Number.Uint64(), rawBody); err != nil {
		return types.Hash{}, err
	}
	rawdb.WriteHeader(tx, header)
	return hash, nil
}
