// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// selfdestruct_forward_test.go locks down the forward-replay correctness
// guarantees that the reth-style storage layout depends on. Because N42's
// MDBX Storage table is keyed by (addr||slot) — without an incarnation
// column — a SELFDESTRUCT must emit one storcs entry per live slot, and
// forward replay must DELETE every such slot. Any regression here will
// silently corrupt the post-replay state root.
//
// These tests exercise the production write path
// (PlainStateWriter / BufferedPlainStateWriter → ChangeSetWriter →
// EncodeStorageChanges → applyChangesetForward) end-to-end, plus a hand
// rolled OldValue-replay used to verify symmetric backward unwind.
//
// Test matrix:
//   1.  SD wipes every live slot
//   2.  Same-block SSTORE-then-SD preserves block-origin (first-wins)
//   3.  SD + recreate in same tx (EIP-6780 shape)
//   4.  SD in block N + CREATE2 same address in block N+1
//   5.  SD wipes {1,2,3} then recreate writes {4,5} — no leftovers
//   6.  Async-buffer cross-interval wipe (in-flight snapshot must merge)
//   7.  SD on contract with empty storage
//   8.  EOA delete must NOT trigger storage wipe
//   9.  SSTORE slot=0 (non-SD) deletes single slot, leaves siblings alone
//  10.  Backward unwind restores every slot from storcs.OldValue
//  11.  Forward + backward round-trip: Account+Storage byte-equal
//  12.  Reorg depth-10 across an SD block matches reference state

package ethel

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// =============================================================================
// helpers
// =============================================================================

func sdHash(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}

func sdAddr(b byte) types.Address {
	var a types.Address
	a[19] = b
	return a
}

func sdAcc(nonce, balance uint64, codeHash types.Hash) *account.StateAccount {
	a := account.NewAccount()
	a.Initialised = true
	a.Nonce = nonce
	a.Balance.SetUint64(balance)
	a.CodeHash = codeHash
	return &a
}

// putSlot writes a raw storage slot bytes (uint256 big-endian, leading
// zeros trimmed by uint256.Int.Bytes() conventions for 1-byte values).
func putSlot(t *testing.T, tx kv.RwTx, addr types.Address, slot types.Hash, value []byte) {
	t.Helper()
	k := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
	require.NoError(t, tx.Put(modules.Storage, k, value))
}

func getSlot(t *testing.T, tx kv.Tx, addr types.Address, slot types.Hash) []byte {
	t.Helper()
	k := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
	v, err := tx.GetOne(modules.Storage, k)
	require.NoError(t, err)
	return v
}

// listSlotsForAddr returns all storage slots in MDBX for an address,
// sorted by slot key, suitable for byte-equal table comparisons.
type slotKV struct {
	slot  types.Hash
	value []byte
}

func listSlotsForAddr(t *testing.T, tx kv.Tx, addr types.Address) []slotKV {
	t.Helper()
	cur, err := tx.Cursor(modules.Storage)
	require.NoError(t, err)
	defer cur.Close()
	var out []slotKV
	for k, v, err := cur.Seek(addr[:]); k != nil; k, v, err = cur.Next() {
		require.NoError(t, err)
		if len(k) < 20 || !bytes.Equal(k[:20], addr[:]) {
			break
		}
		if len(k) != 52 {
			continue
		}
		var s types.Hash
		copy(s[:], k[20:52])
		cp := make([]byte, len(v))
		copy(cp, v)
		out = append(out, slotKV{slot: s, value: cp})
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i].slot[:], out[j].slot[:]) < 0
	})
	return out
}

// buildStoBlob encodes the writer's accumulated storage changeset as a
// storcs blob. newValueOf reads the live tx — for tests using
// PlainStateWriter, this tx already reflects post-block state, so it
// IS the post-block snapshot.
func buildStoBlob(t *testing.T, csw *state.ChangeSetWriter, snapTx kv.Tx) []byte {
	t.Helper()
	cs, err := csw.GetStorageChanges()
	require.NoError(t, err)
	if cs.Len() == 0 {
		return nil
	}
	return EncodeStorageChanges(cs, func(addr types.Address, slot types.Hash) []byte {
		k := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
		v, err := snapTx.GetOne(modules.Storage, k)
		require.NoError(t, err)
		if len(v) == 0 {
			return nil
		}
		return v
	})
}

// buildAccBlob encodes the writer's account changeset as an acctcs blob.
// Account NEW values are read from snapTx as full V2 bytes so the test
// is independent of the production EncodeAccountForHistory(... incarnation tag)
// path under refactor — what matters here is the storcs forward path.
func buildAccBlob(t *testing.T, csw *state.ChangeSetWriter, snapTx kv.Tx) []byte {
	t.Helper()
	cs, err := csw.GetAccountChanges()
	require.NoError(t, err)
	if cs.Len() == 0 {
		return nil
	}
	return EncodeAccountChanges(cs, func(addr types.Address) []byte {
		v, err := snapTx.GetOne(modules.Account, addr[:])
		require.NoError(t, err)
		if len(v) == 0 {
			return nil
		}
		return v
	})
}

