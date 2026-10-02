// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// journal_verify_synth_test.go drives JournalVerifier.Run end-to-end over a
// tiny synthetic chain: a geth-format header freezer (via the helpers in
// executor_e2e_test.go) paired with an acctcs/storcs changeset freezer built
// the same way cs_freezer_test.go builds one (CSFreezerSink +
// CSFreezerWriter), with header roots computed independently so the happy
// path and the mismatch path are both exercised.

package ethel

import (
	"context"
	"testing"

	"github.com/golang/snappy"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// jvAcct builds a simple StateAccount for journal-verify synth tests.
func jvAcct(nonce, bal uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, Nonce: nonce}
	a.Balance = *uint256.NewInt(bal)
	return a
}

// jvEncodeBlock builds one block's acctcs/storcs blobs via the real
// CSFreezerWriter path (post-state resolved from HashedAccounts, same as
// production) and queues them on sink.
func jvEncodeBlock(t *testing.T, sink *CSFreezerSink, blockNum uint64, addr types.Address, old, newAcc *account.StateAccount) {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	if newAcc != nil {
		require.NoError(t, tx.Put(modules.HashedAccounts, crypto.Keccak256(addr[:]), newAcc.MarshalV2()))
	}
	w := NewCSFreezerWriter(tx, blockNum, sink)
	if old == nil {
		old = &account.StateAccount{}
	}
	if newAcc == nil {
		newAcc = &account.StateAccount{}
	}
	require.NoError(t, w.UpdateAccountData(addr, old, newAcc))
	require.NoError(t, w.WriteChangeSets())
	require.NoError(t, sink.Flush())
}

// jvBuildHeaderFreezer writes a geth-format header/body/receipt/hash/diff
// freezer in dir with the given per-block state roots.
func jvBuildHeaderFreezer(t *testing.T, dir string, roots []types.Hash) *freezer.Freezer {
	t.Helper()
	fz, err := freezer.New(dir, 0)
	require.NoError(t, err)

	var headers, bodies, receipts, hashes, diffs [][]byte
	var parent types.Hash
	for i, root := range roots {
		h := mkHeader(uint64(i), tsAnchor+uint64(i), parent, root, EthReceiptHash(nil))
		parent = h.Hash()
		headers = append(headers, encodeGethHeader(t, h))
		bodies = append(bodies, emptyGethBodyRLP(t))
		receipts = append(receipts, snappy.Encode(nil, mustRLP(t, []interface{}{})))
		hb := h.Hash()
		hashes = append(hashes, hb[:])
		diffs = append(diffs, []byte{0})
	}
	require.NoError(t, fz.Freeze(0, &freezer.FreezeData{
		Headers:    headers,
		Bodies:     bodies,
		Receipts:   receipts,
		Hashes:     hashes,
		Difficulty: diffs,
	}))
	return fz
}

// jvComputeRoot applies a seed function against a fresh memdb and returns
// the resulting state root via the independent VerifyStateRoot oracle.
func jvComputeRoot(t *testing.T, apply func(tx kv.RwTx)) types.Hash {
	t.Helper()
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	apply(tx)
	root, err := VerifyStateRoot(tx)
	require.NoError(t, err)
	return root
}

// TestJournalVerifier_RunAndRevert replays a 2-block synthetic chain
// (genesis account + one balance update) through JournalVerifier.Run with
// verify=1, which also triggers the backward revertTest path.
func TestJournalVerifier_RunAndRevert(t *testing.T) {
	ctx := context.Background()
	addr := types.HexToAddress("0x1111111111111111111111111111111111111111")

	root0 := jvComputeRoot(t, func(tx kv.RwTx) {
		require.NoError(t, tx.Put(modules.Account, addr[:], jvAcct(1, 100).MarshalV2()))
	})
	root1 := jvComputeRoot(t, func(tx kv.RwTx) {
		require.NoError(t, tx.Put(modules.Account, addr[:], jvAcct(1, 200).MarshalV2()))
	})

	inputDir := t.TempDir()
	inFz := jvBuildHeaderFreezer(t, inputDir, []types.Hash{root0, root1})
	defer inFz.Close()

	outFz := mkFreezer(t)
	sink, err := NewCSFreezerSink(outFz, ^uint64(0))
	require.NoError(t, err)
	jvEncodeBlock(t, sink, 0, addr, nil, jvAcct(1, 100))
	jvEncodeBlock(t, sink, 1, addr, jvAcct(1, 100), jvAcct(1, 200))

	db := memdb.New(t.TempDir())
	defer db.Close()

	v := NewJournalVerifier(db, inFz, outFz, 1, 2)
	require.NoError(t, v.Run(ctx))

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	final, err := FullStateRootVerify(nil, tx, 1)
	require.NoError(t, err)
	// Run's revertTest leaves the DB reverted back through its checkpoints
	// (here, back to the genesis state at block 0) after the forward replay
	// has already verified block 1 against root1 above.
	require.Equal(t, root0, final)
}

// TestJournalVerifier_Run_Mismatch feeds a deliberately wrong header root
// at block 1 and checks Run reports the mismatch as an error.
func TestJournalVerifier_Run_Mismatch(t *testing.T) {
	ctx := context.Background()
	addr := types.HexToAddress("0x2222222222222222222222222222222222222222")

	root0 := jvComputeRoot(t, func(tx kv.RwTx) {
		require.NoError(t, tx.Put(modules.Account, addr[:], jvAcct(1, 100).MarshalV2()))
	})
	wrongRoot1 := types.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	inputDir := t.TempDir()
	inFz := jvBuildHeaderFreezer(t, inputDir, []types.Hash{root0, wrongRoot1})
	defer inFz.Close()

	outFz := mkFreezer(t)
	sink, err := NewCSFreezerSink(outFz, ^uint64(0))
	require.NoError(t, err)
	jvEncodeBlock(t, sink, 0, addr, nil, jvAcct(1, 100))
	jvEncodeBlock(t, sink, 1, addr, jvAcct(1, 100), jvAcct(1, 200))

	db := memdb.New(t.TempDir())
	defer db.Close()

	v := NewJournalVerifier(db, inFz, outFz, 1, 2)
	err = v.Run(ctx)
	require.Error(t, err)
}
