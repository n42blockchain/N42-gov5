// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestStorageRecordPathMatchesFold checks the storage record path against the
// leaf history, which is the truth: for every contract and every depth-1 child,
// the hash assembled from the node records must equal the folded one. A proof
// mixes the two (records on top, a folded subtree below), so a record that
// disagrees produces a proof that does not verify against the header root.
//
// The schedule must keep e[0] > 1: with a per-block level 0 the builder skips
// the aggregation row whose key used to overwrite the next level's path nibble,
// and the whole record path below level 0 stays unexercised.
func TestStorageRecordPathMatchesFold(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	logger := log.New()
	db, err := openDatcDB(logger, out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)

	o := e2eOpts{sched: schedE0is4, batch: 50, stoCache: 64}
	end := uint64(len(sc.blocks))
	b := newTestBuilder(t, db, out, sc, o, 0)
	if err := b.run(0, end, o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}
	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()

	owners := append(append([]types.Address{}, sc.big...), sc.small...)
	bad := 0
	for n := uint64(0); n < end && bad < 6; n++ {
		for _, addr := range owners {
			ah := keccak(addr[:])
			domain := ah[:]
			for nib := byte(0); nib < 16 && bad < 6; nib++ {
				path := []byte{nib}
				rec, recOK, err := q.nodeHashAt(domain, path, n)
				if err != nil {
					t.Fatalf("nodeHashAt: %v", err)
				}
				fold, foldOK, err := q.foldAt(domain, path, n)
				if err != nil {
					t.Fatalf("foldAt: %v", err)
				}
				if recOK != foldOK || (recOK && rec != fold) {
					bad++
					t.Errorf("n=%d addr=%x path=%x: record=%x(ok=%v) fold=%x(ok=%v)",
						n, addr[:4], path, rec[:8], recOK, fold[:8], foldOK)
				}
			}
		}
	}
}
