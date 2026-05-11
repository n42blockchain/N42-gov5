// Copyright 2022-2026 The N42 Authors

// hashstate_equivalence_test.go — pin that the streaming ETL path in
// RebuildHashedStateETL produces byte-identical HashedAccounts +
// HashedStorage as the legacy random-Put path (hashAllAccounts +
// hashAllStorage). fc83e221 swapped one for the other in
// FullStateRootVerify, and the only way to know they're truly
// interchangeable for state-root purposes is to compare the resulting
// tables on real Account/Storage shapes (precompiles, contracts, EOAs,
// emptyCodeHash normalisation, AutoDupSort 72→40+32 keys).

package ethel

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// seedPlainStateLarge writes a wider Account + Storage population to
// exercise edge cases:
//   - Bittrex-class high-nonce EOA (nonce in 3-byte BE)
//   - Many-slot contract (>64 slots, > one DupSort page-aligned batch)
//   - Slot values across 1B / 4B / 32B trimmed lengths
//   - Address ordering that won't trivially match plain-state cursor
//     ordering (so the ETL sort+merge is exercised vs random tx.Put)
func seedPlainStateLarge(t *testing.T, tx kv.RwTx) {
	t.Helper()
	emptyCodeHash := "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	_ = emptyCodeHash

	// 200 accounts at semi-random addresses (avoid prefix clusters).
	for i := 0; i < 200; i++ {
		var addr types.Address
		addr[0] = byte(i * 37)
		addr[1] = byte(i * 113)
		addr[2] = byte((i ^ 0x5a) * 7)
		addr[19] = byte(i)
		var acc account.StateAccount
		acc.Initialised = true
		acc.Nonce = uint64(i*1000 + 17) // varied nonce sizes (1-2 byte)
		acc.Balance = *uint256.NewInt(uint64(i+1) * 1000_000_000_000_000_000)
		if i%5 == 0 {
			var ch types.Hash
			for j := range ch {
				ch[j] = byte(i + j)
			}
			acc.CodeHash = ch
		}
		if err := tx.Put(modules.Account, addr[:], acc.MarshalV2()); err != nil {
			t.Fatal(err)
		}
	}

	// One high-nonce account (Bittrex-class)
	var hot types.Address
	for j := range hot {
		hot[j] = 0xd5
	}
	var hotAcc account.StateAccount
	hotAcc.Initialised = true
	hotAcc.Nonce = 4_977_492
	hotAcc.Balance = *uint256.NewInt(0xdeadbeefcafe)
	if err := tx.Put(modules.Account, hot[:], hotAcc.MarshalV2()); err != nil {
		t.Fatal(err)
	}

	// Storage: one big-slot-count contract (200 slots) — exceeds typical
	// DupSort sub-page boundary, exercises AutoDupSort 72→40+32 splits.
	var bigAddr types.Address
	bigAddr[0] = 0xab
	bigAddr[19] = 0xcd
	for i := 0; i < 200; i++ {
		var k [52]byte
		copy(k[:20], bigAddr[:])
		k[51] = byte(i)
		k[50] = byte(i >> 8)
		// Varied value lengths: 1B for i<50, 4B for 50≤i<100, 32B otherwise
		var val []byte
		switch {
		case i < 50:
			val = []byte{byte(i + 1)}
		case i < 100:
			val = []byte{0xde, 0xad, 0xbe, byte(i)}
		default:
			val = make([]byte, 32)
			for j := range val {
				val[j] = byte(i + j)
			}
		}
		if err := tx.Put(modules.Storage, k[:], val); err != nil {
			t.Fatal(err)
		}
	}
}

