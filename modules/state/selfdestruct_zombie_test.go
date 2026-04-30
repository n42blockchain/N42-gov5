// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reproduces the production "MDBX zombie storage after SELFDESTRUCT" bug
// observed at block 10941306 on the Ethereum mainnet replay (d:\N42-eth1).
//
// Symptom — diff-cs found two slots whose n42 storcs entry has a
// non-zero `oldVal` while reth's `preVal` is zero:
//
//	[VAL DIFF] addr=00000000002bde777710c370e08fc83d61b2b8e1
//	            slot=...0008 n42_old=2432 reth_old=  (wipe-stale)
//	[VAL DIFF] addr=00000000002bde777710c370e08fc83d61b2b8e1
//	            slot=...0009 n42_old=043e reth_old=  (wipe-stale)
//
// reth says these slots became 0 at end of an earlier block (10941141);
// n42's MDBX still holds the pre-block values 0x2432 / 0x043e. So a
// later block (10941306) reading MDBX during its own wipe enumeration
// re-discovers the zombie and emits a phantom changeset entry.
//
// These tests exercise the BufferedPlainStateWriter + ApplyTo flush
// path on a memdb to see whether the wipe really removes MDBX rows.
// They are intentionally minimal — no IBS, no EVM, no journal — so the
// fix-test cycle is ~1s instead of the ~30 minute bootstrap-and-replay
// cycle we tried first.

package state

import (
	"context"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

// liveAccountBytes returns a properly-encoded StateAccount for seeding
// MDBX directly. Uses MarshalV2 (the production encoding).
func liveAccountBytes(t *testing.T) []byte {
	a := &account.StateAccount{Initialised: true, Nonce: 1}
	a.Balance.SetUint64(100)
	return a.MarshalV2()
}

// n42InitOnce serializes modules.N42Init across the parallel sub-tests
// in this file. N42Init mutates the package-level kv.ChaindataTablesCfg
// map so concurrent calls race. Tests that need it must call this once
// at the top, BEFORE t.Parallel(); they should not call modules.N42Init
// directly.
var n42InitOnce sync.Once

func initN42Tables(t *testing.T) {
	n42InitOnce.Do(func() { modules.N42Init() })
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })
}

// pathSelfdestructOnly: the contract had pre-existing storage in MDBX,
// the block SELFDESTRUCTs it, no recreation. After flush, MDBX storage
// for the address must be empty.
//
// Mirrors the IBS MakeWriteSet path for `shouldDelete = selfdestructed`
// (intra_block_state.go:850-858):
//
//	stateWriter.DeleteAccount(addr, &original)
//	stateWriter.CreateContract(addr)
//
// — followed by SnapshotForFlush + ApplyTo (the periodic commit-interval
// flush in executor.go:387).
func TestSelfdestructWipe_PureDestroy_NoRecreate(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)

	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	slot9 := hashFromByte(9)
	val8 := []byte{0x24, 0x32}
	val9 := []byte{0x04, 0x3e}

	// 1. Seed MDBX with the pre-block values that diff-cs saw zombied
	//    in production. Also seed the account so DeleteAccount has work.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), val8))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]), val9))
		require.NoError(t, rwTx.Commit())
	}

	// 2. Drive the writer the way IBS.MakeWriteSet does for a pure
	//    SELFDESTRUCT'd account.
	buf := NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	w := NewBufferedPlainStateWriter(buf, roTx, 10941141)

	require.NoError(t, w.DeleteAccount(addr, nil))
	require.NoError(t, w.CreateContract(addr))
	roTx.Rollback()

	// 3. Hand snapshot to MDBX (simulates the bg flush at end of
	//    commit interval).
	snap := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
	}

	// 4. Verify MDBX no longer has any storage for this address —
	//    not even the zombie 0x2432 / 0x043e.
	roTx2, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx2.Rollback()

	v8, _ := roTx2.GetOne(modules.Storage,
		modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]))
	v9, _ := roTx2.GetOne(modules.Storage,
		modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]))
	require.Nil(t, v8, "slot 8 zombie: MDBX still has %x after SELFDESTRUCT wipe", v8)
	require.Nil(t, v9, "slot 9 zombie: MDBX still has %x after SELFDESTRUCT wipe", v9)
}

