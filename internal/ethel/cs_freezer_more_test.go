// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// cs_freezer_more_test.go covers the CSFreezerSink/Writer paths not
// exercised by cs_freezer_test.go: Rewind, BlockChangesProvider, and
// CSFreezerWriter.UpdateAccountCode.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestCSFreezerSink_Rewind covers the re-align-after-reorg path: queued
// pending blobs are dropped and both tables are truncated back to
// newHead+1.
func TestCSFreezerSink_Rewind(t *testing.T) {
	fz := mkFreezer(t)
	const head = uint64(100)

	sink, err := NewCSFreezerSink(fz, head)
	require.NoError(t, err)
	blob := func(b byte) []byte { return []byte{0, 0, b} }
	for blk := head + 1; blk <= head+5; blk++ {
		require.NoError(t, sink.Add(blk, blob(byte(blk)), blob(byte(blk))))
	}
	require.NoError(t, sink.Flush())

	acct := fz.Table(freezer.TableAccountChanges)
	require.Equal(t, head+6, acct.Items())

	// Rewind to head+2: next append must be head+3, and the already
	// flushed tail (head+3..head+5) must be truncated away.
	require.NoError(t, sink.Rewind(head+2))
	require.Equal(t, head+3, acct.Items(), "rewind must truncate the flushed tail")

	// Re-execute head+3 with new content and flush; must land cleanly.
	require.NoError(t, sink.Add(head+3, blob(0xEE), blob(0xEE)))
	require.NoError(t, sink.Flush())
	v, err := acct.Retrieve(head + 3)
	require.NoError(t, err)
	require.Equal(t, byte(0xEE), v[2])

	// Rewind below the table's current start: re-origin path.
	require.NoError(t, sink.Rewind(head - 10))
	require.Equal(t, head-9, acct.StartItem())
	require.Equal(t, acct.StartItem(), acct.Items())
}

// TestCSFreezerSink_BlockChangesProvider round-trips one block's account
// and storage changes through WriteChangeSets and reads them back via
// BlockChangesProvider, including the "created in n" (nil OldValue)
// unwind-delete marker and the out-of-coverage false case.
func TestCSFreezerSink_BlockChangesProvider(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	fz := mkFreezer(t)
	const blockNum = uint64(50)
	sink, err := NewCSFreezerSink(fz, blockNum-1)
	require.NoError(t, err)

	addrExisting := types.HexToAddress("0x1111111111111111111111111111111111111")
	addrNew := types.HexToAddress("0x2222222222222222222222222222222222222")
	slot := types.HexToHash("0x07")

	oldAcc := &account.StateAccount{Initialised: true, Nonce: 1}
	oldAcc.Balance.SetUint64(10)
	oldAcc.CodeHash = types.BytesToHash(crypto.Keccak256(nil))
	postExisting := &account.StateAccount{Initialised: true, Nonce: 2}
	postExisting.Balance.SetUint64(5)
	postExisting.CodeHash = oldAcc.CodeHash

	postNew := &account.StateAccount{Initialised: true, Nonce: 0}
	postNew.Balance.SetUint64(1)
	postNew.CodeHash = oldAcc.CodeHash

	require.NoError(t, tx.Put(modules.HashedAccounts, crypto.Keccak256(addrExisting[:]), postExisting.MarshalV2()))
	require.NoError(t, tx.Put(modules.HashedAccounts, crypto.Keccak256(addrNew[:]), postNew.MarshalV2()))
	var comp [64]byte
	copy(comp[:32], crypto.Keccak256(addrExisting[:]))
	copy(comp[32:], crypto.Keccak256(slot[:]))
	require.NoError(t, tx.Put(modules.HashedStorage, comp[:], []byte{0x11}))

	w := NewCSFreezerWriter(tx, blockNum, sink)
	require.NoError(t, w.UpdateAccountData(addrExisting, oldAcc, postExisting))
	emptyOrig := account.NewAccount()
	require.NoError(t, w.UpdateAccountData(addrNew, &emptyOrig, postNew)) // created in this block
	var oldV, newV uint256.Int
	oldV.SetUint64(3)
	newV.SetUint64(0x11)
	require.NoError(t, w.WriteAccountStorage(addrExisting, slot, oldV, newV))
	require.NoError(t, w.WriteChangeSets())
	require.NoError(t, sink.Flush())

	provider := sink.BlockChangesProvider()

	accounts, storage, found, err := provider(blockNum)
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, accounts, addrExisting)
	require.NotNil(t, accounts[addrExisting])
	require.Equal(t, uint64(1), accounts[addrExisting].Nonce)
	require.Contains(t, accounts, addrNew)
	require.Nil(t, accounts[addrNew], "account created in n must unwind-delete (nil)")

	require.Contains(t, storage, addrExisting)
	require.Contains(t, storage[addrExisting], slot)
	require.Equal(t, uint64(3), storage[addrExisting][slot].Uint64())

	// Out of coverage: a block number outside [StartItem, Items) returns
	// found=false, no error.
	_, _, found2, err := provider(blockNum + 1000)
	require.NoError(t, err)
	require.False(t, found2)
}

// TestCSFreezerWriter_UpdateAccountCode covers the code-persistence path
// (writes non-empty code to modules.Code) and its no-op-on-empty guard.
func TestCSFreezerWriter_UpdateAccountCode(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	fz := mkFreezer(t)
	sink, err := NewCSFreezerSink(fz, 0)
	require.NoError(t, err)
	w := NewCSFreezerWriter(tx, 1, sink)

	addr := types.HexToAddress("0x3333333333333333333333333333333333333")
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	codeHash := types.BytesToHash(crypto.Keccak256(code))
	require.NoError(t, w.UpdateAccountCode(addr, codeHash, code))
	got, err := tx.GetOne(modules.Code, codeHash[:])
	require.NoError(t, err)
	require.Equal(t, code, got)

	// Empty code is a no-op — must not write an empty-keyed row.
	require.NoError(t, w.UpdateAccountCode(addr, types.Hash{}, nil))
	var zeroHash types.Hash
	got2, err := tx.GetOne(modules.Code, zeroHash[:])
	require.NoError(t, err)
	require.Empty(t, got2)
}