// seedPlainState writes a representative Account + Storage population:
//   - 3 EOAs (no code) with various balances + nonces
//   - 2 contracts (with codeHash) with multiple storage slots
//   - 1 zero-codeHash account (must be normalised to emptyCodeHash)
func seedPlainState(t *testing.T, tx kv.RwTx) {
	t.Helper()
	type acct struct {
		addr  string // 40 hex
		nonce uint64
		bal   uint64
		ch    string // 64 hex; "" means zero hash → normalise to emptyCodeHash
	}
	accounts := []acct{
		{"1111111111111111111111111111111111111111", 1, 100, ""},
		{"2222222222222222222222222222222222222222", 5, 250000, ""},
		{"3333333333333333333333333333333333333333", 0, 1, ""},
		// Two contracts:
		{"4444444444444444444444444444444444444444", 1, 0,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"5555555555555555555555555555555555555555", 1, 0,
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		// Zero codeHash → normaliseable to emptyCodeHash
		{"6666666666666666666666666666666666666666", 2, 42, ""},
	}
	for _, a := range accounts {
		addr := types.HexToAddress("0x" + a.addr)
		var acc account.StateAccount
		acc.Initialised = true
		acc.Nonce = a.nonce
		acc.Balance = *uint256.NewInt(a.bal)
		if a.ch != "" {
			acc.CodeHash = types.HexToHash("0x" + a.ch)
		}
		if err := tx.Put(modules.Account, addr[:], acc.MarshalV2()); err != nil {
			t.Fatal(err)
		}
	}
	// Storage for the two contracts: 3 slots each with assorted value
	// lengths (1B, 4B, 32B) to exercise BE-trimmed value paths.
	type slot struct {
		addr, slot, val string
	}
	slots := []slot{
		{"4444444444444444444444444444444444444444",
			"0000000000000000000000000000000000000000000000000000000000000000",
			"01"},
		{"4444444444444444444444444444444444444444",
			"1111111111111111111111111111111111111111111111111111111111111111",
			"deadbeef"},
		{"4444444444444444444444444444444444444444",
			"2222222222222222222222222222222222222222222222222222222222222222",
			"112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"},
		{"5555555555555555555555555555555555555555",
			"abcdef0000000000000000000000000000000000000000000000000000000000",
			"42"},
		{"5555555555555555555555555555555555555555",
			"f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0",
			"ffffffffffffffffffffffffffffffff"},
	}
	for _, s := range slots {
		var k [52]byte
		addr := types.HexToAddress("0x" + s.addr)
		slot := types.HexToHash("0x" + s.slot)
		copy(k[:20], addr[:])
		copy(k[20:], slot[:])
		val, _ := hex.DecodeString(s.val)
		if err := tx.Put(modules.Storage, k[:], val); err != nil {
			t.Fatal(err)
		}
	}
}

// dumpTable returns every (key, value) pair in iteration order.
func dumpTable(t *testing.T, tx kv.Tx, table string) [][2][]byte {
	t.Helper()
	cur, err := tx.Cursor(table)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	var out [][2][]byte
	for k, v, err := cur.First(); k != nil; k, v, err = cur.Next() {
		if err != nil {
			t.Fatal(err)
		}
		kc := make([]byte, len(k))
		vc := make([]byte, len(v))
		copy(kc, k)
		copy(vc, v)
		out = append(out, [2][]byte{kc, vc})
	}
	return out
}

func runEquivalence(t *testing.T, seed func(*testing.T, kv.RwTx)) {
	ctx := context.Background()
	legacyDB := memdb.New(t.TempDir())
	defer legacyDB.Close()
	{
		tx, err := legacyDB.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, tx)
		if err := hashAllAccounts(tx); err != nil {
			t.Fatalf("hashAllAccounts: %v", err)
		}
		if err := hashAllStorage(tx); err != nil {
			t.Fatalf("hashAllStorage: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	etlDB := memdb.New(t.TempDir())
	defer etlDB.Close()
	{
		tx, err := etlDB.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, tx)
		if err := RebuildHashedStateETL(ctx, tx, t.TempDir(), nil); err != nil {
			t.Fatalf("RebuildHashedStateETL: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	rotxL, _ := legacyDB.BeginRo(ctx)
	defer rotxL.Rollback()
	rotxE, _ := etlDB.BeginRo(ctx)
	defer rotxE.Rollback()
	for _, table := range []string{kv.HashedAccounts, kv.HashedStorage} {
		l := dumpTable(t, rotxL, table)
		e := dumpTable(t, rotxE, table)
		if len(l) != len(e) {
			t.Errorf("%s: row count mismatch legacy=%d etl=%d", table, len(l), len(e))
			continue
		}
		for i := range l {
			if !bytes.Equal(l[i][0], e[i][0]) {
				t.Errorf("%s row %d KEY mismatch legacy=%x etl=%x", table, i, l[i][0], e[i][0])
			}
			if !bytes.Equal(l[i][1], e[i][1]) {
				t.Errorf("%s row %d VAL mismatch key=%x legacy=%x etl=%x",
					table, i, l[i][0], l[i][1], e[i][1])
			}
		}
	}
}

func TestRebuildHashedStateETLMatchesLegacy(t *testing.T) {
	runEquivalence(t, seedPlainState)
}

func TestRebuildHashedStateETLMatchesLegacy_Large(t *testing.T) {
	runEquivalence(t, seedPlainStateLarge)
}
