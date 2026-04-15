// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestApplyAccountValue_RoundTrip exercises the trivial Phase C
// applyAccountValue: forward apply writes, backward apply rewrites the
// pre-state byte-for-byte. Replaces the legacy
// TestApplyAccountValueAndRevertIncarnationPreserveVersionedCodeHash
// test that locked down Codex's incarnation-recovery fix; that fix is
// no longer needed because Phase B made acctcs OldValue self-contained
// (full V2 with CodeHash inline).
func TestApplyAccountValue_RoundTrip(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xAB

	oldAcc := account.NewAccount()
	oldAcc.Initialised = true
	oldAcc.Nonce = 1
	oldAcc.Balance.SetUint64(5)
	oldAcc.CodeHash = ethelFilledHash(0x11)

	newAcc := oldAcc
	newAcc.Nonce = 2
	newAcc.CodeHash = ethelFilledHash(0x22)

	oldBytes := oldAcc.MarshalV2()
	newBytes := newAcc.MarshalV2()

	require.NoError(t, tx.Put(modules.Account, addr[:], oldBytes))

	// Forward: write NEW value.
	require.NoError(t, applyAccountValue(tx, addr, newBytes))
	gotNew, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.Equal(t, newBytes, gotNew, "forward apply must write byte-equal NEW")

	// Backward: write OLD value — reproduces the pre-block account.
	require.NoError(t, applyAccountValue(tx, addr, oldBytes))
	gotOld, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.Equal(t, oldBytes, gotOld, "backward apply must write byte-equal OLD")
}

// TestApplyAccountValue_DeleteOnEmpty verifies that an empty encodedValue
// triggers a full Account-row delete (the SELFDESTRUCT / new-account-empty
// shape produced by acctcs entries with len(NewValue)==0 or
// len(OldValue)==0 for previously-absent accounts).
func TestApplyAccountValue_DeleteOnEmpty(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xCD

	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 7
	require.NoError(t, tx.Put(modules.Account, addr[:], acc.MarshalV2()))

	require.NoError(t, applyAccountValue(tx, addr, nil))

	got, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	require.Empty(t, got, "empty encodedValue must delete the row")
}

func ethelFilledHash(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}
