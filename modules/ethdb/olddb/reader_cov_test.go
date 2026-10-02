package olddb

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestStateReaderReadsFromDBAndFallsBackToOverlay(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addrInDB := types.Address{0x01}
	addrInOverlay := types.Address{0x02}

	acc := account.NewAccount()
	acc.Nonce = 7
	buf := make([]byte, acc.EncodingLengthForStorage())
	acc.EncodeForStorage(buf)
	require.NoError(t, tx.Put(modules.Account, addrInDB[:], buf))

	overlayBuf := make([]byte, acc.EncodingLengthForStorage())
	overlayAcc := account.NewAccount()
	overlayAcc.Nonce = 9
	overlayAcc.EncodeForStorage(overlayBuf)

	data := map[string][]byte{
		string(addrInOverlay[:]): overlayBuf,
	}

	r := NewStateReader(data, nil, tx, 10)
	r.SetBlockNumber(11)

	gotFromDB, err := r.ReadAccountData(addrInDB)
	require.NoError(t, err)
	require.NotNil(t, gotFromDB)
	require.Equal(t, uint64(7), gotFromDB.Nonce)

	gotFromOverlay, err := r.ReadAccountData(addrInOverlay)
	require.NoError(t, err)
	require.NotNil(t, gotFromOverlay)
	require.Equal(t, uint64(9), gotFromOverlay.Nonce)

	missingAddr := types.Address{0x03}
	gotMissing, err := r.ReadAccountData(missingAddr)
	require.NoError(t, err)
	require.Nil(t, gotMissing)
}

func TestStateReaderReadAccountStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr := types.Address{0x01}
	slot := types.Hash{0x02}
	compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes())
	require.NoError(t, tx.Put(modules.Storage, compositeKey, []byte("val-in-db")))

	addr2 := types.Address{0x04}
	slot2 := types.Hash{0x05}
	compositeKey2 := modules.PlainGenerateCompositeStorageKey(addr2.Bytes(), slot2.Bytes())

	data := map[string][]byte{
		string(compositeKey2): []byte("val-in-overlay"),
	}

	r := NewStateReader(data, nil, tx, 1)

	v, err := r.ReadAccountStorage(addr, &slot)
	require.NoError(t, err)
	require.Equal(t, []byte("val-in-db"), v)

	v2, err := r.ReadAccountStorage(addr2, &slot2)
	require.NoError(t, err)
	require.Equal(t, []byte("val-in-overlay"), v2)

	addr3 := types.Address{0x06}
	slot3 := types.Hash{0x07}
	v3, err := r.ReadAccountStorage(addr3, &slot3)
	require.NoError(t, err)
	require.Nil(t, v3)
}

func TestStateReaderReadAccountCode(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	r := NewStateReader(nil, nil, tx, 1)

	emptyCodeHash := types.BytesHash(crypto.Keccak256(nil))
	code, err := r.ReadAccountCode(types.Address{}, emptyCodeHash)
	require.NoError(t, err)
	require.Nil(t, code)

	size, err := r.ReadAccountCodeSize(types.Address{}, emptyCodeHash)
	require.NoError(t, err)
	require.Equal(t, 0, size)

	codeHash := types.Hash{0x09}
	codes := map[types.Hash][]byte{codeHash: []byte("bytecode")}
	r2 := NewStateReader(nil, codes, tx, 1)
	code2, err := r2.ReadAccountCode(types.Address{}, codeHash)
	require.NoError(t, err)
	require.Equal(t, []byte("bytecode"), code2)

	size2, err := r2.ReadAccountCodeSize(types.Address{}, codeHash)
	require.NoError(t, err)
	require.Equal(t, len("bytecode"), size2)

	called := false
	r3 := NewStateReader(nil, nil, tx, 1)
	r3.SetReadCodeF(func(h types.Hash) ([]byte, error) {
		called = true
		require.Equal(t, codeHash, h)
		return []byte("from-func"), nil
	})
	code3, err := r3.ReadAccountCode(types.Address{}, codeHash)
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, []byte("from-func"), code3)
}

func TestStateReaderGetOneEmptyBucket(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	r := NewStateReader(nil, nil, tx, 1)

	v, err := r.GetOne("", []byte("k"))
	require.NoError(t, err)
	require.Nil(t, v)
}
