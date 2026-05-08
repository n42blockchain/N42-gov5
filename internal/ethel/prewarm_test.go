// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestPrewarmStaticAL_PopulatesStorageLRU pins the property that the
// main-thread synchronous prewarm helper DOES populate the readStorage
// LRU with the current MDBX values for every (address, slot) pair
// listed in the txs' AccessList. This is the race-free replacement for
// the prefetcher's removed storage LRU writes — main goroutine, main
// RoTx, unconditional Put via BufferedPlainStateReader's MDBX-miss path.
func TestPrewarmStaticAL_PopulatesStorageLRU(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee30")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")
	val := []byte{0x99}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, val); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()

	reader := state.NewBufferedPlainStateReader(buf, roTx)

	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})
	prewarmStaticAL(reader, []*transaction.Transaction{tx})

	v, present := buf.LookupReadStorage(addr, slot)
	if !present {
		t.Fatalf("prewarmStaticAL MUST populate readStorage LRU for AL slots")
	}
	if len(v) != 1 || v[0] != 0x99 {
		t.Fatalf("LRU populated with wrong value: got %x want 99", v)
	}
}

// TestPrewarmStaticAL_NoAccessList confirms that legacy txs (no AL)
// produce zero LRU writes — the helper iterates an empty AL and
// returns cleanly. Pin this so a future "always read sender + to even
// without AL" tweak is a deliberate choice, not an accidental
// regression.
func TestPrewarmStaticAL_NoAccessList(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee32")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, []byte{0xab}); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	reader := state.NewBufferedPlainStateReader(buf, roTx)

	// Legacy tx — no AL.
	tx := transaction.NewTx(&transaction.LegacyTx{Nonce: 0, To: &addr, Gas: 21000})
	prewarmStaticAL(reader, []*transaction.Transaction{tx})

	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("legacy tx (no AL) must not produce any LRU writes")
	}
}

// TestPrewarmStaticAL_DoesNotChangeReadResult is the determinism guard:
// running prewarm before a sequence of reads must produce the same
// (address, slot) -> value answers as the same sequence without
// prewarm. If this ever fails, prewarm has somehow shifted the read
// view (e.g. accidentally writing the buffer or shadowing the bufReader
// fall-through tiers) and any forward-replay using prewarm would
// silently diverge in EVM semantics.
func TestPrewarmStaticAL_DoesNotChangeReadResult(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee33")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000003")
	val := []byte{0x12, 0x34}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, val); err != nil {
			t.Fatal(err)
		}
		// Also seed the account so the post-prewarm read covers the
		// account-tier pass-through too.
		acct := &account.StateAccount{Initialised: true, Nonce: 1}
		acct.Balance.SetUint64(1000)
		if err := rwTx.Put(modules.Account, addr.Bytes(), acct.MarshalV2()); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buildReader := func() (*state.BufferedPlainStateReader, func()) {
		buf := state.NewPlainStateBuffer()
		roTx, err := db.BeginRo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return state.NewBufferedPlainStateReader(buf, roTx), func() { roTx.Rollback() }
	}

	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})

	// Run A: no prewarm.
	rA, doneA := buildReader()
	defer doneA()
	gotA, err := rA.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}

	// Run B: with prewarm.
	rB, doneB := buildReader()
	defer doneB()
	prewarmStaticAL(rB, []*transaction.Transaction{tx})
	gotB, err := rB.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}

	if string(gotA) != string(gotB) {
		t.Fatalf("prewarm changed read result: noPrewarm=%x withPrewarm=%x", gotA, gotB)
	}
	if len(gotB) != 2 || gotB[0] != 0x12 || gotB[1] != 0x34 {
		t.Fatalf("read returned wrong value: got %x want 1234", gotB)
	}
}

// TestPrewarmStaticAL_BufShadowsLRU is the critical race-safety check:
// if the active buffer holds a NEWER value for a slot than MDBX (e.g.
// a prior tx in this same block SSTORE'd it), the executor's main read
// path returns the buffer value, not the LRU value the prewarm warmed.
// This pins the invariant that prewarm CANNOT corrupt subsequent
// reads even if the LRU it wrote is "stale relative to buffer".
//
// Construction:
//   - MDBX has slot=V_old=0xAA
//   - Active buffer has slot=V_new=0xBB (simulating earlier tx in
//     this block)
//   - Prewarm reads MDBX, populates LRU[slot]=V_old
//   - bufReader.ReadAccountStorage MUST still return V_new
func TestPrewarmStaticAL_BufShadowsLRU(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee34")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000004")

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, []byte{0xAA}); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	reader := state.NewBufferedPlainStateReader(buf, roTx)

	// Simulate a prior tx in this block that SSTORE'd slot=0xBB.
	writer := state.NewBufferedPlainStateWriterAt(buf, roTx, 1, 0)
	oldVal := uint256.NewInt(0xAA)
	newVal := uint256.NewInt(0xBB)
	if err := writer.WriteAccountStorage(addr, slot, *oldVal, *newVal); err != nil {
		t.Fatal(err)
	}

	// Prewarm AFTER the buffer write to exercise the worst case: prewarm
	// sees fresh MDBX while the buffer is non-empty for this slot. The
	// invariant under test: bufReader returns buffer-truth, NOT LRU.
	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})
	prewarmStaticAL(reader, []*transaction.Transaction{tx})

	got, err := reader.ReadAccountStorage(addr, &slot)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 0xBB {
		t.Fatalf("buf MUST shadow LRU after prewarm: got %x want BB", got)
	}
}
