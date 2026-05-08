// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestCreateContract_RecordsPreWipeSlots verifies that CreateContract on a
// ChangeSetWriter-aware writer enumerates pre-destruction storage slots from
// the current visible state (buffer ∪ MDBX) and records each as an old-value
// entry in the storage changeset, so backward unwind past a SELFDESTRUCT
// correctly restores the wiped state. Mirrors reth's write_state_reverts
// wiped-storage enumeration semantics.
func TestCreateContract_RecordsPreWipeSlots_Buffered(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xAA
	// Another address, to verify prefix seek never spills across contracts.
	var other types.Address
	other[19] = 0xBB

	// Pre-populate MDBX with 3 slots for addr and 1 slot for `other`.
	type slotVal struct {
		slot types.Hash
		v    []byte
	}
	seed := []slotVal{
		{slot: hashFromByte(0x01), v: []byte{0x11}},
		{slot: hashFromByte(0x02), v: []byte{0x22}},
		{slot: hashFromByte(0x03), v: []byte{0x33}},
	}
	for _, s := range seed {
		k := modules.PlainGenerateCompositeStorageKey(addr[:], s.slot[:])
		require.NoError(t, tx.Put(modules.Storage, k, s.v))
	}
	// `other`'s slot must NOT bleed into addr's changeset.
	otherSlot := hashFromByte(0x01)
	otherKey := modules.PlainGenerateCompositeStorageKey(
		other[:], otherSlot[:])
	require.NoError(t, tx.Put(modules.Storage, otherKey, []byte{0x99}))

	buf := NewPlainStateBuffer()
	w := NewBufferedPlainStateWriter(buf, tx, 1)

	// Simulate an earlier SSTORE in the same block that overwrote slot 0x02
	// with a new value. The changeset must contain the block-origin value
	// 0x22 regardless of what CreateContract enumerates later.
	csw := w.ChangeSetWriter()
	slot2 := hashFromByte(0x02)
	preOrigKey := modules.PlainGenerateCompositeStorageKey(addr[:], slot2[:])
	csw.storageChanges[[52]byte(preOrigKey)] = []byte{0x22} // block-origin
	csw.storageChanged[addr] = true
	// Buffer reflects the post-SSTORE value (0xFF) for slot 0x02.
	buf.storage[addr] = map[types.Hash]storageEntry{
		hashFromByte(0x02): storageEntryFromBytes([]byte{0xFF}),
	}

	require.NoError(t, w.CreateContract(addr))

	// Changeset must now hold all 3 slots for addr, each with their
	// pre-destruction (block-origin or MDBX) value. The earlier-recorded
	// block-origin value for slot 0x02 must be preserved (first-wins).
	for _, s := range seed {
		k := modules.PlainGenerateCompositeStorageKey(addr[:], s.slot[:])
		got, ok := csw.storageChanges[[52]byte(k)]
		require.True(t, ok, "slot %x missing from storageChanges", s.slot)
		require.Equal(t, s.v, got, "slot %x wrong value", s.slot)
	}
	require.True(t, csw.storageChanged[addr])

	// `other`'s slot must NOT appear in addr's changeset.
	for k := range csw.storageChanges {
		if string(k[:20]) == string(other[:]) {
			t.Fatalf("unrelated contract slot leaked into changeset: %x", k)
		}
	}

	// Wipe bookkeeping must be set.
	require.Len(t, buf.ContractWipes(), 1)
	require.Equal(t, addr, buf.ContractWipes()[0])
	_, wiped := buf.wipedStorage[addr]
	require.True(t, wiped)
}

// TestCreateContract_RecordsPreWipeSlots_Plain exercises the non-buffered
// PlainStateWriter path. PlainStateWriter writes SSTOREs directly to MDBX, so
// by the time CreateContract runs MDBX already reflects the post-SSTORE
// values; csw's first-wins must still preserve the earlier block-origin
// entries captured via WriteAccountStorage.
func TestCreateContract_RecordsPreWipeSlots_Plain(t *testing.T) {
	t.Parallel()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xCC

	// Pre-populate 2 slots directly in MDBX (simulating earlier blocks).
	slots := []types.Hash{hashFromByte(0x10), hashFromByte(0x20)}
	values := [][]byte{{0xAA}, {0xBB}}
	for i, s := range slots {
		k := modules.PlainGenerateCompositeStorageKey(addr[:], s[:])
		require.NoError(t, tx.Put(modules.Storage, k, values[i]))
	}

	w := NewPlainStateWriter(tx, tx, 1)
	csw := w.ChangeSetWriter()

	// Simulate an earlier block-origin entry for slot 0x10 that must survive.
	originKey := modules.PlainGenerateCompositeStorageKey(addr[:], slots[0][:])
	csw.storageChanges[[52]byte(originKey)] = []byte{0x00} // block-origin was 0 (slot was created earlier this block)
	csw.storageChanged[addr] = true

	require.NoError(t, w.CreateContract(addr))

	// First-wins preserved the earlier block-origin 0x00 for slot 0x10.
	got1, ok := csw.storageChanges[[52]byte(originKey)]
	require.True(t, ok)
	require.Equal(t, []byte{0x00}, got1, "block-origin for slot 0x10 must be preserved")

	// Slot 0x20 was enumerated via cursor and recorded with MDBX value.
	k20 := modules.PlainGenerateCompositeStorageKey(addr[:], slots[1][:])
	got2, ok := csw.storageChanges[[52]byte(k20)]
	require.True(t, ok)
	require.Equal(t, []byte{0xBB}, got2)

	// PlainStateWriter's wipeAccountStorage must have actually removed the
	// slots from MDBX (the wipe is part of CreateContract).
	leftover1, _ := tx.GetOne(modules.Storage, originKey)
	leftover2, _ := tx.GetOne(modules.Storage, k20)
	require.Nil(t, leftover1)
	require.Nil(t, leftover2)
}

func hashFromByte(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}

// TestWriteAccountStorage_FirstWinsAfterWipe pins the ordering that happens on
// SELFDESTRUCT+CREATE2 metamorphic blocks: recordStorageWipe writes the
// pre-block V_OLD into storageChanges first, then MakeWriteSet's updateTrie
// calls WriteAccountStorage for the CREATE2-era SSTORE with original=0 (the
// fresh stateObject's blockOriginStorage default). Without first-wins the
// 0 clobbers V_OLD and backward unwind past the metamorphic block restores
// 0 instead of the true block-origin value.
func TestWriteAccountStorage_FirstWinsAfterWipe(t *testing.T) {
	t.Parallel()
	var addr types.Address
	addr[19] = 0xDD
	slot := hashFromByte(0x42)
	compKey := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])

	csw := NewChangeSetWriter()
	vOld := []byte{0x01, 0x02, 0x03}
	csw.storageChanges[[52]byte(compKey)] = vOld
	csw.storageChanged[addr] = true

	orig := uint256.NewInt(0)    // post-create blockOriginStorage default
	val := uint256.NewInt(0x99) // SSTORE'd value in CREATE2-era tx
	require.NoError(t, csw.WriteAccountStorage(addr, slot, *orig, *val))

	got, ok := csw.storageChanges[[52]byte(compKey)]
	require.True(t, ok)
	require.Equal(t, vOld, got, "first-wins must preserve recordStorageWipe's V_OLD")
	require.True(t, csw.storageChanged[addr])
}
