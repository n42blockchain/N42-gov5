package internal

import (
	"context"
	"errors"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

func putPrefetchAccount(t *testing.T, tx kv.RwTx, addr types.Address, balance uint64) {
	t.Helper()
	a := account.NewAccount()
	a.Nonce = balance + 1
	a.Balance.SetUint64(balance)
	encoded := make([]byte, a.EncodingLengthForStorage())
	a.EncodeForStorage(encoded)
	if err := tx.Put(modules.Account, addr[:], encoded); err != nil {
		t.Fatal(err)
	}
}
func prefetchTestDB(t *testing.T) (kv.RwDB, []types.Address) {
	t.Helper()
	db := memdb.NewTestDB(t)
	addrs := make([]types.Address, 1001)
	for i := range addrs {
		addrs[i][18] = byte(i >> 8)
		addrs[i][19] = byte(i)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i, addr := range addrs {
			if i%3 != 0 {
				putPrefetchAccount(t, tx, addr, uint64(i))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return db, addrs
}
func TestAccountPrefetchSnapshotAndOwnership(t *testing.T) {
	db, addrs := prefetchTestDB(t)
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	plain := state.NewPlainStateReader(base)
	for _, workers := range []int{1, 2, 4, 8, 16} {
		values, err := readSnapshotAccounts(context.Background(), db, base.ViewID(), append(addrs, addrs...), workers)
		if err != nil {
			t.Fatal(err)
		}
		if len(values) != len(addrs) {
			t.Fatalf("dedup: %d", len(values))
		}
		r := &blockAccountPrefetchReader{PlainStateReader: plain, accounts: values}
		for _, addr := range addrs {
			want, err := plain.ReadAccountData(addr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.ReadAccountData(addr)
			if err != nil {
				t.Fatal(err)
			}
			if (want == nil) != (got == nil) || (want != nil && *want != *got) {
				t.Fatalf("value differs: %s", addr)
			}
			if got != nil {
				got.Nonce++
				got.Balance.Clear()
			}
			again, err := r.ReadAccountData(addr)
			if err != nil {
				t.Fatal(err)
			}
			if (want == nil) != (again == nil) || (want != nil && *want != *again) {
				t.Fatal("returned value polluted snapshot")
			}
		}
		if r.hits != uint64(len(addrs)) || len(r.accounts) != 0 {
			t.Fatal("owned values were not consumed")
		}
	}
	// Even metadata-only commits change ViewID. Never admit those later snapshots.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { putPrefetchAccount(t, tx, addrs[1], 9999); return nil }); err != nil {
		t.Fatal(err)
	}
	values, err := readSnapshotAccounts(context.Background(), db, base.ViewID(), addrs, 4)
	if err == nil || values != nil {
		t.Fatal("newer snapshot was admitted")
	}
	original, err := plain.ReadAccountData(addrs[1])
	if err != nil || original.Balance.Uint64() != 1 {
		t.Fatalf("base snapshot changed: %v %v", original, err)
	}
}

type prefetchFaultDB struct {
	kv.RwDB
	beginErr  error
	readErr   error
	viewDelta uint64
	active    atomic.Int64
}
type prefetchFaultTx struct {
	kv.Tx
	owner *prefetchFaultDB
}

func (d *prefetchFaultDB) BeginRo(ctx context.Context) (kv.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	tx, err := d.RwDB.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	d.active.Add(1)
	return &prefetchFaultTx{tx, d}, nil
}
func (t *prefetchFaultTx) ViewID() uint64 { return t.Tx.ViewID() + t.owner.viewDelta }
func (t *prefetchFaultTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.Account && t.owner.readErr != nil {
		return nil, t.owner.readErr
	}
	return t.Tx.GetOne(table, key)
}
func (t *prefetchFaultTx) Rollback() { t.Tx.Rollback(); t.owner.active.Add(-1) }
func TestAccountPrefetchErrorsAndCancellation(t *testing.T) {
	db, addrs := prefetchTestDB(t)
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	injected := errors.New("injected prefetch I/O error")
	for _, f := range []*prefetchFaultDB{{RwDB: db, beginErr: injected}, {RwDB: db, readErr: injected}, {RwDB: db, viewDelta: 1}} {
		values, err := readSnapshotAccounts(context.Background(), f, base.ViewID(), addrs, 8)
		if err == nil || values != nil {
			t.Fatal("failed batch returned cached accounts")
		}
		if f.active.Load() != 0 {
			t.Fatal("worker read transaction leaked")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if values, err := readSnapshotAccounts(ctx, db, base.ViewID(), addrs, 4); !errors.Is(err, context.Canceled) || values != nil {
		t.Fatalf("cancellation: %v", err)
	}
	for _, workers := range []int{-1, 0, 17} {
		if _, err := readSnapshotAccounts(context.Background(), db, base.ViewID(), addrs, workers); err == nil {
			t.Fatal("invalid worker limit accepted")
		}
	}
	if _, err := readSnapshotAccounts(context.Background(), db, base.ViewID(), make([]types.Address, maxAccountPrefetchAddresses+1), 4); err == nil {
		t.Fatal("unbounded account batch accepted")
	}
}
func TestAccountPrefetchMalformedAccount(t *testing.T) {
	db, addrs := prefetchTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return tx.Put(modules.Account, addrs[1][:], []byte{0xff}) }); err != nil {
		t.Fatal(err)
	}
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	if values, err := readSnapshotAccounts(context.Background(), db, base.ViewID(), addrs, 4); err == nil || values != nil {
		t.Fatal("decode error was hidden")
	}
	// The execution reader still reports the original error at the actual read.
	plain := state.NewPlainStateReader(base)
	r := &blockAccountPrefetchReader{PlainStateReader: plain}
	if _, err := r.ReadAccountData(addrs[1]); err == nil {
		t.Fatal("execution swallowed decode error")
	}
}
func TestParseAccountPrefetchWorkers(t *testing.T) {
	for value, want := range map[string]int{"": 0, "bad": 0, "-1": 0, "0": 0, "1": 1, "8": 8, "16": 16, "99": 16} {
		if got := parseAccountPrefetchWorkers(value); got != want {
			t.Fatalf("%q: %d != %d", value, got, want)
		}
	}
}

func TestAccountPrefetchStorageEnumeration(t *testing.T) {
	db, addrs := prefetchTestDB(t)
	addr := addrs[1]
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := byte(1); i <= 3; i++ {
			var slot types.Hash
			slot[31] = i
			key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
			if err := tx.Put(modules.Storage, key, []byte{i}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	r := &blockAccountPrefetchReader{PlainStateReader: state.NewPlainStateReader(base)}
	count := 0
	if err := r.ForEachStorage(addr, func(slot types.Hash, value []byte) bool {
		count++
		if len(value) != 1 || value[0] != slot[31] {
			t.Fatal("wrong slot value")
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("enumerated %d slots, want 3", count)
	}
}

func TestAccountPrefetchPrepareFallsBack(t *testing.T) {
	db, addrs := prefetchTestDB(t)
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	plain := state.NewPlainStateReader(base)
	injected := errors.New("unavailable preload")
	addr := addrs[1]
	txs := make(transaction.Transactions, 2048)
	for i := range txs {
		txs[i] = transaction.NewTransaction(uint64(i), addr, &addr, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	}
	r := &blockAccountPrefetchReader{PlainStateReader: plain, ctx: context.Background(), db: &prefetchFaultDB{RwDB: db, beginErr: injected}, viewID: base.ViewID(), workers: 4, accounts: map[types.Address]*account.StateAccount{addr: nil}}
	r.prepare(txs)
	if r.accounts != nil {
		t.Fatal("failed preload retained stale entries")
	}
	got, err := r.ReadAccountData(addr)
	if err != nil || got == nil || got.Balance.Uint64() != 1 {
		t.Fatalf("fallback did not use base reader: %v %v", got, err)
	}
}
