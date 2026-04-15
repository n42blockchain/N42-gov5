package ethel

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	statemod "github.com/n42blockchain/N42/modules/state"
)

func TestApplyAccountValueAndRevertIncarnationPreserveVersionedCodeHash(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xAB

	oldCodeHash := ethelFilledHash(0x11)
	newCodeHash := ethelFilledHash(0x22)

	oldAcc := account.NewAccount()
	oldAcc.Initialised = true
	oldAcc.Nonce = 1
	oldAcc.Balance.SetUint64(5)
	oldAcc.CodeHash = oldCodeHash

	newAcc := oldAcc
	newAcc.Nonce = 2
	newAcc.CodeHash = newCodeHash

	var inc1 [2]byte
	binary.BigEndian.PutUint16(inc1[:], 1)
	require.NoError(t, tx.Put(modules.Account, addr[:], oldAcc.MarshalV2()))
	require.NoError(t, tx.Put(modules.IncarnationMap, addr[:], inc1[:]))
	require.NoError(t, tx.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:]), oldCodeHash[:]))
	require.NoError(t, tx.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(addr[:], 1), oldCodeHash[:]))

	oldValue := statemod.EncodeAccountForHistory(&oldAcc, true, 1)
	newValue := statemod.EncodeAccountForHistory(&newAcc, false, 2)

	forwardIncarnation, err := valueIncarnation(tx, addr, newValue)
	require.NoError(t, err)
	require.Equal(t, uint16(2), forwardIncarnation)
	require.NoError(t, applyAccountValue(tx, addr, newValue, forwardIncarnation))

	currentAcc, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	var decodedCurrent account.StateAccount
	require.NoError(t, decodedCurrent.DecodeForStorage(currentAcc))
	require.Equal(t, newCodeHash, decodedCurrent.CodeHash)

	currentIncarnation, err := readIncarnation(tx, addr)
	require.NoError(t, err)
	require.Equal(t, uint16(2), currentIncarnation)
	currentCodeHash, err := tx.GetOne(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:]))
	require.NoError(t, err)
	require.Equal(t, newCodeHash[:], currentCodeHash)
	version1CodeHash, err := tx.GetOne(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(addr[:], 1))
	require.NoError(t, err)
	require.Equal(t, oldCodeHash[:], version1CodeHash)

	oldIncarnation, err := revertIncarnation(tx, addr, oldValue, newValue)
	require.NoError(t, err)
	require.Equal(t, uint16(1), oldIncarnation)
	require.NoError(t, applyAccountValue(tx, addr, oldValue, oldIncarnation))

	restoredAcc, err := tx.GetOne(modules.Account, addr[:])
	require.NoError(t, err)
	var decodedRestored account.StateAccount
	require.NoError(t, decodedRestored.DecodeForStorage(restoredAcc))
	require.Equal(t, oldCodeHash, decodedRestored.CodeHash)

	restoredIncarnation, err := readIncarnation(tx, addr)
	require.NoError(t, err)
	require.Equal(t, uint16(1), restoredIncarnation)
	restoredCodeHash, err := tx.GetOne(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:]))
	require.NoError(t, err)
	require.Equal(t, oldCodeHash[:], restoredCodeHash)
}

func ethelFilledHash(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}
