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

// walkFrom mirrors the INCREMENTAL loader's mid-trie read: SeekBothRange(addr,
// valPrefix) with a NON-empty prefix (seek into the middle of a storage subtree),
// then NextDup forward. Returns the dup values (slot||value) as hex.
func walkFrom(t *testing.T, c kv.CursorDupSort, addr, valPrefix []byte) []string {
	t.Helper()
	// Mirror the trie loader EXACTLY: `for vS,_ := SeekBothRange(...); vS!=nil; _,vS,_ = NextDup()`.
	// The loop condition checks only the value; the error is not consulted (native
	// SeekBothRange surfaces NOTFOUND/EOF as a non-nil error with a nil value).
	var out []string
	v, _ := c.SeekBothRange(addr, valPrefix)
	for v != nil {
		out = append(out, hex.EncodeToString(v))
		_, v, _ = c.NextDup()
	}
	return out
}

// TestStateDupCursorSeekRangeMatchesNative pins the 103423 bug class: the
// incremental trie loader seeks into the MIDDLE of a storage subtree with a
// non-empty valPrefix (SeekBothRange(addr, slotNibblePrefix)) — including
// prefixes that land on a slot the overlay has TOMBSTONED (range-skip semantics).
// The overlay stateDupCursor must return EXACTLY what a native cursor returns
// after the overlay's writes are really applied to MDBX, for every such prefix.
func TestStateDupCursorSeekRangeMatchesNative(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	A := ovAddr(0x11)
	B := ovAddr(0x22) // a second account AFTER A, so a seek past A's last slot must NOT bleed into B
	// Base: A has slots 1..8; B has 1,2.
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for s := byte(1); s <= 8; s++ {
		if err := wtx.Put(modules.HashedStorage, ovFull(A, ovSlot(s)), []byte{0xa0 | s}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wtx.Put(modules.HashedStorage, ovFull(B, ovSlot(1)), []byte{0xb1}); err != nil {
		t.Fatal(err)
	}
	if err := wtx.Put(modules.HashedStorage, ovFull(B, ovSlot(2)), []byte{0xb2}); err != nil {
		t.Fatal(err)
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Overlay: override s2, delete s4 + s5 (tombstones in the middle), add s9.
	ov := NewStateOverlay()
	ov.Put(modules.HashedStorage, ovFull(A, ovSlot(2)), []byte{0xff})
	ov.Delete(modules.HashedStorage, ovFull(A, ovSlot(4)))
	ov.Delete(modules.HashedStorage, ovFull(A, ovSlot(5)))
	ov.Put(modules.HashedStorage, ovFull(A, ovSlot(9)), []byte{0xa9})

	// Ground truth: apply the same ops to a native DB.
	gtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = gtx.Put(modules.HashedStorage, ovFull(A, ovSlot(2)), []byte{0xff})
	_ = gtx.Delete(modules.HashedStorage, ovFull(A, ovSlot(4)))
	_ = gtx.Delete(modules.HashedStorage, ovFull(A, ovSlot(5)))
	_ = gtx.Put(modules.HashedStorage, ovFull(A, ovSlot(9)), []byte{0xa9})
	if err := gtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Seek prefixes: before-first, each existing slot, each DELETED slot (range
	// must skip to the next live one), a between-slots partial prefix, and
	// after-last (must return nil, not bleed into account B).
	prefixes := [][]byte{
		nil,
		ovSlot(1), ovSlot(2), ovSlot(3),
		ovSlot(4), ovSlot(5), // deleted → range-skip
		ovSlot(6), ovSlot(7), ovSlot(8), ovSlot(9),
		ovSlot(10),     // after last → nil
		{0x00},         // before first (1-byte partial)
		{0x04}, {0x05}, // partial landing on deleted region
		{0x09}, {0x0a}, // partial at/after last
	}

	roT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roT.Rollback()
	gT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gT.Rollback()

	for _, vp := range prefixes {
		oc, err := WrapStateOverlay(roT, ov).CursorDupSort(modules.HashedStorage)
		if err != nil {
			t.Fatal(err)
		}
		nc, err := gT.CursorDupSort(modules.HashedStorage)
		if err != nil {
			t.Fatal(err)
		}
		got := walkFrom(t, oc, A, vp)
		want := walkFrom(t, nc, A, vp)
		oc.Close()
		nc.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("SeekBothRange(A, %x):\n  overlay = %v\n  native  = %v", vp, got, want)
		}
	}
	if !t.Failed() {
		t.Logf("MATCH: stateDupCursor SeekBothRange+NextDup == native for all %d mid-trie prefixes", len(prefixes))
	}
}

// TestMergedCursorAccountsSeekMatchesNative differentially tests the flat
// HashedAccounts merged cursor (base ⊕ overlay) against a native cursor with the
// same writes really applied, for the INCREMENTAL loader's access pattern:
// Seek(prefix) then Next forward. Only the concurrent StateOverlay path routes
// account leaves through mergedCursor (serial reads them natively), so a flat-
// merge Seek/Next divergence here would be invisible to serial yet corrupt the
// concurrent account-trie walk — the 103423 shape.
func TestMergedCursorAccountsSeekMatchesNative(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	mkKey := func(b byte) []byte {
		k := make([]byte, 32)
		k[0] = b
		k[31] = 0x01
		return k
	}
	// Base accounts at 0x10,0x20,...,0x80.
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for b := byte(0x10); b <= 0x80; b += 0x10 {
		if err := wtx.Put(modules.HashedAccounts, mkKey(b), []byte{b, 0xaa}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Overlay: override 0x30, delete 0x40 and 0x50 (mid-range tombstones), add 0x35 and 0x90.
	ov := NewStateOverlay()
	ov.Put(modules.HashedAccounts, mkKey(0x30), []byte{0x30, 0xff})
	ov.Delete(modules.HashedAccounts, mkKey(0x40))
	ov.Delete(modules.HashedAccounts, mkKey(0x50))
	ov.Put(modules.HashedAccounts, mkKey(0x35), []byte{0x35, 0xcc})
	ov.Put(modules.HashedAccounts, mkKey(0x90), []byte{0x90, 0xdd})

	// Ground truth: apply the same to native.
	gtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = gtx.Put(modules.HashedAccounts, mkKey(0x30), []byte{0x30, 0xff})
	_ = gtx.Delete(modules.HashedAccounts, mkKey(0x40))
	_ = gtx.Delete(modules.HashedAccounts, mkKey(0x50))
	_ = gtx.Put(modules.HashedAccounts, mkKey(0x35), []byte{0x35, 0xcc})
	_ = gtx.Put(modules.HashedAccounts, mkKey(0x90), []byte{0x90, 0xdd})
	if err := gtx.Commit(); err != nil {
		t.Fatal(err)
	}

	walk := func(c kv.Cursor, seek []byte) []string {
		var out []string
		k, v, _ := c.Seek(seek)
		for k != nil {
			out = append(out, hex.EncodeToString(k)+":"+hex.EncodeToString(v))
			k, v, _ = c.Next()
		}
		return out
	}
	// Seek points: before-all, exact-existing, exact-overridden, exact-deleted
	// (must skip), exact-added, between, after-all, and partial 1-byte prefixes.
	seeks := [][]byte{
		nil,
		mkKey(0x10), mkKey(0x30), mkKey(0x35),
		mkKey(0x40), mkKey(0x50), // deleted → skip
		mkKey(0x80), mkKey(0x90), mkKey(0xa0),
		{0x00}, {0x30}, {0x40}, {0x45}, {0x90}, {0xff},
	}
	roT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roT.Rollback()
	gT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gT.Rollback()
	for _, sk := range seeks {
		oc, err := WrapStateOverlay(roT, ov).Cursor(modules.HashedAccounts)
		if err != nil {
			t.Fatal(err)
		}
		nc, err := gT.Cursor(modules.HashedAccounts)
		if err != nil {
			t.Fatal(err)
		}
		got := walk(oc, sk)
		want := walk(nc, sk)
		oc.Close()
		nc.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Seek(%x):\n  overlay = %v\n  native  = %v", sk, got, want)
		}
	}
	if !t.Failed() {
		t.Logf("MATCH: mergedCursor(HashedAccounts) Seek+Next == native for all %d seeks", len(seeks))
	}
}

// TestMergedCursorReuseAfterExhaustion mirrors how the trie loader REUSES one
// cursor across many ascending Seeks with partial Next walks (it never makes a
// fresh cursor per account). The risk: the overlay iterator m.it, once advanced
// to exhaustion by a walk, may not re-Seek correctly on the next Seek — which
// would silently drop overlay rows for later seeks. Compares a reused merged
// cursor against a native cursor for a loader-like ascending seek sequence, plus
// a deliberate backward re-seek after full exhaustion.
func TestMergedCursorReuseAfterExhaustion(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	mkKey := func(b byte) []byte {
		k := make([]byte, 32)
		k[0] = b
		k[31] = 0x01
		return k
	}
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60} {
		if err := wtx.Put(modules.HashedAccounts, mkKey(b), []byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}
	ov := NewStateOverlay()
	for _, b := range []byte{0x15, 0x25, 0x55, 0x65} { // interleaved overlay adds
		ov.Put(modules.HashedAccounts, mkKey(b), []byte{b, 0xcc})
	}
	gtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{0x15, 0x25, 0x55, 0x65} {
		_ = gtx.Put(modules.HashedAccounts, mkKey(b), []byte{b, 0xcc})
	}
	if err := gtx.Commit(); err != nil {
		t.Fatal(err)
	}

	roT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roT.Rollback()
	gT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gT.Rollback()

	// ONE cursor each, reused across a sequence of Seeks (ascending, then a
	// backward re-seek after the prior walk exhausted the iterators).
	oc, err := WrapStateOverlay(roT, ov).Cursor(modules.HashedAccounts)
	if err != nil {
		t.Fatal(err)
	}
	defer oc.Close()
	nc, err := gT.Cursor(modules.HashedAccounts)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// Sequence: seek 0x20, walk 2; seek 0x50, walk to END (exhaust); then
	// re-seek BACK to 0x15 (after exhaustion) and walk a few — the danger zone.
	type step struct {
		seek  byte
		walkN int // -1 = walk to end
	}
	steps := []step{{0x20, 2}, {0x50, -1}, {0x15, 3}, {0x60, -1}, {0x00, 4}}
	cwalk := func(c kv.Cursor, sk byte, n int) []string {
		var out []string
		k, v, _ := c.Seek(mkKey(sk))
		for k != nil && (n < 0 || len(out) < n) {
			out = append(out, hex.EncodeToString(k)[:4]+":"+hex.EncodeToString(v))
			k, v, _ = c.Next()
		}
		return out
	}
	for _, s := range steps {
		got := cwalk(oc, s.seek, s.walkN)
		want := cwalk(nc, s.seek, s.walkN)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("after reuse, Seek(%02x) walk %d:\n  overlay = %v\n  native  = %v", s.seek, s.walkN, got, want)
		}
	}
	if !t.Failed() {
		t.Logf("MATCH: reused mergedCursor == native across %d ascending+backward seeks", len(steps))
	}
}

// TestStateDupCursorOverlayOnlyAccountExactSeek reproduces the 103423/64511 bug:
// an account whose storage lives ENTIRELY in the overlay (committed base has NO
// row for it, so base.Seek lands on a LATER account), seeked with a non-empty
// prefix EQUAL to one of its slot hashes. The full-walk (nil prefix) is fine, but
// SeekBothRange(addr, existingSlotHash) returned [] instead of that slot onward.
func TestStateDupCursorOverlayOnlyAccountExactSeek(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	A := ovAddr(0x11) // the buggy account: low slots in base, HIGH slots overlay-only
	B := ovAddr(0x99) // a LATER account that lives in committed base
	sl := func(b byte) []byte { s := make([]byte, 32); s[0] = b; s[31] = 0x7e; return s }

	baseLow := []byte{0x10, 0x20, 0x30} // A's committed slots — all BELOW the high overlay slots
	ovHigh := []byte{0x80, 0x90, 0xa0}  // A's overlay-only slots — all ABOVE every base slot

	// Committed base: A's low slots + account B.
	wtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range baseLow {
		if err := wtx.Put(modules.HashedStorage, ovFull(A, sl(s)), []byte{0xa0 | s}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wtx.Put(modules.HashedStorage, ovFull(B, sl(0x01)), []byte{0xb1}); err != nil {
		t.Fatal(err)
	}
	if err := wtx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Overlay: A's HIGH slots (overlay-only) + a B override.
	ov := NewStateOverlay()
	for _, s := range ovHigh {
		ov.Put(modules.HashedStorage, ovFull(A, sl(s)), []byte{0xc0 | s})
	}
	ov.Put(modules.HashedStorage, ovFull(B, sl(0x01)), []byte{0xbb})

	// Native ground truth: apply overlay to native.
	gtx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ovHigh {
		_ = gtx.Put(modules.HashedStorage, ovFull(A, sl(s)), []byte{0xc0 | s})
	}
	_ = gtx.Put(modules.HashedStorage, ovFull(B, sl(0x01)), []byte{0xbb})
	if err := gtx.Commit(); err != nil {
		t.Fatal(err)
	}

	roT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roT.Rollback()
	gT, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gT.Rollback()

	// REUSED cursor (as the loader does): nil-walk first (moves PAST A to the
	// next account), then seek BACK to A with the first HIGH overlay-only slot —
	// the exact 64511 pattern (base.Seek's getBothRange fails on A, advances to B,
	// then the overlay iterator must seek backward from mid-stream to A's highs).
	prefixes := [][]byte{nil, sl(0x80), sl(0x90), sl(0xa0)}
	oc, _ := WrapStateOverlay(roT, ov).CursorDupSort(modules.HashedStorage)
	nc, _ := gT.CursorDupSort(modules.HashedStorage)
	defer oc.Close()
	defer nc.Close()
	for _, p := range prefixes {
		got := walkFrom(t, oc, A, p)
		want := walkFrom(t, nc, A, p)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("overlay-only A, REUSED SeekBothRange(A, %x):\n  overlay = %v\n  native  = %v", p, got, want)
		}
	}
	if !t.Failed() {
		t.Logf("MATCH: overlay-only account reused exact-slot seeks == native")
	}
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
	ov.Delete(modules.HashedStorage, ovFull(B, ovSlot(1)))            // delete only slot
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