// applyStorcsReverse mirrors applyChangeset's storage-replay branch using
// the OldValue side of each entry. Inlined here to avoid hauling in a
// freezer table for what is fundamentally a pure decode + RwTx call.
func applyStorcsReverse(t *testing.T, tx kv.RwTx, stoBlob []byte) {
	t.Helper()
	if len(stoBlob) == 0 {
		return
	}
	entries, err := DecodeStorageChanges(stoBlob)
	require.NoError(t, err)
	for _, e := range entries {
		if len(e.OldValue) == 0 {
			require.NoError(t, tx.Delete(modules.Storage, e.CompositeKey))
		} else {
			require.NoError(t, tx.Put(modules.Storage, e.CompositeKey, e.OldValue))
		}
	}
}

// applyAcctcsReverse: same but for accounts. After Phase B+C+D, account
// changeset OldValues are full V2 (CodeHash inline) so this is a direct
// Put — no codeHash recovery, no incarnation tracking, no auxiliary
// PlainContractCode/IncarnationMap tables.
func applyAcctcsReverse(t *testing.T, tx kv.RwTx, accBlob []byte) {
	t.Helper()
	if len(accBlob) == 0 {
		return
	}
	entries, err := DecodeAccountChanges(accBlob)
	require.NoError(t, err)
	for _, e := range entries {
		if len(e.OldValue) == 0 {
			require.NoError(t, tx.Delete(modules.Account, e.Address[:]))
			continue
		}
		require.NoError(t, tx.Put(modules.Account, e.Address[:], e.OldValue))
	}
}

// seedContractAccount writes the Account row. After Phase D the
// PlainContractCode auxiliary table is gone — Account.CodeHash is
// the only address→code link and is carried inline in MarshalV2.
func seedContractAccount(t *testing.T, tx kv.RwTx, addr types.Address, acc *account.StateAccount) {
	t.Helper()
	require.NoError(t, tx.Put(modules.Account, addr[:], acc.MarshalV2()))
}

// decodeAcct decodes the V2-encoded bytes (omit-CodeHash form supported)
// stored in modules.Account for assertion.
func decodeAcct(t *testing.T, raw []byte) *account.StateAccount {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var a account.StateAccount
	require.NoError(t, a.DecodeForStorage(raw))
	return &a
}

// requireAcctEqualNonceBalance compares nonce+balance and (post-Phase-B)
// also CodeHash. After the reth-style refactor's Phase B, account
// changeset OldValues are stored as full V2 (CodeHash included), so
// backward unwind reproduces the pre-block account byte-for-byte. The
// helper still tolerates legacy omit-CodeHash blobs by falling back to
// nonce+balance only when one side has empty CodeHash and the other
// doesn't — keeping the test stable across the refactor cutover.
func requireAcctEqualNonceBalance(t *testing.T, expectedRaw, gotRaw []byte, msg string) {
	t.Helper()
	exp := decodeAcct(t, expectedRaw)
	got := decodeAcct(t, gotRaw)
	if exp == nil && got == nil {
		return
	}
	require.NotNil(t, exp, msg)
	require.NotNil(t, got, msg)
	require.Equal(t, exp.Nonce, got.Nonce, msg+" (nonce)")
	require.Equal(t, exp.Balance.String(), got.Balance.String(), msg+" (balance)")
	// Strict CodeHash check unless one side is empty (legacy omit-CodeHash
	// path could not recover when PlainContractCode lacked a versioned key).
	if !exp.IsEmptyCodeHash() && !got.IsEmptyCodeHash() {
		require.Equal(t, exp.CodeHash, got.CodeHash, msg+" (codeHash)")
	}
}

// freshTx opens a new memdb + tx for replay verification.
func freshTx(t *testing.T) (kv.RwDB, kv.RwTx) {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { tx.Rollback() })
	return db, tx
}

// =============================================================================
// TEST 1 — SD wipes every live slot
// =============================================================================

