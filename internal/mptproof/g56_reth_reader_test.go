package mptproof

// g56: exercises reth_trie_reader.go / reth_trie_reader_v2.go /
// reth_trie_walk.go against a synthetic, hand-encoded AccountsTrie /
// StoragesTrie / StoragesTrieV2 fixture — no production reth datadir
// required (the existing tests for these files all `t.Skip("--short")`
// against a real D:\...  path, so under -short they contribute zero
// coverage). g56EncodeBranch is the inverse of DecodeRethBranchNodeCompact.

import (
	"context"
	"encoding/binary"
	"math/bits"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// g56EncodeBranch is the exact inverse of DecodeRethBranchNodeCompact:
// [state_mask:u16 LE][tree_mask:u16 LE][hash_mask:u16 LE][optional 32B
// rootHash][popcount(hashMask) * 32B hashes].
func g56EncodeBranch(stateMask, treeMask, hashMask uint16, rootHash *[32]byte, hashes [][32]byte) []byte {
	n := bits.OnesCount16(hashMask)
	if len(hashes) != n {
		panic("g56EncodeBranch: hashes count must equal popcount(hashMask)")
	}
	size := 6 + n*32
	if rootHash != nil {
		size += 32
	}
	out := make([]byte, size)
	binary.LittleEndian.PutUint16(out[0:2], stateMask)
	binary.LittleEndian.PutUint16(out[2:4], treeMask)
	binary.LittleEndian.PutUint16(out[4:6], hashMask)
	off := 6
	if rootHash != nil {
		copy(out[off:off+32], rootHash[:])
		off += 32
	}
	for _, h := range hashes {
		copy(out[off:off+32], h[:])
		off += 32
	}
	return out
}

// g56FixtureTableCfg mirrors the table set NewRethHashedLeafSource
// registers (it opens its env Readonly(), so fixture rows must be
// written through a separate, writable handle on the same directory
// BEFORE the readonly source is opened).
func g56FixtureTableCfg(d kv.TableCfg) kv.TableCfg {
	d[rethHashedAccountsTable] = kv.TableCfgItem{}
	d[rethHashedStoragesTable] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethPlainAccountStateTable] = kv.TableCfgItem{}
	d[rethPlainStorageStateTable] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethAccountsTrieTable] = kv.TableCfgItem{}
	d[rethStoragesTrieTable] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// g56OpenFixtureWriter opens a fresh temp-dir MDBX env, writable, with
// the same table set reth_hashed.go's readonly reader expects. The
// caller seeds rows and MUST call db.Close() before opening a
// RethHashedLeafSource on the same dir (MDBX allows only one writer).
func g56OpenFixtureWriter(t *testing.T) (dir string, db kv.RwDB) {
	t.Helper()
	dir = t.TempDir()
	logger := log.New()
	rwdb, err := mdbxkv.NewMDBX(logger).
		Path(dir).Label(kv.ChainDB).PageSize(4096).MapSize(1 * datasize.GB).
		WithTableCfg(g56FixtureTableCfg).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open fixture writer: %v", err)
	}
	return dir, rwdb
}

// g56OpenFixtureSource seeds nothing itself; it writes rows via seed,
// closes the writer, then opens the production readonly
// RethHashedLeafSource on the same directory.
func g56OpenFixtureSource(t *testing.T, seed func(db kv.RwDB)) *RethHashedLeafSource {
	t.Helper()
	dir, db := g56OpenFixtureWriter(t)
	if seed != nil {
		seed(db)
	}
	db.Close()

	src, err := NewRethHashedLeafSource(filepath.Join(dir), 1)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	return src
}

