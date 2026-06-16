package commitment

// End-to-end P4b validation: the parallel ComputeRoot (SetConcurrentRoot → 16
// nibble shards over per-worker RoTx ⊕ StateOverlay) produces the SAME root AND
// the SAME TrieOfAccounts/TrieOfStorage as the serial ComputeRoot, on an
// incremental window against a bootstrapped trie. Two identically-seeded memdbs;
// serial on one, parallel on the other; compare root + both trie tables byte-for-byte.

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func cAddr(i uint64) types.Address {
	var a types.Address
	binary.BigEndian.PutUint64(a[0:], i*2654435761+1)
	binary.BigEndian.PutUint64(a[12:], i+7)
	return a
}
func cSlot(i, j uint64) types.Hash {
	var h types.Hash
	binary.BigEndian.PutUint64(h[0:], i*131+1)
	binary.BigEndian.PutUint64(h[24:], j*1099511628211+1)
	return h
}
func cAcct(nonce, bal uint64) *account.StateAccount {
	a := account.NewAccount()
	a.Nonce = nonce
	a.Balance = *uint256.NewInt(bal)
	return &a
}

// buildBootstrappedState seeds 400 accounts (60 with storage) and bootstraps
// TrieOf* via a serial full-rebuild ComputeRoot, then commits. Deterministic, so
// two calls produce byte-identical committed DBs.
func buildBootstrappedState(t *testing.T) kv.RwDB {
	t.Helper()
	db := memdb.New(t.TempDir())
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	for i := uint64(0); i < 400; i++ {
		accts[cAddr(i)] = cAcct(i+1, i*1_000_000_007+13)
		if i < 60 {
			m := map[types.Hash]*uint256.Int{}
			for j := uint64(0); j < 2+(i%5); j++ {
				m[cSlot(i, j)] = uint256.NewInt(i*97 + j + 1)
			}
			stor[cAddr(i)] = m
		}
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false) // legacy full rebuild → bootstraps TrieOf*
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

// genWindow returns a FRESH dirty set (so serial/parallel runs don't share
// mutable account pointers): change every 9th account, override slot0 + add a
// new slot for every 3rd storage account.
func genWindow() (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
	wa := map[types.Address]*account.StateAccount{}
	ws := map[types.Address]map[types.Hash]*uint256.Int{}
	for i := uint64(0); i < 400; i += 9 {
		wa[cAddr(i)] = cAcct(i+5000, i*4242+7)
	}
	for i := uint64(0); i < 60; i += 3 {
		ws[cAddr(i)] = map[types.Hash]*uint256.Int{
			cSlot(i, 0):  uint256.NewInt(i*131 + 5), // override
			cSlot(i, 99): uint256.NewInt(i + 1),     // add
		}
	}
	return wa, ws
}

func dumpTbl(t *testing.T, db kv.RoDB, table string) map[string]string {
	t.Helper()
	out := map[string]string{}
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c, err := tx.Cursor(table)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for k, v, e := c.First(); k != nil; k, v, e = c.Next() {
		if e != nil {
			t.Fatal(e)
		}
		out[string(append([]byte{}, k...))] = string(append([]byte{}, v...))
	}
	return out
}

// accumWindow returns a FRESH dirty set for accumulation round w. The sequence
// stresses exactly what the single-window test never did and what the real build
// hits over many windows: account DELETE+wipe, slot zeroing, INSERT of brand-new
// accounts (storage only in the overlay), re-create of a deleted account, and —
// critically (round 5) — DELETE of an account whose ENTIRE storage subtree lives
// ONLY in the overlay (created round 2, mutated 3/4). The wipe must enumerate
// those overlay-only slots through the merged cursor.
func accumWindow(w int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
	wa := map[types.Address]*account.StateAccount{}
	ws := map[types.Address]map[types.Hash]*uint256.Int{}
	switch w {
	case 0:
		for i := uint64(0); i < 60; i += 3 {
			ws[cAddr(i)] = map[types.Hash]*uint256.Int{
				cSlot(i, 0):  uint256.NewInt(i*131 + 5), // override
				cSlot(i, 99): uint256.NewInt(i + 1),     // add
			}
		}
		for i := uint64(0); i < 400; i += 9 {
			wa[cAddr(i)] = cAcct(i+5000, i*4242+7)
		}
	case 1:
		wa[cAddr(3)] = nil // delete (has storage → wipe)
		wa[cAddr(6)] = nil
		ws[cAddr(9)] = map[types.Hash]*uint256.Int{cSlot(9, 0): uint256.NewInt(0)} // zero base slot
	case 2:
		wa[cAddr(500)] = cAcct(1, 999) // insert new account, storage only in overlay
		ws[cAddr(500)] = map[types.Hash]*uint256.Int{
			cSlot(500, 0): uint256.NewInt(11),
			cSlot(500, 1): uint256.NewInt(22),
		}
		wa[cAddr(3)] = cAcct(2, 333) // re-create the account deleted in w1
		ws[cAddr(3)] = map[types.Hash]*uint256.Int{cSlot(3, 7): uint256.NewInt(77)}
	case 3:
		ws[cAddr(500)] = map[types.Hash]*uint256.Int{
			cSlot(500, 0): uint256.NewInt(111), // override overlay-only slot
			cSlot(500, 2): uint256.NewInt(33),  // add overlay-only slot
		}
		wa[cAddr(12)] = nil
		for i := uint64(1); i < 400; i += 11 {
			wa[cAddr(i)] = cAcct(i+9000, i*7+1)
		}
	case 4:
		ws[cAddr(500)] = map[types.Hash]*uint256.Int{cSlot(500, 1): uint256.NewInt(0)} // zero overlay-only slot
		ws[cAddr(15)] = map[types.Hash]*uint256.Int{cSlot(15, 0): uint256.NewInt(0)}   // zero base slot
		wa[cAddr(501)] = cAcct(1, 55)
		ws[cAddr(501)] = map[types.Hash]*uint256.Int{cSlot(501, 0): uint256.NewInt(5)}
	case 5:
		wa[cAddr(500)] = nil // DELETE account whose storage is ONLY in the overlay
	case 6:
		wa[cAddr(500)] = cAcct(9, 9) // re-create again
		ws[cAddr(500)] = map[types.Hash]*uint256.Int{cSlot(500, 5): uint256.NewInt(5)}
		for i := uint64(0); i < 60; i += 7 {
			ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 0): uint256.NewInt(i + 1000)}
		}
	default: // 7+
		for i := uint64(0); i < 400; i += 5 {
			wa[cAddr(i)] = cAcct(i+12000+uint64(w), i*3+9)
		}
		for i := uint64(0); i < 60; i += 2 {
			ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 1): uint256.NewInt(i*2 + 1 + uint64(w))}
		}
	}
	return wa, ws
}