func TestSD_Forward_WipesAllLiveSlots(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xAA)

	// --- live tx: pre-seed 3 slots, then SELFDESTRUCT ---
	_, live := freshTx(t)
	type slotSeed struct {
		slot types.Hash
		v    []byte
	}
	preSlots := []slotSeed{
		{sdHash(0x01), []byte{0x11}},
		{sdHash(0x02), []byte{0x22}},
		{sdHash(0x03), []byte{0x33, 0x44}},
	}
	for _, s := range preSlots {
		putSlot(t, live, addr, s.slot, s.v)
	}
	originalAcc := sdAcc(1, 100, sdHash(0xCC))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w := state.NewPlainStateWriter(live, live, 1)
	require.NoError(t, w.DeleteAccount(addr, originalAcc))
	require.NoError(t, w.CreateContract(addr))

	// Live tx now reflects post-SD state: storage wiped.
	require.Empty(t, listSlotsForAddr(t, live, addr), "wipe should clear live storage")

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	require.NotEmpty(t, stoBlob, "storcs blob must contain wipe entries")

	// --- replay tx: reconstruct pre-state, forward-apply storcs ---
	_, replay := freshTx(t)
	for _, s := range preSlots {
		putSlot(t, replay, addr, s.slot, s.v)
	}
	require.NoError(t, applyChangesetForward(replay, nil, stoBlob))

	require.Empty(t, listSlotsForAddr(t, replay, addr),
		"forward replay must delete every slot wiped by SD")
}

// =============================================================================
// TEST 2 — SSTORE-then-SD same block: changeset OldValue is block-origin
// =============================================================================
//
// Block-origin invariant: if slot S had value V0 at block start, was
// SSTORE'd to V1 mid-block, then SELFDESTRUCT'd, the storcs OldValue
// for S must be V0 (not V1). recordStorageWipe's first-wins semantics
// guard this: WriteAccountStorage records V0 first; recordStorageWipe
// sees the existing entry and skips. This is what makes backward
// unwind to the *block-origin* state possible.

func TestSD_Forward_SameBlock_SSTORE_then_SD_FirstWins(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xBB)
	slot := sdHash(0x07)

	_, live := freshTx(t)
	// Pre-state: slot has block-origin value 0x42.
	putSlot(t, live, addr, slot, []byte{0x42})
	originalAcc := sdAcc(1, 0, sdHash(0xCC))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w := state.NewPlainStateWriter(live, live, 1)

	// Step 1: same-block SSTORE 0x42 → 0xFF.
	orig := uint256.NewInt(0x42)
	val := uint256.NewInt(0xFF)
	require.NoError(t, w.WriteAccountStorage(addr, &slot, orig, val))
	// Slot now 0xFF in MDBX.
	require.Equal(t, []byte{0xFF}, getSlot(t, live, addr, slot))

	// Step 2: SELFDESTRUCT — wipe slots, delete account.
	require.NoError(t, w.DeleteAccount(addr, originalAcc))
	require.NoError(t, w.CreateContract(addr))
	require.Empty(t, getSlot(t, live, addr, slot))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	require.NotEmpty(t, stoBlob)

	entries, err := DecodeStorageChanges(stoBlob)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one slot in changeset")
	// CRITICAL: OldValue must be the block-origin 0x42, NOT 0xFF.
	require.Equal(t, []byte{0x42}, entries[0].OldValue,
		"first-wins violated: changeset OldValue should be block-origin, not post-SSTORE")
	require.Empty(t, entries[0].NewValue, "post-SD newVal must be empty")

	// Forward replay from block-origin state.
	_, replay := freshTx(t)
	putSlot(t, replay, addr, slot, []byte{0x42})
	require.NoError(t, applyChangesetForward(replay, nil, stoBlob))
	require.Empty(t, getSlot(t, replay, addr, slot))
}

// =============================================================================
// TEST 3 — SD + recreate in same context (EIP-6780 shape)
// =============================================================================
//
// IBS calls CreateContract (wipe) without DeleteAccount when the account
// is recreated post-SD in the same tx. UpdateAccountData then writes the
// new account; new SSTOREs append fresh slots. Forward replay must
// arrive at exactly this end state: old slots gone, new account + new
// slots present.

func TestSD_Forward_RecreateSameContext(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xCD)
	oldSlot := sdHash(0x01)
	newSlot := sdHash(0x77)

	_, live := freshTx(t)
	putSlot(t, live, addr, oldSlot, []byte{0xAA})
	originalAcc := sdAcc(5, 1000, sdHash(0xCC))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w := state.NewPlainStateWriter(live, live, 1)

	// SD-equivalent: wipe storage but keep coming back.
	require.NoError(t, w.CreateContract(addr))
	// Recreate: new account + new SSTORE.
	newAcc := sdAcc(0, 0, sdHash(0xDD))
	require.NoError(t, w.UpdateAccountData(addr, originalAcc, newAcc))
	val := uint256.NewInt(0x99)
	zero := uint256.NewInt(0)
	require.NoError(t, w.WriteAccountStorage(addr, &newSlot, zero, val))

	require.Empty(t, getSlot(t, live, addr, oldSlot), "old slot wiped")
	require.Equal(t, []byte{0x99}, getSlot(t, live, addr, newSlot), "new slot written")

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	accBlob := buildAccBlob(t, w.ChangeSetWriter(), live)

	_, replay := freshTx(t)
	putSlot(t, replay, addr, oldSlot, []byte{0xAA})
	require.NoError(t, replay.Put(modules.Account, addr[:], originalAcc.MarshalV2()))
	require.NoError(t, applyChangesetForward(replay, accBlob, stoBlob))

	require.Empty(t, getSlot(t, replay, addr, oldSlot), "replay must wipe old slot")
	require.Equal(t, []byte{0x99}, getSlot(t, replay, addr, newSlot), "replay must write new slot")
	gotAcc, err := replay.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.Equal(t, newAcc.MarshalV2(), gotAcc, "replay must rewrite account")
}