// pathSelfdestructPlusCreate2: the contract was destroyed AND
// recreated in the same block, with the new incarnation writing only
// some slots. Slots that the new incarnation does NOT touch must NOT
// linger from the pre-destroy state in MDBX.
//
// Mirrors the IBS MakeWriteSet path for `needsWipe && created`
// (intra_block_state.go:859-883), where:
//
//	stateWriter.CreateContract(addr)            // wipe
//	stateWriter.UpdateAccountCode(...)          // optional
//	stateWriter.CreateContract(addr)            // again, from `created` branch
//	stateObject.updateTrie(stateWriter)         // writes only slot 6 (say)
//	stateWriter.UpdateAccountData(...)
//
// pre-block: slots 8, 9 = 0x2432, 0x043e (the zombies)
// in-block:  new incarnation writes only slot 6 = 0x03 (per reth at
//            block 10941307).
// post-flush expected: slot 6 = 0x03; slots 8, 9 absent.
func TestSelfdestructWipe_DestroyAndRecreate(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)

	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot6 := hashFromByte(6)
	slot8 := hashFromByte(8)
	slot9 := hashFromByte(9)
	val8 := []byte{0x24, 0x32}
	val9 := []byte{0x04, 0x3e}
	newVal6 := []byte{0x03}

	// Seed MDBX with the zombie storage + an existing account encoding.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), val8))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]), val9))
		require.NoError(t, rwTx.Commit())
	}

	buf := NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	w := NewBufferedPlainStateWriter(buf, roTx, 10941141)

	// updateAccountWithWipe(needsWipe=true) path:
	//   1) CreateContract (the `else if needsWipe` branch)
	require.NoError(t, w.CreateContract(addr))
	//   2) UpdateAccountCode is omitted for this minimal case.
	//   3) CreateContract again (the `if stateObject.created` branch).
	require.NoError(t, w.CreateContract(addr))
	//   4) updateTrie → WriteAccountStorage for the new incarnation's
	//      single dirty slot. blockOriginStorage of a `created`
	//      stateObject is 0 because so.created short-circuits to 0 in
	//      state_object.go GetCommittedState (line 183-186).
	zero := uint256.NewInt(0)
	postVal := new(uint256.Int).SetBytes(newVal6)
	require.NoError(t, w.WriteAccountStorage(addr, &slot6, zero, postVal))
	roTx.Rollback()

	snap := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
	}

	roTx2, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx2.Rollback()

	v6, _ := roTx2.GetOne(modules.Storage,
		modules.PlainGenerateCompositeStorageKey(addr[:], slot6[:]))
	v8, _ := roTx2.GetOne(modules.Storage,
		modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]))
	v9, _ := roTx2.GetOne(modules.Storage,
		modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]))
	require.Equal(t, newVal6, v6, "slot 6: new incarnation's value lost")
	require.Nil(t, v8, "slot 8 zombie: MDBX still has %x after SELFDESTRUCT+CREATE2", v8)
	require.Nil(t, v9, "slot 9 zombie: MDBX still has %x after SELFDESTRUCT+CREATE2", v9)
}

// pathTwoBlocks: the bug we observed straddles a block boundary.
// Block N: SELFDESTRUCT (or wipe) — should clear MDBX.
// Block N+5: another SELFDESTRUCT with no intervening writes for
//            slots 8/9. If the block-N flush left zombies, block N+5
//            sees them via collectPreWipeSlots' MDBX scan and emits a
//            phantom (oldVal=0x2432, newVal=0) entry — exactly what
//            n42-slot-history showed in production.
func TestSelfdestructWipe_PhantomEntryOnSecondWipe(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)

	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	val8 := []byte{0x24, 0x32}

	// Seed.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), val8))
		require.NoError(t, rwTx.Commit())
	}

	// --- Block N: SELFDESTRUCT ---
	buf := NewPlainStateBuffer()
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941141)
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))
		roTx.Rollback()
	}

	// Flush block N's interval to MDBX. This is the step under
	// suspicion — does prefix-scan + tx.Delete actually clear the
	// addr's storage rows?
	snap := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
	}

	// --- Block N+5: another SELFDESTRUCT, no intervening writes. ---
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941146)
		csw := w.ChangeSetWriter()
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))

		// If block N's flush correctly wiped MDBX, collectPreWipeSlots
		// in this block finds 0 slots (MDBX has nothing for the addr,
		// buf has nothing). So csw.storageChanges contains no entry
		// for slot 8.
		//
		// If block N's flush left a zombie (0x2432) in MDBX,
		// collectPreWipeSlots' MDBX scan picks it up and records a
		// phantom (slot 8 → 0x2432) into csw, which is the exact
		// failure diff-cs surfaced at block 10941306 in production.
		key := modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:])
		_, exists := csw.storageChanges[string(key)]
		require.False(t, exists,
			"block N+5 has a phantom storcs entry for slot 8 — block N's wipe didn't clear MDBX")
		roTx.Rollback()
	}
}

