// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"bytes"
	"context"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestE2E_ExactLadder builds a leaf-seg archive, derives the exact storage
// records offline (derive-ns) for contracts at depths 1, 2 and 3 — one of
// them far too small for its depth, so single-child and absent nodes occur —
// and then requires, at EVERY height, that the storage root assembled through
// the exact ladder equals the reference root, for listed and unlisted
// contracts alike; sampled slot proofs (live and absent) must verify and
// yield the reference value. derive-ns itself must pass its check against
// the storage-root history.
func TestE2E_ExactLadder(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, leafSeg: true, batch: 50, stoCache: 64}
	end := uint64(len(sc.blocks))
	if err := newTestBuilder(t, db, out, sc, o, 0).run(0, end, o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}

	dom := func(a types.Address) []byte { h := keccak(a[:]); return h[:] }
	listed := map[types.Address]int{sc.big[0]: 2, sc.big[1]: 3, sc.small[2]: 1, sc.small[5]: 3}
	var contracts []deriveContract
	for a, d := range listed {
		contracts = append(contracts, deriveContract{dom(a), d})
	}
	stageBin = 8 // the scenario is 360 blocks long
	defer func() { stageBin = 1024 }()
	if err := deriveNS(out, out, contracts, 4, 32, deriveOpts{}); err != nil {
		t.Fatalf("derive-ns: %v", err)
	}
	if err := deriveNS(out, out, contracts[:1], 4, 32, deriveOpts{}); err == nil {
		t.Fatal("deriving a listed contract twice must be refused")
	}
	// Account birth partitions: every account fold below block 250 reads them.
	if err := deriveAccParts(out, out, []uint64{40, 120, 250}, 4); err != nil {
		t.Fatalf("derive-acc-parts: %v", err)
	}

	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()
	if q.exact == nil || q.segNS == nil {
		t.Fatal("archive has no exact ladder after derive-ns")
	}
	if q.segAP[0] == nil || len(q.accStages) != 3 {
		t.Fatal("archive has no account birth partitions after derive-acc-parts")
	}
	staged := 0
	for a, d := range listed {
		rungs := q.exact.m[string(dom(a))]
		if len(rungs) != d {
			t.Fatalf("%x: %d rungs, want %d", a[:4], len(rungs), d)
		}
		if d > 1 && rungs[0].from < rungs[d-1].from {
			staged++
		}
		if parts := q.birthPartsAt(dom(a), rungs[d-1].from); parts != nil {
			t.Fatalf("%x: the last stage must read the main history", a[:4])
		}
	}
	havePart := false
	for _, p := range q.segSP {
		havePart = havePart || p != nil
	}
	if staged == 0 || !havePart {
		t.Fatal("no contract grew through more than one stage: the scenario does not exercise the rungs")
	}
	// The account trie at every height, early ones through the partitions.
	for n := uint64(0); n < end; n++ {
		root, exists, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatalf("account root at %d: %v", n, err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		if root != sc.roots[n] {
			t.Fatalf("account root at %d (stage %d): %x != reference %x", n, stageOf(q.accStages, n), root[:6], sc.roots[n][:6])
		}
	}

	addrs := []types.Address{sc.big[0], sc.big[1], sc.small[2], sc.small[5], sc.small[7]}
	recsBy := map[types.Address]int{}
	for n := uint64(0); n < end; n++ {
		for _, a := range addrs {
			q.recs = 0
			slots, nKids, usable, err := q.branchSlotsAt(dom(a), nil, n)
			if err != nil {
				t.Fatalf("%x at %d: %v", a[:4], n, err)
			}
			root := emptyTrieRoot
			switch {
			case !usable:
				h, exists, err := q.foldAt(dom(a), nil, n)
				if err != nil {
					t.Fatal(err)
				}
				if exists {
					root = h
				}
			case nKids > 0:
				root = branch17Hash(slots)
			}
			if want := sc.storageRootAt(a, n); root != want {
				t.Fatalf("%x (depth %d) at %d: root %x != reference %x (usable=%v nKids=%d recs=%d)",
					a[:4], listed[a], n, root[:6], want[:6], usable, nKids, q.recs)
			}
			recsBy[a] += q.recs
		}
	}
	for a, d := range listed {
		if recsBy[a] == 0 && (a == sc.big[0] || a == sc.big[1]) {
			t.Errorf("%x (depth %d): the exact records were never read", a[:4], d)
		}
	}
	if recsBy[sc.small[7]] != 0 {
		t.Errorf("unlisted contract read %d records; it must fold whole", recsBy[sc.small[7]])
	}

	rng := rand.New(rand.NewSource(5))
	for trial := 0; trial < 60; trial++ {
		n := uint64(rng.Intn(int(end)))
		for _, a := range addrs {
			sm := sc.storageAt(a, n)
			root := sc.storageRootAt(a, n)
			if root == emptyTrieRoot {
				continue
			}
			var slots []types.Hash
			for s := range sm {
				if slots = append(slots, s); len(slots) == 3 {
					break
				}
			}
			var absent types.Hash
			rng.Read(absent[:])
			slots = append(slots, absent)
			for _, slot := range slots {
				sh := keccak(slot[:])
				nib := nibblesOfBytes(sh[:])
				q.folds = 0
				nodes, err := q.proofPath(dom(a), nib, n)
				if err != nil {
					t.Fatalf("proofPath %x/%x at %d: %v", a[:4], slot[:4], n, err)
				}
				got, err := walkProof(root, nodes, nib)
				if err != nil {
					t.Fatalf("proof %x/%x at %d does not verify: %v", a[:4], slot[:4], n, err)
				}
				if v, live := sm[slot]; live {
					if !bytes.Equal(got, rStr(v)) {
						t.Fatalf("slot %x/%x at %d: value %x != %x", a[:4], slot[:4], n, got, rStr(v))
					}
				} else if got != nil {
					t.Fatalf("slot %x/%x at %d: expected absence", a[:4], slot[:4], n)
				}
			}
		}
	}
	_ = context.Background
}