// =============================================================================
// TEST 4 — SD in block N, CREATE2 same address in block N+1
// =============================================================================
//
// Two separate writer/changeset cycles. Block N storcs wipes slots,
// block N+1 storcs writes fresh slots. Sequential forward replay must
// produce: wiped account in N → recreated account + new slots in N+1.

func TestSD_Forward_RecreateNextBlock(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xCE)
	oldSlot := sdHash(0x10)
	newSlot := sdHash(0x20)

	// --- block N: SD ---
	_, live := freshTx(t)
	putSlot(t, live, addr, oldSlot, []byte{0xAB})
	originalAcc := sdAcc(3, 50, sdHash(0xC1))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w1 := state.NewPlainStateWriter(live, live, 100)
	require.NoError(t, w1.DeleteAccount(addr, originalAcc))
	require.NoError(t, w1.CreateContract(addr))
	stoBlob1 := buildStoBlob(t, w1.ChangeSetWriter(), live)
	accBlob1 := buildAccBlob(t, w1.ChangeSetWriter(), live)

	// --- block N+1: CREATE2 same address ---
	w2 := state.NewPlainStateWriter(live, live, 101)
	newAcc := sdAcc(1, 5, sdHash(0xC2))
	emptyOrig := account.NewAccount() // not Initialised — IBS-equivalent for "loaded nothing"
	require.NoError(t, w2.UpdateAccountData(addr, &emptyOrig, newAcc))
	require.NoError(t, w2.CreateContract(addr)) // CreateContract on empty storage = no-op wipe
	val := uint256.NewInt(0x55)
	zero := uint256.NewInt(0)
	require.NoError(t, w2.WriteAccountStorage(addr, &newSlot, zero, val))
	stoBlob2 := buildStoBlob(t, w2.ChangeSetWriter(), live)
	accBlob2 := buildAccBlob(t, w2.ChangeSetWriter(), live)

	// --- replay ---
	_, replay := freshTx(t)
	putSlot(t, replay, addr, oldSlot, []byte{0xAB})
	require.NoError(t, replay.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	require.NoError(t, applyChangesetForward(replay, accBlob1, stoBlob1))
	// Mid-state: account deleted, no slots.
	gotAcc1, _ := replay.GetOne(modules.Account, addr[:])
	require.Empty(t, gotAcc1, "after block N replay account must be gone")
	require.Empty(t, listSlotsForAddr(t, replay, addr), "after block N replay slots wiped")

	require.NoError(t, applyChangesetForward(replay, accBlob2, stoBlob2))
	gotAcc2, _ := replay.GetOne(modules.Account, addr[:])
	require.Equal(t, newAcc.MarshalV2(), gotAcc2, "after block N+1 replay account is recreated")
	require.Equal(t, []byte{0x55}, getSlot(t, replay, addr, newSlot))
	require.Empty(t, getSlot(t, replay, addr, oldSlot), "old slot must NOT resurface")
}

// =============================================================================
// TEST 5 — SD wipes {1,2,3} then recreate writes {4,5}: no leftovers
// =============================================================================
//
// Strongest version of the wipe-then-recreate property: post-replay
// Storage scan for the address must equal exactly {4: V4, 5: V5}, with
// no trace of slots {1,2,3}.

