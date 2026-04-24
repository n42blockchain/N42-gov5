// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mpt_concurrent_context.go — per-worker PatriciaContext factory for
// ConcurrentPatriciaHashed.
//
// Mirrors erigon's execution/commitment/commitmentdb/commitment_context.go
// `concurrentTrieContextFactory`. Each call to the returned factory:
//
//   1. Opens a fresh MDBX RoTx from the supplied DB.
//   2. Builds a per-worker PlainStateMPTReader over that RoTx.
//   3. Builds a per-worker mdbxBranchStore clone over that RoTx.
//   4. Builds a per-worker etl.Collector so PutBranch writes go to a
//      goroutine-private sortable buffer (tmp-file backed) instead of
//      racing on the shared memBranchStore.
//   5. Returns the new mptContext + a closer that rolls back the RoTx.
//
// After ParallelHashSort completes, the caller invokes `drain()` to
// fetch all collectors produced during the parallel phase, sort-merges
// their contents, and applies them to the real shared store via the
// sequential root context — yielding the same end-state as if every
// PutBranch had gone through the root context directly, without any
// goroutine race on the store internals.

package commitment

import (
	"context"
	"sync"

	"github.com/c2h5oh/datasize"

	libcommit "github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// concurrentMPTCollector wraps an etl.Collector with the address it was
// created for, so drain() callers know which worker produced each batch
// if needed for diagnostics. The actual sort-merge on drain concatenates
// all collectors' contents.
type concurrentMPTCollector struct {
	c *etl.Collector
}

// newConcurrentMPTContextFactory returns a TrieContextFactory (+ drain
// function) suitable for passing into ConcurrentPatriciaHashed.Process
// via WarmupConfig.CtxFactory.
//
// Parameters:
//   - ctx: lifetime context; each factory call uses this as its RoTx ctx.
//   - db:  MDBX RoDB used to open per-worker RoTxs.
//   - mem: shared in-memory branch store (read by workers; writes go to
//          the per-worker collector). Must be goroutine-safe — the
//          updated memBranchStore with sync.RWMutex satisfies this.
//   - persist: optional persistent branch store used for fall-through
//              reads when mem misses. May be nil (non-persistent mode).
//   - tmpdir: directory for etl.Collector temp files.
//   - logger: for etl.Collector progress logs.
//
// The caller typically invokes drain() AFTER ParallelHashSort returns
// (success OR failure) to release collector resources. On success the
// caller iterates drained collectors and replays their entries through
// the sequential mptContext's PutBranch to merge into mem (and persist
// if non-nil).
func newConcurrentMPTContextFactory(
	ctx context.Context,
	db kv.RoDB,
	mem *memBranchStore,
	persist *mdbxBranchStore,
	reader *PlainStateMPTReader,
	tmpdir string,
	logger log2.Logger,
) (libcommit.TrieContextFactory, func() []*etl.Collector) {
	var mu sync.Mutex
	var collectors []*etl.Collector

	factory := func() (libcommit.PatriciaContext, func()) {
		roTx, err := db.BeginRo(ctx)
		if err != nil {
			// Return an error-context so downstream calls fail cleanly
			// rather than deref-panicking on a nil reader.
			return &errorMPTContext{err: err}, func() {}
		}

		// Per-worker collector. Buffer is small (16 MB) — we expect
		// ~1-2 MB of branch data per nibble subtrie at 15M state scale.
		// tmp-file backed; no in-memory race with other workers.
		collector := etl.NewCollector(
			"[mpt-concurrent]", tmpdir,
			etl.NewSortableBuffer(16*datasize.MB),
			logger,
		)
		collector.LogLvl(log2.LvlDebug)

		mu.Lock()
		collectors = append(collectors, collector)
		mu.Unlock()

		// Per-worker reader + branch store bound to this RoTx.
		workerReader := reader.Clone(roTx)
		workerPersist := persist.cloneWithTx(roTx)

		workerCtx := &mptContext{
			mem:       mem, // shared, goroutine-safe (sync.RWMutex)
			persist:   workerPersist,
			reader:    workerReader,
			collector: collector,
		}

		cleanup := func() {
			roTx.Rollback()
		}
		return workerCtx, cleanup
	}

	drain := func() []*etl.Collector {
		mu.Lock()
		defer mu.Unlock()
		out := collectors
		collectors = nil
		return out
	}

	return factory, drain
}

// errorMPTContext is returned by the factory when BeginRo fails, so the
// ConcurrentPatriciaHashed worker gets a clean error on its first state
// access rather than a nil-deref panic. Mirrors erigon's
// errorTrieContext.
type errorMPTContext struct {
	err error
}

func (e *errorMPTContext) Branch(_ []byte) ([]byte, kv.Step, error) {
	return nil, 0, e.err
}

func (e *errorMPTContext) PutBranch(_ []byte, _ []byte, _ []byte) error {
	return e.err
}

func (e *errorMPTContext) Account(_ []byte) (*libcommit.Update, error) {
	return nil, e.err
}

func (e *errorMPTContext) Storage(_ []byte) (*libcommit.Update, error) {
	return nil, e.err
}

func (e *errorMPTContext) TxNum() uint64 { return 0 }

// Compile-time check that errorMPTContext satisfies the interface.
var _ libcommit.PatriciaContext = (*errorMPTContext)(nil)
