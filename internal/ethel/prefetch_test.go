// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestPrewarmFromAccessList_NeverPopulatesAccountLRU pins the invariant
// that the static-AL prefetch helper does NOT publish accounts into the
// readAccounts LRU. Block 6411933 hit a stale-codeHash race when this
// path called CacheAccountIfAbsentEpoch — see prewarmFromAccessList doc.
// Re-introducing such a write would silently pass type-checks; this test
// is the structural guard.
func TestPrewarmFromAccessList_NeverPopulatesAccountLRU(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000beef")
	acct := &account.StateAccount{Initialised: true, Nonce: 7}
	acct.Balance.SetUint64(42)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Put(modules.Account, addr.Bytes(), acct.MarshalV2()); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()

	addrs := map[types.Address]struct{}{addr: {}}
	prewarmFromAccessList(buf, roTx, addrs, nil, 0)

	if _, present := buf.LookupReadAccount(addr); present {
		t.Fatalf("static-AL prewarm MUST NOT populate readAccounts LRU (codeHash race — block 6411933)")
	}
}

// TestPrewarmFromAccessList_StorageIsPublished confirms the asymmetric
// policy: storage slots referenced via tx AccessList ARE published to
// readStorage LRU. Storage prewarm is safe (storageWipes interception in
// bufReader handles stale slots) and is the path that actually drives
// sto% hit rate.
func TestPrewarmFromAccessList_StorageIsPublished(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee02")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")
	val := []byte{0x99}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, val); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()

	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})

	prewarmFromAccessList(buf, roTx, nil, []*transaction.Transaction{tx}, 0)

	v, present := buf.LookupReadStorage(addr, slot)
	if !present {
		t.Fatalf("static-AL prewarm MUST populate readStorage LRU for AL slots")
	}
	if len(v) != 1 || v[0] != 0x99 {
		t.Fatalf("LRU populated with wrong value: got %x want 99", v)
	}
}

// TestStaticALPrewarm_RunsWithPrecomputedSenders pins the property
// that the prefetcher's Phase-1 staticALPrewarm runs the AL slot
// warming when senders are pre-computed (the common case on mainnet
// where sender-recovery has already populated the senders SegmentStore
// or freezer table). Without senders, ecrecover would dominate the
// per-block budget; with them, this path is ~1-2 ms and primes the
// LRU before Phase 2's speculative EVM starts so the speculative
// reads on AL slots become LRU hits instead of MDBX cursor descents.
func TestStaticALPrewarm_RunsWithPrecomputedSenders(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee10")
	slot := types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000000a")
	val := []byte{0xab, 0xcd}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, val); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, nil, nil, nil, nil, &current, false)

	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})
	body := &GethBodyResult{Transactions: []*transaction.Transaction{tx}}
	senders := []types.Address{addr}

	p.staticALPrewarm(0, body, senders, nil)

	v, present := buf.LookupReadStorage(addr, slot)
	if !present {
		t.Fatalf("staticALPrewarm with senders!=nil MUST populate readStorage LRU")
	}
	if len(v) != 2 || v[0] != 0xab || v[1] != 0xcd {
		t.Fatalf("LRU populated with wrong value: got %x want abcd", v)
	}
}

// TestStaticALPrewarm_NoOpWithoutSendersOrSigner pins the safety
// property: when senders are unavailable AND no fallback signer is
// supplied, staticALPrewarm collects only `to` and AL addresses (no
// senders). It must not panic on the nil signer path.
func TestStaticALPrewarm_NoOpWithoutSendersOrSigner(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee11")
	slot := types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000000b")

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, []byte{0xee}); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, nil, nil, nil, nil, &current, false)

	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})
	body := &GethBodyResult{Transactions: []*transaction.Transaction{tx}}

	// Both nil — must not panic, must still warm AL slot via the addrs
	// collected from `to` + AL Address fields.
	p.staticALPrewarm(0, body, nil, nil)

	v, present := buf.LookupReadStorage(addr, slot)
	if !present || len(v) != 1 || v[0] != 0xee {
		t.Fatalf("staticALPrewarm with nil senders+signer MUST still warm AL slots: present=%v v=%x", present, v)
	}
}