// TestConcurrentOverlayAccumulation mirrors the real build's WITHIN-BATCH cadence:
// the StateOverlay accumulates many windows' writes (no flush between), while the
// serial reference writes the same windows to a direct tx. Each window's root must
// match. This is the path that diverges on real data (gold-check mismatch); the
// single-window equivalence test cannot catch an accumulation drift.
func TestConcurrentOverlayAccumulation(t *testing.T) {
	dbS := buildBootstrappedState(t)
	defer dbS.Close()
	dbP := buildBootstrappedState(t)
	defer dbP.Close()

	txS, err := dbS.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer txS.Rollback()
	trcS := NewTrieRootComputer()
	trcS.SetRwTx(txS)
	trcS.SetIncremental(true)

	txP, err := dbP.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer txP.Rollback()
	ov := NewStateOverlay()
	wtx := WrapStateOverlayRW(txP, ov)
	trcP := NewTrieRootComputer()
	trcP.SetRwTx(wtx)
	trcP.SetIncremental(true)
	trcP.SetConcurrentRoot(dbP, ov, 16)

	const numAccumWindows = 12
	for w := 0; w < numAccumWindows; w++ {
		sa, ss := accumWindow(w)
		rS, err := trcS.ComputeRoot(sa, ss)
		if err != nil {
			t.Fatalf("serial window %d: %v", w, err)
		}
		pa, ps := accumWindow(w)
		rP, err := trcP.ComputeRoot(pa, ps)
		if err != nil {
			t.Fatalf("concurrent window %d: %v", w, err)
		}
		if rS != rP {
			t.Fatalf("window %d ROOT mismatch (overlay accumulation drift):\n  serial     = %s\n  concurrent = %s", w, rS.Hex(), rP.Hex())
		}
		t.Logf("window %d OK root=%s", w, rS.Hex())
	}
}