// TestSelfdestructWipe_SameInterval_NoStaleSnapEntries reproduces the
// block-10941146 production diff: same address SELFDESTRUCT'd twice
// in the SAME commit interval (no flush between), with the second
// CreateContract running collectPreWipeSlots while inFlight still
// holds a snap from a PRIOR interval that contains stale storage
// entries for this address.
//
// Pre-fix: collectPreWipeSlots's step 2 (snap section) populates
// `out` with the snap's stale entries, then step 2b (`if bufWiped {
// return out, nil }`) returns the populated `out`, causing
// recordStorageWipe to write phantom (slot, oldVal=stale_V) entries
// at the second-wipe block. diff-cs flagged 11 such entries at
// block 10941146 with the n42_old==n42_new "no-op (stale propagated
// to PlainState)" pattern — the values match the pre-first-wipe
// state because the constructor SSTOREs in the second incarnation
// happen to write the same values back, and csw's first-wins guard
// preserves the stale recorded oldVal over the constructor's fresh
// (slot, oldVal=0) writes.
//
// Post-fix: bufWiped is checked at the TOP of collectPreWipeSlots,
// before any out population. The function returns nil immediately,
// recordStorageWipe is a no-op, and constructor SSTOREs go through
// the normal (slot, oldVal=0) path matching reth's semantics.
func TestSelfdestructWipe_SameInterval_NoStaleSnapEntries(t *testing.T) {
	initN42Tables(t)

	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot0 := hashFromByte(0)
	slot1 := hashFromByte(1)

	buf := NewPlainStateBuffer()

	// Step 1 — Set up an inFlight snap that has stale entries for the
	// address. Simulates a previous interval (N-1) that wrote slot 0 = V0
	// and slot 1 = V1, then committed.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 100)
		require.NoError(t, w.WriteAccountStorage(addr, &slot0, uint256.NewInt(0), uint256.NewInt(0x0fc0)))
		require.NoError(t, w.WriteAccountStorage(addr, &slot1, uint256.NewInt(0), uint256.NewInt(0x5d46)))
		roTx.Rollback()
	}
	// Snapshot the writes so they live in inFlight (without applying
	// to MDBX — simulates the moment after SnapshotForFlush but before
	// bg flush completes).
	snap := buf.SnapshotForFlush()
	_ = snap

	// Step 2 — In the CURRENT interval (N), block 10941141: SELFDESTRUCT
	// the address. Active buf gets contractWipes + wipedStorage[addr].
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941141)
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))
		roTx.Rollback()
	}

	// Step 3 — Block 10941146 in the SAME interval: same address gets
	// re-CREATE2'd. CreateContract calls collectPreWipeSlots a second
	// time. Pre-fix: snap section populates out with V0/V1, bufWiped
	// returns it. Post-fix: empty return, no phantom entries.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941146)
		csw := w.ChangeSetWriter()
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))

		// recordStorageWipe MUST NOT have added stale entries for
		// slot 0 or slot 1.
		key0 := modules.PlainGenerateCompositeStorageKey(addr[:], slot0[:])
		key1 := modules.PlainGenerateCompositeStorageKey(addr[:], slot1[:])
		_, exists0 := csw.storageChanges[string(key0)]
		_, exists1 := csw.storageChanges[string(key1)]
		require.False(t, exists0,
			"block 10941146 has phantom storcs entry for slot 0 — collectPreWipeSlots leaked snap value past bufWiped guard")
		require.False(t, exists1,
			"block 10941146 has phantom storcs entry for slot 1 — same bug")
		roTx.Rollback()
	}
}

// Same scenario as TestSelfdestructWipe_PhantomEntryOnSecondWipe but
// with the two SELFDESTRUCT blocks in DIFFERENT commit intervals. In
// production a CommitInterval=10000 separates 10941141 and 10941306 by
// only ~165 blocks, so both fall in the same interval. But if the
// executor was running with a smaller CommitInterval (e.g. 100 or 1000)
// — or if the bug interacts with the bg flusher's snapshot rotation —
// cross-interval is exactly when buf.wipedStorage gets cleared
// (SnapshotForFlush:496). After the clear, the second wipe block falls
// through to MDBX in collectPreWipeSlots — that's the path that emits
// the phantom (oldVal=non-zero, newVal=0) entry diff-cs flagged.
//
// If this test FAILS, the bug is in the boundary between SnapshotForFlush
// and the bg ApplyTo: the wipe step in ApplyTo didn't actually delete
// MDBX rows, OR something between intervals re-populates them.
func TestSelfdestructWipe_CrossInterval_PhantomOnSecondWipe(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)

	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	val8 := []byte{0x24, 0x32}

	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), val8))
		require.NoError(t, rwTx.Commit())
	}

	buf := NewPlainStateBuffer()

	// --- Interval 1: block N=10941141 SELFDESTRUCT ---
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941141)
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))
		roTx.Rollback()
	}

	// Interval boundary: SnapshotForFlush + bg ApplyTo. This rotates
	// active buffer, so buf.wipedStorage is cleared. The snapshot still
	// has wipedStorage[addr] (so reads during the flush window can see
	// the wipe), but once the bg flush completes and a new snapshot
	// happens, that visibility is gone.
	snap1 := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap1.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
	}
	// Drop the in-flight pointer too — simulates the next
	// SnapshotForFlush replacing snap1, which is what really exposes
	// the bug if MDBX wasn't actually wiped.
	snap2 := buf.SnapshotForFlush()
	_ = snap2 // empty snap for the new interval; not applied yet.
	buf.ClearInFlight()

	// --- Interval 2: block N=10941306 second SELFDESTRUCT ---
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		w := NewBufferedPlainStateWriter(buf, roTx, 10941306)
		csw := w.ChangeSetWriter()
		require.NoError(t, w.DeleteAccount(addr, nil))
		require.NoError(t, w.CreateContract(addr))

		key := modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:])
		_, exists := csw.storageChanges[string(key)]
		require.False(t, exists,
			"interval 2 has a phantom storcs entry for slot 8 — interval 1's flush didn't clear MDBX (or LRU/inFlight re-introduced it)")
		roTx.Rollback()
	}

	// Sanity: regardless of csw outcome, MDBX should NOT still hold
	// slot 8 = 0x2432 after interval 1's flush.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		defer roTx.Rollback()
		v, _ := roTx.GetOne(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]))
		require.Nil(t, v, "MDBX still has zombie slot 8=%x after interval 1 flushed the wipe", v)
	}
}

