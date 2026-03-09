// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapshot

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/layered"
)

func testAddr(i byte) types.Address {
	var addr types.Address
	addr[19] = i
	return addr
}

func testHash(i byte) types.Hash {
	var h types.Hash
	h[31] = i
	return h
}

func testAccount(nonce uint64) *account.StateAccount {
	return &account.StateAccount{Nonce: nonce}
}

func newTestTree() (*Tree, types.Hash) {
	cache := layered.NewShardedCache(4, 1024)
	genesisRoot := testHash(0)
	tree := NewTree(cache, 0, genesisRoot, 128)
	return tree, genesisRoot
}

// TestDiffLayer_Account tests basic account lookup in a single diff layer.
func TestDiffLayer_Account(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr1 := testAddr(1)
	addr2 := testAddr(2)
	addr3 := testAddr(3)

	accounts := map[types.Address]*account.StateAccount{
		addr1: testAccount(10),
	}
	dels := map[types.Address]struct{}{
		addr2: {},
	}

	diff := NewDiffLayer(disk, 1, testHash(1), accounts, dels, nil)

	// addr1: modified
	acc, found := diff.Account(addr1)
	if !found || acc == nil || acc.Nonce != 10 {
		t.Fatalf("expected account with nonce 10, got found=%v acc=%v", found, acc)
	}

	// addr2: deleted
	acc, found = diff.Account(addr2)
	if !found || acc != nil {
		t.Fatalf("expected deleted account (nil, true), got found=%v acc=%v", found, acc)
	}

	// addr3: not in layer, should fall through to disk (nil, false)
	acc, found = diff.Account(addr3)
	if found || acc != nil {
		t.Fatalf("expected miss (nil, false), got found=%v acc=%v", found, acc)
	}
}

// TestDiffLayer_Storage tests storage lookup in a diff layer.
func TestDiffLayer_Storage(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)
	key1 := testHash(10)
	key2 := testHash(20)

	storage := map[types.Address]map[types.Hash][]byte{
		addr: {
			key1: {0x42},
		},
	}

	diff := NewDiffLayer(disk, 1, testHash(1), nil, nil, storage)

	// key1: present
	val, found := diff.Storage(addr, key1)
	if !found || len(val) != 1 || val[0] != 0x42 {
		t.Fatalf("expected 0x42, got found=%v val=%x", found, val)
	}

	// key2: not in layer
	val, found = diff.Storage(addr, key2)
	if found {
		t.Fatalf("expected miss, got found=true val=%x", val)
	}
}

// TestDiffLayer_Chain tests lookup through multiple stacked diff layers.
func TestDiffLayer_Chain(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)

	// Block 1: account created with nonce 1
	diff1 := NewDiffLayer(disk, 1, testHash(1),
		map[types.Address]*account.StateAccount{addr: testAccount(1)},
		nil, nil)

	// Block 2: nonce updated to 2
	diff2 := NewDiffLayer(diff1, 2, testHash(2),
		map[types.Address]*account.StateAccount{addr: testAccount(2)},
		nil, nil)

	// Lookup from newest layer should return nonce 2
	acc, found := diff2.Account(addr)
	if !found || acc.Nonce != 2 {
		t.Fatalf("expected nonce 2, got found=%v nonce=%d", found, acc.Nonce)
	}

	// Lookup from older layer should return nonce 1
	acc, found = diff1.Account(addr)
	if !found || acc.Nonce != 1 {
		t.Fatalf("expected nonce 1, got found=%v nonce=%d", found, acc.Nonce)
	}
}

// TestDiffLayer_DeletePropagation tests that deletion stops parent chain walk.
func TestDiffLayer_DeletePropagation(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)

	// Block 1: account exists
	diff1 := NewDiffLayer(disk, 1, testHash(1),
		map[types.Address]*account.StateAccount{addr: testAccount(5)},
		nil, nil)

	// Block 2: account deleted
	diff2 := NewDiffLayer(diff1, 2, testHash(2),
		nil, map[types.Address]struct{}{addr: {}}, nil)

	// Block 3: no change to this address
	diff3 := NewDiffLayer(diff2, 3, testHash(3), nil, nil, nil)

	// From diff3, account should appear as deleted (nil, true)
	acc, found := diff3.Account(addr)
	if !found || acc != nil {
		t.Fatalf("expected deleted account (nil, true), got found=%v acc=%v", found, acc)
	}
}

