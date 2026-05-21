package mptproof

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// buildHashedIndexEnv writes a tiny HashedAccount + HashedStorageRef
// MDBX env from a list of (addr, value) pairs. Slots all map to one
// fixed slot value for storage tests.
func buildHashedIndexEnv(t *testing.T, accounts map[[20]byte][]byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hashed")
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(1 * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[HashedAccountTable] = kv.TableCfgItem{}
			d[HashedStorageRefTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open hashed env: %v", err)
	}
	defer db.Close()

	// Collect & sort by hashed key for AppendDup correctness.
	rows := make([]hashedRow, 0, len(accounts))
	for addr, v := range accounts {
		h := keccak(addr[:])
		var hashed [32]byte
		copy(hashed[:], h)
		rows = append(rows, hashedRow{hashed: hashed, val: v})
	}
	sortRowsByKey(rows)

	tx, _ := db.BeginRw(context.Background())
	c, _ := tx.RwCursor(HashedAccountTable)
	for _, r := range rows {
		if err := c.Append(r.hashed[:], r.val); err != nil {
			t.Fatalf("Append %x: %v", r.hashed[:8], err)
		}
	}
	c.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

type hashedRow struct {
	hashed [32]byte
	val    []byte
}

func sortRowsByKey(rows []hashedRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && bytes.Compare(rows[j-1].hashed[:], rows[j].hashed[:]) > 0; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func makeAccountSet(n int) map[[20]byte][]byte {
	out := make(map[[20]byte][]byte, n)
	for i := 0; i < n; i++ {
		var a [20]byte
		binary.BigEndian.PutUint32(a[:4], uint32(i))
		v := make([]byte, 16)
		binary.BigEndian.PutUint64(v[:8], uint64(i))
		v[15] = byte(i)
		out[a] = v
	}
	return out
}

// TestHashedLeafSource_PrefixDispatch checks two invariants:
//
//  1. collectAccountLeavesWithPrefix dispatches to the hashed cursor
//     path (not the ScanAccounts fallback) when given a HashedLeafSource.
//  2. The returned subLeaf list matches the fallback EXACTLY (same
//     order, same effective keys, same values).
func TestHashedLeafSource_PrefixDispatch(t *testing.T) {
	accounts := makeAccountSet(256)

	// Build hashed env from accounts.
	dir := buildHashedIndexEnv(t, accounts)

	base := &MapLeafSource{Accounts: accounts}
	h, err := OpenHashedLeafSource(dir, base)
	if err != nil {
		t.Fatalf("OpenHashedLeafSource: %v", err)
	}
	defer h.Close()

	// Test a few prefixes including: empty (must return all), short,
	// and one that probably matches nothing.
	for _, prefix := range [][]byte{
		{},
		{0x0},
		{0xa},
		{0x0, 0x0},
		{0xa, 0xb, 0xc, 0xd, 0xe, 0xf, 0x0, 0x1}, // probably empty
	} {
		fast, ferr := collectAccountLeavesWithPrefix(h, prefix)
		if ferr != nil {
			t.Fatalf("fast path prefix=%x: %v", prefix, ferr)
		}
		// Bypass the dispatch by passing the base directly.
		slow, serr := collectAccountLeavesWithPrefix(base, prefix)
		if serr != nil {
			t.Fatalf("slow path prefix=%x: %v", prefix, serr)
		}

		// MapLeafSource iterates a Go map (unordered); sort both for
		// comparison.
		sortSubLeaves(fast)
		sortSubLeaves(slow)
		if len(fast) != len(slow) {
			t.Errorf("prefix=%x: fast=%d slow=%d", prefix, len(fast), len(slow))
			continue
		}
		for i := range fast {
			if !bytes.Equal(fast[i].effectiveKey, slow[i].effectiveKey) {
				t.Errorf("prefix=%x [%d]: effKey fast=%x slow=%x",
					prefix, i, fast[i].effectiveKey, slow[i].effectiveKey)
			}
			if !bytes.Equal(fast[i].value, slow[i].value) {
				t.Errorf("prefix=%x [%d]: value fast=%x slow=%x",
					prefix, i, fast[i].value, slow[i].value)
			}
		}
		t.Logf("prefix=%x: %d leaves match between fast and slow paths", prefix, len(fast))
	}
}

func sortSubLeaves(s []subLeaf) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && bytes.Compare(s[j-1].effectiveKey, s[j].effectiveKey) > 0; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestNibblePrefixToByteSeek covers the even/odd-length nibble
// boundary cases.
func TestNibblePrefixToByteSeek(t *testing.T) {
	cases := []struct {
		prefix      []byte
		wantSeek    []byte
		wantPartial bool
	}{
		{[]byte{}, []byte{}, false},
		{[]byte{0xa}, []byte{0xa0}, true},
		{[]byte{0xa, 0xb}, []byte{0xab}, false},
		{[]byte{0xa, 0xb, 0xc}, []byte{0xab, 0xc0}, true},
		{[]byte{0x0, 0x0, 0x0, 0x0}, []byte{0x00, 0x00}, false},
	}
	for _, tc := range cases {
		seek, partial := nibblePrefixToByteSeek(tc.prefix)
		if !bytes.Equal(seek, tc.wantSeek) || partial != tc.wantPartial {
			t.Errorf("prefix=%x: seek=%x partial=%v (want seek=%x partial=%v)",
				tc.prefix, seek, partial, tc.wantSeek, tc.wantPartial)
		}
	}
}