// IBS-driven reproducer: drives the writer through real IntraBlockState
// API (CreateAccount, SetState, Selfdestruct, FinalizeTx, MakeWriteSet) —
// the same surface the EVM hits in production. The earlier writer-only
// tests bypass IBS's journal / dirtyStorage / blockOriginStorage state,
// so any bug introduced by that interaction is invisible to them.
//
// Production trace at block 10941141:
//   - Pre-block MDBX has slot 8 = 0x2432, slot 9 = 0x043e (set in prior
//     incarnations of the same address — gas-token style contract).
//   - Block 10941141 has SSTOREs followed by SELFDESTRUCT, ending with
//     all storage cleared per reth's view.
//   - Some block(s) later (10941146, 10941306, ...) the contract is
//     destroyed again. n42 storcs records oldVal=0x2432/0x043e for
//     slots 8/9 — meaning collectPreWipeSlots' MDBX scan still found
//     the pre-10941141 values. The wipe never reached MDBX.
//
// The test mirrors a 2-block sequence: block 1 seeds the storage,
// block 2 SELFDESTRUCTs; after the snapshot+ApplyTo for block 2's
// interval, MDBX must hold no storage rows for the address.
func TestSelfdestructWipe_IBS_Driven(t *testing.T) {
	// NOT t.Parallel() — initN42Tables mutates the global
	// kv.ChaindataTablesCfg map, racing across parallel sub-tests.
	initN42Tables(t)

	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	slot9 := hashFromByte(9)
	rules := &params.Rules{IsSpuriousDragon: true, IsConstantinople: true}

	buf := NewPlainStateBuffer()

	// --- Block 1: deploy contract + write slot 8 = 0x2432, slot 9 = 0x043e ---
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		bufReader := NewBufferedPlainStateReader(buf, rwTx)
		writer := NewBufferedPlainStateWriter(buf, rwTx, 1)
		sdb := New(bufReader)

		sdb.CreateAccount(addr, true)
		sdb.SetCode(addr, []byte{0x60, 0x00})
		sdb.SetBalance(addr, uint256.NewInt(100))
		sdb.SetState(addr, &slot8, *uint256.NewInt(0x2432))
		sdb.SetState(addr, &slot9, *uint256.NewInt(0x043e))
		require.NoError(t, sdb.FinalizeTx(rules, writer))
		require.NoError(t, sdb.CommitBlock(rules, writer))
		require.NoError(t, rwTx.Commit())
	}

	// Flush interval 1 → MDBX. Now MDBX has slot 8 = 0x2432, slot 9 = 0x043e.
	{
		snap := buf.SnapshotForFlush()
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
		// Replicate executor.go:130 — after async flush completes, the
		// LRU is refreshed against the just-flushed snap.
		buf.RefreshLRUForSnapshot(snap)
	}

	// Sanity: confirm MDBX seeded.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		v8, _ := roTx.GetOne(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]))
		require.Equal(t, []byte{0x24, 0x32}, v8)
		v9, _ := roTx.GetOne(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]))
		require.Equal(t, []byte{0x04, 0x3e}, v9)
		roTx.Rollback()
	}

	// --- Block 2 (= production 10941141): SELFDESTRUCT the contract. ---
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		bufReader := NewBufferedPlainStateReader(buf, rwTx)
		writer := NewBufferedPlainStateWriter(buf, rwTx, 2)
		sdb := New(bufReader)

		// Sanity: IBS sees the seeded storage (read goes via buf → LRU
		// → MDBX, depending on what RefreshLRU put where).
		var v8, v9 uint256.Int
		sdb.GetState(addr, &slot8, &v8)
		sdb.GetState(addr, &slot9, &v9)
		require.Equal(t, *uint256.NewInt(0x2432), v8, "IBS read slot 8 pre-SELFDESTRUCT")
		require.Equal(t, *uint256.NewInt(0x043e), v9, "IBS read slot 9 pre-SELFDESTRUCT")

		// SELFDESTRUCT (sub-Cancun → full storage wipe).
		require.True(t, sdb.Selfdestruct(addr))
		require.NoError(t, sdb.FinalizeTx(rules, writer))
		require.NoError(t, sdb.CommitBlock(rules, writer))
		require.NoError(t, rwTx.Commit())
	}

	// Flush interval 2 → MDBX. The wipe step here is what we suspect.
	{
		snap := buf.SnapshotForFlush()
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
		buf.RefreshLRUForSnapshot(snap)
	}

	// Verify MDBX has NO storage rows for the address.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		defer roTx.Rollback()
		v8, _ := roTx.GetOne(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]))
		v9, _ := roTx.GetOne(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]))
		require.Nil(t, v8, "slot 8 zombie: MDBX still has %x after IBS-driven SELFDESTRUCT", v8)
		require.Nil(t, v9, "slot 9 zombie: MDBX still has %x after IBS-driven SELFDESTRUCT", v9)

		// Account also gone.
		acct, _ := roTx.GetOne(modules.Account, addr[:])
		require.Nil(t, acct, "account: MDBX still has %x after SELFDESTRUCT", acct)
	}
}