// TestDiffLayer_StorageDeleteOnAccountDelete tests that storage returns deleted
// when account was deleted in a layer.
func TestDiffLayer_StorageDeleteOnAccountDelete(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)
	key := testHash(10)

	// Block 1: storage written
	diff1 := NewDiffLayer(disk, 1, testHash(1), nil, nil,
		map[types.Address]map[types.Hash][]byte{addr: {key: {0x01}}})

	// Block 2: account deleted
	diff2 := NewDiffLayer(diff1, 2, testHash(2),
		nil, map[types.Address]struct{}{addr: {}}, nil)

	// Storage should show as deleted since account was deleted
	val, found := diff2.Storage(addr, key)
	if !found {
		t.Fatal("expected found=true (deleted)")
	}
	if val != nil {
		t.Fatalf("expected nil value for deleted storage, got %x", val)
	}
}

// TestDiffLayer_Stale tests that stale layers return (nil, false).
func TestDiffLayer_Stale(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)
	diff := NewDiffLayer(disk, 1, testHash(1),
		map[types.Address]*account.StateAccount{addr: testAccount(1)},
		nil, nil)

	// Before marking stale
	acc, found := diff.Account(addr)
	if !found || acc.Nonce != 1 {
		t.Fatal("expected to find account before marking stale")
	}

	diff.MarkStale()

	// After marking stale
	acc, found = diff.Account(addr)
	if found {
		t.Fatal("expected miss on stale layer")
	}
}

