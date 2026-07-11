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
	// Account DELETIONS (acct==nil → SELFDESTRUCT/emptied; also wipes storage):
	// drop every 50th account. Exercises overlay HashedAccounts tombstones + the
	// deleteAccountStorage full-key wipe in the merged shard reads.
	for i := uint64(0); i < 400; i += 50 {
		wa[cAddr(i)] = nil
	}
	for i := uint64(1); i < 60; i += 3 {
		ws[cAddr(i)] = map[types.Hash]*uint256.Int{
			cSlot(i, 0):  uint256.NewInt(i*131 + 5), // override
			cSlot(i, 99): uint256.NewInt(i + 1),     // add
			cSlot(i, 2):  uint256.NewInt(0),         // slot DELETE (zero value)
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

// TestConcurrentRootSerialFallback verifies the gold-check safety net: when the
// concurrent combined root does not match the expected (header) root, the
// computer transparently recomputes the window with the serial loader and
// returns the CORRECT root with byte-identical TrieOf* — rather than persisting
// a wrong root. We force the path by arming SetExpectRoot with a bogus value;
// the real concurrent root then "mismatches", triggering the fallback. The
// returned root and trie tables must equal a pure-serial run on the same window.
func TestConcurrentRootSerialFallback(t *testing.T) {
	// Serial reference.
	dbS := buildBootstrappedState(t)
	defer dbS.Close()
	waS, wsS := genWindow()
	txS, err := dbS.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trcS := NewTrieRootComputer()
	trcS.SetRwTx(txS)
	trcS.SetIncremental(true)
	rS, err := trcS.ComputeRoot(waS, wsS)
	if err != nil {
		t.Fatal(err)
	}
	if err := txS.Commit(); err != nil {
		t.Fatal(err)
	}

	// Concurrent run with a deliberately-wrong expected root → forces fallback.
	dbP := buildBootstrappedState(t)
	defer dbP.Close()
	waP, wsP := genWindow()
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
	trcP.SetExpectRoot(types.Hash{0xde, 0xad, 0xbe, 0xef}) // bogus → mismatch → serial fallback
	rP, err := trcP.ComputeRoot(waP, wsP)
	if err != nil {
		t.Fatalf("fallback ComputeRoot: %v", err)
	}
	if err := ov.FlushTo(txP); err != nil {
		t.Fatal(err)
	}
	if err := txP.Commit(); err != nil {
		t.Fatal(err)
	}

	if rP != rS {
		t.Fatalf("fallback root mismatch:\n  serial   = %s\n  fallback = %s", rS.Hex(), rP.Hex())
	}
	for _, tbl := range []string{modules.TrieOfAccounts, modules.TrieOfStorage} {
		s := dumpTbl(t, dbS, tbl)
		p := dumpTbl(t, dbP, tbl)
		if len(s) != len(p) {
			t.Errorf("%s count: serial=%d fallback=%d", tbl, len(s), len(p))
		}
		for k, sv := range s {
			if pv, ok := p[k]; !ok || pv != sv {
				t.Errorf("%s key=%x: serial=%x fallback(ok=%v)=%x", tbl, []byte(k), []byte(sv), ok, []byte(pv))
			}
		}
		for k := range p {
			if _, ok := s[k]; !ok {
				t.Errorf("%s key=%x only in fallback", tbl, []byte(k))
			}
		}
	}
	t.Logf("MATCH serial-fallback recovered correct root %s with byte-identical TrieOf*", rS.Hex())
}

type winCh struct {
	a map[types.Address]*account.StateAccount
	s map[types.Address]map[types.Hash]*uint256.Int
}

// runWindows applies a SEQUENCE of windows on ONE tx (and, when concurrent, ONE
// overlay accumulated across windows without committing between them — exactly
// the build's mid-batch state). Returns the final window's root.
func runWindows(t *testing.T, db kv.RwDB, concurrent bool, windows []winCh) types.Hash {
	t.Helper()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ov *StateOverlay
	var wtx kv.RwTx = tx
	if concurrent {
		ov = NewStateOverlay()
		wtx = WrapStateOverlayRW(tx, ov)
	}
	var last types.Hash
	for i, w := range windows {
		trc := NewTrieRootComputer()
		trc.SetRwTx(wtx)
		trc.SetIncremental(true)
		if concurrent {
			trc.SetConcurrentRoot(db, ov, 16)
		}
		r, err := trc.ComputeRoot(w.a, w.s)
		if err != nil {
			t.Fatalf("window %d: %v", i, err)
		}
		last = r
	}
	if concurrent {
		if err := ov.FlushTo(tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return last
}

// TestConcurrentMultiWindowWipe targets the cross-window overlay case the single
// window test misses: account X gains storage in window 1 (lives only in the
// overlay, uncommitted), then is DELETED in window 2 — so deleteAccountStorage
// must enumerate AND wipe X's overlay-resident slots. Serial vs concurrent must
// still match.
func TestConcurrentMultiWindowWipe(t *testing.T) {
	mkWindows := func() []winCh {
		X := cAddr(7) // has bootstrap storage (i<60)
		w1 := winCh{
			a: map[types.Address]*account.StateAccount{cAddr(18): cAcct(123, 456)},
			s: map[types.Address]map[types.Hash]*uint256.Int{
				X: {cSlot(7, 200): uint256.NewInt(42), cSlot(7, 201): uint256.NewInt(7)},
			},
		}
		w2 := winCh{
			a: map[types.Address]*account.StateAccount{
				X:        nil, // DELETE X (wipes its storage incl. window-1 overlay slots)
				cAddr(9): cAcct(999, 111),
			},
			s: map[types.Address]map[types.Hash]*uint256.Int{
				cAddr(12): {cSlot(12, 5): uint256.NewInt(88)},
			},
		}
		return []winCh{w1, w2}
	}

	dbS := buildBootstrappedState(t)
	defer dbS.Close()
	dbP := buildBootstrappedState(t)
	defer dbP.Close()

	rS := runWindows(t, dbS, false, mkWindows())
	rP := runWindows(t, dbP, true, mkWindows())

	if rS != rP {
		t.Fatalf("multi-window root mismatch:\n  serial   = %s\n  parallel = %s", rS.Hex(), rP.Hex())
	}
	for _, tbl := range []string{modules.TrieOfAccounts, modules.TrieOfStorage} {
		s := dumpTbl(t, dbS, tbl)
		p := dumpTbl(t, dbP, tbl)
		if len(s) != len(p) {
			t.Errorf("%s count: serial=%d parallel=%d", tbl, len(s), len(p))
		}
		for k, sv := range s {
			if pv, ok := p[k]; !ok || pv != sv {
				t.Errorf("%s key=%x: serial=%x parallel(ok=%v)=%x", tbl, []byte(k), []byte(sv), ok, []byte(pv))
			}
		}
	}
	t.Logf("MATCH multi-window: %s", rS.Hex())
}

// buildN bootstraps n accounts (25% with storage) and TrieOf*, committed.
func buildN(t *testing.T, n int) kv.RwDB {
	t.Helper()
	db := memdb.New(t.TempDir())
	accts := map[types.Address]*account.StateAccount{}
	stor := map[types.Address]map[types.Hash]*uint256.Int{}
	for i := uint64(0); i < uint64(n); i++ {
		accts[cAddr(i)] = cAcct(i+1, i*1_000_000_007+13)
		if i%4 == 0 {
			m := map[types.Hash]*uint256.Int{}
			for j := uint64(0); j < 1+(i%8); j++ {
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
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, stor); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestConcurrentRichMultiWindow exercises a deep (3000-account) trie with 4
// windows accumulated in ONE overlay, mixing INSERTS (new accounts + storage),
// overrides, slot deletes, account deletes (incl. accounts whose storage lives
// only in the overlay), and a re-create — the realistic mid-batch pattern the
// build hits. Serial vs concurrent must match byte-for-byte.
func TestConcurrentRichMultiWindow(t *testing.T) {
	const N = 3000
	mk := func() []winCh {
		acc := func(m map[types.Address]*account.StateAccount, i, non, bal uint64) {
			m[cAddr(i)] = cAcct(non, bal)
		}
		// w1: modify some, INSERT new accounts (N+..) with storage, add slots.
		w1a := map[types.Address]*account.StateAccount{}
		w1s := map[types.Address]map[types.Hash]*uint256.Int{}
		for i := uint64(0); i < N; i += 13 {
			acc(w1a, i, i+1000, i*7+3)
		}
		for k := uint64(0); k < 80; k++ { // INSERT new accounts with storage
			acc(w1a, N+k, k+1, k*1234+5)
			w1s[cAddr(N+k)] = map[types.Hash]*uint256.Int{
				cSlot(N+k, 0): uint256.NewInt(k + 1),
				cSlot(N+k, 1): uint256.NewInt(k*9 + 2),
			}
		}
		for i := uint64(0); i < N; i += 17 { // add a slot to existing accts
			w1s[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 500): uint256.NewInt(i + 7)}
		}
		// w2: override + slot deletes on the w1-inserted accounts and existing.
		w2a := map[types.Address]*account.StateAccount{}
		w2s := map[types.Address]map[types.Hash]*uint256.Int{}
		for k := uint64(0); k < 80; k += 2 {
			w2s[cAddr(N+k)] = map[types.Hash]*uint256.Int{
				cSlot(N+k, 0): uint256.NewInt(0),       // DELETE slot
				cSlot(N+k, 2): uint256.NewInt(k*3 + 1), // add slot
			}
		}
		for i := uint64(0); i < N; i += 23 {
			acc(w2a, i, i+2000, i*11+1)
		}
		// w3: DELETE accounts — including w1-inserted ones (overlay-only storage).
		w3a := map[types.Address]*account.StateAccount{}
		w3s := map[types.Address]map[types.Hash]*uint256.Int{}
		for k := uint64(1); k < 80; k += 3 {
			w3a[cAddr(N+k)] = nil // delete inserted account (storage only in overlay)
		}
		for i := uint64(0); i < N; i += 50 {
			w3a[cAddr(i)] = nil // delete bootstrap account (committed storage)
		}
		for i := uint64(5); i < N; i += 50 {
			w3s[cAddr(i)] = map[types.Hash]*uint256.Int{cSlot(i, 9): uint256.NewInt(i + 99)}
		}
		// w4: RE-CREATE a deleted account with fresh storage + more churn.
		w4a := map[types.Address]*account.StateAccount{}
		w4s := map[types.Address]map[types.Hash]*uint256.Int{}
		acc(w4a, N+1, 7, 7) // re-create cAddr(N+1) (deleted in w3)
		w4s[cAddr(N+1)] = map[types.Hash]*uint256.Int{cSlot(N+1, 77): uint256.NewInt(777)}
		for i := uint64(0); i < N; i += 31 {
			acc(w4a, i, i+4000, i*13+2)
		}
		return []winCh{
			{w1a, w1s}, {w2a, w2s}, {w3a, w3s}, {w4a, w4s},
		}
	}

	dbS := buildN(t, N)
	defer dbS.Close()
	dbP := buildN(t, N)
	defer dbP.Close()

	rS := runWindows(t, dbS, false, mk())
	rP := runWindows(t, dbP, true, mk())

	if rS != rP {
		t.Fatalf("rich multi-window root mismatch:\n  serial   = %s\n  parallel = %s", rS.Hex(), rP.Hex())
	}
	for _, tbl := range []string{modules.TrieOfAccounts, modules.TrieOfStorage} {
		s := dumpTbl(t, dbS, tbl)
		p := dumpTbl(t, dbP, tbl)
		if len(s) != len(p) {
			t.Errorf("%s count: serial=%d parallel=%d", tbl, len(s), len(p))
		}
		mism := 0
		for k, sv := range s {
			if pv, ok := p[k]; !ok || pv != sv {
				mism++
				if mism <= 6 {
					t.Errorf("%s key=%x: serial=%x parallel(ok=%v)=%x", tbl, []byte(k), []byte(sv), ok, []byte(pv))
				}
			}
		}
		for k := range p {
			if _, ok := s[k]; !ok {
				t.Errorf("%s key=%x only in parallel", tbl, []byte(k))
			}
		}
	}
	t.Logf("MATCH rich multi-window (N=%d, 4 windows): %s", N, rS.Hex())
}
