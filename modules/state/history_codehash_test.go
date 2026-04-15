package state

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestAccountHistoryEncodingOmitsCodeHashButKeepsIncarnation(t *testing.T) {
	t.Parallel()

	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 3
	acc.Balance.SetUint64(9)
	acc.CodeHash = filledHash(0x11)

	encoded := EncodeAccountForHistory(&acc, true, 7)

	var decoded account.StateAccount
	require.NoError(t, decoded.DecodeForStorage(encoded))
	require.True(t, decoded.IsEmptyCodeHash())

	incarnation, ok, err := DecodeAccountHistoryIncarnation(encoded)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint16(7), incarnation)
}

// TestPlainStateHistoricalReadCodeHashSelfContained verifies the Phase B
// (reth-style) behavior: account changeset OldValues carry the historical
// CodeHash inline as full V2, so historical reads no longer require
// the PlainContractCode versioned-key fallback. The fallback machinery
// (LookupPlainContractCodeHash + RestoreHistoricalAccountCodeHash) is
// retained for legacy data compatibility but is a no-op for new entries.
func TestPlainStateHistoricalReadCodeHashSelfContained(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xAA

	oldCodeHash := filledHash(0x11)
	newCodeHash := filledHash(0x22)

	var inc1, inc2 [2]byte
	binary.BigEndian.PutUint16(inc1[:], 1)
	binary.BigEndian.PutUint16(inc2[:], 2)

	// Pre-state PlainContractCode write — kept for the current-state
	// reader path (UpdateAccountCode populates it). Phase D will revisit
	// whether this table can be removed entirely.
	require.NoError(t, tx.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(addr[:], 1), oldCodeHash[:]))
	require.NoError(t, tx.Put(modules.IncarnationMap, addr[:], inc1[:]))

	orig := account.NewAccount()
	orig.Initialised = true
	orig.Nonce = 1
	orig.Balance.SetUint64(7)
	orig.CodeHash = oldCodeHash

	next := orig
	next.Nonce = 2
	next.CodeHash = newCodeHash

	writer := NewPlainStateWriter(tx, tx, 1)
	writer.NoteAccountIncarnations(addr, 1, 2)
	require.NoError(t, writer.UpdateAccountCode(addr, 2, newCodeHash, []byte{0x60, 0x00}))
	require.NoError(t, writer.UpdateAccountData(addr, &orig, &next))
	require.NoError(t, writer.WriteChangeSets())
	require.NoError(t, writer.WriteHistory())

	require.NoError(t, tx.Put(modules.IncarnationMap, addr[:], inc2[:]))

	csw := writer.ChangeSetWriter()
	require.NotNil(t, csw)
	accCS, err := csw.GetAccountChanges()
	require.NoError(t, err)
	require.Len(t, accCS.Changes, 1)

	// Phase B invariant: OldValue is full V2 with CodeHash present —
	// no omit, no hidden incarnation tag.
	var storedOld account.StateAccount
	require.NoError(t, storedOld.DecodeForStorage(accCS.Changes[0].Value))
	require.False(t, storedOld.IsEmptyCodeHash(),
		"Phase B: changeset OldValue must carry CodeHash inline")
	require.Equal(t, oldCodeHash, storedOld.CodeHash)
	_, ok, err := DecodeAccountHistoryIncarnation(accCS.Changes[0].Value)
	require.NoError(t, err)
	require.False(t, ok, "Phase B: full V2 must not carry hidden incarnation tag")

	// Historical read still returns the correct (old) CodeHash — now
	// directly from the changeset, with the legacy PlainContractCode
	// fallback acting as a harmless no-op.
	historical := NewPlainState(tx, 1)
	got, err := historical.ReadAccountData(addr)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, oldCodeHash, got.CodeHash)

	current, err := NewPlainStateReader(tx).ReadAccountData(addr)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, newCodeHash, current.CodeHash)
}

func TestDeleteAccountClearsIncarnationMapWhenCurrentIncarnationIsZero(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xBB

	var inc [2]byte
	binary.BigEndian.PutUint16(inc[:], 1)
	require.NoError(t, tx.Put(modules.IncarnationMap, addr[:], inc[:]))

	orig := account.NewAccount()
	orig.Initialised = true
	orig.CodeHash = filledHash(0x33)

	writer := NewPlainStateWriter(tx, tx, 1)
	writer.NoteAccountIncarnations(addr, 1, 0)
	require.NoError(t, writer.DeleteAccount(addr, &orig))

	got, err := tx.GetOne(modules.IncarnationMap, addr[:])
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestBufferedDeleteAccountClearsIncarnationMapWhenCurrentIncarnationIsZero(t *testing.T) {
	t.Parallel()

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	var addr types.Address
	addr[19] = 0xBC

	var inc [2]byte
	binary.BigEndian.PutUint16(inc[:], 1)
	require.NoError(t, tx.Put(modules.IncarnationMap, addr[:], inc[:]))

	orig := account.NewAccount()
	orig.Initialised = true
	orig.CodeHash = filledHash(0x44)

	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriter(buf, tx, 1)
	writer.NoteAccountIncarnations(addr, 1, 0)
	require.NoError(t, writer.DeleteAccount(addr, &orig))

	snap := buf.SnapshotForFlush()
	require.NoError(t, snap.ApplyTo(tx))

	got, err := tx.GetOne(modules.IncarnationMap, addr[:])
	require.NoError(t, err)
	require.Nil(t, got)
}

func filledHash(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}