// Sanity test that PutBatch on the storage S3FIFO actually populates
// the live map. If this fails, the LRU layer itself is broken and the
// downstream regression tests can't trust the LRU read path.
func TestS3FIFOStorage_PutBatch_RoundTrip(t *testing.T) {
	t.Parallel()
	buf := NewPlainStateBuffer()
	addr := types.HexToAddress("0xdeadbeef000000000000000000000000deadbeef")
	slot := hashFromByte(0x42)
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], addr[:])
	copy(ck[20:], slot[:])

	buf.readStorage.PutBatch(
		[][storageCompositeKeyLen]byte{ck},
		[][]byte{{0x12, 0x34}},
		[]int{storageCompositeKeyLen + 2 + cacheOverheadPerEntry},
	)
	v, ok := buf.readStorage.Get(ck)
	require.True(t, ok, "PutBatch put didn't surface in Get")
	require.Equal(t, []byte{0x12, 0x34}, v)
}

// Regression for the LRU-zombie root cause documented in
// buffered_plain_state.go:753-759 — production blocks 10941141 / 10941146 /
// 10941306 / 17150008 / 3180272 emitted phantom (oldVal=non-zero,
// newVal=0) storcs entries because the read LRU kept the pre-SELFDESTRUCT
// slot value across the wipe. The fix at line 780-798 sweeps the LRU
// prefix for every contractWipes addr inside RefreshLRUForSnapshot,
// AFTER StampWipes increments the flush epoch.
//
// This test models the exact production timeline:
//   - Pre-block: contract was created in some earlier interval, MDBX
//     holds slot 8 = 0x2432, slot 9 = 0x043e (and contractWipes is
//     long-cleared by intervening flushes).
//   - Block N: a normal SLOAD populates the LRU with the MDBX values.
//   - Block N+1: the contract is SELFDESTRUCT'd.
//   - Flush: RefreshLRUForSnapshot must sweep the LRU prefix for the
//     wiped address. If it doesn't, a later block's IBS read picks up
//     the zombie 0x2432 / 0x043e and writes it as oldVal into storcs.
func TestSelfdestructWipe_LRU_NoZombieAfterRefresh(t *testing.T) {
	// NOT t.Parallel() — initN42Tables mutates the global
	// kv.ChaindataTablesCfg map, racing across parallel sub-tests.
	initN42Tables(t)

	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	slot9 := hashFromByte(9)
	val8 := []byte{0x24, 0x32}
	val9 := []byte{0x04, 0x3e}
	rules := &params.Rules{IsSpuriousDragon: true, IsConstantinople: true}

	buf := NewPlainStateBuffer()

	// Pre-state: account + storage already in MDBX from some past
	// interval. We seed directly to avoid the IBS path adding the
	// addr to contractWipes (which would invalidate this test by
	// sweeping the LRU before we even get to block 2).
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		// Real-shape encoded account would be 50+ bytes; for this test
		// we just need the GetOne to return something.
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), val8))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]), val9))
		require.NoError(t, rwTx.Commit())
	}

	// Block N: a vanilla SLOAD on slot 8 / slot 9. ReadAccountStorage
	// falls through buf (empty) → inFlight (nil) → LRU (empty) → MDBX
	// → returns the seeded values AND populates the LRU on the way back
	// (line 1148-1153 in buffered_plain_state.go).
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		bufReader := NewBufferedPlainStateReader(buf, roTx)
		v8, err := bufReader.ReadAccountStorage(addr, &slot8)
		require.NoError(t, err)
		require.Equal(t, val8, v8)
		v9, err := bufReader.ReadAccountStorage(addr, &slot9)
		require.NoError(t, err)
		require.Equal(t, val9, v9)
		roTx.Rollback()
	}

	// Sanity: LRU has both entries (added by ReadAccountStorage's
	// fall-through-cache path).
	{
		var ck8, ck9 [storageCompositeKeyLen]byte
		copy(ck8[:20], addr[:])
		copy(ck8[20:], slot8[:])
		copy(ck9[:20], addr[:])
		copy(ck9[20:], slot9[:])
		v8, ok := buf.readStorage.Get(ck8)
		require.True(t, ok, "LRU should have slot 8 after a fresh SLOAD")
		require.Equal(t, val8, v8)
		_, ok = buf.readStorage.Get(ck9)
		require.True(t, ok, "LRU should have slot 9 after a fresh SLOAD")
	}

	// Block N+1: SELFDESTRUCT. Driven through full IBS → MakeWriteSet.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		bufReader := NewBufferedPlainStateReader(buf, rwTx)
		writer := NewBufferedPlainStateWriter(buf, rwTx, 1)
		sdb := New(bufReader)
		require.True(t, sdb.Selfdestruct(addr))
		require.NoError(t, sdb.FinalizeTx(rules, writer))
		require.NoError(t, sdb.CommitBlock(rules, writer))
		require.NoError(t, rwTx.Commit())
	}

	// Interval flush: ApplyTo wipes MDBX, RefreshLRUForSnapshot must
	// sweep the LRU.
	{
		snap := buf.SnapshotForFlush()
		require.NotEmpty(t, snap.contractWipes, "SELFDESTRUCT didn't add to contractWipes — IBS path mismatch")
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
		buf.RefreshLRUForSnapshot(snap)
	}

	// THE INVARIANT: LRU is clean for the wiped address (no zombie).
	for _, slot := range []types.Hash{slot8, slot9} {
		var ck [storageCompositeKeyLen]byte
		copy(ck[:20], addr[:])
		copy(ck[20:], slot[:])
		v, ok := buf.readStorage.Get(ck)
		require.False(t, ok,
			"LRU still has %x for slot %x after SELFDESTRUCT — prefix sweep at buffered_plain_state.go:782-798 is broken (regression of the 10941141 bug)",
			v, slot)
	}

	// And reads through the bufReader must return 0 (not zombie).
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()
	bufReader := NewBufferedPlainStateReader(buf, roTx)
	v8, err := bufReader.ReadAccountStorage(addr, &slot8)
	require.NoError(t, err)
	require.Nil(t, v8, "post-SELFDESTRUCT read of slot 8 returned zombie %x", v8)
	v9, err := bufReader.ReadAccountStorage(addr, &slot9)
	require.NoError(t, err)
	require.Nil(t, v9, "post-SELFDESTRUCT read of slot 9 returned zombie %x", v9)
}

