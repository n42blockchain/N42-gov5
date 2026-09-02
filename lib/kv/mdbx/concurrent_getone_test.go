// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mdbx

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestGetOneIsNotConcurrencySafe documents, with the race detector rather than
// with a comment, that a single MdbxTx cannot serve concurrent readers.
//
// This is not academic. internal/parallel_processor.go hands ONE state reader
// to every Block-STM worker with the note "stateReader (base reader) must be
// safe for concurrent reads". The reader it is given is a PlainStateReader over
// an MdbxTx, and MdbxTx.GetOne takes a per-bucket cached cursor out of an
// unsynchronised map and calls SeekExact on it -- so every worker shares one
// MDBX cursor. On the bench fleet that path rejected blocks whose very first
// transaction read a funded sender as having zero balance, and the chain
// halted (docs/QS_TPS_BENCHMARK.md).
//
// Run with -race. Without the detector this test can pass while the reads are
// silently wrong, which is exactly how the defect survived.
func TestGetOneIsNotConcurrencySafe(t *testing.T) {
	// Opt-in: this test PROVES a real race, so leaving it on would make
	// `make race` fail for a defect it is documenting rather than introducing.
	//   N42_PROVE_MDBX_RACE=1 go test -race -run TestGetOneIsNotConcurrencySafe ./lib/kv/mdbx/
	if os.Getenv("N42_PROVE_MDBX_RACE") != "1" {
		t.Skip("set N42_PROVE_MDBX_RACE=1 (with -race) to reproduce the shared-cursor race")
	}
	if !raceEnabled {
		t.Skip("meaningful only under -race")
	}
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(
		func(kv.TableCfg) kv.TableCfg { return kv.TableCfg{"t": {}} }).MustOpen()
	t.Cleanup(db.Close)

	ctx := context.Background()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		for i := 0; i < 64; i++ {
			if err := tx.Put("t", []byte{byte(i)}, []byte{byte(i), byte(i)}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := db.View(ctx, func(tx kv.Tx) error {
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 200; i++ {
					_, _ = tx.GetOne("t", []byte{byte((w*31 + i) % 64)})
				}
			}(w)
		}
		wg.Wait()
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	t.Log("if this passed under -race, the shared-cursor path has changed; re-check ProcessParallel's base reader")
}
