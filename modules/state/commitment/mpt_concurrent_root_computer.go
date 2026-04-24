// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mpt_concurrent_root_computer.go — 16-way concurrent variant of
// MPTRootComputer using libcommit.ConcurrentPatriciaHashed.
//
// Architecture mirrors erigon's SharedDomainsCommitmentContext parallel
// path. Sequential MPTRootComputer is kept unchanged; this type is an
// opt-in alternative with identical ComputeRoot semantics but parallel
// internal processing.
//
// ComputeRoot flow (concurrent):
//
//   1. Build Updates (same as sequential; shared hasher).
//   2. updates.SetConcurrentCommitment(true) tells ParallelHashSort to
//      partition keys by their top hex nibble into 16 per-worker streams.
//   3. ConcurrentPatriciaHashed.Process dispatches to ParallelHashSort,
//      which invokes the TrieContextFactory once per worker to get a
//      private PatriciaContext (own RoTx + own etl.Collector).
//   4. Workers process their nibble's keys with NO shared-state writes:
//      PutBranch goes to per-worker collector.
//   5. When all 16 workers finish their subtries, the root trie folds
//      sequentially. root hash returned.
//   6. drainCollectors() returns every worker's collector; main thread
//      replays them through the sequential mptContext's PutBranch to
//      merge into the shared mem store. This preserves the invariant
//      that all branch writes are visible to subsequent ComputeRoot
//      calls.
//
// Fallback: if ConcurrentPatriciaHashed.CanDoConcurrentNext() returns
// false (e.g. root has an extension making nibble partition useless),
// Process internally falls back to the sequential HexPatriciaHashed.
// Updates.SetConcurrentCommitment(false) is then set for next block.

package commitment