// Regression for the GLOBAL flush-epoch guard added on 2026-04-28.
// Locks the invariant that any flush happening after the prefetcher
// captured its epoch causes Cache{Account,Storage}IfAbsentEpoch to
// reject the write — even if the addr was NOT in contractWipes (this
// is the gap that hit block 2520771: a sender's balance update isn't
// a SELFDESTRUCT, so wipedAtEpoch never stamped, and the wipedAtEpoch-
// only check let stale prefetcher writes through).
func TestPrefetcherFlushEpochGuard_RejectsAfterFlush(t *testing.T) {
	t.Parallel()
	buf := NewPlainStateBuffer()
	addr := types.HexToAddress("0xb8cc0f060aad92d4eb8b36b3b95ce9e90eb383d7")
	slot := hashFromByte(0x01)

	// 1. Prefetcher captures epoch BEFORE BeginRo.
	prefetcherEpoch := buf.CurrentFlushEpoch()

	// 2. A flush happens between capture and the prefetcher's write.
	//    StampWipes is what the async flusher calls — even with no
	//    contractWipes (regular balance/storage update), it bumps
	//    flushEpoch so subsequent IfAbsentEpoch writes can detect
	//    the interim flush.
	buf.StampWipes(nil)

	// 3. Prefetcher tries to publish stale value with the pre-flush epoch.
	//    Both account and storage paths must reject.
	require.False(t, buf.CacheAccountIfAbsentEpoch(addr, []byte{0xde, 0xad}, prefetcherEpoch),
		"CacheAccountIfAbsentEpoch must reject when prefetcherEpoch < flushEpoch.Load() — the block 2520771 bug fingerprint")
	require.False(t, buf.CacheStorageIfAbsentEpoch(addr, slot, []byte{0xde, 0xad}, prefetcherEpoch),
		"CacheStorageIfAbsentEpoch must reject when prefetcherEpoch < flushEpoch.Load()")

	// 4. LRU should still be empty (writes were rejected).
	if v, ok := buf.LookupReadAccount(addr); ok {
		t.Fatalf("account LRU got polluted: %x present=%v", v, ok)
	}
	if v, ok := buf.LookupReadStorage(addr, slot); ok {
		t.Fatalf("storage LRU got polluted: %x present=%v", v, ok)
	}
}