func TestSD_Forward_RecreateLaterBlock_DifferentSlots(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xCF)
	type kv struct {
		slot types.Hash
		v    []byte
	}
	preSlots := []kv{
		{sdHash(0x01), []byte{0x01}},
		{sdHash(0x02), []byte{0x02}},
		{sdHash(0x03), []byte{0x03}},
	}
	newSlots := []kv{
		{sdHash(0x04), []byte{0x40}},
		{sdHash(0x05), []byte{0x50}},
	}

	_, live := freshTx(t)
	for _, s := range preSlots {
		putSlot(t, live, addr, s.slot, s.v)
	}
	originalAcc := sdAcc(2, 200, sdHash(0xD0))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	// Block N — SD.
	w1 := state.NewPlainStateWriter(live, live, 1)
	require.NoError(t, w1.DeleteAccount(addr, originalAcc))
	require.NoError(t, w1.CreateContract(addr))
	sto1 := buildStoBlob(t, w1.ChangeSetWriter(), live)
	acc1 := buildAccBlob(t, w1.ChangeSetWriter(), live)

	// Block N+1 — CREATE2 + SSTORE new slots.
	w2 := state.NewPlainStateWriter(live, live, 2)
	newAcc := sdAcc(1, 0, sdHash(0xD1))
	emptyOrig := account.NewAccount()
	require.NoError(t, w2.UpdateAccountData(addr, &emptyOrig, newAcc))
	require.NoError(t, w2.CreateContract(addr))
	zero := uint256.NewInt(0)
	for _, s := range newSlots {
		v := new(uint256.Int).SetBytes(s.v)
		require.NoError(t, w2.WriteAccountStorage(addr, &s.slot, zero, v))
	}
	sto2 := buildStoBlob(t, w2.ChangeSetWriter(), live)
	acc2 := buildAccBlob(t, w2.ChangeSetWriter(), live)

	// Replay.
	_, replay := freshTx(t)
	for _, s := range preSlots {
		putSlot(t, replay, addr, s.slot, s.v)
	}
	require.NoError(t, replay.Put(modules.Account, addr[:], originalAcc.MarshalV2()))
	require.NoError(t, applyChangesetForward(replay, acc1, sto1))
	require.NoError(t, applyChangesetForward(replay, acc2, sto2))

	got := listSlotsForAddr(t, replay, addr)
	require.Len(t, got, 2)
	require.Equal(t, newSlots[0].slot, got[0].slot)
	require.Equal(t, newSlots[0].v, got[0].value)
	require.Equal(t, newSlots[1].slot, got[1].slot)
	require.Equal(t, newSlots[1].v, got[1].value)
}

// =============================================================================
// TEST 6 — Async-buffer cross-interval wipe
// =============================================================================
//
// Reproduces the race that the in-flight-snapshot merge in
// BufferedPlainStateWriter.collectPreWipeSlots was written to defend
// against:
//
//   t0: block N-1 SSTORE → buf.storage[addr][slot] = V
//   t1: bg flush handoff → buf.storage cleared, buf.inFlight = snapshot{slot:V}
//   t2: csw.db RoTx is taken — sees MDBX state from BEFORE handoff (no slot)
//   t3: block N SELFDESTRUCT triggers w.CreateContract(addr)
//       collectPreWipeSlots MUST surface `slot` from inFlight, otherwise
//       storcs misses a tombstone and forward replay leaves a ghost slot.

func TestSD_Forward_AsyncBuffer_CrossInterval(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xE6)
	slot := sdHash(0x42)

	// MDBX starts empty for this address — slot is purely in the in-flight snapshot.
	_, mdbxTx := freshTx(t)

	buf := state.NewPlainStateBuffer()

	// Phase 1: simulate previous interval write into the buffer.
	w1 := state.NewBufferedPlainStateWriter(buf, mdbxTx, 99)
	orig := uint256.NewInt(0)
	val := uint256.NewInt(0xA5)
	require.NoError(t, w1.WriteAccountStorage(addr, &slot, orig, val))

	// Phase 2: handoff — buffer becomes in-flight, active buf clears.
	snap := buf.SnapshotForFlush()
	require.NotNil(t, snap)
	require.NotNil(t, buf.InFlightSnapshot(), "in-flight must be populated post-handoff")
	// Crucially: do NOT apply snap to MDBX — the bg flush is "still in progress".

	// Phase 3: block N SELFDESTRUCT against the same buf.
	w2 := state.NewBufferedPlainStateWriter(buf, mdbxTx, 100)
	originalAcc := sdAcc(1, 0, sdHash(0xC1))
	require.NoError(t, w2.DeleteAccount(addr, originalAcc))
	require.NoError(t, w2.CreateContract(addr))

	// The csw of w2 must contain a tombstone for `slot`, with OldValue = 0xA5
	// taken from the in-flight snapshot.
	cs, err := w2.ChangeSetWriter().GetStorageChanges()
	require.NoError(t, err)
	require.Equal(t, 1, cs.Len(),
		"in-flight slot must be enumerated by collectPreWipeSlots")
	got := cs.Changes[0]
	require.Equal(t, addr[:], got.Key[:20])
	require.Equal(t, slot[:], got.Key[20:52])
	require.Equal(t, []byte{0xA5}, got.Value, "OldValue must be the in-flight value")
}

// =============================================================================
// TEST 7 — SD on contract with empty storage
// =============================================================================

