// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestE2E_PartialWithBase queries an UPPER-range build before its merge: it
// has history only from the split block on, so keys untouched since the
// split have no rows and every fold, floor and storage root that involves
// them must come from the pristine prep-state base (--base). Without the
// base the same queries fail; with it every proof in the range verifies
// against the reference root and yields the reference value.
func TestE2E_PartialWithBase(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	logger := log.New()
	lo, hi, base := t.TempDir(), t.TempDir(), t.TempDir()
	o := e2eOpts{sched: epochSchedule{e: [maxChgDepth + 1]uint64{8, 64, 16, 1, 4096, 4096}}, batch: 50, stoCache: 64, accDepth: 4, stoDepth: 2, leafSeg: true, accRoot: 1}
	end := uint64(len(sc.blocks))
	split := uint64(150)

	dbLo, err := openDatcDB(logger, lo, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, dbLo, sc)
	if err := newTestBuilder(t, dbLo, lo, sc, o, 0).run(0, split, o.batch); err != nil {
		t.Fatalf("lower build: %v", err)
	}
	dbLo.Close()

	// Upper output seeded from the lower state; the base keeps that state.
	src, err := os.ReadFile(filepath.Join(lo, "mdbx.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hi, "mdbx.dat"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	runPrepState([]string{"--out", hi, "--map.gb", "4"})
	prepped, err := os.ReadFile(filepath.Join(hi, "mdbx.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "mdbx.dat"), prepped, 0o644); err != nil {
		t.Fatal(err)
	}
	dbHi, err := openDatcDB(logger, hi, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, dbHi, sc)
	if err := newTestBuilder(t, dbHi, hi, sc, o, split).run(split, end, o.batch); err != nil {
		t.Fatalf("upper build: %v", err)
	}
	dbHi.Close()

	dbQ, err := openDatcDB(logger, hi, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer dbQ.Close()

	keys := []types.Address{sc.eoas[0], sc.eoas[7], sc.big[0], sc.big[1], sc.small[2], sc.small[5], sc.absent}
	heights := []uint64{end - 2, end - 1, (split + end) / 2, split + 64, split + 63, split + 7, split + 1, split}

	// check runs the proof battery; it returns the number of failed proofs
	// (fatal=false) so the no-base run can show the base is what fixes them.
	check := func(q *querier, fatal bool) int {
		t.Helper()
		fails := 0
		fail := func(format string, args ...any) {
			fails++
			if fatal {
				t.Fatalf(format, args...)
			}
		}
		for _, n := range heights {
			for _, addr := range keys {
				ah := keccak(addr[:])
				nib := nibblesOfBytes(ah[:])
				nodes, err := q.proofPath(nil, nib, n)
				if err != nil {
					fail("proofPath(%x, %d): %v", addr[:4], n, err)
					continue
				}
				got, err := walkProof(sc.roots[n], nodes, nib)
				if err != nil {
					fail("proof for %x at %d does not verify: %v", addr[:4], n, err)
					continue
				}
				want := sc.accountAt(addr, n)
				if want == nil {
					if got != nil {
						fail("%x at %d: expected absence", addr[:4], n)
					}
					continue
				}
				w := *want
				w.Root = sc.storageRootAt(addr, n)
				buf := make([]byte, w.EncodingLengthForHashing())
				w.EncodeForHashing(buf)
				if !bytes.Equal(got, buf) {
					fail("%x at %d: account leaf mismatch", addr[:4], n)
					continue
				}
				if w.Root == emptyTrieRoot {
					continue
				}
				sm := sc.storageAt(addr, n)
				var slots []types.Hash
				for s := range sm {
					slots = append(slots, s)
					if len(slots) == 3 {
						break
					}
				}
				slots = append(slots, types.Hash{0xde, 0xad})
				for _, slot := range slots {
					sh := keccak(slot[:])
					sNib := nibblesOfBytes(sh[:])
					sNodes, err := q.proofPath(ah[:], sNib, n)
					if err != nil {
						fail("storage proofPath(%x/%x, %d): %v", addr[:4], slot[:4], n, err)
						continue
					}
					sgot, err := walkProof(w.Root, sNodes, sNib)
					if err != nil {
						fail("storage proof %x/%x at %d does not verify: %v", addr[:4], slot[:4], n, err)
						continue
					}
					if v, live := sm[slot]; live {
						if !bytes.Equal(sgot, rStr(v)) {
							fail("slot %x/%x at %d: value mismatch", addr[:4], slot[:4], n)
						}
					} else if sgot != nil {
						fail("slot %x/%x at %d: expected absence", addr[:4], slot[:4], n)
					}
				}
			}
		}
		return fails
	}

	// Without the base: the partial archive cannot serve these.
	q0, close0 := openTestQuerier(t, dbQ, hi, o)
	noBase := check(q0, false)
	close0()
	if noBase == 0 {
		t.Fatalf("expected failures on the partial archive without a base; the test has no teeth")
	}

	if err := openPartialBase(logger, base, 4); err != nil {
		t.Fatal(err)
	}
	defer func() { partialBaseDB.Close(); partialBaseDB = nil }()
	q, closeQ := openTestQuerier(t, dbQ, hi, o)
	defer closeQ()
	if q.base == nil {
		t.Fatal("querier did not open the base")
	}
	check(q, true)
	// Every height in the range must still reconstruct its root.
	for n := split; n < end; n++ {
		root, exists, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		if root != sc.roots[n] {
			t.Fatalf("height %d: root %x != reference %x", n, root[:6], sc.roots[n][:6])
		}
	}
	t.Logf("partial archive: %d proof failures without the base, 0 with it", noBase)
}
