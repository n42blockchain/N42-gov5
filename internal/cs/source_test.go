package cs

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// makeTestFreezer creates a tiny freezer with acctcs/storcs each
// holding `n` blocks of synthetic blobs. Returns the freezer (open
// for read/write) and the dir.
func makeTestFreezer(t *testing.T, n uint64) (*freezer.Freezer, string) {
	t.Helper()
	dir := t.TempDir()
	fr, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer.New: %v", err)
	}
	t.Cleanup(func() { fr.Close() })

	acctTbl, err := fr.EnsureTableCompressed(freezer.TableAccountChanges, "c")
	if err != nil {
		t.Fatalf("EnsureTableCompressed acct: %v", err)
	}
	stoTbl, err := fr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	if err != nil {
		t.Fatalf("EnsureTableCompressed sto: %v", err)
	}
	for i := uint64(0); i < n; i++ {
		// Tag each blob with its block number for verifiability
		acctBlob := []byte{byte('a'), byte(i), byte(i >> 8)}
		stoBlob := []byte{byte('s'), byte(i), byte(i >> 8)}
		if err := acctTbl.Append(i, acctBlob); err != nil {
			t.Fatalf("Append acct[%d]: %v", i, err)
		}
		if err := stoTbl.Append(i, stoBlob); err != nil {
			t.Fatalf("Append sto[%d]: %v", i, err)
		}
	}
	if err := fr.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return fr, dir
}

func TestFreezerSourceFullRange(t *testing.T) {
	fr, _ := makeTestFreezer(t, 100)
	src := NewFreezerSource(fr)

	if !src.Available(0) || !src.Available(99) {
		t.Errorf("Available should be true for [0, 100)")
	}
	if src.Available(100) {
		t.Errorf("Available should be false for 100")
	}

	for _, blk := range []uint64{0, 50, 99} {
		data, err := src.RetrieveAccount(blk)
		if err != nil {
			t.Errorf("RetrieveAccount(%d): %v", blk, err)
		}
		if len(data) != 3 || data[0] != 'a' || data[1] != byte(blk) {
			t.Errorf("blk %d: unexpected acct blob %x", blk, data)
		}
	}
	_, err := src.RetrieveAccount(100)
	if !errors.Is(err, ErrDeepReorg) {
		t.Errorf("RetrieveAccount(100): want ErrDeepReorg, got %v", err)
	}
}