// TestConcurrentOverlayMultiBatch exercises the FlushTo → commit → next-batch
// cycle the single-overlay accumulation test skips. The overlay is flushed and
// committed every batchWindows windows (mirroring the real build's per-batch
// flush), so each new batch reads the PREVIOUSLY-FLUSHED committed HashedStorage/
// TrieOf* from MDBX. A bad DupSort delete-before-put in FlushTo would corrupt
// committed state here and diverge from the serial reference on a later batch —
// exactly the shape of the real block-103423 gold-check mismatch.
func TestConcurrentOverlayMultiBatch(t *testing.T) {
	dbS := buildBootstrappedState(t)
	defer dbS.Close()
	dbP := buildBootstrappedState(t)
	defer dbP.Close()

	const (
		numWindows   = 24
		batchWindows = 4
	)
	ov := NewStateOverlay() // reused across batches; FlushTo clears it

	var txS, txP kv.RwTx
	var wtx kv.RwTx
	var trcS, trcP *TrieRootComputer
	openBatch := func() {
		var err error
		if txS, err = dbS.BeginRw(context.Background()); err != nil {
			t.Fatal(err)
		}
		trcS = NewTrieRootComputer()
		trcS.SetRwTx(txS)
		trcS.SetIncremental(true)

		if txP, err = dbP.BeginRw(context.Background()); err != nil {
			t.Fatal(err)
		}
		wtx = WrapStateOverlayRW(txP, ov)
		trcP = NewTrieRootComputer()
		trcP.SetRwTx(wtx)
		trcP.SetIncremental(true)
		trcP.SetConcurrentRoot(dbP, ov, 16)
	}
	closeBatch := func() {
		if err := txS.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := ov.FlushTo(txP); err != nil {
			t.Fatal(err)
		}
		if err := txP.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	openBatch()
	for w := 0; w < numWindows; w++ {
		sa, ss := accumWindow(w)
		rS, err := trcS.ComputeRoot(sa, ss)
		if err != nil {
			t.Fatalf("serial window %d: %v", w, err)
		}
		pa, ps := accumWindow(w)
		rP, err := trcP.ComputeRoot(pa, ps)
		if err != nil {
			t.Fatalf("concurrent window %d: %v", w, err)
		}
		if rS != rP {
			t.Fatalf("window %d (batch %d) ROOT mismatch (cross-batch flush drift):\n  serial     = %s\n  concurrent = %s",
				w, w/batchWindows, rS.Hex(), rP.Hex())
		}
		t.Logf("window %d (batch %d) OK root=%s", w, w/batchWindows, rS.Hex())
		if (w+1)%batchWindows == 0 {
			closeBatch()
			openBatch()
		}
	}
	txS.Rollback()
	txP.Rollback()
}

// buildDeepStorageState bootstraps 400 accounts (16-nibble coverage so the
// concurrent path uses CombineNibbleSubtries, not the serial fallback) PLUS one
// "contract" account cAddr(700) carrying nSlots storage slots — a deep storage
// trie like a real busy contract. Deterministic.
func buildDeepStorageState(t *testing.T, nSlots uint64) kv.RwDB {
	t.Helper()
	db := memdb.New(t.TempDir())
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	for i := uint64(0); i < 400; i++ {
		accts[cAddr(i)] = cAcct(i+1, i*1_000_000_007+13)
	}
	deep := map[types.Hash]*uint256.Int{}
	for j := uint64(0); j < nSlots; j++ {
		deep[cSlot(700, j)] = uint256.NewInt(j*1099511628211 + 7)
	}
	accts[cAddr(700)] = cAcct(1, 1)
	stor[cAddr(700)] = deep

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestConcurrentDeepStorageTombstones drives the deep-storage-trie path the
// shallow tests miss: a contract with ~800 slots, then a window that scatters
// overlay TOMBSTONES (zeroed slots) AND new slots through the middle of that
// trie. The loader's StorageTrieCursor then does mid-account SeekBothRange +
// NextDup over the merged (base ⊕ overlay-with-tombstones) view — the stateDup
// Cursor surface that real data exercises and synthetic shallow tries don't.
func TestConcurrentDeepStorageTombstones(t *testing.T) {
	const nSlots = 800
	dbS := buildDeepStorageState(t, nSlots)
	defer dbS.Close()
	dbP := buildDeepStorageState(t, nSlots)
	defer dbP.Close()

	// Window: zero every 7th slot (scattered tombstones), override every 5th,
	// add a band of new high slots — all on the deep contract.
	mkWin := func() (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		s := map[types.Hash]*uint256.Int{}
		for j := uint64(0); j < nSlots; j++ {
			switch {
			case j%7 == 0:
				s[cSlot(700, j)] = uint256.NewInt(0) // tombstone
			case j%5 == 0:
				s[cSlot(700, j)] = uint256.NewInt(j + 424242) // override
			}
		}
		for j := uint64(1000); j < 1080; j++ {
			s[cSlot(700, j)] = uint256.NewInt(j) // new slots
		}
		return map[types.Address]*account.StateAccount{}, map[types.Address]map[types.Hash]*uint256.Int{cAddr(700): s}
	}

	txS, err := dbS.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trcS := NewTrieRootComputer()
	trcS.SetRwTx(txS)
	trcS.SetIncremental(true)
	sa, ss := mkWin()
	rS, err := trcS.ComputeRoot(sa, ss)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	if err := txS.Commit(); err != nil {
		t.Fatal(err)
	}

	txP, err := dbP.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ov := NewStateOverlay()
	wtx := WrapStateOverlayRW(txP, ov)
	trcP := NewTrieRootComputer()
	trcP.SetRwTx(wtx)
	trcP.SetIncremental(true)
	trcP.SetConcurrentRoot(dbP, ov, 16)
	pa, ps := mkWin()
	rP, err := trcP.ComputeRoot(pa, ps)
	if err != nil {
		t.Fatalf("concurrent: %v", err)
	}
	if err := ov.FlushTo(txP); err != nil {
		t.Fatal(err)
	}
	if err := txP.Commit(); err != nil {
		t.Fatal(err)
	}

	if rS != rP {
		t.Fatalf("deep-storage ROOT mismatch:\n  serial     = %s\n  concurrent = %s", rS.Hex(), rP.Hex())
	}
	for _, tbl := range []string{modules.TrieOfAccounts, modules.TrieOfStorage, modules.HashedStorage} {
		s := dumpTbl(t, dbS, tbl)
		p := dumpTbl(t, dbP, tbl)
		if len(s) != len(p) {
			t.Errorf("%s entry count: serial=%d concurrent=%d", tbl, len(s), len(p))
		}
		mism := 0
		for k, sv := range s {
			if pv, ok := p[k]; !ok || pv != sv {
				if mism++; mism <= 5 {
					t.Errorf("%s key=%x: serial=%x concurrent(present=%v)=%x", tbl, []byte(k), []byte(sv), ok, []byte(pv))
				}
			}
		}
		for k := range p {
			if _, ok := s[k]; !ok {
				t.Errorf("%s key=%x only in concurrent", tbl, []byte(k))
			}
		}
	}
	t.Logf("deep-storage MATCH root=%s", rS.Hex())
}

// TestConcurrentEmbeddedStorageTransitions targets the embedded/inline storage
// trie edge case: an account with exactly ONE storage slot has its single leaf
// EMBEDDED in the account (storageRoot present, often no separate TrieOfStorage
// node). The 1↔2 and 1↔0 slot transitions exercise the loader's virtual-root
// synthesis through the overlay merged cursor — a surface real contracts hit on
// their first/last slot that shallow multi-slot synthetic tries never reach.
func TestConcurrentEmbeddedStorageTransitions(t *testing.T) {
	// Bootstrap: 400 accounts for nibble coverage; a band cAddr(600..619) each
	// with exactly ONE storage slot (embedded-leaf storage tries).
	build := func() kv.RwDB {
		db := memdb.New(t.TempDir())
		accts := map[types.Address]*account.StateAccount{}
		stor := map[types.Address]map[types.Hash]*uint256.Int{}
		for i := uint64(0); i < 400; i++ {
			accts[cAddr(i)] = cAcct(i+1, i*1_000_000_007+13)
		}
		for i := uint64(600); i < 620; i++ {
			accts[cAddr(i)] = cAcct(1, i)
			stor[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 0): uint256.NewInt(i + 1)}
		}
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		trc := NewTrieRootComputer()
		trc.SetRwTx(tx)
		trc.SetIncremental(false)
		if _, err := trc.ComputeRoot(accts, stor); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return db
	}
	dbS, dbP := build(), build()
	defer dbS.Close()
	defer dbP.Close()

	// Each window transitions the embedded-storage band:
	//  w0: 1→2 slots (embedded leaf becomes a real 2-leaf storage trie)
	//  w1: 2→1 slots (delete slot1, back to embedded — was a separate node, now inline)
	//  w2: 1→0 slots (delete the last slot → storageRoot becomes empty)
	//  w3: 0→1 slots (re-add a slot → embedded again)
	win := func(w int) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
		ws := map[types.Address]map[types.Hash]*uint256.Int{}
		for i := uint64(600); i < 620; i++ {
			switch w {
			case 0:
				ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 1): uint256.NewInt(i + 100)}
			case 1:
				ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 1): uint256.NewInt(0)}
			case 2:
				ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 0): uint256.NewInt(0)}
			case 3:
				ws[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 2): uint256.NewInt(i + 200)}
			}
		}
		return map[types.Address]*account.StateAccount{}, ws
	}

	txS, err := dbS.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer txS.Rollback()
	trcS := NewTrieRootComputer()
	trcS.SetRwTx(txS)
	trcS.SetIncremental(true)

	txP, err := dbP.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer txP.Rollback()
	ov := NewStateOverlay()
	wtx := WrapStateOverlayRW(txP, ov)
	trcP := NewTrieRootComputer()
	trcP.SetRwTx(wtx)
	trcP.SetIncremental(true)
	trcP.SetConcurrentRoot(dbP, ov, 16)

	for w := 0; w < 4; w++ {
		sa, ss := win(w)
		rS, err := trcS.ComputeRoot(sa, ss)
		if err != nil {
			t.Fatalf("serial w%d: %v", w, err)
		}
		pa, ps := win(w)
		rP, err := trcP.ComputeRoot(pa, ps)
		if err != nil {
			t.Fatalf("concurrent w%d: %v", w, err)
		}
		if rS != rP {
			t.Fatalf("embedded-storage w%d ROOT mismatch:\n  serial     = %s\n  concurrent = %s", w, rS.Hex(), rP.Hex())
		}
		t.Logf("embedded-storage w%d OK root=%s", w, rS.Hex())
	}
}

