package mptproof

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// g49WritePlainTables writes PlainAccountState / PlainStorageState
// rows using the SAME table config NewRethLeafSource itself opens
// with (source.go's WithTableCfg): neither table is flagged DupSort.
// One (addr -> value) row and, at most, ONE storage dup per address —
// writing a second slot for the same address would silently overwrite
// the first because the underlying MDBX table isn't DupSort (see the
// defect note on TestRethLeafSource_MultiSlotPerAddr_Defect below).
func g49WritePlainTables(t *testing.T, dir string, accounts []g49FixtureAccount) {
	t.Helper()
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(1) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethPlainAccountStateTable] = kv.TableCfgItem{}
			d[rethPlainStorageStateTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open plain mdbx fixture: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	defer tx.Rollback()

	for _, a := range accounts {
		if err := tx.Put(rethPlainAccountStateTable, a.addr[:], a.value); err != nil {
			t.Fatalf("put PlainAccountState: %v", err)
		}
		for slot, v := range a.slots {
			sv := append(append([]byte{}, slot[:]...), v...)
			if err := tx.Put(rethPlainStorageStateTable, a.addr[:], sv); err != nil {
				t.Fatalf("put PlainStorageState: %v", err)
			}
			break // only one slot per addr — see defect note above.
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit plain fixture: %v", err)
	}
}

func TestRethLeafSource_AccountAndStorageValue(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "plain")
	accounts := make([]g49FixtureAccount, 5)
	for i := range accounts {
		addr := g49MkAddr(byte(i*5 + 2))
		slot := g49MkSlot(byte(i + 1))
		accounts[i] = g49FixtureAccount{
			addr:  addr,
			value: []byte{byte(i), 0x11, 0x22},
			slots: map[[32]byte][]byte{slot: {byte(i), 0x33}},
		}
	}
	g49WritePlainTables(t, dir, accounts)

	src, err := NewRethLeafSource(dir, "", "", 1)
	if err != nil {
		t.Fatalf("NewRethLeafSource: %v", err)
	}
	defer src.Close()

	target := accounts[2]
	v, ok, err := src.AccountValue(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !bytes.Equal(v, target.value) {
		t.Errorf("AccountValue: ok=%v got=%x want=%x", ok, v, target.value)
	}

	var slot [32]byte
	for s := range target.slots {
		slot = s
	}
	// NOTE (defect, not fixed — see TestRethLeafSource_MultiSlotPerAddr_Defect):
	// NewRethLeafSource opens PlainStorageState WITHOUT the DupSort
	// flag, so the CursorDupSort().SeekBothRange() call in
	// StorageValue hits MDBX_INCOMPATIBLE against a real MDBX env
	// instead of falling back to storageValueLinear (that fallback
	// only triggers if CursorDupSort() itself errors, which the kv
	// wrapper never does). We exercise the unexported fallback
	// directly below to cover it.
	if _, _, err := src.StorageValue(target.addr, slot); err == nil {
		t.Log("StorageValue unexpectedly succeeded — defect may be fixed in this build")
	}

	tx, err := src.db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	sv, sok, serr := src.storageValueLinear(tx, target.addr, slot)
	if serr != nil {
		t.Fatal(serr)
	}
	if !sok || !bytes.Equal(sv, target.slots[slot]) {
		t.Errorf("storageValueLinear: ok=%v got=%x want=%x", sok, sv, target.slots[slot])
	}

	// Absent account / absent slot.
	var absentAddr [20]byte
	absentAddr[0] = 0xFC
	_, ok, err = src.AccountValue(absentAddr)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected absent account not found")
	}
	_, sok, serr = src.storageValueLinear(tx, target.addr, g49MkSlot(0xFE))
	if serr != nil {
		t.Fatal(serr)
	}
	if sok {
		t.Error("expected absent slot not found")
	}

	// ScanAccounts / ScanStorage full-table walk.
	seen := map[[20]byte][]byte{}
	if err := src.ScanAccounts(func(addr [20]byte, value []byte) error {
		cp := append([]byte{}, value...)
		seen[addr] = cp
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(accounts) {
		t.Errorf("ScanAccounts: got %d accounts want %d", len(seen), len(accounts))
	}
	for _, a := range accounts {
		if !bytes.Equal(seen[a.addr], a.value) {
			t.Errorf("ScanAccounts value for %x: got %x want %x", a.addr, seen[a.addr], a.value)
		}
	}

	scanned := 0
	if err := src.ScanStorage(func(addr [20]byte, slot [32]byte, value []byte) error {
		scanned++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if scanned != len(accounts) {
		t.Errorf("ScanStorage: got %d rows want %d", scanned, len(accounts))
	}
}

// TestRethLeafSource_MultiSlotPerAddr_Defect documents a real defect:
// NewRethLeafSource (source.go) opens PlainStorageState WITHOUT
// Flags: kv.DupSort in its WithTableCfg, unlike RethHashedLeafSource
// and RethBackedReader which correctly flag the equivalent tables as
// DupSort. Reth's real PlainStorageState is DupSort (one address maps
// to MANY (slot,value) dups) — against a non-DupSort-flagged table,
// writing a second slot for the same address silently overwrites the
// first (last-write-wins on that single key), so only the LAST slot
// written per address is ever retrievable. This test demonstrates the
// data loss; it is NOT asserting desired behavior, and the underlying
// code is left unmodified per task instructions ("do not fix").
func TestRethLeafSource_MultiSlotPerAddr_Defect(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "plain-multi")

	addr := g49MkAddr(9)
	slotA := g49MkSlot(1)
	slotB := g49MkSlot(2)
	valA := []byte{0xAA}
	valB := []byte{0xBB}

	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(1) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethPlainStorageStateTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	// Write slot A then slot B for the SAME address — on a true
	// DupSort table both would coexist; here the second Put overwrites
	// the first because the table has no DupSort flag.
	if err := tx.Put(rethPlainStorageStateTable, addr[:], append(append([]byte{}, slotA[:]...), valA...)); err != nil {
		t.Fatalf("put slotA: %v", err)
	}
	if err := tx.Put(rethPlainStorageStateTable, addr[:], append(append([]byte{}, slotB[:]...), valB...)); err != nil {
		t.Fatalf("put slotB: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	db.Close()

	src, err := NewRethLeafSource(dir, "", "", 1)
	if err != nil {
		t.Fatalf("NewRethLeafSource: %v", err)
	}
	defer src.Close()

	// StorageValue itself errors (MDBX_INCOMPATIBLE) against a real
	// non-DupSort-flagged MDBX table — see the defect note in the doc
	// comment above. Use the unexported linear fallback directly to
	// observe the actual data-loss effect: only the LAST-written slot
	// for a given address survives, because PlainStorageState was
	// opened as a regular (non-DupSort) table, so each Put to the
	// same key overwrote the previous one.
	if _, _, err := src.StorageValue(addr, slotB); err == nil {
		t.Log("StorageValue unexpectedly succeeded — defect may be fixed in this build")
	}

	rotx, err := src.db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rotx.Rollback()
	_, okA, errA := src.storageValueLinear(rotx, addr, slotA)
	if errA != nil {
		t.Fatal(errA)
	}
	vB, okB, errB := src.storageValueLinear(rotx, addr, slotB)
	if errB != nil {
		t.Fatal(errB)
	}
	if okA {
		t.Log("unexpected: slot A survived — defect may already be partially mitigated in this build")
	}
	if !okB || !bytes.Equal(vB, valB) {
		t.Errorf("expected only the LAST-written slot (B) to survive: ok=%v got=%x want=%x", okB, vB, valB)
	}
}