func g56PutAccountsTrieRow(t *testing.T, db kv.RwDB, nibblePath []byte, branch []byte) {
	t.Helper()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Put(rethAccountsTrieTable, nibblePath, branch); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func g56PutStoragesTrieRow(t *testing.T, db kv.RwDB, addrHash, nibblePath []byte, branch []byte) {
	t.Helper()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c, err := tx.RwCursorDupSort(rethStoragesTrieTable)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	subKey := EncodeStoredNibbles(nibblePath)
	val := append(append([]byte{}, subKey...), branch...)
	if err := c.Put(addrHash, val); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func g56PutStoragesTrieV2Row(t *testing.T, db kv.RwDB, addrHash, nibblePath []byte, branch []byte) {
	t.Helper()
	subKey, err := PackNibblesV2(nibblePath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c, err := tx.RwCursorDupSort(rethStoragesTrieV2Table)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	val := append(append([]byte{}, subKey...), branch...)
	if err := c.Put(addrHash, val); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRethTrieReader_AccountBranchAt_And_Root(t *testing.T) {
	var rootHash [32]byte
	for i := range rootHash {
		rootHash[i] = byte(i + 1)
	}
	// Root branch at the empty prefix, with HasRoot set.
	rootBranch := g56EncodeBranch(0x0003 /*state bits 0,1*/, 0, 0, &rootHash, nil)
	src := g56OpenFixtureSource(t, func(db kv.RwDB) {
		g56PutAccountsTrieRow(t, db, nil, rootBranch)
	})
	defer src.Close()

	r := NewRethTrieReader(src)
	bn, ok, err := r.AccountBranchAt(nil)
	if err != nil || !ok {
		t.Fatalf("AccountBranchAt(nil): ok=%v err=%v", ok, err)
	}
	if !bn.HasRoot || bn.RootHash != rootHash {
		t.Fatalf("root branch roundtrip wrong: hasRoot=%v root=%x", bn.HasRoot, bn.RootHash)
	}
	if bn.HasState != 0x0003 {
		t.Errorf("state mask mismatch: %04x", bn.HasState)
	}

	got, err := r.AccountTrieRoot()
	if err != nil {
		t.Fatalf("AccountTrieRoot: %v", err)
	}
	if got != rootHash {
		t.Errorf("AccountTrieRoot mismatch: got %x want %x", got, rootHash)
	}

	// Missing prefix -> ok=false, no error.
	if _, ok, err := r.AccountBranchAt([]byte{0x5}); err != nil || ok {
		t.Errorf("expected miss for absent prefix: ok=%v err=%v", ok, err)
	}
}

func TestRethTrieReader_AccountTrieRoot_NoRootBranchErrors(t *testing.T) {
	// A branch at the empty prefix exists but has no hashes at all
	// (hash_mask=0) and therefore HasRoot=false.
	branch := g56EncodeBranch(0x0001, 0, 0, nil, nil)
	src := g56OpenFixtureSource(t, func(db kv.RwDB) {
		g56PutAccountsTrieRow(t, db, nil, branch)
	})
	defer src.Close()

	r := NewRethTrieReader(src)
	if _, err := r.AccountTrieRoot(); err == nil {
		t.Error("expected error when root branch lacks RootHash")
	}
}

func TestRethTrieReader_StorageBranchAt(t *testing.T) {
	addrHash := make([]byte, 32)
	addrHash[0] = 0xAB
	path := []byte{3, 7}
	branch := g56EncodeBranch(0x0005, 0, 0, nil, nil)
	src := g56OpenFixtureSource(t, func(db kv.RwDB) {
		g56PutStoragesTrieRow(t, db, addrHash, path, branch)
		// A second, lexicographically later subkey under the same addrHash so
		// SeekBothRange has to land exactly on our target and not just the
		// first dup.
		g56PutStoragesTrieRow(t, db, addrHash, []byte{3, 7, 9}, g56EncodeBranch(0x0001, 0, 0, nil, nil))
	})
	defer src.Close()

	r := NewRethTrieReader(src)
	bn, ok, err := r.StorageBranchAt(addrHash, path)
	if err != nil || !ok {
		t.Fatalf("StorageBranchAt: ok=%v err=%v", ok, err)
	}
	if bn.HasState != 0x0005 {
		t.Errorf("storage branch state mask mismatch: %04x", bn.HasState)
	}

	// Mismatched addrHash -> no match.
	otherAddr := make([]byte, 32)
	otherAddr[0] = 0xCD
	if _, ok, err := r.StorageBranchAt(otherAddr, path); err != nil || ok {
		t.Errorf("expected miss for unseeded addrHash: ok=%v err=%v", ok, err)
	}

	// Bad addrHash length is a programming error -> explicit error.
	if _, _, err := r.StorageBranchAt([]byte{1, 2, 3}, path); err == nil {
		t.Error("expected error for short addrHash")
	}

	// A nibble path that sorts after everything seeded for this
	// addrHash -> SeekBothRange returns nil (exercises the "not found,
	// past the last dup" branch).
	if _, ok, err := r.StorageBranchAt(addrHash, []byte{0xf, 0xf, 0xf, 0xf}); err != nil || ok {
		t.Errorf("expected miss past the last dup: ok=%v err=%v", ok, err)
	}
}

func TestRethTrieReaderV2_StorageBranchAt(t *testing.T) {
	dir := t.TempDir()
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).Label(kv.ChainDB).PageSize(4096).MapSize(1 * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethStoragesTrieV2Table] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).Open(context.Background())
	if err != nil {
		t.Fatalf("open v2 fixture: %v", err)
	}
	addrHash := make([]byte, 32)
	addrHash[0] = 0x11
	path := []byte{1, 2, 3}
	branch := g56EncodeBranch(0x0002, 0, 0, nil, nil)
	g56PutStoragesTrieV2Row(t, db, addrHash, path, branch)
	db.Close()

	r, err := OpenRethTrieReaderV2(dir, 1)
	if err != nil {
		t.Fatalf("OpenRethTrieReaderV2: %v", err)
	}
	defer r.Close()

	bn, ok, err := r.StorageBranchAt(addrHash, path)
	if err != nil || !ok {
		t.Fatalf("v2 StorageBranchAt: ok=%v err=%v", ok, err)
	}
	if bn.HasState != 0x0002 {
		t.Errorf("v2 branch state mask mismatch: %04x", bn.HasState)
	}

	if _, _, err := r.StorageBranchAt([]byte{1}, path); err == nil {
		t.Error("expected error for short addrHash in v2 reader")
	}

	otherAddr := make([]byte, 32)
	otherAddr[0] = 0x22
	if _, ok, err := r.StorageBranchAt(otherAddr, path); err != nil || ok {
		t.Errorf("v2: expected miss for unseeded addrHash: ok=%v err=%v", ok, err)
	}

	// Closing twice must be a no-op, not a panic/double-close error.
	if err := r.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

// TestWalkRethAccount_TwoHopPath builds a minimal 2-level AccountsTrie
// (depth-1 branch at nibble [n0] pointing at a depth-2 branch at
// [n0,n1]) and checks WalkRethAccount reaches WalkExhaustedTarget with
// exactly two hops, following the documented depth-1-start convention.
func TestWalkRethAccount_TwoHopPath(t *testing.T) {
	// hashedKey = single byte 0x27 -> nibbles [2,7].
	hashedKey := []byte{0x27}

	src := g56OpenFixtureSource(t, func(db kv.RwDB) {
		// depth-1 branch at prefix [2]: state bit 7 set (child nibble 7 exists).
		depth1 := g56EncodeBranch(1<<7, 0, 0, nil, nil)
		g56PutAccountsTrieRow(t, db, []byte{2}, depth1)
		// depth-2 branch at prefix [2,7]: arbitrary leaf-ish branch.
		depth2 := g56EncodeBranch(1<<0, 0, 0, nil, nil)
		g56PutAccountsTrieRow(t, db, []byte{2, 7}, depth2)
	})
	defer src.Close()

	r := NewRethTrieReader(src)
	walk, err := WalkRethAccount(r, hashedKey)
	if err != nil {
		t.Fatalf("WalkRethAccount: %v", err)
	}
	if len(walk.Hops) != 2 {
		t.Fatalf("expected 2 hops, got %d (outcome=%v)", len(walk.Hops), walk.Outcome)
	}
	if walk.Hops[0].PrefixDepth != 1 || walk.Hops[1].PrefixDepth != 2 {
		t.Errorf("unexpected hop depths: %d, %d", walk.Hops[0].PrefixDepth, walk.Hops[1].PrefixDepth)
	}
}

// TestWalkRethAccount_NoBranchAtPath exercises the miss path: nothing
// seeded at all, so the very first depth-1 read fails.
func TestWalkRethAccount_NoBranchAtPath(t *testing.T) {
	src := g56OpenFixtureSource(t, nil)
	defer src.Close()

	r := NewRethTrieReader(src)
	walk, err := WalkRethAccount(r, []byte{0x99})
	if err != nil {
		t.Fatalf("WalkRethAccount: %v", err)
	}
	if len(walk.Hops) != 0 {
		t.Errorf("expected no hops on immediate miss, got %d", len(walk.Hops))
	}
}

// TestWalkRethStorage_NoSuchChild seeds a depth-1 branch whose state
// mask does NOT include the target's first nibble after depth-1, so
// the walk must report NoSuchChild rather than descending further.
func TestWalkRethStorage_NoSuchChild(t *testing.T) {
	addrHash := make([]byte, 32)
	addrHash[0] = 0x44
	slotHashedKey := []byte{0x13} // nibbles [1,3]

	src := g56OpenFixtureSource(t, func(db kv.RwDB) {
		// depth-1 branch at prefix [1]: state bit for nibble 3 is CLEAR.
		depth1 := g56EncodeBranch(1<<9 /* bit 9, not 3 */, 0, 0, nil, nil)
		g56PutStoragesTrieRow(t, db, addrHash, []byte{1}, depth1)
	})
	defer src.Close()

	r := NewRethTrieReader(src)
	walk, err := WalkRethStorage(r, addrHash, slotHashedKey)
	if err != nil {
		t.Fatalf("WalkRethStorage: %v", err)
	}
	if len(walk.Hops) != 1 {
		t.Fatalf("expected exactly 1 hop before NoSuchChild, got %d", len(walk.Hops))
	}
}

func TestWalkRethStorage_BadAddrHashLen(t *testing.T) {
	src := g56OpenFixtureSource(t, nil)
	defer src.Close()
	r := NewRethTrieReader(src)
	if _, err := WalkRethStorage(r, []byte{1, 2}, []byte{0x01}); err == nil {
		t.Error("expected error for short addrHash in WalkRethStorage")
	}
}