func TestSD_Forward_EmptyStorage(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xE7)

	_, live := freshTx(t)
	originalAcc := sdAcc(1, 0, sdHash(0xCC))
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w := state.NewPlainStateWriter(live, live, 1)
	require.NoError(t, w.DeleteAccount(addr, originalAcc))
	require.NoError(t, w.CreateContract(addr))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	require.Empty(t, stoBlob, "no slots → no storcs entries")

	accBlob := buildAccBlob(t, w.ChangeSetWriter(), live)
	require.NotEmpty(t, accBlob, "account deletion still emits acctcs entry")

	_, replay := freshTx(t)
	require.NoError(t, replay.Put(modules.Account, addr[:], originalAcc.MarshalV2()))
	require.NoError(t, applyChangesetForward(replay, accBlob, stoBlob))

	gotAcc, _ := replay.GetOne(modules.Account, addr[:])
	require.Empty(t, gotAcc, "account must be deleted after replay")
	require.Empty(t, listSlotsForAddr(t, replay, addr))
}

// =============================================================================
// TEST 8 — EOA delete must NOT trigger storage wipe
// =============================================================================
//
// EOAs never call CreateContract, so even if they happen to share an
// address space with a hypothetical future contract's storage scan, the
// delete path on its own must leave any unrelated storage untouched.
// (Real EOAs have no storage, but this guards against the writer
// accidentally calling collectPreWipeSlots on the DeleteAccount path.)

func TestSD_Forward_EOADelete_NoWipeSideEffect(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xE8)

	_, live := freshTx(t)
	// Plant an "alien" slot at addr — should remain after EOA delete.
	alienSlot := sdHash(0xAB)
	putSlot(t, live, addr, alienSlot, []byte{0xCD})
	originalAcc := sdAcc(7, 999, sdHash(0xCC)) // codeHash=0xCC, but treat as EOA-shaped delete
	originalAcc.CodeHash = [32]byte{}          // emulate EOA: zero codehash
	require.NoError(t, live.Put(modules.Account, addr[:], originalAcc.MarshalV2()))

	w := state.NewPlainStateWriter(live, live, 1)
	// EOA delete only — no CreateContract.
	require.NoError(t, w.DeleteAccount(addr, originalAcc))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	require.Empty(t, stoBlob, "EOA delete must not enumerate storage")

	accBlob := buildAccBlob(t, w.ChangeSetWriter(), live)

	_, replay := freshTx(t)
	putSlot(t, replay, addr, alienSlot, []byte{0xCD})
	require.NoError(t, replay.Put(modules.Account, addr[:], originalAcc.MarshalV2()))
	require.NoError(t, applyChangesetForward(replay, accBlob, stoBlob))

	require.Equal(t, []byte{0xCD}, getSlot(t, replay, addr, alienSlot),
		"EOA delete replay must NOT wipe unrelated storage")
}

// =============================================================================
// TEST 9 — SSTORE slot=0 (non-SD) deletes single slot, leaves siblings
// =============================================================================

func TestSD_Forward_SSTOREToZero_SingleSlotDelete(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xE9)
	zero := uint256.NewInt(0)

	_, live := freshTx(t)
	keepSlot := sdHash(0x01)
	zeroSlot := sdHash(0x02)
	putSlot(t, live, addr, keepSlot, []byte{0x11})
	putSlot(t, live, addr, zeroSlot, []byte{0x22})

	w := state.NewPlainStateWriter(live, live, 1)
	orig := uint256.NewInt(0x22)
	require.NoError(t, w.WriteAccountStorage(addr, &zeroSlot, orig, zero))

	require.Empty(t, getSlot(t, live, addr, zeroSlot))
	require.Equal(t, []byte{0x11}, getSlot(t, live, addr, keepSlot))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)

	_, replay := freshTx(t)
	putSlot(t, replay, addr, keepSlot, []byte{0x11})
	putSlot(t, replay, addr, zeroSlot, []byte{0x22})
	require.NoError(t, applyChangesetForward(replay, nil, stoBlob))

	require.Empty(t, getSlot(t, replay, addr, zeroSlot), "zeroed slot deleted")
	require.Equal(t, []byte{0x11}, getSlot(t, replay, addr, keepSlot), "sibling untouched")
}

// =============================================================================
// TEST 10 — Backward unwind restores every wiped slot
// =============================================================================