// Regression for the prefetcher-LRU-race subroot of the same bug
// family (project_prefetcher_lru_race.md, project_lru_empty_slot_leftover.md).
// The scenario: between SnapshotForFlush installing the in-flight snap
// and the bg goroutine completing ApplyTo, a prefetcher with its own
// stale RoTx (one whose snapshot pre-dates the wipe) calls
// CacheStorageIfAbsentEpoch with the pre-wipe value. Without the
// epoch guard, the prefetcher would re-introduce the zombie into LRU.
//
// The fix lives in two parts:
//   1. PlainStateBuffer.flushEpoch (atomic) bumps on each StampWipes;
//      prefetcher captures epoch BEFORE BeginRo; PutIfAbsentEpoch
//      rejects the cache write when capturedEpoch < wipedAtEpoch[addr].
//   2. The order in RefreshLRUForSnapshot at line 766-779 — StampWipes
//      runs BEFORE the LRU prefix sweep.
//
// This test pins the epoch-guard half: a stale-RoTx prefetcher cannot
// poison the LRU after a SELFDESTRUCT.
func TestSelfdestructWipe_PrefetcherEpochGuard_NoZombieReintroduce(t *testing.T) {
	// NOT t.Parallel() — initN42Tables mutates the global
	// kv.ChaindataTablesCfg map, racing across parallel sub-tests.
	initN42Tables(t)

	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	zombie := []byte{0x24, 0x32}

	buf := NewPlainStateBuffer()

	// Seed MDBX with the pre-wipe value (no LRU pollution yet).
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), zombie))
		require.NoError(t, rwTx.Commit())
	}

	// Step 1: prefetcher captures the current flush epoch BEFORE its
	// own RoTx — this is exactly what runSpeculative does at
	// prefetch_speculative.go:189 (CurrentFlushEpoch before BeginRo).
	prefetcherEpoch := buf.CurrentFlushEpoch()

	// Step 2: SELFDESTRUCT happens in the executor goroutine. Active buf
	// gets contractWipes appended. SnapshotForFlush installs the snap
	// in-flight; ApplyTo wipes MDBX.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		writer := NewBufferedPlainStateWriter(buf, roTx, 1)
		require.NoError(t, writer.DeleteAccount(addr, nil))
		require.NoError(t, writer.CreateContract(addr))
		roTx.Rollback()
	}
	snap := buf.SnapshotForFlush()
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snap.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
		buf.RefreshLRUForSnapshot(snap) // bumps wipedAtEpoch[addr]
	}

	// Step 3: prefetcher (with its STALE pre-wipe epoch) tries to
	// publish the zombie value into the LRU. The epoch guard must
	// reject this write.
	ok := buf.CacheStorageIfAbsentEpoch(addr, slot8, zombie, prefetcherEpoch)
	require.False(t, ok,
		"prefetcher with stale epoch %d (wipedAt=%d) should NOT be able to insert into LRU — epoch guard at buffered_plain_state.go is broken",
		prefetcherEpoch, buf.wipedAtEpoch)

	// Step 4: a fresh-epoch read sees no LRU entry and falls through
	// to MDBX (which was wiped by ApplyTo) — return value is nil/zero.
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()
	bufReader := NewBufferedPlainStateReader(buf, roTx)
	v, err := bufReader.ReadAccountStorage(addr, &slot8)
	require.NoError(t, err)
	require.Nil(t, v, "post-SELFDESTRUCT read after stale-prefetcher attempt returned zombie %x — epoch guard didn't hold", v)
}