// TestTree_UpdateAndSnapshot tests adding layers and looking them up.
func TestTree_UpdateAndSnapshot(t *testing.T) {
	tree, genesis := newTestTree()

	addr := testAddr(1)
	root1 := testHash(1)

	err := tree.Update(1, root1, genesis,
		map[types.Address]*account.StateAccount{addr: testAccount(10)},
		nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	layer := tree.Snapshot(root1)
	if layer == nil {
		t.Fatal("expected to find layer for root1")
	}

	acc, found := layer.Account(addr)
	if !found || acc.Nonce != 10 {
		t.Fatalf("expected nonce 10, got found=%v acc=%v", found, acc)
	}
}

// TestTree_DuplicateInsert tests that duplicate root insertion is rejected.
func TestTree_DuplicateInsert(t *testing.T) {
	tree, genesis := newTestTree()
	root1 := testHash(1)

	if err := tree.Update(1, root1, genesis, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := tree.Update(1, root1, genesis, nil, nil, nil); err != ErrLayerExists {
		t.Fatalf("expected ErrLayerExists, got %v", err)
	}
}

// TestTree_MissingParent tests that inserting with unknown parent fails.
func TestTree_MissingParent(t *testing.T) {
	tree, _ := newTestTree()
	bogusParent := testHash(99)
	root1 := testHash(1)

	if err := tree.Update(1, root1, bogusParent, nil, nil, nil); err != ErrParentNotFound {
		t.Fatalf("expected ErrParentNotFound, got %v", err)
	}
}

// TestTree_Cap tests flattening diff layers into the disk cache.
func TestTree_Cap(t *testing.T) {
	tree, genesis := newTestTree()
	addr := testAddr(1)

	parent := genesis
	for i := uint64(1); i <= 5; i++ {
		root := testHash(byte(i))
		err := tree.Update(i, root, parent,
			map[types.Address]*account.StateAccount{addr: testAccount(i * 10)},
			nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		parent = root
	}

	diffCount, _ := tree.Size()
	if diffCount != 5 {
		t.Fatalf("expected 5 diff layers, got %d", diffCount)
	}

	if err := tree.Cap(2); err != nil {
		t.Fatal(err)
	}

	diffCount, _ = tree.Size()
	if diffCount > 2 {
		t.Fatalf("expected at most 2 diff layers after cap, got %d", diffCount)
	}

	// Latest layer should still be accessible
	layer := tree.Snapshot(testHash(5))
	if layer == nil {
		t.Fatal("expected latest layer to survive cap")
	}
	acc, found := layer.Account(addr)
	if !found || acc.Nonce != 50 {
		t.Fatalf("expected nonce 50, got found=%v acc=%v", found, acc)
	}
}

// TestTree_Discard tests reorg handling.
func TestTree_Discard(t *testing.T) {
	tree, genesis := newTestTree()
	root1 := testHash(1)
	root2 := testHash(2)
	root3 := testHash(3)

	tree.Update(1, root1, genesis, nil, nil, nil)
	tree.Update(2, root2, root1, nil, nil, nil)
	tree.Update(3, root3, root2, nil, nil, nil)

	tree.Discard(root2)

	if tree.Snapshot(root2) != nil {
		t.Fatal("root2 should be discarded")
	}
	if tree.Snapshot(root3) != nil {
		t.Fatal("root3 should be discarded as child of root2")
	}
	if tree.Snapshot(root1) == nil {
		t.Fatal("root1 should survive")
	}
}

// TestTree_AutoFlatten tests auto-flattening when exceeding maxDiffLayers.
func TestTree_AutoFlatten(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	genesisRoot := testHash(0)
	tree := NewTree(cache, 0, genesisRoot, 5)

	parent := genesisRoot
	for i := uint64(1); i <= 10; i++ {
		root := testHash(byte(i))
		tree.Update(i, root, parent, nil, nil, nil)
		parent = root
	}

	diffCount, _ := tree.Size()
	if diffCount > 5 {
		t.Fatalf("expected at most 5 diff layers, got %d", diffCount)
	}
}

// TestSnapshotStateReader_Fallback tests that the reader falls back to inner.
func TestSnapshotStateReader_Fallback(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr1 := testAddr(1)
	addr2 := testAddr(2)

	diff := NewDiffLayer(disk, 1, testHash(1),
		map[types.Address]*account.StateAccount{addr1: testAccount(42)},
		nil, nil)

	inner := &mockStateReader{
		accounts: map[types.Address]*account.StateAccount{
			addr2: testAccount(99),
		},
	}

	reader := NewSnapshotStateReader(diff, inner)

	// addr1: found in diff layer
	acc, err := reader.ReadAccountData(addr1)
	if err != nil || acc == nil || acc.Nonce != 42 {
		t.Fatalf("expected nonce 42 from diff, got err=%v acc=%v", err, acc)
	}

	// addr2: falls back to inner
	acc, err = reader.ReadAccountData(addr2)
	if err != nil || acc == nil || acc.Nonce != 99 {
		t.Fatalf("expected nonce 99 from inner, got err=%v acc=%v", err, acc)
	}
}

// TestSnapshotStateReader_DeletedAccount tests deleted accounts return nil.
func TestSnapshotStateReader_DeletedAccount(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	addr := testAddr(1)
	diff := NewDiffLayer(disk, 1, testHash(1),
		nil, map[types.Address]struct{}{addr: {}}, nil)

	inner := &mockStateReader{
		accounts: map[types.Address]*account.StateAccount{
			addr: testAccount(99),
		},
	}

	reader := NewSnapshotStateReader(diff, inner)

	acc, err := reader.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if acc != nil {
		t.Fatal("expected nil for deleted account")
	}
}

// TestDiffLayer_ConcurrentReads tests concurrent read safety.
func TestDiffLayer_ConcurrentReads(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	accounts := make(map[types.Address]*account.StateAccount)
	for i := byte(0); i < 50; i++ {
		accounts[testAddr(i)] = testAccount(uint64(i))
	}

	diff := NewDiffLayer(disk, 1, testHash(1), accounts, nil, nil)

	var wg sync.WaitGroup
	for i := byte(0); i < 50; i++ {
		wg.Add(1)
		go func(idx byte) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				acc, found := diff.Account(testAddr(idx))
				if !found || acc == nil || acc.Nonce != uint64(idx) {
					t.Errorf("concurrent read mismatch for addr %d", idx)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestDiffLayer_Memory tests memory estimation.
func TestDiffLayer_Memory(t *testing.T) {
	cache := layered.NewShardedCache(4, 1024)
	disk := NewDiskLayer(cache, 0, testHash(0))

	// Empty diff layer
	diff := NewDiffLayer(disk, 1, testHash(1), nil, nil, nil)
	if diff.Memory() != 0 {
		t.Fatalf("expected 0 memory for empty diff, got %d", diff.Memory())
	}

	// Non-empty diff layer
	accounts := map[types.Address]*account.StateAccount{
		testAddr(1): testAccount(1),
		testAddr(2): testAccount(2),
	}
	storage := map[types.Address]map[types.Hash][]byte{
		testAddr(1): {testHash(10): {0x01, 0x02}},
	}
	diff2 := NewDiffLayer(disk, 2, testHash(2), accounts, nil, storage)
	if diff2.Memory() == 0 {
		t.Fatal("expected non-zero memory for non-empty diff")
	}
}

// mockStateReader is a simple in-memory StateReader for testing.
type mockStateReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
}

func (m *mockStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	return m.accounts[address], nil
}

func (m *mockStateReader) ReadAccountStorage(address types.Address, _ uint16, key *types.Hash) ([]byte, error) {
	if slots, ok := m.storage[address]; ok {
		return slots[*key], nil
	}
	return nil, nil
}

func (m *mockStateReader) ReadAccountCode(_ types.Address, _ uint16, _ types.Hash) ([]byte, error) {
	return nil, nil
}

func (m *mockStateReader) ReadAccountCodeSize(_ types.Address, _ uint16, _ types.Hash) (int, error) {
	return 0, nil
}

func (m *mockStateReader) ReadAccountIncarnation(_ types.Address) (uint16, error) {
	return 0, nil
}