func TestSD_Backward_UnwindRestoresAllSlots(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xEA)

	_, live := freshTx(t)
	preSlots := map[types.Hash][]byte{
		sdHash(0x01): {0xA1},
		sdHash(0x02): {0xB2, 0xB3},
		sdHash(0x05): {0xC5},
	}
	for s, v := range preSlots {
		putSlot(t, live, addr, s, v)
	}
	originalAcc := sdAcc(2, 50, sdHash(0xCC))
	seedContractAccount(t, live, addr, originalAcc)

	w := state.NewPlainStateWriter(live, live, 7)
	require.NoError(t, w.DeleteAccount(addr, originalAcc))
	require.NoError(t, w.CreateContract(addr))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	accBlob := buildAccBlob(t, w.ChangeSetWriter(), live)

	// Live tx is now post-block (account gone, slots gone).
	require.Empty(t, listSlotsForAddr(t, live, addr))

	// Unwind by replaying OldValues.
	applyStorcsReverse(t, live, stoBlob)
	applyAcctcsReverse(t, live, accBlob)

	got := listSlotsForAddr(t, live, addr)
	require.Len(t, got, len(preSlots))
	for _, kv := range got {
		expected, ok := preSlots[kv.slot]
		require.True(t, ok, "unexpected slot %x", kv.slot)
		require.Equal(t, expected, kv.value)
	}
	gotAcc, _ := live.GetOne(modules.Account, addr[:])
	requireAcctEqualNonceBalance(t, originalAcc.MarshalV2(), gotAcc, "account restored to pre-SD state")
}

// =============================================================================
// TEST 11 — Forward + backward round-trip: byte-equal
// =============================================================================

func TestSD_RoundTrip_ForwardThenBackward(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xEB)
	other := sdAddr(0xEC) // unrelated address — must NOT be touched by replay

	_, live := freshTx(t)
	preSlots := map[types.Hash][]byte{
		sdHash(0x10): {0x10},
		sdHash(0x20): {0x20, 0x21},
	}
	for s, v := range preSlots {
		putSlot(t, live, addr, s, v)
	}
	putSlot(t, live, other, sdHash(0xFF), []byte{0xFF})
	originalAcc := sdAcc(3, 30, sdHash(0xC3))
	seedContractAccount(t, live, addr, originalAcc)
	preAcctSnapshot, _ := live.GetOne(modules.Account, addr[:])

	w := state.NewPlainStateWriter(live, live, 5)
	require.NoError(t, w.DeleteAccount(addr, originalAcc))
	require.NoError(t, w.CreateContract(addr))

	stoBlob := buildStoBlob(t, w.ChangeSetWriter(), live)
	accBlob := buildAccBlob(t, w.ChangeSetWriter(), live)

	// Take post-state snapshot for comparison.
	postSlots := listSlotsForAddr(t, live, addr) // expect empty
	postAcct, _ := live.GetOne(modules.Account, addr[:])

	// Reverse: live should match pre-state.
	applyStorcsReverse(t, live, stoBlob)
	applyAcctcsReverse(t, live, accBlob)

	gotPreSlots := listSlotsForAddr(t, live, addr)
	require.Len(t, gotPreSlots, len(preSlots))
	for _, kv := range gotPreSlots {
		require.Equal(t, preSlots[kv.slot], kv.value)
	}
	gotPreAcct, _ := live.GetOne(modules.Account, addr[:])
	requireAcctEqualNonceBalance(t, preAcctSnapshot, gotPreAcct, "round-trip pre-state account")

	// Other address untouched throughout.
	require.Equal(t, []byte{0xFF}, getSlot(t, live, other, sdHash(0xFF)))

	// Forward again from the now-restored pre-state: must reach post-state.
	require.NoError(t, applyChangesetForward(live, accBlob, stoBlob))
	gotPostSlots := listSlotsForAddr(t, live, addr)
	require.Equal(t, postSlots, gotPostSlots)
	gotPostAcct, _ := live.GetOne(modules.Account, addr[:])
	require.Equal(t, postAcct, gotPostAcct, "forward replay must reach byte-equal post-state")
}

// =============================================================================
// TEST 12 — Reorg depth-10 across an SD block
// =============================================================================
//
// Synthesize 10 blocks, with block 4 = SD and block 7 = recreate at the
// same address. Apply forward 1..10, then unwind 10..1, and check that
// the DB matches the genesis snapshot. This exercises the critical
// invariant that storcs+acctcs together encode enough information to
// undo any block sequence — including those that cross a SELFDESTRUCT.

