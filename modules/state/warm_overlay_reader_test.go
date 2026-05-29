package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func overlayTestCfg(d kv.TableCfg) kv.TableCfg {
	d[modules.Account] = kv.TableCfgItem{}
	d[modules.Storage] = kv.TableCfgItem{
		Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 52, DupToLen: 20,
	}
	return d
}

// mockCold is a fixed in-memory snapshot (H0) StateReader.
type mockCold struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash][]byte
}

func (m *mockCold) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	return m.accts[a], nil
}
func (m *mockCold) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	if s, ok := m.stor[a]; ok {
		return s[*k], nil
	}
	return nil, nil
}
func (m *mockCold) ReadAccountCode(types.Address, types.Hash) ([]byte, error)   { return nil, nil }
func (m *mockCold) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }

func bal(n uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, CodeHash: crypto.Keccak256Hash(nil)}
	a.Balance.SetUint64(n)
	return a
}

func TestWarmOverlayReaderThreeState(t *testing.T) {
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(overlayTestCfg).MustOpen()
	defer db.Close()

	var A, B, C types.Address
	A[0], B[0], C[0] = 1, 2, 3
	var S, S2, S3 types.Hash
	S[0], S2[0], S3[0] = 1, 2, 3

	// --- warm tier (H0+1..tip deltas + tombstones) ---
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	accB := bal(99)
	accB.Nonce = 1
	require.NoError(t, tx.Put(modules.Account, B[:], accB.MarshalV2())) // warm value
	require.NoError(t, tx.Put(modules.Account, A[:], []byte{}))         // account tombstone
	ck2 := modules.PlainGenerateCompositeStorageKey(A.Bytes(), S2.Bytes())
	require.NoError(t, tx.Put(modules.Storage, ck2, []byte{0x14})) // warm storage value (20)
	ck := modules.PlainGenerateCompositeStorageKey(A.Bytes(), S.Bytes())
	require.NoError(t, tx.Put(modules.Storage, ck, []byte{})) // storage tombstone (SSTORE slot->0)
	require.NoError(t, tx.Commit())

	// --- cold tier (immutable H0 snapshot) ---
	cold := &mockCold{
		accts: map[types.Address]*account.StateAccount{A: bal(5), C: bal(7)},
		stor:  map[types.Address]map[types.Hash][]byte{A: {S: {0x0a}, S3: {0x1e}}}, // S=10, S3=30
	}

	rtx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer rtx.Rollback()
	r := NewWarmOverlayReader(rtx, cold)

	// B: warm value wins
	a, err := r.ReadAccountData(B)
	require.NoError(t, err)
	require.NotNil(t, a)
	require.Equal(t, uint64(99), a.Balance.Uint64())

	// A: warm tombstone → absent, MUST NOT fall through to cold's balance 5
	a, err = r.ReadAccountData(A)
	require.NoError(t, err)
	require.Nil(t, a, "account tombstone must not fall through to snapshot")

	// C: only in cold → fallback
	a, err = r.ReadAccountData(C)
	require.NoError(t, err)
	require.NotNil(t, a)
	require.Equal(t, uint64(7), a.Balance.Uint64())

	// storage A.S2: warm value wins
	v, err := r.ReadAccountStorage(A, &S2)
	require.NoError(t, err)
	require.Equal(t, []byte{0x14}, v)

	// storage A.S: warm tombstone → absent, MUST NOT fall through to cold's 10.
	// This is the SSTORE slot->0 case the user flagged — the overlay's #1
	// correctness requirement (storage tombstones are mandatory).
	v, err = r.ReadAccountStorage(A, &S)
	require.NoError(t, err)
	require.Nil(t, v, "storage tombstone must not fall through to snapshot")

	// storage A.S3: only in cold → fallback
	v, err = r.ReadAccountStorage(A, &S3)
	require.NoError(t, err)
	require.Equal(t, []byte{0x1e}, v)
}

// TestOverlayWriterReaderRoundTrip checks the write side (OverlayStateWriter)
// and read side (WarmOverlayReader) together: a SSTORE slot->0 and an account
// delete written via the overlay writer become empty-value tombstones that the
// reader resolves as "deleted" WITHOUT falling through to the cold snapshot.
func TestOverlayWriterReaderRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(overlayTestCfg).MustOpen()
	defer db.Close()

	var A types.Address
	A[0] = 1
	var S, S2 types.Hash
	S[0], S2[0] = 1, 2

	// write side: clear A.S (5->0 tombstone), set A.S2 (->7), delete account A
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	w := NewOverlayStateWriter(tx)
	require.NoError(t, w.WriteAccountStorage(A, S, *uint256.NewInt(5), *uint256.NewInt(0)))
	require.NoError(t, w.WriteAccountStorage(A, S2, *uint256.NewInt(0), *uint256.NewInt(7)))
	require.NoError(t, w.DeleteAccount(A, bal(5)))
	require.NoError(t, tx.Commit())

	// cold snapshot (H0): A balance 5, A.S=10
	cold := &mockCold{
		accts: map[types.Address]*account.StateAccount{A: bal(5)},
		stor:  map[types.Address]map[types.Hash][]byte{A: {S: {0x0a}}},
	}

	rtx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	defer rtx.Rollback()
	r := NewWarmOverlayReader(rtx, cold)

	// A.S cleared via writer → tombstone → nil (NOT cold's 10)
	v, err := r.ReadAccountStorage(A, &S)
	require.NoError(t, err)
	require.Nil(t, v, "writer-written storage tombstone must not fall through")

	// A.S2 set via writer → warm value
	v, err = r.ReadAccountStorage(A, &S2)
	require.NoError(t, err)
	require.Equal(t, []byte{0x07}, v)

	// A deleted via writer → tombstone → nil (NOT cold's balance 5)
	a, err := r.ReadAccountData(A)
	require.NoError(t, err)
	require.Nil(t, a, "writer-written account tombstone must not fall through")
}
