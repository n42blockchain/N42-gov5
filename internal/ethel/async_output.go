// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// async_output.go — background changeset encoder + freezer writer.
//
// Changeset encoding (EncodeAccountChangesV1 / EncodeStorageChangesV1) and
// the subsequent zstd compression + disk write dominate per-block P99
// latency (~16ms). This module moves that work to a dedicated background
// goroutine so the main executor can start EVM execution of the next
// block immediately after snapshotting the new values.
//
// Safety invariant: the new-value snapshot is captured SYNCHRONOUSLY on
// the main goroutine (before the next block touches stateBuf), so the
// background goroutine never accesses shared mutable state.
//
// Dict interning: dictionary id allocation also happens SYNCHRONOUSLY on
// the main goroutine via BufferedDictWriter — id maps are frozen into the
// pendingOutput so the async encoder needs only an in-memory lookup. New
// dict assignments are persisted to MDBX at commit-interval flush time
// (BufferedDictWriter.Flush), which is the same boundary at which freezer
// segments are fsynced.

package ethel

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// frozenInterner is the read-only DictInterner the async encoder uses.
// It is populated by snapshotOutputs from a BufferedDictWriter (or any
// pre-resolved id map). Lookups never touch MDBX.
type frozenInterner struct {
	addrs      map[types.Address]uint32
	codeHashes map[types.Hash]uint32
}

// Compile-time interface assertion.
var _ DictInterner = (*frozenInterner)(nil)

func (f *frozenInterner) InternAddr(addr types.Address) (uint32, error) {
	id, ok := f.addrs[addr]
	if !ok {
		return 0, fmt.Errorf("frozenInterner: addr %x not pre-resolved", addr)
	}
	return id, nil
}

func (f *frozenInterner) InternCodeHash(h types.Hash) (uint32, error) {
	if h == (types.Hash{}) {
		return 0, nil
	}
	id, ok := f.codeHashes[h]
	if !ok {
		return 0, fmt.Errorf("frozenInterner: codeHash %x not pre-resolved", h)
	}
	return id, nil
}

// pendingOutput holds everything the background encoder needs, with no
// references to shared mutable state. The new-value maps are snapshots
// captured synchronously on the main goroutine.
type pendingOutput struct {
	blockNum uint64

	// Pre-encoded genesis data (block 0 only).
	genesisAcct []byte
	genesisSto  []byte

	// Changeset data (block > 0).
	accCS *changeset.ChangeSet
	stoCS *changeset.ChangeSet

	// Snapshotted post-block values for the newValueOf callbacks.
	accNewVals map[types.Address][]byte // addr → full account bytes + hidden current incarnation (nil = deleted)
	stoNewVals map[[52]byte][]byte      // [addr(20)||slot(32)] → raw slot bytes (nil = zero)

	// Pre-resolved dictionary ids. Built by snapshotOutputs from the main
	// goroutine's BufferedDictWriter, so the encoder can avoid all MDBX
	// access. Both maps include every address / codeHash referenced by
	// the per-block changesets (old + new sides).
	addrIDs     map[types.Address]uint32
	codeHashIDs map[types.Hash]uint32

	// Witness data (already encoded, block-scoped, safe to hand off).
	witnessData []byte
}

// asyncOutputWriter processes pendingOutput entries on a background
// goroutine. The channel provides ordering (FIFO) and back-pressure.
type asyncOutputWriter struct {
	batcher *outputBatcher
	ch      chan pendingOutput
	done    chan struct{}
	err     atomic.Pointer[error]

	// Back-pressure instrumentation. enqueue increments
	// stallCount/stallNanos when the channel was full at send time.
	// Read+reset at commit-interval log time via DrainStallStats.
	stallCount atomic.Uint64
	stallNanos atomic.Int64
}

func newAsyncOutputWriter(batcher *outputBatcher) *asyncOutputWriter {
	w := &asyncOutputWriter{
		batcher: batcher,
		ch:      make(chan pendingOutput, 128),
		done:    make(chan struct{}),
	}
	go w.loop()
	return w
}

