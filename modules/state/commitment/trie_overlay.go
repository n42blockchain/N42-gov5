// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TrieOverlay — a RAM write-back layer for TrieOfAccounts/TrieOfStorage.
//
// The incremental TrieRootComputer rewrites the SAME hot trie nodes (the top
// of the account trie, busy contracts' upper storage nodes) every block;
// profiled on the DATC mainnet build, those MDBX puts plus the loader's
// cursor reads were >60% of all CPU (runtime.cgocall). The overlay absorbs
// every TrieOf* write into two ordered in-RAM btrees and gives reads a
// merged view (overlay over MDBX); the caller flushes ONCE per commit batch,
// which both batches the cgo calls and collapses the per-block rewrites of a
// hot node into a single final put.
//
// Integration is a pure kv.RwTx wrapper (WrapTrieOverlay): Put/Delete/GetOne
// and Cursor/CursorDupSort on the two trie tables are intercepted; everything
// else passes through. The TrieRootComputer itself is untouched — it reads
// and writes through the tx exactly as before. The trie loader drives its
// cursors with flat Seek/Next/First semantics only (TrieOfStorage's DupSort
// shape is already flattened by AutoDupSortKeysConversion), which is exactly
// what mergedCursor implements; the remaining Cursor methods fail loudly so
// any future use is caught immediately by the equivalence tests.
package commitment

import (
	"bytes"
	"fmt"

	"github.com/tidwall/btree"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type overlayKV struct {
	k []byte
	v []byte // nil = tombstone (delete pending)
}

func overlayLess(a, b overlayKV) bool { return bytes.Compare(a.k, b.k) < 0 }

// TrieOverlay holds pending TrieOfAccounts/TrieOfStorage writes.
type TrieOverlay struct {
	acc  *btree.BTreeG[overlayKV]
	stor *btree.BTreeG[overlayKV]
}

func NewTrieOverlay() *TrieOverlay {
	// NoLocks: the computer/builder is single-threaded, and the default
	// tree's iterator holds an internal RLock until Release — a cursor left
	// open across a Delete (the wipe paths use `defer cursor.Close()`)
	// self-deadlocks. Cursors additionally iterate an O(1) COW Copy of the
	// tree (newMergedCursor), so a mutation never invalidates a live cursor.
	opts := btree.Options{NoLocks: true}
	return &TrieOverlay{
		acc:  btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
		stor: btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
	}
}

func (o *TrieOverlay) tree(table string) *btree.BTreeG[overlayKV] {
	if table == modules.TrieOfAccounts {
		return o.acc
	}
	return o.stor
}

// Handles reports whether the overlay intercepts this table.
func (o *TrieOverlay) Handles(table string) bool {
	return table == modules.TrieOfAccounts || table == modules.TrieOfStorage
}

func (o *TrieOverlay) Put(table string, k, v []byte) {
	o.tree(table).Set(overlayKV{k: append([]byte{}, k...), v: append([]byte{}, v...)})
}

func (o *TrieOverlay) Delete(table string, k []byte) {
	o.tree(table).Set(overlayKV{k: append([]byte{}, k...)})
}

// Get returns (value, present-in-overlay). value nil with present=true means
// a pending delete.
func (o *TrieOverlay) Get(table string, k []byte) ([]byte, bool) {
	e, ok := o.tree(table).Get(overlayKV{k: k})
	if !ok {
		return nil, false
	}
	return e.v, true
}

func (o *TrieOverlay) Len() int { return o.acc.Len() + o.stor.Len() }

// FlushTo applies the pending writes to the transaction in sorted order
// (near-append B-tree insertion) and clears the overlay. TrieOfStorage keeps
// the delete-before-put discipline (DupSort stale-version fix).
func (o *TrieOverlay) FlushTo(tx kv.RwTx) error {
	it := o.acc.Iter()
	for ok := it.First(); ok; ok = it.Next() {
		e := it.Item()
		if e.v == nil {
			if err := tx.Delete(modules.TrieOfAccounts, e.k); err != nil {
				return err
			}
		} else if err := tx.Put(modules.TrieOfAccounts, e.k, e.v); err != nil {
			return err
		}
	}
	it.Release()
	it = o.stor.Iter()
	for ok := it.First(); ok; ok = it.Next() {
		e := it.Item()
		if err := tx.Delete(modules.TrieOfStorage, e.k); err != nil {
			return err
		}
		if e.v != nil {
			if err := tx.Put(modules.TrieOfStorage, e.k, e.v); err != nil {
				return err
			}
		}
	}
	it.Release()
	o.acc.Clear()
	o.stor.Clear()
	return nil
}

// ---------------------------------------------------------------------------
// tx wrapper

// overlayTx routes TrieOf* operations through the overlay; every other table
// passes straight to the embedded transaction.
type overlayTx struct {
	kv.RwTx
	o *TrieOverlay
}

// WrapTrieOverlay returns tx with TrieOfAccounts/TrieOfStorage intercepted.
func WrapTrieOverlay(tx kv.RwTx, o *TrieOverlay) kv.RwTx {
	return &overlayTx{RwTx: tx, o: o}
}

func (t *overlayTx) Put(table string, k, v []byte) error {
	if t.o.Handles(table) {
		t.o.Put(table, k, v)
		return nil
	}
	return t.RwTx.Put(table, k, v)
}

func (t *overlayTx) Delete(table string, k []byte) error {
	if t.o.Handles(table) {
		t.o.Delete(table, k)
		return nil
	}
	return t.RwTx.Delete(table, k)
}

func (t *overlayTx) GetOne(table string, k []byte) ([]byte, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v, nil // v nil = deleted
		}
	}
	return t.RwTx.GetOne(table, k)
}

