// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// dictTablesInitOnce guarantees N42Init runs exactly once even when several
// t.Parallel tests race to open their first dict-aware DB.
var dictTablesInitOnce sync.Once

// newDictTestDB creates an in-memory MDBX with N42TableCfg applied — that
// gives us AddrDict / CodeHashDict / DictMeta / etc. The default
// memdb.NewTestDB skips the N42 schema, so dictionary writes would otherwise
// hit MDBX_NOTFOUND on the missing table.
func newDictTestDB(t *testing.T) kv.RwDB {
	t.Helper()
	dictTablesInitOnce.Do(modules.N42Init)
	db := mdbx.NewMDBX(log.New()).
		InMem(t.TempDir()).
		MapSize(1 * datasize.GB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
		MustOpen()
	t.Cleanup(db.Close)
	return db
}

// withDictTx opens a fresh test DB and runs fn with a writable tx + matching
// DictWriter / DictReader within the same tx so newly-interned ids are
// visible to the reader.
func withDictTx(t *testing.T, fn func(t *testing.T, tx kv.RwTx, dw *DictWriter, dr *DictReader)) {
	t.Helper()
	db := newDictTestDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	dw := NewDictWriter(tx)
	dr, err := NewDictReader(tx)
	require.NoError(t, err)

	fn(t, tx, dw, dr)
}

func makeAcct(nonce uint64, balance uint64, codeHash types.Hash) account.StateAccount {
	a := account.NewAccount()
	a.Initialised = true
	a.Nonce = nonce
	a.Balance.SetUint64(balance)
	a.CodeHash = codeHash
	return a
}

// TestEncodeDecodeAccountChangesV1_EOATx is the dominant tx pattern: nonce++
// and balance decreases (gas paid). Both old and new exist; codeHash is empty
// throughout so flagCodeHashField must NOT be set.
func TestEncodeDecodeAccountChangesV1_EOATx(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[19] = 0x01

		oldAcc := makeAcct(5, 1_000_000, types.Hash{})
		newAcc := makeAcct(6, 999_790, types.Hash{}) // gas paid

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		newVals := map[types.Address][]byte{addr: newAcc.MarshalV2()}
		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return newVals[a] }, dw)
		require.NoError(t, err)
		require.NotNil(t, blob)

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, addr, entries[0].Address)
		require.Equal(t, oldAcc.MarshalV2(), entries[0].OldValue, "old V2 must round-trip")
		require.Equal(t, newAcc.MarshalV2(), entries[0].NewValue, "new V2 must round-trip")
	})
}

// TestEncodeDecodeAccountChangesV1_ContractOnlyNonce is the high-savings
// pattern: a contract is called, nonce ticks but balance and codeHash do not
// move. The encoded entry MUST NOT contain the 32B codeHash — the whole point
// of V1 is to make this case 6 bytes (entry-level) / 8 bytes (with 2B count).
//
// The decoded V2 carries ONLY the changed nonce; balance and codeHash come
// out as defaults because V1 omits them. Forward replay is responsible for
// merging the decoded delta with PlainState's existing value.
func TestEncodeDecodeAccountChangesV1_ContractOnlyNonce(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[19] = 0xAB
		codeHash := ethelFilledHash(0x77)

		oldAcc := makeAcct(10, 0, codeHash)
		newAcc := makeAcct(11, 0, codeHash)

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		newVals := map[types.Address][]byte{addr: newAcc.MarshalV2()}
		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return newVals[a] }, dw)
		require.NoError(t, err)

		// Sanity: 2B count + 3B addrID + 1B flags + 2B (oldNonce + newNonce
		// uvarints, both <128 so 1B each) = 8B. This is the ~90% saving over
		// V0 for the contract-call modification pattern.
		require.Equal(t, 8, len(blob), "contract only-nonce should compress to 8 bytes")

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)

		// V1 omits unchanged balance + codeHash, so decoded V2 reflects the
		// nonce alone. The codeHash hole must be filled by the caller from
		// PlainState before applying the change anywhere.
		expectedOld := makeAcct(10, 0, types.Hash{})
		expectedNew := makeAcct(11, 0, types.Hash{})
		require.Equal(t, expectedOld.MarshalV2(), entries[0].OldValue)
		require.Equal(t, expectedNew.MarshalV2(), entries[0].NewValue)
	})
}