func (w *asyncOutputWriter) loop() {
	defer close(w.done)
	for po := range w.ch {
		if w.err.Load() != nil {
			continue // drain channel but skip work after first error
		}
		if err := w.process(po); err != nil {
			e := fmt.Errorf("async output block %d: %w", po.blockNum, err)
			w.err.Store(&e)
		}
	}
}

func (w *asyncOutputWriter) process(po pendingOutput) error {
	var accCSBytes, stoCSBytes []byte

	if po.genesisAcct != nil || po.genesisSto != nil {
		// Block 0 was pre-encoded synchronously on the main goroutine.
		accCSBytes = po.genesisAcct
		stoCSBytes = po.genesisSto
	} else if po.accCS != nil || po.stoCS != nil {
		// V0 codec: per-block self-contained, no shared dictionary
		// state between encoder and async writer. The V1 dict path
		// is gated off until snapshotOutputs is wired up to pre-
		// resolve every (addr, codeHash) it touches.
		accCSBytes = EncodeAccountChanges(po.accCS,
			func(addr types.Address) []byte { return po.accNewVals[addr] })
		stoCSBytes = EncodeStorageChanges(po.stoCS,
			func(addr types.Address, slot types.Hash) []byte {
				var key [52]byte
				copy(key[:20], addr[:])
				copy(key[20:], slot[:])
				return po.stoNewVals[key]
			})
	}

	if err := w.batcher.addEntry(freezer.TableAccountChanges, "c", accCSBytes); err != nil {
		return fmt.Errorf("account changes: %w", err)
	}
	if err := w.batcher.addEntry(freezer.TableStorageChanges, "c", stoCSBytes); err != nil {
		return fmt.Errorf("storage changes: %w", err)
	}
	if err := w.batcher.addEntry(freezer.TableBlockWitness, "c", po.witnessData); err != nil {
		return fmt.Errorf("block witness: %w", err)
	}
	if _, err := w.batcher.flushFullBatches(); err != nil {
		return fmt.Errorf("flush batches: %w", err)
	}
	return nil
}

// enqueue sends a pendingOutput to the background goroutine.
// Blocks if the channel is full (back-pressure). When the channel is
// observed full at send time, stallCount and stallNanos accumulate
// for the commit-interval log line. The count can over-report by one
// if the channel drains between the failed try-send and the blocking
// send (rare, harmless — stallNanos for that case is near zero).
func (w *asyncOutputWriter) enqueue(po pendingOutput) {
	select {
	case w.ch <- po:
		return
	default:
	}
	t0 := time.Now()
	w.ch <- po
	w.stallCount.Add(1)
	w.stallNanos.Add(time.Since(t0).Nanoseconds())
}

// DrainStallStats returns and resets the accumulated channel-full stall
// counters. Called once per commit interval from the executor's stat log.
func (w *asyncOutputWriter) DrainStallStats() (count uint64, total time.Duration) {
	count = w.stallCount.Swap(0)
	total = time.Duration(w.stallNanos.Swap(0))
	return
}

// waitDrain blocks until all enqueued items are processed.
// Must be called before accessing the batcher from the main goroutine
// (e.g., at commit intervals or shutdown).
func (w *asyncOutputWriter) waitDrain() error {
	close(w.ch)
	<-w.done // wait for loop to exit (all items processed)

	// Restart for next interval.
	w.ch = make(chan pendingOutput, 128)
	w.done = make(chan struct{})
	go w.loop()

	if ep := w.err.Load(); ep != nil {
		return *ep
	}
	return nil
}

// stop shuts down the background goroutine permanently.
func (w *asyncOutputWriter) stop() error {
	close(w.ch)
	<-w.done
	if ep := w.err.Load(); ep != nil {
		return *ep
	}
	return nil
}

// checkError returns the first background error (if any) without blocking.
func (w *asyncOutputWriter) checkError() error {
	if ep := w.err.Load(); ep != nil {
		return *ep
	}
	return nil
}