import (
	"context"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// ConcurrentMPTRootComputer is the 16-way parallel variant of
// MPTRootComputer. ComputeRoot dispatches to ParallelHashSort via
// ConcurrentPatriciaHashed, using per-worker PatriciaContexts (each
// with its own MDBX RoTx).
type ConcurrentMPTRootComputer struct {
	// Shared (goroutine-safe) stores.
	mem     *memBranchStore  // mutex-protected
	persist *mdbxBranchStore // may be nil for non-persistent mode

	// Sequential fallback reader (used when CanDoConcurrentNext==false).
	// For parallel mode, each worker gets its own clone via the factory.
	reader *PlainStateMPTReader

	// Sequential "root context" used for:
	//   - fall-through PutBranch during the root fold (post-worker phase)
	//   - merging drained collectors after ParallelHashSort
	//   - sequential fallback path
	// Its collector field is ALWAYS nil — root-phase writes go to mem.
	rootCtx *mptContext

	// Concurrent trie: root HPH + 16 mounted subtries.
	ctrie *libcommit.ConcurrentPatriciaHashed

	updates *libcommit.Updates
	hasher  func([]byte) []byte

	// Factory dependencies.
	db     kv.RoDB
	tmpdir string
	logger log2.Logger
}

// NewConcurrentMPTRootComputer constructs a concurrent variant.
// db is required (for per-worker BeginRo); tmpdir and logger are for
// etl.Collector instances.
func NewConcurrentMPTRootComputer(db kv.RoDB, tmpdir string, logger log2.Logger) *ConcurrentMPTRootComputer {
	return newConcurrentMPTRootComputer(nil, db, tmpdir, logger)
}

// NewPersistentConcurrentMPTRootComputer is the persistent variant.
func NewPersistentConcurrentMPTRootComputer(db kv.RoDB, tmpdir string, logger log2.Logger) *ConcurrentMPTRootComputer {
	persist := newMDBXBranchStore(modules.MPTBranch)
	return newConcurrentMPTRootComputer(persist, db, tmpdir, logger)
}

func newConcurrentMPTRootComputer(persist *mdbxBranchStore, db kv.RoDB, tmpdir string, logger log2.Logger) *ConcurrentMPTRootComputer {
	mem := newMemBranchStore()
	rootCtx := &mptContext{mem: mem, persist: persist}
	root := libcommit.NewHexPatriciaHashed(int16(length.Addr), rootCtx)
	m := &ConcurrentMPTRootComputer{
		mem:     mem,
		persist: persist,
		rootCtx: rootCtx,
		ctrie:   libcommit.NewConcurrentPatriciaHashed(root, rootCtx),
		db:      db,
		tmpdir:  tmpdir,
		logger:  logger,
		hasher:  makeSharedHasher(),
	}
	return m
}

// SetStateReader sets the reader used on the sequential fallback path
// and as the template for per-worker reader clones. Must be called
// before ComputeRoot.
func (m *ConcurrentMPTRootComputer) SetStateReader(r *PlainStateMPTReader) {
	m.reader = r
	m.rootCtx.reader = r
}

// SetReadTx wires the read tx into the persistent branch store (used
// for fall-through reads that miss mem).
func (m *ConcurrentMPTRootComputer) SetReadTx(tx kv.Tx) {
	if m.persist != nil {
		m.persist.SetReadTx(tx)
	}
}

// Trie returns the underlying ConcurrentPatriciaHashed for callers
// that need to inspect variant/state.
func (m *ConcurrentMPTRootComputer) Trie() *libcommit.ConcurrentPatriciaHashed {
	return m.ctrie
}

// BranchCount reports in-memory branch store size (diagnostics only).
func (m *ConcurrentMPTRootComputer) BranchCount() int {
	return m.mem.Len()
}

// ResetTrie clears the trie (root + all mounted subtries).
func (m *ConcurrentMPTRootComputer) ResetTrie() {
	m.ctrie.Reset()
}

// RootScheme identifies this computer as the Ethereum MPT.
func (m *ConcurrentMPTRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeEthereumMPT
}

// FlushBranches persists all in-memory branches to MPTBranch table.
// Caller typically invokes at commit boundary. Identical semantics to
// MPTRootComputer.FlushBranches.
func (m *ConcurrentMPTRootComputer) FlushBranches(tx kv.RwTx) error {
	m.mem.mu.RLock()
	defer m.mem.mu.RUnlock()
	for k, v := range m.mem.data {
		if len(v) == 0 {
			if err := tx.Delete(modules.MPTBranch, []byte(k)); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.MPTBranch, []byte(k), v); err != nil {
				return err
			}
		}
	}
	return nil
}

// EncodeTrieState returns the root trie's serialized state.
func (m *ConcurrentMPTRootComputer) EncodeTrieState() ([]byte, error) {
	return m.ctrie.RootTrie().EncodeCurrentState(nil)
}

// RestoreTrieState loads previously-serialized state into the root trie.
func (m *ConcurrentMPTRootComputer) RestoreTrieState(trieState []byte) error {
	if len(trieState) == 0 {
		return nil
	}
	return m.ctrie.RootTrie().SetState(trieState)
}

// SaveCheckpoint flushes branches + writes root + serialized trie state.
func (m *ConcurrentMPTRootComputer) SaveCheckpoint(tx kv.RwTx, blockNum uint64, root types.Hash) error {
	if err := m.FlushBranches(tx); err != nil {
		return fmt.Errorf("flush branches: %w", err)
	}
	if err := WriteMPTRoot(tx, root); err != nil {
		return fmt.Errorf("write root: %w", err)
	}
	trieState, err := m.EncodeTrieState()
	if err != nil {
		return fmt.Errorf("encode trie state: %w", err)
	}
	if err := WriteMPTTrieState(tx, blockNum, trieState); err != nil {
		return fmt.Errorf("write trie state: %w", err)
	}
	return nil
}

