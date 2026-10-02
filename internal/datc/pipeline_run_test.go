// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// pipeline_run_test.go covers the decode pipeline (pipeline.go), which only
// the real build path exercises: it decodes blocks from a builder's
// freezer-backed b.acctTbl/b.storTbl, not the MDBX-staged tFwdAcctCS/
// tFwdStorCS tables the e2e harness feeds a fwdMode builder from.
package datc

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// pipelineTestBuilder returns a minimal builder wired to the fixture's
// changeset freezer tables — everything decodeOne/prefetchState touch.
func pipelineTestBuilder(t *testing.T, f *runFixture, prefetch bool) (*builder, func()) {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	dir := t.TempDir()
	db, err := openDatcDB(log.New(), dir, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	b := &builder{
		db:       db,
		acctTbl:  openCS(f.csDir, "acctcs"),
		storTbl:  openCS(f.csDir, "storcs"),
		prefetch: prefetch,
	}
	return b, func() {
		b.acctTbl.Close()
		b.storTbl.Close()
		db.Close()
	}
}

func TestDecodePipelineInOrder(t *testing.T) {
	f := newRunFixture(t)
	b, closeB := pipelineTestBuilder(t, f, true)
	defer closeB()

	const start, end, workers = 0, 30, 4
	p := startDecodePipeline(b, start, end, workers)
	for n := uint64(start); n < end; n++ {
		d, err := p.Next(n)
		if err != nil {
			t.Fatalf("Next(%d): %v", n, err)
		}
		if d.n != n {
			t.Fatalf("Next(%d) returned block %d", n, d.n)
		}
		gb := f.sc.blocks[n]
		if len(d.dirtyA) != len(gb.accs) {
			t.Errorf("block %d: decoded %d account changes, scenario has %d", n, len(d.dirtyA), len(gb.accs))
		}
		for addr, want := range gb.accs {
			got, ok := d.dirtyA[addr]
			if !ok {
				t.Errorf("block %d: addr %x missing from decoded dirty set", n, addr)
				continue
			}
			if want == nil {
				if got != nil {
					t.Errorf("block %d: addr %x expected deletion (nil), got %+v", n, addr, got)
				}
				continue
			}
			if got == nil || got.Nonce != want.Nonce {
				t.Errorf("block %d: addr %x nonce mismatch: got %+v want %+v", n, addr, got, want)
			}
		}
	}
	p.Stop()
}

// TestDecodePipelineStop exercises the mid-stream Stop() path (an error
// elsewhere in the build loop cancels decode work still in flight).
func TestDecodePipelineStop(t *testing.T) {
	f := newRunFixture(t)
	b, closeB := pipelineTestBuilder(t, f, false)
	defer closeB()

	p := startDecodePipeline(b, 0, uint64(len(f.sc.blocks)), 4)
	if _, err := p.Next(0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Next(1); err != nil {
		t.Fatal(err)
	}
	p.Stop()
}

// TestDecodeOneAndPrefetchDirect calls decodeOne/prefetchState directly
// (outside the pipeline's worker goroutines) for one block with both
// account and storage changes.
func TestDecodeOneAndPrefetchDirect(t *testing.T) {
	f := newRunFixture(t)
	b, closeB := pipelineTestBuilder(t, f, false)
	defer closeB()

	n, found := uint64(0), false
	for i, gb := range f.sc.blocks {
		if len(gb.accs) > 0 && len(gb.slots) > 0 {
			n, found = uint64(i), true
			break
		}
	}
	if !found {
		t.Skip("no block with both account and storage changes found")
	}
	ac := make(map[types.Address][32]byte)
	sc := make(map[types.Hash][32]byte)
	d := decodeOne(b, n, ac, sc)
	if d.err != nil {
		t.Fatal(d.err)
	}
	if len(d.dirtyA) == 0 {
		t.Fatal("expected decoded account changes")
	}
	if len(d.dirtyS) == 0 {
		t.Fatal("expected decoded storage changes")
	}
	prefetchState(b, d) // best-effort, read-only; must not panic on an empty DB
}