func (t *overlayTx) Has(table string, k []byte) (bool, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v != nil, nil
		}
	}
	return t.RwTx.Has(table, k)
}

func (t *overlayTx) Cursor(table string) (kv.Cursor, error) {
	c, err := t.RwTx.Cursor(table)
	if err != nil || !t.o.Handles(table) {
		return c, err
	}
	return newMergedCursor(c, t.o.tree(table)), nil
}

func (t *overlayTx) CursorDupSort(table string) (kv.CursorDupSort, error) {
	if !t.o.Handles(table) {
		return t.RwTx.CursorDupSort(table)
	}
	c, err := t.RwTx.CursorDupSort(table)
	if err != nil {
		return nil, err
	}
	// The trie loader drives this cursor with flat Seek/Next/First only (the
	// kv layer's AutoDupSortKeysConversion presents full keys); dup-specific
	// methods fail loudly below.
	return &mergedDupCursor{mergedCursor: newMergedCursor(c, t.o.tree(table))}, nil
}

// ---------------------------------------------------------------------------
// merged cursor: overlay btree ∪ base cursor, flat key order, tombstones mask

type mergedCursor struct {
	base kv.Cursor
	tree *btree.BTreeG[overlayKV]
	it   btree.IterG[overlayKV]
	// lookahead
	bk, bv []byte // base current (nil bk = exhausted)
	ok_    bool   // overlay iterator valid
	emit   int    // which side emitted last: 0 none, 1 base, 2 overlay, 3 both(equal)
	// tolerateBaseSeekEOF: only the AutoDupSort HashedStorage base cursor can
	// surface a non-NOTFOUND "end of file" error from its flat Seek (seekDupSort's
	// bare Next() after a GetBothRange miss at end-of-data); for it that error
	// MEANS "no base row >= seek". The other three tables' base.Seek already maps
	// NOTFOUND→nil and only errors on a real fault, so they must NOT swallow it.
	tolerateBaseSeekEOF bool
}

func newMergedCursor(base kv.Cursor, tree *btree.BTreeG[overlayKV]) *mergedCursor {
	// Iterate a COW snapshot: O(1) to take, and later overlay mutations
	// (e.g. the wipe paths deleting while their cursor is still deferred-
	// open) cannot invalidate or deadlock a live cursor.
	snap := tree.Copy()
	return &mergedCursor{base: base, tree: snap, it: snap.Iter()}
}

func (m *mergedCursor) Close()           { m.it.Release(); m.base.Close() }
func (m *mergedCursor) overlayK() []byte { return m.it.Item().k }

// current returns the merged (k,v) at the lookahead, resolving tombstones by
// advancing past them.
func (m *mergedCursor) current() ([]byte, []byte, error) {
	for {
		hasB := m.bk != nil
		hasO := m.ok_
		switch {
		case !hasB && !hasO:
			m.emit = 0
			return nil, nil, nil
		case hasB && (!hasO || bytes.Compare(m.bk, m.overlayK()) < 0):
			m.emit = 1
			return m.bk, m.bv, nil
		case hasO && (!hasB || bytes.Compare(m.overlayK(), m.bk) < 0):
			e := m.it.Item()
			if e.v == nil {
				// Tombstone with no base row at this key: skip.
				m.ok_ = m.it.Next()
				continue
			}
			m.emit = 2
			return e.k, e.v, nil
		default: // equal keys: overlay wins; tombstone masks the base row
			e := m.it.Item()
			if e.v == nil {
				if err := m.advanceBase(); err != nil {
					return nil, nil, err
				}
				m.ok_ = m.it.Next()
				continue
			}
			m.emit = 3
			return e.k, e.v, nil
		}
	}
}

