// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// codes_reth_reader_test.go covers the pure Compact-format decoders
// (rethAccountCodeHash, rethCompactRawCode) directly, and drives
// NewRethCodesReader/GetCode/Close against a real, on-disk MDBX built
// with the same PlainAccountState/Bytecodes table shapes reth produces
// — no real reth datadir needed.

package ethel

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// buildRethAccountValue encodes the reth Compact PlainAccountState value
// with nonceLen=0, balLen=0, hasCodeHash=1, matching rethAccountCodeHash's
// expected flags layout exactly.
func buildRethAccountValue(codeHash types.Hash) []byte {
	v := make([]byte, 2+32)
	binary.LittleEndian.PutUint16(v[0:2], 1<<10) // hasHash bit only
	copy(v[2:], codeHash[:])
	return v
}

// buildRethBytecodeValue encodes the reth Compact revm Bytecode value
// with the [padded_len][code][variant][original_len] trailer that
// rethCompactRawCode decodes.
func buildRethBytecodeValue(code []byte) []byte {
	v := make([]byte, 4+len(code)+1+8)
	binary.BigEndian.PutUint32(v[0:4], uint32(len(code)))
	copy(v[4:], code)
	v[4+len(code)] = 0 // variant byte, unused by the decoder
	binary.BigEndian.PutUint64(v[4+len(code)+1:], uint64(len(code)))
	return v
}

func TestRethAccountCodeHash(t *testing.T) {
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000ab")
	v := buildRethAccountValue(codeHash)
	ch, ok := rethAccountCodeHash(v)
	require.True(t, ok)
	require.Equal(t, codeHash, ch)

	// hasHash bit clear -> ok=false.
	v2 := make([]byte, 2)
	_, ok2 := rethAccountCodeHash(v2)
	require.False(t, ok2)

	// Too short to even hold the flags.
	_, ok3 := rethAccountCodeHash([]byte{0x01})
	require.False(t, ok3)

	// hasHash set but length doesn't match nonceLen/balLen/hash layout.
	bad := make([]byte, 2+10) // wrong trailing length
	binary.LittleEndian.PutUint16(bad[0:2], 1<<10)
	_, ok4 := rethAccountCodeHash(bad)
	require.False(t, ok4)
}

func TestRethCompactRawCode(t *testing.T) {
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x01, 0x00}
	v := buildRethBytecodeValue(code)
	got := rethCompactRawCode(v)
	require.Equal(t, code, got)

	// LegacyAnalyzed padding: padded_len > original_len, trailer recovers
	// the real (shorter) length.
	padded := append(append([]byte{}, code...), 0x00, 0x00, 0x00) // 3 pad bytes
	vp := make([]byte, 4+len(padded)+1+8)
	binary.BigEndian.PutUint32(vp[0:4], uint32(len(padded)))
	copy(vp[4:], padded)
	binary.BigEndian.PutUint64(vp[4+len(padded)+1:], uint64(len(code)))
	gotP := rethCompactRawCode(vp)
	require.Equal(t, code, gotP, "trailer original_len must recover the unpadded code")

	// No trailer (e.g. EIP-7702 designator): raw bytes verbatim.
	noTrailer := make([]byte, 4+len(code))
	binary.BigEndian.PutUint32(noTrailer[0:4], uint32(len(code)))
	copy(noTrailer[4:], code)
	require.Equal(t, code, rethCompactRawCode(noTrailer))

	// Malformed: too short to hold the length prefix.
	require.Nil(t, rethCompactRawCode([]byte{0x01, 0x02}))

	// Malformed: padded_len overruns the buffer.
	overrun := []byte{0x00, 0x00, 0x00, 0xff, 0x01}
	require.Nil(t, rethCompactRawCode(overrun))
}

func TestRethCodesReader_GetCode(t *testing.T) {
	dir := t.TempDir()

	addr := types.HexToAddress("0x6666666666666666666666666666666666666")
	addrNoCode := types.HexToAddress("0x7777777777777777777777777777777777777")
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52}
	codeHash := types.HexToHash("0x1234000000000000000000000000000000000000000000000000000000ff")

	// Create the MDBX file (normal RW open) with the two reth table shapes.
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
			defaults[rethTabPlainAccount] = kv.TableCfgItem{}
			defaults[rethTabBytecodes] = kv.TableCfgItem{}
			return defaults
		}).
		Open(context.Background())
	require.NoError(t, err)

	rwTx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	require.NoError(t, rwTx.Put(rethTabPlainAccount, addr[:], buildRethAccountValue(codeHash)))
	require.NoError(t, rwTx.Put(rethTabBytecodes, codeHash[:], buildRethBytecodeValue(code)))
	// addrNoCode has an account row but no code hash bit set.
	require.NoError(t, rwTx.Put(rethTabPlainAccount, addrNoCode[:], make([]byte, 2)))
	require.NoError(t, rwTx.Commit())
	db.Close()

	r, err := NewRethCodesReader(dir)
	require.NoError(t, err)
	defer r.Close()

	got, err := r.GetCode(addr)
	require.NoError(t, err)
	require.Equal(t, code, got)

	// Cache hit path: second call must return the same bytes without error.
	got2, err := r.GetCode(addr)
	require.NoError(t, err)
	require.Equal(t, code, got2)

	gotNoCode, err := r.GetCode(addrNoCode)
	require.NoError(t, err)
	require.Nil(t, gotNoCode)

	// Address absent entirely.
	gotAbsent, err := r.GetCode(types.HexToAddress("0x9999999999999999999999999999999999999"))
	require.NoError(t, err)
	require.Nil(t, gotAbsent)
}
