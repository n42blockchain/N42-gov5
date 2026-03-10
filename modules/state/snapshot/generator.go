// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const (
	// genBatchSize is the number of accounts processed per DB transaction.
	genBatchSize = 10_000

	// genLogInterval is how often progress is logged.
	genLogInterval = 30 * time.Second
)

// Generator builds the initial flat snapshot by scanning the Account and
// Storage tables. It runs in the background, periodically committing
// progress markers for crash recovery.
type Generator struct {
	db        kv.RwDB
	diskRoot  types.Hash
	diskBlock uint64
	progress  atomic.Uint64 // accounts processed
	done      chan struct{}
	aborted   atomic.Bool
}

// NewGenerator creates a snapshot generator that will build a flat snapshot
// of the state at the given root/block.
func NewGenerator(db kv.RwDB, root types.Hash, blockNum uint64) *Generator {
	return &Generator{
		db:        db,
		diskRoot:  root,
		diskBlock: blockNum,
		done:      make(chan struct{}),
	}
}

// Run performs the generation. Call this in a goroutine.
// It resumes from any existing generation marker.
func (g *Generator) Run(ctx context.Context) {
	defer close(g.done)

	start := time.Now()
	log.Info("Snapshot generation started", "block", g.diskBlock, "root", g.diskRoot)

	// Check if already complete.
	if err := g.db.View(ctx, func(tx kv.Tx) error {
		complete, err := rawdb.IsSnapshotGenComplete(tx)
		if err != nil {
			return err
		}
		if complete {
			g.aborted.Store(true) // signal: no work needed
			return nil
		}
		return nil
	}); err != nil {
		log.Error("Snapshot generation: check complete failed", "err", err)
		return
	}
	if g.aborted.Load() {
		log.Info("Snapshot generation: already complete")
		return
	}

	// Read resume marker.
	var marker []byte
	if err := g.db.View(ctx, func(tx kv.Tx) error {
		m, found, err := rawdb.ReadSnapshotGenMarker(tx)
		if err != nil {
			return err
		}
		if found {
			marker = m
			log.Info("Snapshot generation: resuming from marker", "marker_len", len(marker))
		}
		return nil
	}); err != nil {
		log.Error("Snapshot generation: read marker failed", "err", err)
		return
	}

	logTicker := time.NewTicker(genLogInterval)
	defer logTicker.Stop()

	for {
		if ctx.Err() != nil {
			log.Info("Snapshot generation: context cancelled")
			return
		}

		// Process one batch.
		var batchDone bool
		var lastKey []byte
		var batchCount int

		err := g.db.Update(ctx, func(tx kv.RwTx) error {
			c, err := tx.Cursor(modules.Account)
			if err != nil {
				return err
			}
			defer c.Close()

			var k, v []byte
			if marker != nil {
				k, v, err = c.Seek(marker)
			} else {
				k, v, err = c.First()
			}

			for ; k != nil && batchCount < genBatchSize; k, v, err = c.Next() {
				if err != nil {
					return err
				}

				// Copy account to snapshot table.
				accKey := make([]byte, len(k))
				copy(accKey, k)
				accVal := make([]byte, len(v))
				copy(accVal, v)

				if err := tx.Put(modules.SnapshotAccount, accKey, accVal); err != nil {
					return err
				}

				// Copy storage for this account.
				if len(accKey) >= types.AddressLength {
					if err := g.copyAccountStorage(tx, accKey[:types.AddressLength]); err != nil {
						return err
					}
				}

				lastKey = accKey
				batchCount++
				g.progress.Add(1)
			}

			// If we've exhausted all accounts, mark generation complete.
			if k == nil && err == nil {
				batchDone = true
				if err := rawdb.WriteSnapshotDiskRoot(tx, g.diskRoot, g.diskBlock); err != nil {
					return err
				}
				if err := rawdb.SetSnapshotGenComplete(tx); err != nil {
					return err
				}
			} else if lastKey != nil {
				// Save progress marker (next key to start from).
				// Increment last byte to avoid re-processing the same key.
				nextMarker := make([]byte, len(lastKey))
				copy(nextMarker, lastKey)
				// Increment for next seek.
				for i := len(nextMarker) - 1; i >= 0; i-- {
					nextMarker[i]++
					if nextMarker[i] != 0 {
						break
					}
				}
				if err := rawdb.WriteSnapshotGenMarker(tx, nextMarker); err != nil {
					return err
				}
			}

			return err
		})

		if err != nil {
			log.Error("Snapshot generation batch failed", "err", err)
			return
		}

		marker = lastKey

		// Log progress periodically.
		select {
		case <-logTicker.C:
			log.Info("Snapshot generation in progress",
				"accounts", g.progress.Load(),
				"elapsed", time.Since(start).Round(time.Second),
			)
		default:
		}

		if batchDone {
			elapsed := time.Since(start)
			snapshotGenDuration.Set(uint64(elapsed.Milliseconds()))
			log.Info("Snapshot generation completed",
				"accounts", g.progress.Load(),
				"elapsed", elapsed.Round(time.Second),
			)
			return
		}
	}
}

// copyAccountStorage copies all storage entries for an address from the
// Storage table to the SnapshotStorage table.
func (g *Generator) copyAccountStorage(tx kv.RwTx, address []byte) error {
	sc, err := tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	defer sc.Close()

	for k, v, err := sc.Seek(address); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return err
		}
		// Storage key: address(20) + incarnation(2) + storageKey(32) = 54 bytes.
		// Stop when we leave this address's prefix.
		if len(k) < types.AddressLength || !keyHasPrefix(k, address) {
			break
		}
		sk := make([]byte, len(k))
		copy(sk, k)
		sv := make([]byte, len(v))
		copy(sv, v)
		if err := tx.Put(modules.SnapshotStorage, sk, sv); err != nil {
			return err
		}
	}
	return nil
}

func keyHasPrefix(key, prefix []byte) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i, b := range prefix {
		if key[i] != b {
			return false
		}
	}
	return true
}

// Progress returns the number of accounts processed so far.
func (g *Generator) Progress() uint64 { return g.progress.Load() }

// Done returns a channel that is closed when generation completes or aborts.
func (g *Generator) Done() <-chan struct{} { return g.done }
