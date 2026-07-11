package commitment

// Validates the riskiest P4b piece: the stateDupCursor's HashedStorage DupSort
// surface (SeekBothRange + NextDup) over the merged base⊕overlay view returns
// EXACTLY what a raw cursor returns after the overlay's writes are really applied
// to MDBX. Covers override / delete / add / all-deleted / pure-base / new-account.

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func ovAddr(b byte) []byte {
	a := make([]byte, 32)
	a[0] = b
	a[31] = b
	return a
}
func ovSlot(b byte) []byte {
	s := make([]byte, 32)
	s[0] = b
	s[31] = 0xfe
	return s
}
func ovFull(addr, slot []byte) []byte { return append(append([]byte{}, addr...), slot...) }

// readStorageDup mirrors the trie loader's HashedStorage read: for each account,
// SeekBothRange(addr, nil) then NextDup until exhausted, collecting the dup
// values (slotHash||value) as hex strings.
func readStorageDup(t *testing.T, tx kv.Tx, addrs [][]byte) map[string][]string {
	t.Helper()
	res := map[string][]string{}
	ss, err := tx.CursorDupSort(modules.HashedStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	for _, a := range addrs {
		var dups []string
		vS, err := ss.SeekBothRange(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		for vS != nil {
			dups = append(dups, hex.EncodeToString(vS))
			_, vS, err = ss.NextDup()
			if err != nil {
				t.Fatal(err)
			}
		}
		res[hex.EncodeToString(a)] = dups
	}
	return res
}

func TestStateDupCursorMatchesAppliedRaw(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	A, B, C, D := ovAddr(0x11), ovAddr(0x22), ovAddr(0x33), ovAddr(0x44)
	addrs := [][]byte{A, B, C, D}

	// Base state (committed): A{1,2,3}, B{1}, D{1,2}; C has none.
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	put := func(addr, slot []byte, val byte) {
		if err := wtx.Put(modules.HashedStorage, ovFull(addr, slot), []byte{val}); err != nil {
			t.Fatal(err)
		}
	}
	put(A, ovSlot(1), 0xa1)
	put(A, ovSlot(2), 0xa2)
	put(A, ovSlot(3), 0xa3)
	put(B, ovSlot(1), 0xb1)
	put(D, ovSlot(1), 0xd1)
	put(D, ovSlot(2), 0xd2)
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Overlay ops: A override s2 + delete s3 + add s4; B delete s1; C add s1,s2; D none.
	ov := NewStateOverlay()
	ov.Put(modules.HashedStorage, ovFull(A, ovSlot(2)), []byte{0xff}) // override
	ov.Delete(modules.HashedStorage, ovFull(A, ovSlot(3)))            // delete
	ov.Put(modules.HashedStorage, ovFull(A, ovSlot(4)), []byte{0xa4}) // add
	ov.Delete(modules.HashedStorage, ovFull(B, ovSlot(1)))           // delete only slot
	ov.Put(modules.HashedStorage, ovFull(C, ovSlot(1)), []byte{0xc1}) // new account
	ov.Put(modules.HashedStorage, ovFull(C, ovSlot(2)), []byte{0xc2})

	// Read via overlay (base RoTx ⊕ in-memory overlay).
	roTx, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overlayRead := readStorageDup(t, WrapStateOverlay(roTx, ov), addrs)
	roTx.Rollback()

	// Now REALLY apply the overlay ops and read raw — the ground truth.
	wtx2, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustPut := func(addr, slot []byte, val byte) {
		if err := wtx2.Put(modules.HashedStorage, ovFull(addr, slot), []byte{val}); err != nil {
			t.Fatal(err)
		}
	}
	mustDel := func(addr, slot []byte) {
		if err := wtx2.Delete(modules.HashedStorage, ovFull(addr, slot)); err != nil {
			t.Fatal(err)
		}
	}
	mustPut(A, ovSlot(2), 0xff)
	mustDel(A, ovSlot(3))
	mustPut(A, ovSlot(4), 0xa4)
	mustDel(B, ovSlot(1))
	mustPut(C, ovSlot(1), 0xc1)
	mustPut(C, ovSlot(2), 0xc2)
	if err := wtx2.Commit(); err != nil {
		t.Fatal(err)
	}
	roTx2, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	groundTruth := readStorageDup(t, roTx2, addrs)
	roTx2.Rollback()

	if !reflect.DeepEqual(overlayRead, groundTruth) {
		for _, a := range addrs {
			ah := hex.EncodeToString(a)
			if !reflect.DeepEqual(overlayRead[ah], groundTruth[ah]) {
				t.Errorf("account %s:\n  overlay = %v\n  raw     = %v", ah[:4], overlayRead[ah], groundTruth[ah])
			}
		}
		t.FailNow()
	}
	t.Logf("MATCH: stateDupCursor overlay read == applied-raw for all %d accounts", len(addrs))
}