// ComputeRoot builds Updates + dispatches to ConcurrentPatriciaHashed.Process.
// Updates must be constructed with ModeDirect + sortPerNibble enabled
// (ParallelHashSort requires both).
func (m *ConcurrentMPTRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	if m.reader == nil {
		return types.Hash{}, fmt.Errorf("ConcurrentMPTRootComputer: reader not set")
	}
	if m.db == nil {
		return types.Hash{}, fmt.Errorf("ConcurrentMPTRootComputer: db not set")
	}

	// ModeDirect + sortPerNibble: required for ParallelHashSort. The
	// non-concurrent fallback inside Process handles CanDoConcurrentNext
	// false gracefully.
	if m.updates == nil {
		m.updates = libcommit.NewUpdates(libcommit.ModeDirect, m.tmpdir, m.hasher)
		m.updates.SetConcurrentCommitment(true)
		// ConcurrentPatriciaHashed.Process calls CanDoConcurrentNext after
		// each run; if the root shape is unsuitable (e.g. root has an
		// extension, early chain), it flips this back to false for the
		// next invocation and we run sequentially.
	} else {
		m.updates.Reset()
	}

	// Enqueue account + storage touches.
	for addr, acct := range accounts {
		pk := string(addr.Bytes())
		if acct == nil || isAccountEmpty(acct) {
			m.updates.TouchPlainKey(pk, nil, m.updates.TouchAccount)
			continue
		}
		m.updates.TouchPlainKey(pk, acct.MarshalV2(), m.updates.TouchAccount)
	}
	for addr, slots := range storage {
		for slot, val := range slots {
			var pk [52]byte
			copy(pk[:20], addr[:])
			copy(pk[20:], slot[:])
			if val == nil || val.IsZero() {
				m.updates.TouchPlainKey(string(pk[:]), nil, m.updates.TouchStorage)
				continue
			}
			b := val.Bytes32()
			start := 0
			for start < 31 && b[start] == 0 {
				start++
			}
			m.updates.TouchPlainKey(string(pk[:]), b[start:], m.updates.TouchStorage)
		}
	}

	// Fast path: no updates → just return current root hash.
	if m.updates.Size() == 0 {
		rh, err := m.ctrie.RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		var h types.Hash
		copy(h[:], rh)
		return h, nil
	}

	// Build per-worker factory + drain.
	ctx := context.Background()
	factory, drain := newConcurrentMPTContextFactory(ctx, m.db, m.mem, m.persist, m.reader, m.tmpdir, m.logger)
	warmupCfg := libcommit.WarmupConfig{
		Enabled:    false, // warmup disabled until we validate correctness
		CtxFactory: factory,
		NumWorkers: 16,
		MaxDepth:   libcommit.WarmupMaxDepth,
		LogPrefix:  "mpt-concurrent",
	}

	rootBytes, err := m.ctrie.Process(ctx, m.updates, "", nil, warmupCfg)

	// Always drain collectors (success or error) to avoid resource leaks.
	collectors := drain()
	defer func() {
		for _, c := range collectors {
			if c != nil {
				c.Close()
			}
		}
	}()

	if err != nil {
		return types.Hash{}, fmt.Errorf("concurrent MPT process: %w", err)
	}

	// Merge each per-worker collector's entries into the shared mem store.
	// This runs on the main goroutine AFTER all workers have finished,
	// so no concurrency issue.
	for _, c := range collectors {
		if c == nil {
			continue
		}
		if loadErr := c.Load(nil, "", mergeCollectorIntoMemFn(m.mem), etl.TransformArgs{}); loadErr != nil {
			return types.Hash{}, fmt.Errorf("merge collector: %w", loadErr)
		}
	}

	var h types.Hash
	copy(h[:], rootBytes)
	return h, nil
}

// mergeCollectorIntoMemFn returns an etl.LoadFunc that copies each
// collector entry into the shared mem branch store. Empty values ==
// delete.
func mergeCollectorIntoMemFn(mem *memBranchStore) etl.LoadFunc {
	return func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		if len(v) == 0 {
			mem.Put(k, nil)
		} else {
			mem.Put(k, v)
		}
		return nil
	}
}