func TestWarmSourceWindowEnforcement(t *testing.T) {
	dir := t.TempDir()
	// Simulate prune output: a warm freezer holding items 0..49 mapped to
	// absolute blocks [50, 99].
	warmFr, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer.New: %v", err)
	}
	acctTbl, _ := warmFr.EnsureTableCompressed(freezer.TableAccountChanges, "c")
	stoTbl, _ := warmFr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	for i := uint64(0); i < 50; i++ {
		acctTbl.Append(i, []byte{byte('A'), byte(i + 50)})
		stoTbl.Append(i, []byte{byte('S'), byte(i + 50)})
	}
	warmFr.Sync()
	warmFr.Close()

	if err := WriteMeta(dir, Meta{
		BaseBlock:      50,
		HeadBlock:      99,
		KeepBlocks:     50,
		CreatedAt:      time.Now(),
		SrcFreezerPath: "test",
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	warm, err := Open(dir)
	if err != nil {
		t.Fatalf("Open warm: %v", err)
	}
	t.Cleanup(func() { warm.Close() })

	src := NewWarmSource(warm)

	// In-window blocks
	for _, blk := range []uint64{50, 75, 99} {
		if !src.Available(blk) {
			t.Errorf("blk %d should be available", blk)
		}
		data, err := src.RetrieveAccount(blk)
		if err != nil {
			t.Errorf("RetrieveAccount(%d): %v", blk, err)
		}
		if len(data) != 2 || data[0] != 'A' || data[1] != byte(blk) {
			t.Errorf("blk %d: bad blob %x", blk, data)
		}
	}

	// Out-of-window blocks → ErrDeepReorg
	for _, blk := range []uint64{0, 49, 100, 999} {
		if src.Available(blk) {
			t.Errorf("blk %d should NOT be available", blk)
		}
		_, err := src.RetrieveAccount(blk)
		if !errors.Is(err, ErrDeepReorg) {
			t.Errorf("blk %d acct: want ErrDeepReorg, got %v", blk, err)
		}
		_, err = src.RetrieveStorage(blk)
		if !errors.Is(err, ErrDeepReorg) {
			t.Errorf("blk %d sto: want ErrDeepReorg, got %v", blk, err)
		}
	}

	desc := src.WindowDescription()
	if want := "warm[50, 99] (50 blocks)"; desc != want {
		t.Errorf("WindowDescription: got %q want %q", desc, want)
	}
}

func TestTieredSourceFallthrough(t *testing.T) {
	// Layer 1: full freezer over blocks [0, 100)
	full, _ := makeTestFreezer(t, 100)

	// Layer 2: warm over [80, 99] — newer data, takes priority via order
	warmDir := t.TempDir()
	warmFr, _ := freezer.New(warmDir, 0)
	acctTbl, _ := warmFr.EnsureTableCompressed(freezer.TableAccountChanges, "c")
	stoTbl, _ := warmFr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	for i := uint64(0); i < 20; i++ {
		// Mark warm blobs distinctly so we can tell which source served
		acctTbl.Append(i, []byte{byte('W'), byte(i + 80)})
		stoTbl.Append(i, []byte{byte('W'), byte(i + 80)})
	}
	warmFr.Sync()
	warmFr.Close()
	WriteMeta(warmDir, Meta{BaseBlock: 80, HeadBlock: 99, KeepBlocks: 20})
	warm, _ := Open(warmDir)
	t.Cleanup(func() { warm.Close() })

	src := NewTieredSource(NewWarmSource(warm), NewFreezerSource(full))

	// Block 50: not in warm → falls through to full freezer
	data, err := src.RetrieveAccount(50)
	if err != nil {
		t.Fatalf("blk 50: %v", err)
	}
	if data[0] != 'a' {
		t.Errorf("blk 50: should come from full freezer (a=...), got %x", data)
	}

	// Block 90: in warm → warm serves it
	data, err = src.RetrieveAccount(90)
	if err != nil {
		t.Fatalf("blk 90: %v", err)
	}
	if data[0] != 'W' {
		t.Errorf("blk 90: should come from warm (W=...), got %x", data)
	}

	// Block 150: out of both → ErrDeepReorg
	_, err = src.RetrieveAccount(150)
	if !errors.Is(err, ErrDeepReorg) {
		t.Errorf("blk 150: want ErrDeepReorg, got %v", err)
	}

	// Description includes both windows
	desc := src.WindowDescription()
	wantParts := []string{"warm", "freezer"}
	for _, p := range wantParts {
		if !contains(desc, p) {
			t.Errorf("WindowDescription %q missing %q", desc, p)
		}
	}
}

func TestTieredSourceWarmOnlyDeepReorg(t *testing.T) {
	// Simulate post-prune scenario: only warm tier, no full freezer
	warmDir := t.TempDir()
	warmFr, _ := freezer.New(warmDir, 0)
	acctTbl, _ := warmFr.EnsureTableCompressed(freezer.TableAccountChanges, "c")
	stoTbl, _ := warmFr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	for i := uint64(0); i < 10; i++ {
		acctTbl.Append(i, []byte{0xff})
		stoTbl.Append(i, []byte{0xff})
	}
	warmFr.Sync()
	warmFr.Close()
	WriteMeta(warmDir, Meta{BaseBlock: 1000, HeadBlock: 1009, KeepBlocks: 10})
	warm, _ := Open(warmDir)
	t.Cleanup(func() { warm.Close() })

	src := NewTieredSource(NewWarmSource(warm))

	// Reorg to anything older than 1000 → ErrDeepReorg, descriptive
	_, err := src.RetrieveAccount(500)
	if !errors.Is(err, ErrDeepReorg) {
		t.Errorf("deep reorg: want ErrDeepReorg, got %v", err)
	}
	if !contains(err.Error(), "warm[1000, 1009]") {
		t.Errorf("error message missing window info: %v", err)
	}
}

func TestFreezerSourceMissingTable(t *testing.T) {
	dir := t.TempDir()
	fr, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer.New: %v", err)
	}
	defer fr.Close()
	// Don't create any tables
	src := NewFreezerSource(fr)
	if src.Available(0) {
		t.Errorf("Available should be false when no acctcs table exists")
	}
	data, err := src.RetrieveAccount(0)
	if err != nil || data != nil {
		t.Errorf("no-table RetrieveAccount: got (%v, %v), want (nil, nil)", data, err)
	}
	_ = filepath.Base(dir) // silence unused import warning if needed
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
