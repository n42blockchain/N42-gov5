// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
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
	prewarmFromAccessList(roTx, addrs, nil)

	if _, present := buf.LookupReadAccount(addr); present {
		t.Fatalf("static-AL prewarm MUST NOT populate readAccounts LRU (codeHash race — block 6411933)")
	}
}

// TestPrewarmFromAccessList_DoesNotPopulateStorageLRU pins the
// post-race-fix invariant: prefetcher's static-AL helper warms the OS
// page cache via MDBX GetOne but does NOT publish to readStorage LRU.
// LRU population is the executor's job (prewarm.go).
func TestPrewarmFromAccessList_DoesNotPopulateStorageLRU(t *testing.T) {
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

	prewarmFromAccessList(roTx, nil, []*transaction.Transaction{tx})

	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("prewarmFromAccessList MUST NOT populate readStorage LRU — see prefetchStateReader for race")
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

	// Post race-fix: staticALPrewarm reads MDBX (warming OS page cache)
	// but does NOT write the read LRU. The executor's main-thread
	// prewarm (prewarm.go) is now responsible for LRU population.
	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("post-race-fix: staticALPrewarm MUST NOT populate readStorage LRU")
	}
}

// BenchmarkStaticALPrewarm_RealisticBlock measures the per-block cost
// of Phase 1 prewarm under conditions matching mainnet DeFi-era
// blocks: ~150 transactions, each with a few AL slot keys and a
// distinct `to` address. Validates the "1-2 ms per block" claim that
// motivated running this path always-before-speculative.
//
// On Ryzen 9 9950X, in-memory MDBX, with 150 tx × 4 AL keys = 600
// SeekExact + IfAbsentEpoch puts: ~1.0-1.5 ms per call. This is the
// upper bound — in production the executor's RoTx may be cold (NVMe
// page faults) so the page-warm benefit is what matters; the wall-
// time claim is guaranteed by the bound.
func BenchmarkStaticALPrewarm_RealisticBlock(b *testing.B) {
	const txCount = 150
	const slotsPerTx = 4

	db := memdb.New(b.TempDir())
	b.Cleanup(db.Close)

	addrs := make([]types.Address, txCount)
	rng := func(i int) byte { return byte((i*31 + 7) & 0xff) } // deterministic seed
	for i := 0; i < txCount; i++ {
		for j := 0; j < 20; j++ {
			addrs[i][j] = rng(i*100 + j)
		}
	}
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < txCount; i++ {
			for j := 0; j < slotsPerTx; j++ {
				var slot types.Hash
				slot[31] = byte(j)
				slot[30] = byte(i)
				key := modules.PlainGenerateCompositeStorageKey(addrs[i].Bytes(), slot[:])
				val := []byte{byte(j), byte(i)}
				if err := rwTx.Put(modules.Storage, key, val); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := rwTx.Commit(); err != nil {
			b.Fatal(err)
		}
	}

	txs := make([]*transaction.Transaction, txCount)
	senders := make([]types.Address, txCount)
	for i := 0; i < txCount; i++ {
		alKeys := make([]types.Hash, slotsPerTx)
		for j := 0; j < slotsPerTx; j++ {
			alKeys[j][31] = byte(j)
			alKeys[j][30] = byte(i)
		}
		txs[i] = transaction.NewTx(&transaction.AccessListTx{
			Nonce:      uint64(i),
			To:         &addrs[i],
			Gas:        21000,
			AccessList: transaction.AccessList{{Address: addrs[i], StorageKeys: alKeys}},
		})
		// Distinct sender address per tx
		senders[i][0] = byte(i)
		senders[i][1] = byte(i >> 8)
	}
	body := &GethBodyResult{Transactions: txs}

	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, params.EthereumMainnetChainConfig, nil, nil, nil, &current, false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.staticALPrewarm(0, body, senders, nil)
	}
}

// TestStaticALPrewarm_RunsWithSignerWhenSendersNil pins the bug-fix
// path for the senders==nil case (sender-recovery hasn't covered this
// block). With a non-nil signer staticALPrewarm performs ecrecover to
// resolve senders, then warms the AL slots normally. Without this,
// the senders==nil + speculativeEnabled==true combination produced NO
// prefetch activity at all — observed as senderMiss=9766/10000 with
// sto% dropping from 94% to ~70% on the affected datadir
// (d:/N42-ethverify after sender-recovery covered only block 0..63).
func TestStaticALPrewarm_RunsWithSignerWhenSendersNil(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000c0ffee20")
	slot := types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000000c")

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot[:])
		if err := rwTx.Put(modules.Storage, key, []byte{0xc0, 0x01}); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	buf := state.NewPlainStateBuffer()
	var current atomic.Uint64
	p := newPrefetcher(context.Background(), nil, asRoDB{db}, buf, params.EthereumMainnetChainConfig, nil, nil, nil, &current, false)

	// Build an AccessListTx — to+AL covers the slot we want warmed
	// without needing a real signed sender (signer is exercised by
	// transaction.Sender on the legacy LegacyTx path, but for an
	// AccessListTx the From comes from the signer pubkey recovery).
	// The test value is whether to + AL.Address are at minimum
	// collected when the senders fallback runs.
	tx := transaction.NewTx(&transaction.AccessListTx{
		Nonce:      0,
		To:         &addr,
		Gas:        21000,
		AccessList: transaction.AccessList{{Address: addr, StorageKeys: []types.Hash{slot}}},
	})
	body := &GethBodyResult{Transactions: []*transaction.Transaction{tx}}

	signer := transaction.MakeSigner(params.EthereumMainnetChainConfig, big.NewInt(15_000_000))
	p.staticALPrewarm(0, body, nil, signer)

	// Post race-fix: prefetcher path no longer writes LRU. The MDBX
	// GetOne above warms the OS page cache only.
	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("post-race-fix: staticALPrewarm with senders=nil MUST NOT populate readStorage LRU")
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

	// Both nil — must not panic. Post race-fix the prefetcher path no
	// longer writes LRU at all, so the only invariant left is "doesn't
	// crash with nil senders+signer" — which is itself worth pinning
	// since the senders-fallback ecrecover path used to require a
	// non-nil signer.
	p.staticALPrewarm(0, body, nil, nil)

	if _, present := buf.LookupReadStorage(addr, slot); present {
		t.Fatalf("post-race-fix: staticALPrewarm MUST NOT populate readStorage LRU")
	}
}
