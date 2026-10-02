// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec_errors_test.go covers the V0 (changeset_codec.go)
// decoder's truncation branches via the same prefix-fuzz technique used
// for the V1 decoders, plus EncodeGenesisAccounts' oversize-value guard.

package ethel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

var errCustom = errors.New("iterator failure")

func TestDecodeAccountChanges_Truncated(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[19] = 0x42
	require.NoError(t, cs.Add(addr[:], []byte{1, 2, 3}))

	data := EncodeAccountChanges(cs, func(a types.Address) []byte { return []byte{9, 9} })
	require.NotNil(t, data)

	_, err := DecodeAccountChanges(data)
	require.NoError(t, err)

	for n := 1; n < len(data); n++ {
		_, err := DecodeAccountChanges(data[:n])
		require.Errorf(t, err, "prefix %d/%d must be rejected", n, len(data))
	}
}

func TestDecodeStorageChanges_Truncated(t *testing.T) {
	cs := changeset.NewStorageChangeSet()
	key := make([]byte, 52)
	key[0] = 0x11
	key[51] = 0x02
	require.NoError(t, cs.Add(key, []byte{1, 2}))

	data := EncodeStorageChanges(cs, func(a types.Address, s types.Hash) []byte { return []byte{7} })
	require.NotNil(t, data)

	_, err := DecodeStorageChanges(data)
	require.NoError(t, err)

	for n := 1; n < len(data); n++ {
		_, err := DecodeStorageChanges(data[:n])
		require.Errorf(t, err, "prefix %d/%d must be rejected", n, len(data))
	}
}

func TestDecodeStorageChanges_OversizeLenRejected(t *testing.T) {
	// addrCount=1, addr(20B), slotCount=1, slot(32B), oldLen byte too
	// large, plus one pad byte to clear the pos+34 minimum-length check.
	blob := []byte{0x01, 0x00}
	blob = append(blob, make([]byte, 20)...) // addr
	blob = appendUint16LE(blob, 1)           // slotCount = 1
	blob = append(blob, make([]byte, 32)...) // slot
	blob = append(blob, byte(maxStorageValueLen+1))
	blob = append(blob, 0x00) // padding past the truncation check

	_, err := DecodeStorageChanges(blob)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oldLen")
}

func TestEncodeGenesisAccounts_ValueTooLong(t *testing.T) {
	_, err := EncodeGenesisAccounts(func(yield func(addr types.Address, v []byte) error) error {
		var addr types.Address
		addr[0] = 1
		return yield(addr, make([]byte, 256))
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too long")
}

func TestEncodeGenesisAccounts_IteratorError(t *testing.T) {
	_, err := EncodeGenesisAccounts(func(yield func(addr types.Address, v []byte) error) error {
		return errCustom
	})
	require.Error(t, err)
	require.Equal(t, errCustom, err)
}
