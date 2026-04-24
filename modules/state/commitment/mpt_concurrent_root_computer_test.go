// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// mpTestCase is one scenario: N accounts + M storage slots across K addrs,
// deterministic keys/values.
type mpTestCase struct {
	name     string
	nAcct    int
	perAddr  int // storage slots per address (for first storAddrs addresses)
	storeFor int // how many addrs get storage
}

var mpCases = []mpTestCase{
	{"tiny", 3, 0, 0},
	{"accts-only", 50, 0, 0},
	{"small-mixed", 20, 5, 10},
	{"medium-mixed", 256, 8, 64},
	// 512 accounts × 16 storage slots = enough to populate every top nibble
	// heavily so concurrent partition has meaningful per-worker load
	{"wide-mixed", 512, 16, 256},
}

// buildTestData produces deterministic accounts + storage maps for a case.
func buildTestData(c mpTestCase) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	[]types.Address,
) {
	accts := make(map[types.Address]*account.StateAccount, c.nAcct)
	stor := make(map[types.Address]map[types.Hash]*uint256.Int, c.storeFor)
	addrs := make([]types.Address, 0, c.nAcct)
	for i := 0; i < c.nAcct; i++ {
		var a types.Address
		// pseudo-uniform distribution over first nibble: i%16 for index 0
		a[0] = byte(i % 16)
		a[1] = byte((i / 16) % 256)
		a[2] = byte(i / (16 * 256))
		a[19] = byte(i)
		addrs = append(addrs, a)
		acct := &account.StateAccount{
			Initialised: true,
			Nonce:       uint64(i) + 1,
		}
		acct.Balance.SetUint64(uint64(i)*1000 + 1)
		accts[a] = acct
	}
	for i := 0; i < c.storeFor && i < len(addrs); i++ {
		addr := addrs[i]
		slots := make(map[types.Hash]*uint256.Int, c.perAddr)
		for j := 0; j < c.perAddr; j++ {
			var s types.Hash
			s[0] = byte(j % 16)
			s[31] = byte(j)
			slots[s] = uint256.NewInt(uint64(i*1000 + j + 42))
		}
		stor[addr] = slots
	}
	return accts, stor, addrs
}

// seedPlainState writes the test accounts + storage into the Account and
// Storage tables so PlainStateMPTReader can find them.
func seedPlainState(t *testing.T, db kv.RwDB, accts map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for a, acct := range accts {
			if acct == nil {
				continue
			}
			if err := tx.Put(modules.Account, a.Bytes(), acct.MarshalV2()); err != nil {
				return err
			}
		}
		for a, slots := range stor {
			for s, v := range slots {
				if v == nil || v.IsZero() {
					continue
				}
				key := modules.PlainGenerateCompositeStorageKey(a[:], s[:])
				b := v.Bytes32()
				start := 0
				for start < 31 && b[start] == 0 {
					start++
				}
				if err := tx.Put(modules.Storage, key, b[start:]); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestConcurrentMPTMatchesSequential asserts the concurrent computer
// produces the same root as the sequential computer for each test case.
func TestConcurrentMPTMatchesSequential(t *testing.T) {
	for _, c := range mpCases {
		t.Run(c.name, func(t *testing.T) {
			accts, stor, _ := buildTestData(c)

			// --- Sequential ---
			seqDB := memdb.NewTestDB(t)
			seedPlainState(t, seqDB, accts, stor)
			seqRoot, err := runSequentialMPT(seqDB, accts, stor)
			if err != nil {
				t.Fatalf("sequential: %v", err)
			}

			// --- Concurrent ---
			conDB := memdb.NewTestDB(t)
			seedPlainState(t, conDB, accts, stor)
			conRoot, err := runConcurrentMPT(t, conDB, accts, stor)
			if err != nil {
				t.Fatalf("concurrent: %v", err)
			}

			if seqRoot != conRoot {
				t.Fatalf("root mismatch\n  sequential = %x\n  concurrent = %x",
					seqRoot[:], conRoot[:])
			}
		})
	}
}

func runSequentialMPT(
	db kv.RwDB,
	accts map[types.Address]*account.StateAccount,
	stor map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return types.Hash{}, err
	}
	defer tx.Rollback()

	m := NewMPTRootComputer()
	reader := NewPlainStateMPTReader(tx)
	m.SetStateReader(reader)
	return m.ComputeRoot(accts, stor)
}

func runConcurrentMPT(
	t *testing.T,
	db kv.RwDB,
	accts map[types.Address]*account.StateAccount,
	stor map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	t.Helper()
	// Template tx only used for the sequential fallback path inside the
	// root trie. Workers open their own via the factory.
	templateTx, err := db.BeginRo(context.Background())
	if err != nil {
		return types.Hash{}, err
	}
	defer templateTx.Rollback()

	m := NewConcurrentMPTRootComputer(db, t.TempDir(), log2.New())
	reader := NewPlainStateMPTReader(templateTx)
	m.SetStateReader(reader)
	return m.ComputeRoot(accts, stor)
}

// TestConcurrentMPTIdempotent verifies calling ComputeRoot twice with
// the same input yields the same root (no state pollution between runs).
func TestConcurrentMPTIdempotent(t *testing.T) {
	c := mpCases[2] // small-mixed
	accts, stor, _ := buildTestData(c)

	db := memdb.NewTestDB(t)
	seedPlainState(t, db, accts, stor)

	templateTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer templateTx.Rollback()

	m := NewConcurrentMPTRootComputer(db, t.TempDir(), log2.New())
	m.SetStateReader(NewPlainStateMPTReader(templateTx))

	r1, err := m.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	r2, err := m.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("root not idempotent: r1=%x r2=%x", r1[:], r2[:])
	}
}

// TestConcurrentMPTEmpty verifies empty input returns a stable root
// (not a crash or nil).
func TestConcurrentMPTEmpty(t *testing.T) {
	db := memdb.NewTestDB(t)
	templateTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer templateTx.Rollback()

	m := NewConcurrentMPTRootComputer(db, t.TempDir(), log2.New())
	m.SetStateReader(NewPlainStateMPTReader(templateTx))

	r, err := m.ComputeRoot(
		map[types.Address]*account.StateAccount{},
		map[types.Address]map[types.Hash]*uint256.Int{},
	)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	// Root should be the empty-trie root; we just assert non-crash and
	// consistency with the sequential empty root.
	seqM := NewMPTRootComputer()
	seqM.SetStateReader(NewPlainStateMPTReader(templateTx))
	seqR, err := seqM.ComputeRoot(nil, nil)
	if err != nil {
		t.Fatalf("seq empty: %v", err)
	}
	if r != seqR {
		t.Fatalf("empty roots differ: concurrent=%x sequential=%x", r[:], seqR[:])
	}
}