func TestConcurrentComputeRootEquivalence(t *testing.T) {
	dbS := buildBootstrappedState(t)
	defer dbS.Close()
	dbP := buildBootstrappedState(t)
	defer dbP.Close()

	// Serial window.
	wa, ws := genWindow()
	txS, err := dbS.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trcS := NewTrieRootComputer()
	trcS.SetRwTx(txS)
	trcS.SetIncremental(true)
	rS, err := trcS.ComputeRoot(wa, ws)
	if err != nil {
		t.Fatalf("serial ComputeRoot: %v", err)
	}
	if err := txS.Commit(); err != nil {
		t.Fatal(err)
	}

	// Parallel window (overlay-wrapped tx + per-worker RoTx fan-out).
	wa2, ws2 := genWindow()
	txP, err := dbP.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ov := NewStateOverlay()
	wtx := WrapStateOverlayRW(txP, ov)
	trcP := NewTrieRootComputer()
	trcP.SetRwTx(wtx)
	trcP.SetIncremental(true)
	trcP.SetConcurrentRoot(dbP, ov, 16)
	rP, err := trcP.ComputeRoot(wa2, ws2)
	if err != nil {
		t.Fatalf("parallel ComputeRoot: %v", err)
	}
	if err := ov.FlushTo(txP); err != nil { // flush overlay → real tx
		t.Fatal(err)
	}
	if err := txP.Commit(); err != nil {
		t.Fatal(err)
	}

	if rS != rP {
		t.Fatalf("ROOT mismatch:\n  serial   = %s\n  parallel = %s", rS.Hex(), rP.Hex())
	}
	for _, tbl := range []string{modules.TrieOfAccounts, modules.TrieOfStorage} {
		s := dumpTbl(t, dbS, tbl)
		p := dumpTbl(t, dbP, tbl)
		if len(s) != len(p) {
			t.Errorf("%s entry count: serial=%d parallel=%d", tbl, len(s), len(p))
		}
		mism := 0
		for k, sv := range s {
			if pv, ok := p[k]; !ok || pv != sv {
				mism++
				if mism <= 5 {
					t.Errorf("%s key=%x: serial=%x parallel(present=%v)=%x", tbl, []byte(k), []byte(sv), ok, []byte(pv))
				}
			}
		}
		for k := range p {
			if _, ok := s[k]; !ok {
				t.Errorf("%s key=%x only in parallel", tbl, []byte(k))
			}
		}
	}
	t.Logf("MATCH: serial == concurrent ComputeRoot; root=%s; TrieOfAccounts+TrieOfStorage byte-identical", rS.Hex())
}