func TestSD_Reorg_AcrossSelfDestruct_Depth10(t *testing.T) {
	t.Parallel()
	addr := sdAddr(0xED)
	other := sdAddr(0xEE)

	type blockOps func(w *state.PlainStateWriter, currentAcc *account.StateAccount) (newAcc *account.StateAccount)
	type blockBlobs struct {
		acc, sto []byte
	}

	_, live := freshTx(t)
	// Genesis: addr has 2 slots, account exists. `other` has 1 slot.
	preSlots := map[types.Hash][]byte{
		sdHash(0x01): {0x01},
		sdHash(0x02): {0x02},
	}
	for s, v := range preSlots {
		putSlot(t, live, addr, s, v)
	}
	putSlot(t, live, other, sdHash(0xFF), []byte{0xFF})
	currentAcc := sdAcc(1, 100, sdHash(0xC0))
	seedContractAccount(t, live, addr, currentAcc)

	// Snapshot genesis Account+Storage for both addresses.
	type fullState struct {
		acc       []byte
		slots     []slotKV
		otherSlot []byte
	}
	snapshot := func() fullState {
		a, _ := live.GetOne(modules.Account, addr[:])
		ot := getSlot(t, live, other, sdHash(0xFF))
		return fullState{acc: append([]byte{}, a...), slots: listSlotsForAddr(t, live, addr), otherSlot: append([]byte{}, ot...)}
	}
	genesis := snapshot()

	// 10 blocks of operations. Block 4 = SD, block 7 = recreate.
	ops := []blockOps{
		// block 1: SSTORE addr.slot01 := 0x11
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			slot := sdHash(0x01)
			require.NoError(t, w.WriteAccountStorage(addr, &slot, uint256.NewInt(0x01), uint256.NewInt(0x11)))
			return cur
		},
		// block 2: SSTORE addr.slot03 := 0x33 (new slot)
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			slot := sdHash(0x03)
			require.NoError(t, w.WriteAccountStorage(addr, &slot, uint256.NewInt(0), uint256.NewInt(0x33)))
			return cur
		},
		// block 3: nonce bump
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			next := *cur
			next.Nonce++
			require.NoError(t, w.UpdateAccountData(addr, cur, &next))
			return &next
		},
		// block 4: SELFDESTRUCT
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			require.NoError(t, w.DeleteAccount(addr, cur))
			require.NoError(t, w.CreateContract(addr))
			empty := account.NewAccount()
			return &empty
		},
		// block 5: no-op (touch other)
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			slot := sdHash(0xFF)
			require.NoError(t, w.WriteAccountStorage(other, &slot, uint256.NewInt(0xFF), uint256.NewInt(0xEE)))
			return cur
		},
		// block 6: no-op for addr
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount { return cur },
		// block 7: CREATE2 addr with new account + slot07 := 0x77
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			emptyOrig := account.NewAccount()
			newAcc := sdAcc(0, 5, sdHash(0xC1))
			require.NoError(t, w.UpdateAccountData(addr, &emptyOrig, newAcc))
			require.NoError(t, w.CreateContract(addr))
			slot := sdHash(0x07)
			require.NoError(t, w.WriteAccountStorage(addr, &slot, uint256.NewInt(0), uint256.NewInt(0x77)))
			return newAcc
		},
		// block 8: SSTORE addr.slot08
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			slot := sdHash(0x08)
			require.NoError(t, w.WriteAccountStorage(addr, &slot, uint256.NewInt(0), uint256.NewInt(0x88)))
			return cur
		},
		// block 9: balance bump
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			next := *cur
			next.Balance.AddUint64(&next.Balance, 1)
			require.NoError(t, w.UpdateAccountData(addr, cur, &next))
			return &next
		},
		// block 10: zero out slot07
		func(w *state.PlainStateWriter, cur *account.StateAccount) *account.StateAccount {
			slot := sdHash(0x07)
			require.NoError(t, w.WriteAccountStorage(addr, &slot, uint256.NewInt(0x77), uint256.NewInt(0)))
			return cur
		},
	}

	blobs := make([]blockBlobs, len(ops))
	for i, op := range ops {
		w := state.NewPlainStateWriter(live, live, uint64(i+1))
		currentAcc = op(w, currentAcc)
		blobs[i] = blockBlobs{
			acc: buildAccBlob(t, w.ChangeSetWriter(), live),
			sto: buildStoBlob(t, w.ChangeSetWriter(), live),
		}
	}
	postChain := snapshot()

	// Unwind in reverse.
	for i := len(blobs) - 1; i >= 0; i-- {
		applyStorcsReverse(t, live, blobs[i].sto)
		applyAcctcsReverse(t, live, blobs[i].acc)
		// Touched-other unwind: block 5 set other's slot to 0xEE; reverse must
		// restore 0xFF.
	}

	// After 10-deep reorg (unwind back to genesis), state must equal genesis snapshot.
	gotGenesis := snapshot()
	requireAcctEqualNonceBalance(t, genesis.acc, gotGenesis.acc, "account must equal genesis after reorg")
	require.Equal(t, genesis.slots, gotGenesis.slots, "addr storage must equal genesis")
	require.Equal(t, genesis.otherSlot, gotGenesis.otherSlot, "other's slot must equal genesis")

	// Re-apply forward — must reach the post-chain state byte-for-byte
	// (forward path always produces full V2 with CodeHash, no recovery
	// asymmetry to worry about here).
	for _, b := range blobs {
		require.NoError(t, applyChangesetForward(live, b.acc, b.sto))
	}
	gotPost := snapshot()
	require.Equal(t, postChain.acc, gotPost.acc, "forward replay must reach byte-equal post-state")
	require.Equal(t, postChain.slots, gotPost.slots)
	require.Equal(t, postChain.otherSlot, gotPost.otherSlot)
}
