// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestServingCopyAndSlim: a reader-only copy (segments + a DatcMeta-only
// MDBX, no change index) proves every sampled height exactly like the build
// archive; and clearing the dead MDBX tables of the build archive changes no
// answer.
func TestServingCopyAndSlim(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, db, sc)
	// The mainnet shape: account level 3 per block, so both ladders are exact.
	o := e2eOpts{sched: epochSchedule{e: [maxChgDepth + 1]uint64{8, 64, 16, 1, 4096, 4096}}, batch: 50, stoCache: 64, accDepth: 4, stoDepth: 2, leafSeg: true, accRoot: 1}
	end := uint64(len(sc.blocks))
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, end, o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	if n := tableRows(t, db, tDatcStoNode); n == 0 {
		t.Fatal("the build wrote no storage node records: nothing for slim to clear")
	}
	db.Close()
	stageBin = 8
	defer func() { stageBin = 1024 }()
	var contracts []deriveContract
	for a, d := range map[types.Address]int{sc.big[0]: 2, sc.big[1]: 3} {
		h := keccak(a[:])
		contracts = append(contracts, deriveContract{h[:], d})
	}
	if err := deriveNS(out, out, contracts, 4, 16, deriveOpts{}); err != nil {
		t.Fatal(err)
	}

	prove := func(dir string) {
		t.Helper()
		a, err := OpenArchive(dir, ArchiveOptions{MapGB: 4})
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		defer a.Close()
		for _, n := range []uint64{0, 37, 150, 299, end - 1} {
			for _, addr := range []types.Address{sc.eoas[1], sc.big[0], sc.big[1], sc.small[4], sc.absent} {
				var slots []types.Hash
				for s := range sc.storageAt(addr, n) {
					if slots = append(slots, s); len(slots) == 2 {
						break
					}
				}
				root := sc.roots[n]
				if _, err := a.Prove(context.Background(), addr, slots, n, &root); err != nil {
					t.Fatalf("%s: %x at %d: %v", filepath.Base(dir), addr[:4], n, err)
				}
			}
		}
	}

	copyDir := filepath.Join(t.TempDir(), "serving")
	files, _, err := servingCopy(out, copyDir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range []string{"ca.*.seg", "cs.*.seg"} {
		if m, _ := filepath.Glob(filepath.Join(copyDir, leafSegDir, dead)); len(m) > 0 {
			t.Fatalf("the serving copy carries %d %s files no reader opens", len(m), dead)
		}
	}
	srcSt, _ := os.Stat(filepath.Join(out, "mdbx.dat"))
	dstSt, err := os.Stat(filepath.Join(copyDir, "mdbx.dat"))
	if err != nil || files == 0 {
		t.Fatalf("serving copy incomplete: files=%d err=%v", files, err)
	}
	t.Logf("mdbx.dat: build archive %d KB, serving copy %d KB", srcSt.Size()>>10, dstSt.Size()>>10)
	prove(copyDir)
	if _, _, err := servingCopy(out, copyDir, false); err == nil {
		t.Fatal("serving-copy must refuse an existing destination")
	}

	runSlim([]string{"--out", out, "--map.gb", "4"})
	db, err = openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := tableRows(t, db, tDatcStoNode); n != 0 {
		t.Fatalf("slim left %d storage node records", n)
	}
	if n := tableRows(t, db, modules.HashedStorage); n == 0 {
		t.Fatal("slim touched the current-state tables the build resumes from")
	}
	db.Close()
	prove(out)
}