func (m *mergedCursor) advanceBase() error {
	k, v, err := m.base.Next()
	if err != nil {
		return err
	}
	m.bk, m.bv = k, v
	return nil
}

func (m *mergedCursor) Seek(seek []byte) ([]byte, []byte, error) {
	k, v, err := m.base.Seek(seek)
	if err != nil {
		if !m.tolerateBaseSeekEOF {
			return nil, nil, err
		}
		// HashedStorage only: the AutoDupSort base cursor driven through the FLAT
		// Seek interface (seekDupSort) can, after a GetBothRange miss (no dup >= the
		// slot-part of seek under that account), advance via a bare Next() from an
		// undefined cursor position and surface a non-NOTFOUND error
		// ("mdbx_cursor_get: Reached the end of the file"). That only happens when
		// the account has NO base row >= seek, so the correct merged answer is
		// "base contributes nothing at/after seek": treat the base as exhausted and
		// fall through to the overlay. Propagating it (the original code) made
		// stateDupCursor.SeekBothRange return nil and silently DROP an
		// overlay-resident slot — the --concurrent-root 64511/103423 gold-check
		// mismatch. Cursor REUSE positions it to trigger; a fresh cursor returns nil
		// cleanly. See merged_cursor_base_error_test.go.
		k, v = nil, nil
	}
	m.bk, m.bv = k, v
	m.ok_ = m.it.Seek(overlayKV{k: seek})
	return m.current()
}

func (m *mergedCursor) First() ([]byte, []byte, error) { return m.Seek(nil) }

func (m *mergedCursor) Next() ([]byte, []byte, error) {
	switch m.emit {
	case 1:
		if err := m.advanceBase(); err != nil {
			return nil, nil, err
		}
	case 2:
		m.ok_ = m.it.Next()
	case 3:
		if err := m.advanceBase(); err != nil {
			return nil, nil, err
		}
		m.ok_ = m.it.Next()
	default:
		return nil, nil, fmt.Errorf("trie overlay cursor: Next without position")
	}
	return m.current()
}

func (m *mergedCursor) SeekExact(key []byte) ([]byte, []byte, error) {
	if e, ok := m.tree.Get(overlayKV{k: key}); ok {
		if e.v == nil {
			return nil, nil, nil // pending delete masks the base row
		}
		return e.k, e.v, nil
	}
	return m.base.SeekExact(key)
}

// Unused by the trie loader — fail loudly so misuse is caught in tests.
func (m *mergedCursor) Prev() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay cursor: Prev unsupported")
}
func (m *mergedCursor) Last() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay cursor: Last unsupported")
}
func (m *mergedCursor) Current() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay cursor: Current unsupported")
}
func (m *mergedCursor) Count() (uint64, error) {
	return 0, fmt.Errorf("trie overlay cursor: Count unsupported")
}

// mergedDupCursor satisfies kv.CursorDupSort; the dup-specific surface is
// unused on the flattened TrieOfStorage view.
type mergedDupCursor struct {
	*mergedCursor
}

func (m *mergedDupCursor) SeekBothExact(_, _ []byte) ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay dup cursor: SeekBothExact unsupported")
}
func (m *mergedDupCursor) SeekBothRange(_, _ []byte) ([]byte, error) {
	return nil, fmt.Errorf("trie overlay dup cursor: SeekBothRange unsupported")
}
func (m *mergedDupCursor) FirstDup() ([]byte, error) {
	return nil, fmt.Errorf("trie overlay dup cursor: FirstDup unsupported")
}
func (m *mergedDupCursor) NextDup() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay dup cursor: NextDup unsupported")
}
func (m *mergedDupCursor) NextNoDup() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay dup cursor: NextNoDup unsupported")
}
func (m *mergedDupCursor) PrevDup() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay dup cursor: PrevDup unsupported")
}
func (m *mergedDupCursor) PrevNoDup() ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("trie overlay dup cursor: PrevNoDup unsupported")
}
func (m *mergedDupCursor) LastDup() ([]byte, error) {
	return nil, fmt.Errorf("trie overlay dup cursor: LastDup unsupported")
}
func (m *mergedDupCursor) CountDuplicates() (uint64, error) {
	return 0, fmt.Errorf("trie overlay dup cursor: CountDuplicates unsupported")
}