// TestEncodeDecodeAccountChangesV1_Create covers the !old && new case:
// account does not yet exist, post-block has nonce/balance/codeHash. Encoding
// must skip the old half entirely.
func TestEncodeDecodeAccountChangesV1_Create(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[19] = 0xC0
		codeHash := ethelFilledHash(0x42)

		newAcc := makeAcct(1, 12345, codeHash)

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], []byte{})) // empty old

		newVals := map[types.Address][]byte{addr: newAcc.MarshalV2()}
		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return newVals[a] }, dw)
		require.NoError(t, err)

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Empty(t, entries[0].OldValue, "old half must remain empty")
		require.Equal(t, newAcc.MarshalV2(), entries[0].NewValue)
	})
}

// TestEncodeDecodeAccountChangesV1_Selfdestruct covers the old && !new case:
// account is wiped, post-block does not exist. Encoding must skip the new half.
func TestEncodeDecodeAccountChangesV1_Selfdestruct(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[19] = 0xDE
		codeHash := ethelFilledHash(0x99)

		oldAcc := makeAcct(7, 5_000_000_000, codeHash)

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return nil }, dw)
		require.NoError(t, err)

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, oldAcc.MarshalV2(), entries[0].OldValue)
		require.Empty(t, entries[0].NewValue, "new half must remain empty")
	})
}

// TestEncodeDecodeAccountChangesV1_CodeHashChange covers the rare CREATE2
// re-deploy case: old and new both exist, but codeHash actually differs.
// All three field bits should be set; payload carries old/new pairs.
func TestEncodeDecodeAccountChangesV1_CodeHashChange(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[19] = 0xCC
		oldHash := ethelFilledHash(0x10)
		newHash := ethelFilledHash(0x20)

		oldAcc := makeAcct(3, 100, oldHash)
		newAcc := makeAcct(4, 90, newHash)

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		newVals := map[types.Address][]byte{addr: newAcc.MarshalV2()}
		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return newVals[a] }, dw)
		require.NoError(t, err)

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, oldAcc.MarshalV2(), entries[0].OldValue)
		require.Equal(t, newAcc.MarshalV2(), entries[0].NewValue)
	})
}

// TestEncodeDecodeAccountChangesV1_BalanceMaxU256 stresses balance handling
// at the U256 upper bound — verifies the 32-byte path through appendBalance
// and the matching decoder. Past bugs in U256 truncation manifested as
// silent corruption (compact encoders truncating high bytes); this catches
// them.
//
// Both old and new keep nonce=0 / codeHash=empty so the round-trip is
// loss-free for the fields we DO carry. Only balance is in the delta.
func TestEncodeDecodeAccountChangesV1_BalanceMaxU256(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 0xFF

		bigBal := new(uint256.Int)
		_ = bigBal.SetFromHex("0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFE")

		oldAcc := account.NewAccount()
		oldAcc.Initialised = true
		oldAcc.Balance.Set(bigBal)

		newAcc := account.NewAccount()
		newAcc.Initialised = true
		newAcc.Balance.SetUint64(0) // wipe to zero

		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		newVals := map[types.Address][]byte{addr: newAcc.MarshalV2()}
		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte { return newVals[a] }, dw)
		require.NoError(t, err)

		entries, err := DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, oldAcc.MarshalV2(), entries[0].OldValue)
		require.Equal(t, newAcc.MarshalV2(), entries[0].NewValue)
	})
}

