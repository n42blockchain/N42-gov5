// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec_v1_decode_errors_test.go drives DecodeAccountChangesV1
// and DecodeStorageChangesV1's truncation and lookup-failure branches. Each
// "full field" blob is built once via the real encoder (so it has a
// realistic shape, including a valid dict-interned id), then fed back in
// at every possible truncation length: every prefix shorter than the full
// blob must return an error (never panic, never silently succeed), which
// mechanically exercises nearly every "truncated ..." check in the decoder
// without hand-encoding each one.

package ethel

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/changeset"
)

// assertAllPrefixesRejected decodes every strict prefix of full and
// requires an error (the full-length blob itself must decode cleanly,
// proven by the caller beforehand).
func assertAllPrefixesRejected(t *testing.T, full []byte, decode func([]byte) error) {
	t.Helper()
	// n==0 (empty input) is a defined no-op (nil, nil), not an error, so
	for n := 1; n < len(full); n++ {
		err := decode(full[:n])
		require.Errorf(t, err, "prefix length %d of %d must be rejected, got no error", n, len(full))
	}
}

func TestDecodeAccountChangesV1_TruncatedDelete(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 1
		var ch types.Hash
		ch[31] = 0x77

		oldAcc := makeAcct(7, 12345, ch)
		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		blob, err := EncodeAccountChangesV1(cs, nil, dw)
		require.NoError(t, err)

		_, err = DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err, "full blob must decode cleanly")

		assertAllPrefixesRejected(t, blob, func(p []byte) error {
			_, err := DecodeAccountChangesV1(p, dr)
			return err
		})
	})
}

func TestDecodeAccountChangesV1_TruncatedCreate(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 2
		var ch types.Hash
		ch[31] = 0x88

		newAcc := makeAcct(3, 999, ch)
		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], nil))

		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
			return newAcc.MarshalV2()
		}, dw)
		require.NoError(t, err)

		_, err = DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)

		assertAllPrefixesRejected(t, blob, func(p []byte) error {
			_, err := DecodeAccountChangesV1(p, dr)
			return err
		})
	})
}

func TestDecodeAccountChangesV1_TruncatedModify(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 3
		var ch1, ch2 types.Hash
		ch1[31] = 0x11
		ch2[31] = 0x22

		oldAcc := makeAcct(1, 100, ch1)
		newAcc := makeAcct(2, 200, ch2)
		cs := changeset.NewAccountChangeSet()
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))

		blob, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
			return newAcc.MarshalV2()
		}, dw)
		require.NoError(t, err)

		_, err = DecodeAccountChangesV1(blob, dr)
		require.NoError(t, err)

		assertAllPrefixesRejected(t, blob, func(p []byte) error {
			_, err := DecodeAccountChangesV1(p, dr)
			return err
		})
	})
}

// TestDecodeAccountChangesV1_UnknownAddrID and _UnknownCodeHashID cover the
// dict.LookupAddr / dict.LookupCodeHash error-wrapping branches by
// referencing ids that were never interned.
func TestDecodeAccountChangesV1_UnknownAddrID(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, _ *DictWriter, dr *DictReader) {
		blob := []byte{
			0x01, 0x00, // count = 1
			0x00, 0x00, 0x01, // addrID = 1 (never interned)
			0x01, // flags: oldExists only, no field bits
		}
		_, err := DecodeAccountChangesV1(blob, dr)
		require.Error(t, err)
	})
}

func TestDecodeAccountChangesV1_UnknownCodeHashID(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 9
		addrID, err := dw.InternAddr(addr)
		require.NoError(t, err)

		blob := []byte{0x01, 0x00}
		blob = appendBE24(blob, addrID)
		blob = append(blob, flagOldExists|flagCodeHashField)
		blob = appendBE24(blob, 0xFFFFFE) // bogus codeHash id, never interned

		_, err = DecodeAccountChangesV1(blob, dr)
		require.Error(t, err)
	})
}

func TestDecodeAccountChangesV1_NilDictAndEmptyInput(t *testing.T) {
	entries, err := DecodeAccountChangesV1(nil, nil)
	require.NoError(t, err)
	require.Nil(t, entries)

	withDictTx(t, func(t *testing.T, _ kv.RwTx, _ *DictWriter, dr *DictReader) {
		_, err := DecodeAccountChangesV1([]byte{0x01}, dr) // 1 byte < header size
		require.Error(t, err)
	})

	_, err = DecodeAccountChangesV1([]byte{0x01, 0x00}, nil)
	require.Error(t, err, "nil dict must be rejected")
}

func TestDecodeAccountChangesV1_NeitherOldNorNewRejected(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 5
		addrID, err := dw.InternAddr(addr)
		require.NoError(t, err)

		blob := []byte{0x01, 0x00}
		blob = appendBE24(blob, addrID)
		blob = append(blob, 0x00) // flags = 0: neither old nor new

		_, err = DecodeAccountChangesV1(blob, dr)
		require.Error(t, err)
	})
}

func TestDecodeStorageChangesV1_Truncated(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 0x55
		key := make([]byte, 52)
		copy(key[:20], addr[:])
		key[51] = 0x01

		cs := changeset.NewStorageChangeSet()
		require.NoError(t, cs.Add(key, []byte{0xAA, 0xBB}))

		blob, err := EncodeStorageChangesV1(cs, func(a types.Address, s types.Hash) []byte {
			return []byte{0x01, 0x02, 0x03}
		}, dw)
		require.NoError(t, err)

		_, err = DecodeStorageChangesV1(blob, dr)
		require.NoError(t, err)

		assertAllPrefixesRejected(t, blob, func(p []byte) error {
			_, err := DecodeStorageChangesV1(p, dr)
			return err
		})
	})
}

func TestDecodeStorageChangesV1_NilDictAndEmptyInput(t *testing.T) {
	entries, err := DecodeStorageChangesV1(nil, nil)
	require.NoError(t, err)
	require.Nil(t, entries)

	withDictTx(t, func(t *testing.T, _ kv.RwTx, _ *DictWriter, dr *DictReader) {
		_, err := DecodeStorageChangesV1([]byte{0x01}, dr)
		require.Error(t, err)
	})

	_, err = DecodeStorageChangesV1([]byte{0x01, 0x00}, nil)
	require.Error(t, err, "nil dict must be rejected")
}

func TestDecodeStorageChangesV1_UnknownAddrID(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, _ *DictWriter, dr *DictReader) {
		blob := []byte{0x01, 0x00}     // addrCount = 1
		blob = appendBE24(blob, 1)     // addrID = 1 (never interned)
		blob = appendUint16LE(blob, 0) // slotCount = 0
		_, err := DecodeStorageChangesV1(blob, dr)
		require.Error(t, err)
	})
}

func TestDecodeStorageChangesV1_OversizeLenRejected(t *testing.T) {
	withDictTx(t, func(t *testing.T, _ kv.RwTx, dw *DictWriter, dr *DictReader) {
		var addr types.Address
		addr[0] = 0x66
		addrID, err := dw.InternAddr(addr)
		require.NoError(t, err)

		blob := []byte{0x01, 0x00} // addrCount = 1
		blob = appendBE24(blob, addrID)
		blob = appendUint16LE(blob, 1) // slotCount = 1
		blob = append(blob, make([]byte, 32)...)
		blob = append(blob, byte(maxStorageValueLen+1)) // oldLen too large
		blob = append(blob, 0x00)                       // pad past the pos+34 truncation check
		_, err = DecodeStorageChangesV1(blob, dr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "oldLen")
	})
}