// TestSelfdestructWipe_StaleRoTx_PostFlushWipe_NoPhantomChangeset
// reproduces the block 10941306 production regression more precisely
// than the existing CrossInterval test:
//
//   - Interval A SELFDESTRUCTs the contract.
//   - Main thread opens its long-lived RoTx_B BEFORE the bg flush of A
//     completes — RoTx_B's MDBX snapshot is pre-A-flush.
//   - bg flush of A completes: MDBX rows deleted, wipedAtEpoch stamped,
//     inFlight cleared.
//   - Interval B re-CREATE2s the same address. The writer is built on
//     RoTx_B (stale) with txOpenEpoch = pre-A-flush epoch.
//   - collectPreWipeSlots' MDBX cursor over RoTx_B still sees pre-wipe
//     rows. WITHOUT the wipedAtEpoch > txOpenEpoch guard, those rows
//     end up in the new block's storcs as phantom pre-wipe values —
//     exactly the diff-cs signature seen at 10941306 (slot 8 = 0x2432,
//     slot 9 = 0x043e for 0x...beef...e1, neither in reth's changeset).
//
// With the guard active (collectPreWipeSlots line 1735 in
// buffered_plain_state.go), the writer skips the MDBX scan entirely
// and the changeset stays empty.
func TestSelfdestructWipe_StaleRoTx_PostFlushWipe_NoPhantomChangeset(t *testing.T) {
	initN42Tables(t)

	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	slot8 := hashFromByte(8)
	slot9 := hashFromByte(9)
	zombie8 := []byte{0x24, 0x32}
	zombie9 := []byte{0x04, 0x3e}

	// Seed MDBX with the pre-wipe slot values.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, rwTx.Put(modules.Account, addr[:], liveAccountBytes(t)))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot8[:]), zombie8))
		require.NoError(t, rwTx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot9[:]), zombie9))
		require.NoError(t, rwTx.Commit())
	}

	buf := NewPlainStateBuffer()

	// Interval A: SELFDESTRUCT.
	{
		roTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		writer := NewBufferedPlainStateWriter(buf, roTx, 10941141)
		require.NoError(t, writer.DeleteAccount(addr, nil))
		require.NoError(t, writer.CreateContract(addr))
		roTx.Rollback()
	}
	snapA := buf.SnapshotForFlush()

	// Main thread opens RoTx_B BEFORE bg applies snapA. RoTx_B's
	// snapshot still shows the pre-A storage rows.
	roTxB, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTxB.Rollback()
	txOpenEpochB := buf.CurrentFlushEpoch()

	// Confirm the staleness: the cursor should still see zombie rows.
	{
		c, err := roTxB.Cursor(modules.Storage)
		require.NoError(t, err)
		seen := 0
		for k, _, err := c.Seek(addr[:]); k != nil; k, _, err = c.Next() {
			require.NoError(t, err)
			if len(k) < 20 || k[19] != addr[19] {
				break
			}
			seen++
		}
		c.Close()
		require.Equal(t, 2, seen,
			"RoTx_B opened before bg-A; expected to see 2 pre-A-flush storage rows (the bug's preconditions)")
	}

	// bg flush completes: MDBX rows deleted, wipedAtEpoch stamped.
	{
		rwTx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		require.NoError(t, snapA.ApplyTo(rwTx))
		require.NoError(t, rwTx.Commit())
		buf.RefreshLRUForSnapshot(snapA) // bumps wipedAtEpoch[addr]
	}

	// Simulate the next interval starting: a fresh empty SnapshotForFlush
	// replaces inFlight, dropping snapA's wipedStorage visibility. After
	// this point, collectPreWipeSlots' inFlight-snap check (line 1720)
	// returns false for addr — only the wipedAtEpoch guard can save us
	// from harvesting MDBX zombies via the stale RoTx.
	_ = buf.SnapshotForFlush()
	buf.ClearInFlight()

	// Sanity: a FRESH RoTx now sees zero rows. RoTx_B is still stale.
	{
		freshRoTx, err := db.BeginRo(context.Background())
		require.NoError(t, err)
		c, err := freshRoTx.Cursor(modules.Storage)
		require.NoError(t, err)
		seen := 0
		for k, _, err := c.Seek(addr[:]); k != nil; k, _, err = c.Next() {
			require.NoError(t, err)
			if len(k) < 20 || k[19] != addr[19] {
				break
			}
			seen++
		}
		c.Close()
		freshRoTx.Rollback()
		require.Equal(t, 0, seen, "fresh RoTx should see 0 rows after bg-A wipe; staging is broken")
	}

	// Interval B: same address re-CREATE2'd. Writer is built on the
	// STALE RoTx_B with the pre-A-flush epoch — this is the exact
	// production geometry. The wipedAtEpoch guard at
	// collectPreWipeSlots must skip the MDBX cursor scan.
	writer := NewBufferedPlainStateWriterAt(buf, roTxB, 10941306, txOpenEpochB)
	require.NoError(t, writer.DeleteAccount(addr, nil))
	require.NoError(t, writer.CreateContract(addr))

	csw := writer.ChangeSetWriter()
	for _, slot := range []types.Hash{slot8, slot9} {
		key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
		v, exists := csw.storageChanges[string(key)]
		require.False(t, exists,
			"interval B's storcs has phantom pre-wipe entry for slot=%x value=%x — collectPreWipeSlots' wipedAtEpoch guard didn't fire",
			slot[:], v)
	}
}
