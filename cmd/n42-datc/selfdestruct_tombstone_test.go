package main

// Constructive proof of the SELFDESTRUCT leaf-tombstone bug + fix, at the level
// the bug actually bites: the verifier's leaf-history fold (querier.asOfLeaves).
//
// The bug: blockApply/accumulateBlock enumerated a SELFDESTRUCT-ed contract's
// pre-state slots with a 40-byte SeekBothRange prefix against HashedStorage
// (whose physical DupSort key is 32 bytes), which never matched → ZERO per-slot
// tombstones were written to DatcLeafS. Harmless until the SAME address is
// recreated (CREATE2 metamorphic, mainnet ≥ block 7.28M): then asOfLeaves takes
// each slot's floor entry ≤ N, and a slot with NO destruct tombstone floors to
// its pre-destruct value — RESURRECTED into the new contract's storage trie,
// corrupting its storage root (and any EIP-1186 proof of that slot).
//
// This reproduces it deterministically by writing DatcLeafS records directly,
// without building past block 7.28M to find a real metamorphic contract.

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func openDatcTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(t.TempDir()).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(1) * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
			d := kv.TableCfg{}
			for n, it := range kv.ChaindataTablesCfg {
				d[n] = it
			}
			for _, tb := range []string{tDatcAccNode, tDatcStoNode, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS, tDatcMeta} {
				d[tb] = kv.TableCfgItem{}
			}
			return d
		}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestSelfdestructStorageTombstonePreventsFoldResurrection(t *testing.T) {
	// 40-byte leaf domain = addrHash(32) + 8-byte (zero) incarnation, per the
	// DatcLeafS composite-key layout; slot hashes are 32 bytes.
	domain := make([]byte, 40)
	for i := 0; i < 32; i++ {
		domain[i] = 0xab
	}
	sh1 := make([]byte, 32)
	sh1[0] = 0x11 // pre-destruct slot
	sh2 := make([]byte, 32)
	sh2[0] = 0x22 // post-recreate slot
	leafKey := func(sh []byte, blk uint64) []byte {
		k := append(append([]byte{}, domain...), sh...)
		var b8 [8]byte
		binary.BigEndian.PutUint64(b8[:], blk)
		return append(k, b8[:]...)
	}

	// Fold the contract's storage at block 30 (after recreate). withTombstone
	// models the FIXED build (a per-slot destruct tombstone at block 20); without
	// it models the BUGGY build (no tombstone — the 40-byte enumeration found
	// nothing).
	fold := func(withTombstone bool) int {
		db := openDatcTestDB(t)
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		mustPut := func(k, v []byte) {
			if err := tx.Put(tDatcLeafS, k, v); err != nil {
				t.Fatal(err)
			}
		}
		mustPut(leafKey(sh1, 10), []byte{0xde, 0xad}) // block 10: contract A, slot1=v1
		if withTombstone {
			mustPut(leafKey(sh1, 20), nil) // block 20: SELFDESTRUCT → slot1 tombstone
		}
		mustPut(leafKey(sh2, 30), []byte{0xbe, 0xef}) // block 30: A recreated, slot2=v2
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		rtx, err := db.BeginRo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer rtx.Rollback()
		q := &querier{tx: rtx, foldDepth: 0}
		leaves, err := q.asOfLeaves(domain, nil, 30)
		if err != nil {
			t.Fatal(err)
		}
		return len(leaves)
	}

	withTomb := fold(true)
	withoutTomb := fold(false)

	if withTomb != 1 {
		t.Fatalf("FIXED (tombstone present): want 1 live slot (only the recreated slot2), got %d", withTomb)
	}
	if withoutTomb != 2 {
		t.Fatalf("BUGGY (tombstone missing): want 2 (slot1 RESURRECTED + slot2), got %d — bug not reproduced", withoutTomb)
	}
	t.Logf("PROVEN: missing SELFDESTRUCT tombstone resurrects the pre-destruct slot at metamorphic recreate (fold yields %d live slots vs the correct %d); the fix emits the tombstone", withoutTomb, withTomb)
}
