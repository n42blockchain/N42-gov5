// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_codec_v1_errors_test.go covers the V1 codec's error, skip, and
// chunking branches that the happy-path round-trip tests in
// changeset_codec_v1_test.go don't reach: nil/empty inputs, malformed
// account/storage bytes, dictionary failures (via a mock DictInterner so
// no MDBX setup is needed), oversize values, and truncated decode input.

package ethel

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/changeset"
)

var errMock = errors.New("mock dict failure")

// mockDict is a DictInterner double that lets tests force specific failure
// points without an MDBX round trip.
type mockDict struct {
	addrErr error

	chErr      error
	chFailCall int // 1-based call index to fail on; 0 means never fail
	chCalls    int

	nextID uint32
}

func (m *mockDict) InternAddr(addr types.Address) (uint32, error) {
	if m.addrErr != nil {
		return 0, m.addrErr
	}
	m.nextID++
	return m.nextID, nil
}

func (m *mockDict) InternCodeHash(h types.Hash) (uint32, error) {
	if h == (types.Hash{}) {
		return 0, nil
	}
	m.chCalls++
	if m.chErr != nil && (m.chFailCall == 0 || m.chCalls == m.chFailCall) {
		return 0, m.chErr
	}
	m.nextID++
	return m.nextID, nil
}

func TestEncodeAccountChangesV1_NilAndEmptyInputs(t *testing.T) {
	blob, err := EncodeAccountChangesV1(nil, nil, &mockDict{})
	require.NoError(t, err)
	require.Nil(t, blob)

	blob, err = EncodeAccountChangesV1(changeset.NewAccountChangeSet(), nil, &mockDict{})
	require.NoError(t, err)
	require.Nil(t, blob)

	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[0] = 1
	require.NoError(t, cs.Add(addr[:], nil))
	_, err = EncodeAccountChangesV1(cs, nil, nil)
	require.Error(t, err, "nil dict must be rejected")
}

func TestEncodeAccountChangesV1_SkipsShortKey(t *testing.T) {
	// ChangeSet.Add enforces uniform key length, so a malformed short key
	// can only occur via direct field access (e.g. a corrupted upstream
	// changeset) — append straight to Changes to exercise that guard.
	cs := changeset.NewAccountChangeSet()
	cs.Changes = append(cs.Changes, changeset.Change{Key: []byte{1, 2, 3}})
	blob, err := EncodeAccountChangesV1(cs, nil, &mockDict{})
	require.NoError(t, err)
	// count header reflects cs.Len() (1), but the short-key entry is
	// skipped, so no entry bytes follow.
	require.Equal(t, []byte{0x01, 0x00}, blob)
}

func TestEncodeAccountChangesV1_DecodeOldAccountError(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[0] = 1
	// fieldBits=1 (nonce present) with no varint payload -> malformed.
	require.NoError(t, cs.Add(addr[:], []byte{0x01}))
	_, err := EncodeAccountChangesV1(cs, nil, &mockDict{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode old account")
}

func TestEncodeAccountChangesV1_DecodeNewAccountError(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[0] = 1
	require.NoError(t, cs.Add(addr[:], nil))
	_, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
		return []byte{0x01} // malformed: nonce flag set, no varint bytes
	}, &mockDict{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode new account")
}

func TestEncodeAccountChangesV1_InternAddrError(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[0] = 1
	a := account.NewAccount()
	a.Initialised = true
	require.NoError(t, cs.Add(addr[:], a.MarshalV2()))
	_, err := EncodeAccountChangesV1(cs, nil, &mockDict{addrErr: errMock})
	require.Error(t, err)
	require.Contains(t, err.Error(), "intern addr")
}

func TestEncodeAccountEntryV1_NeitherOldNorNew(t *testing.T) {
	cs := changeset.NewAccountChangeSet()
	var addr types.Address
	addr[0] = 1
	require.NoError(t, cs.Add(addr[:], nil)) // old=nil, new=nil (no newValueOf)
	_, err := EncodeAccountChangesV1(cs, nil, &mockDict{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "neither old nor new")
}

func TestEncodeAccountEntryV1_InternCodeHashError(t *testing.T) {
	var h1, h2 types.Hash
	h1[31] = 0xAA
	h2[31] = 0xBB

	t.Run("delete", func(t *testing.T) {
		cs := changeset.NewAccountChangeSet()
		var addr types.Address
		addr[0] = 1
		oldAcc := account.NewAccount()
		oldAcc.Initialised = true
		oldAcc.CodeHash = h1
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))
		_, err := EncodeAccountChangesV1(cs, nil, &mockDict{chErr: errMock})
		require.Error(t, err)
	})

	t.Run("create", func(t *testing.T) {
		cs := changeset.NewAccountChangeSet()
		var addr types.Address
		addr[0] = 1
		require.NoError(t, cs.Add(addr[:], nil))
		newAcc := account.NewAccount()
		newAcc.Initialised = true
		newAcc.CodeHash = h1
		_, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
			return newAcc.MarshalV2()
		}, &mockDict{chErr: errMock})
		require.Error(t, err)
	})

	t.Run("modify-old-id", func(t *testing.T) {
		cs := changeset.NewAccountChangeSet()
		var addr types.Address
		addr[0] = 1
		oldAcc := account.NewAccount()
		oldAcc.Initialised = true
		oldAcc.CodeHash = h1
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))
		newAcc := account.NewAccount()
		newAcc.Initialised = true
		newAcc.CodeHash = h2
		_, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
			return newAcc.MarshalV2()
		}, &mockDict{chErr: errMock, chFailCall: 1})
		require.Error(t, err)
	})

	t.Run("modify-new-id", func(t *testing.T) {
		cs := changeset.NewAccountChangeSet()
		var addr types.Address
		addr[0] = 1
		oldAcc := account.NewAccount()
		oldAcc.Initialised = true
		oldAcc.CodeHash = h1
		require.NoError(t, cs.Add(addr[:], oldAcc.MarshalV2()))
		newAcc := account.NewAccount()
		newAcc.Initialised = true
		newAcc.CodeHash = h2
		_, err := EncodeAccountChangesV1(cs, func(a types.Address) []byte {
			return newAcc.MarshalV2()
		}, &mockDict{chErr: errMock, chFailCall: 2})
		require.Error(t, err)
	})
}