// TestEncodeDecodeStorageChangesV1 covers the simple storage round-trip with
// the 3B addr id replacing the 20B address. Validates per-addr grouping is
// preserved across encode/decode.
func TestEncodeDecodeStorageChangesV1(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		makeKey := func(a, s byte) []byte {
			k := make([]byte, 52)
			k[19] = a
			k[51] = s
			return k
		}
		cs := changeset.NewStorageChangeSet()
		require.NoError(t, cs.Add(makeKey(0x42, 0x01), []byte{0xAA}))
		require.NoError(t, cs.Add(makeKey(0x42, 0x02), []byte{0xBB, 0xCC}))
		require.NoError(t, cs.Add(makeKey(0x99, 0x01), []byte{}))

		newVals := map[string][]byte{
			string(makeKey(0x42, 0x01)): {0xA1},
			string(makeKey(0x42, 0x02)): {0xBB, 0xCD},
			string(makeKey(0x99, 0x01)): {0xFF, 0xFF, 0xFF},
		}
		blob, err := EncodeStorageChangesV1(cs, func(addr types.Address, slot types.Hash) []byte {
			k := make([]byte, 52)
			copy(k[:20], addr[:])
			copy(k[20:], slot[:])
			return newVals[string(k)]
		}, dw)
		require.NoError(t, err)
		require.NotNil(t, blob)

		entries, err := DecodeStorageChangesV1(blob, dr)
		require.NoError(t, err)
		require.Len(t, entries, 3)
		seen := map[string]bool{}
		for _, e := range entries {
			seen[string(e.CompositeKey)] = true
			expected := newVals[string(e.CompositeKey)]
			if !bytes.Equal(e.NewValue, expected) {
				t.Errorf("new mismatch for %x: got %x want %x", e.CompositeKey, e.NewValue, expected)
			}
		}
		for k := range newVals {
			if !seen[k] {
				t.Errorf("missing key %x", []byte(k))
			}
		}
	})
}

// TestDictWriterDeduplicates verifies that interning the same addr / hash
// twice yields the same id and does not allocate a new MDBX entry.
func TestDictWriterDeduplicates(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, tx kv.RwTx, dw *DictWriter, _ *DictReader) {
		var addr types.Address
		addr[10] = 0xAA

		id1, err := dw.InternAddr(addr)
		require.NoError(t, err)
		require.Equal(t, uint32(1), id1, "first allocation must be id=1")

		id2, err := dw.InternAddr(addr)
		require.NoError(t, err)
		require.Equal(t, id1, id2, "re-interning same addr must return same id")

		var addr2 types.Address
		addr2[10] = 0xBB
		id3, err := dw.InternAddr(addr2)
		require.NoError(t, err)
		require.Equal(t, uint32(2), id3, "second distinct addr must get id=2")
	})
}

// TestDictWriterEmptyCodeHashSentinel verifies that the zero hash is never
// interned and always returns id=0.
func TestDictWriterEmptyCodeHashSentinel(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		id, err := dw.InternCodeHash(types.Hash{})
		require.NoError(t, err)
		require.Equal(t, uint32(0), id, "empty hash must intern to id=0")

		// Reader must round-trip 0 -> zero hash without an MDBX read.
		h, err := dr.LookupCodeHash(0)
		require.NoError(t, err)
		require.Equal(t, types.Hash{}, h)
	})
}

// TestDecodeAccountChangesV1_RejectsReservedFlags ensures we hard-fail on
// any attempt to encode reserved bits (incarnation lives there in V0 but is
// permanently retired in V1).
func TestDecodeAccountChangesV1_RejectsReservedFlags(t *testing.T) {
	t.Parallel()
	withDictTx(t, func(t *testing.T, _ kv.RwTx, _ *DictWriter, dr *DictReader) {
		// Hand-crafted 1-entry blob with reserved bit 5 set.
		blob := []byte{
			0x01, 0x00, // count = 1
			0x00, 0x00, 0x01, // addrID = 1
			0x23, // flags: oldExists | newExists | reserved bit5
		}
		_, err := DecodeAccountChangesV1(blob, dr)
		require.Error(t, err, "reserved flag bit must reject")
	})
}
