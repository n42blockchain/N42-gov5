package commitment

// Regression guard for the --concurrent-root 64511/103423 gold-check mismatch.
//
// The StateOverlay drives the base HashedStorage/TrieOfStorage cursor through the
// FLAT Seek interface (seekDupSort). After a GetBothRange miss (account has no
// row >= the seeked slot) seekDupSort advances via a bare Next() from an
// undefined cursor position, which can surface a non-NOTFOUND error
// ("mdbx_cursor_get: Reached the end of the file"). The original mergedCursor.Seek
// propagated that error and returned early — so an overlay-resident row at/after
// the seek key was silently DROPPED. mergedCursor.Seek now treats a base error as
// "base contributes nothing at/after seek" and falls through to the overlay.
//
// This reproduces the exact condition with a mock base cursor (memdb's AutoDupSort
// does not surface the same EOF state, which is why the end-to-end build was the
// only thing that originally tripped it).

import (
	"errors"
	"testing"

	"github.com/tidwall/btree"
)

// errSeekCursor is a kv.Cursor whose Seek returns an EOF-style error (as the real
// AutoDupSort seekDupSort does after a GetBothRange miss at end-of-data). Only the
// methods mergedCursor exercises are meaningful; the rest are inert.
type errSeekCursor struct{ seekErr error }

func (c *errSeekCursor) Seek([]byte) ([]byte, []byte, error)      { return nil, nil, c.seekErr }
func (c *errSeekCursor) Next() ([]byte, []byte, error)            { return nil, nil, nil }
func (c *errSeekCursor) First() ([]byte, []byte, error)           { return nil, nil, nil }
func (c *errSeekCursor) SeekExact([]byte) ([]byte, []byte, error) { return nil, nil, nil }
func (c *errSeekCursor) Prev() ([]byte, []byte, error)            { return nil, nil, nil }
func (c *errSeekCursor) Last() ([]byte, []byte, error)            { return nil, nil, nil }
func (c *errSeekCursor) Current() ([]byte, []byte, error)         { return nil, nil, nil }
func (c *errSeekCursor) Count() (uint64, error)                   { return 0, nil }
func (c *errSeekCursor) Close()                                   {}

func TestMergedCursorBaseSeekErrorFallsThroughToOverlay(t *testing.T) {
	// Overlay holds a row the base "can't reach" (base.Seek errors).
	tree := btree.NewBTreeGOptions[overlayKV](overlayLess, btree.Options{})
	want := []byte("overlay-resident-value")
	key := []byte("acct-slot-key")
	tree.Set(overlayKV{k: key, v: want})

	// tolerateBaseSeekEOF=true is set ONLY for HashedStorage (the sole AutoDupSort
	// table whose flat Seek surfaces the EOF dance).
	mc := newMergedCursor(&errSeekCursor{seekErr: errors.New("mdbx_cursor_get: Reached the end of the file")}, tree)
	mc.tolerateBaseSeekEOF = true
	defer mc.Close()

	k, v, err := mc.Seek(key)
	if err != nil {
		t.Fatalf("Seek must NOT propagate the base error when tolerating EOF: %v", err)
	}
	if string(k) != string(key) || string(v) != string(want) {
		t.Fatalf("overlay row dropped on base error: got k=%q v=%q, want k=%q v=%q", k, v, key, want)
	}

	// And with a tombstone in the overlay, the base error must yield a clean nil
	// (not the propagated error, not a phantom row).
	tree2 := btree.NewBTreeGOptions[overlayKV](overlayLess, btree.Options{})
	tree2.Set(overlayKV{k: key}) // tombstone (v == nil)
	mc2 := newMergedCursor(&errSeekCursor{seekErr: errors.New("eof")}, tree2)
	mc2.tolerateBaseSeekEOF = true
	defer mc2.Close()
	if k, _, err := mc2.Seek(key); err != nil || k != nil {
		t.Fatalf("base error + overlay tombstone: want (nil,nil), got k=%q err=%v", k, err)
	}
}

// TestMergedCursorBaseSeekErrorPropagatesByDefault guards the precision of the
// fix: for tables OTHER than HashedStorage (HashedAccounts/TrieOfAccounts/
// TrieOfStorage — whose base.Seek already maps NOTFOUND→nil and only errors on a
// real fault), a base.Seek error must NOT be swallowed; it must propagate so a
// genuine I/O fault surfaces instead of silently shrinking the merged result.
func TestMergedCursorBaseSeekErrorPropagatesByDefault(t *testing.T) {
	tree := btree.NewBTreeGOptions[overlayKV](overlayLess, btree.Options{})
	tree.Set(overlayKV{k: []byte("k"), v: []byte("v")})
	ioErr := errors.New("mdbx: disk I/O error")
	mc := newMergedCursor(&errSeekCursor{seekErr: ioErr}, tree) // tolerateBaseSeekEOF defaults false
	defer mc.Close()
	if _, _, err := mc.Seek([]byte("k")); err == nil {
		t.Fatal("default mergedCursor must PROPAGATE a base.Seek error, not swallow it")
	}
}