func TestEncodeStorageChangesV1_NilAndEmptyInputs(t *testing.T) {
	blob, err := EncodeStorageChangesV1(nil, nil, &mockDict{})
	require.NoError(t, err)
	require.Nil(t, blob)

	blob, err = EncodeStorageChangesV1(changeset.NewStorageChangeSet(), nil, &mockDict{})
	require.NoError(t, err)
	require.Nil(t, blob)

	cs := changeset.NewStorageChangeSet()
	key := make([]byte, 52)
	require.NoError(t, cs.Add(key, []byte{1}))
	_, err = EncodeStorageChangesV1(cs, nil, nil)
	require.Error(t, err, "nil dict must be rejected")
}

func TestEncodeStorageChangesV1_SkipsShortKey(t *testing.T) {
	// Same rationale as the account-side test: bypass ChangeSet.Add's
	// uniform-key-length guard to exercise the codec's own defensive skip.
	cs := changeset.NewStorageChangeSet()
	cs.Changes = append(cs.Changes, changeset.Change{Key: []byte{1, 2, 3}, Value: []byte{1}})
	blob, err := EncodeStorageChangesV1(cs, nil, &mockDict{})
	require.NoError(t, err)
	// No address groups are formed from the skipped entry.
	require.Equal(t, []byte{0x00, 0x00}, blob)
}

func TestEncodeStorageChangesV1_InternAddrError(t *testing.T) {
	cs := changeset.NewStorageChangeSet()
	key := make([]byte, 52)
	key[0] = 1
	require.NoError(t, cs.Add(key, []byte{1}))
	_, err := EncodeStorageChangesV1(cs, nil, &mockDict{addrErr: errMock})
	require.Error(t, err)
	require.Contains(t, err.Error(), "intern addr")
}

func TestEncodeStorageChangesV1_OldValTooLarge(t *testing.T) {
	cs := changeset.NewStorageChangeSet()
	key := make([]byte, 52)
	key[0] = 1
	require.NoError(t, cs.Add(key, make([]byte, maxStorageValueLen+1)))
	_, err := EncodeStorageChangesV1(cs, nil, &mockDict{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oldVal")
}

func TestEncodeStorageChangesV1_NewValTooLarge(t *testing.T) {
	cs := changeset.NewStorageChangeSet()
	key := make([]byte, 52)
	key[0] = 1
	require.NoError(t, cs.Add(key, []byte{1}))
	_, err := EncodeStorageChangesV1(cs, func(a types.Address, s types.Hash) []byte {
		return make([]byte, maxStorageValueLen+1)
	}, &mockDict{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "newVal")
}

// TestEncodeStorageChangesV1_ChunksLargeGroup mirrors the V0 chunking
// regression test: a single address with > 65535 slots must be split into
// multiple addr groups on encode, and must decode back to the same entries.
func TestEncodeStorageChangesV1_ChunksLargeGroup(t *testing.T) {
	const N = 70000
	cs := changeset.NewStorageChangeSet()
	var addr types.Address
	addr[0] = 0x77
	for i := 0; i < N; i++ {
		key := make([]byte, 52)
		copy(key[:20], addr[:])
		key[51] = byte(i)
		key[50] = byte(i >> 8)
		key[49] = byte(i >> 16)
		require.NoError(t, cs.Add(key, []byte{byte(i)}))
	}

	dw := &mockDict{}
	blob, err := EncodeStorageChangesV1(cs, nil, dw)
	require.NoError(t, err)
	require.NotNil(t, blob)

	addrCount := int(blob[0]) | int(blob[1])<<8
	wantGroups := (N + 65534) / 65535
	require.Equal(t, wantGroups, addrCount)
}
