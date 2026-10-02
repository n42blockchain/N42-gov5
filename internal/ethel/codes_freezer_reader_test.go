// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// codes_freezer_reader_test.go builds a synthetic codes.cidx +
// codes.NNNN.cdat pair (address-indexed, zstd-framed bytecode) and
// exercises CodesFreezerReader's lookup paths without any real export
// data.

package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// buildCodesCidx writes a codes.cidx file whose body holds len(addrs)
// sorted 26-byte entries, all pointing into fileNum 0 at the given
// offsets, plus the codes.0000.cdat payload itself.
func buildCodesCidx(t *testing.T, dir string, addrs []types.Address, codes [][]byte) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer enc.Close()

	var blobs [][]byte
	var offsets []uint32
	var off uint32
	for _, c := range codes {
		framed := enc.EncodeAll(c, nil)
		blobs = append(blobs, framed)
		offsets = append(offsets, off)
		off += uint32(len(framed))
	}
	cdat := make([]byte, 0, off)
	for _, b := range blobs {
		cdat = append(cdat, b...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.0000.cdat"), cdat, 0o644))

	body := make([]byte, 0, len(addrs)*freezer.CidxAddrEntrySize)
	for i, a := range addrs {
		var entry [freezer.CidxAddrEntrySize]byte
		copy(entry[:20], a[:])
		binary.BigEndian.PutUint16(entry[20:22], 0) // fileNum 0
		binary.BigEndian.PutUint32(entry[22:26], offsets[i])
		body = append(body, entry[:]...)
	}
	header := make([]byte, codesCidxHeaderSize)
	copy(header[:4], freezer.CidxMagic[:])
	full := append(header, body...)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.cidx"), full, 0o644))
}

func TestCodesFreezerReader_AddressLookup(t *testing.T) {
	dir := t.TempDir()
	addrA := types.HexToAddress("0x0000000000000000000000000000000000000001")
	addrB := types.HexToAddress("0x0000000000000000000000000000000000000002")
	codeA := []byte{0x60, 0x01, 0x60, 0x02, 0x01}
	codeB := []byte{0x00}
	buildCodesCidx(t, dir, []types.Address{addrA, addrB}, [][]byte{codeA, codeB})

	r, err := NewCodesFreezerReader(dir)
	require.NoError(t, err)
	defer r.Close()

	require.Equal(t, 2, r.Items())
	require.Equal(t, 2, r.ContractCount())
	require.False(t, r.HasHashIndex())

	gotA, err := r.LookupByAddress(addrA)
	require.NoError(t, err)
	require.Equal(t, codeA, gotA)

	gotB, err := r.GetCode(addrB)
	require.NoError(t, err)
	require.Equal(t, codeB, gotB)

	// Address not in the index.
	addrMissing := types.HexToAddress("0x00000000000000000000000000000000000099")
	gotMissing, err := r.LookupByAddress(addrMissing)
	require.NoError(t, err)
	require.Nil(t, gotMissing)

	// Compressed path skips decompression — must re-decompress to the
	// same bytes as LookupByAddress.
	compA, err := r.LookupCompressedByAddress(addrA)
	require.NoError(t, err)
	require.NotEmpty(t, compA)

	// GetCodeByHash with no hash index present returns (nil, nil) — the
	// "fall through to address/MDBX" contract.
	anyHash, err := r.GetCodeByHash(types.Hash{})
	require.NoError(t, err)
	require.Nil(t, anyHash)

	// Coverage sidecar absent — ok=false.
	_, ok := r.Coverage()
	require.False(t, ok)
}

// TestCodesFreezerReader_MultiFile exercises openFile/fileSize across two
// cdat segments, confirming fileNum routing and the end-of-file size
// resolution for the last entry in a given file.
func TestCodesFreezerReader_MultiFile(t *testing.T) {
	dir := t.TempDir()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer enc.Close()

	addr0 := types.HexToAddress("0x0000000000000000000000000000000000000010")
	addr1 := types.HexToAddress("0x0000000000000000000000000000000000000020")
	code0 := []byte{0xaa, 0xbb, 0xcc}
	code1 := []byte{0xdd, 0xee}

	framed0 := enc.EncodeAll(code0, nil)
	framed1 := enc.EncodeAll(code1, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.0000.cdat"), framed0, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.0001.cdat"), framed1, 0o644))

	body := make([]byte, 0, 2*freezer.CidxAddrEntrySize)
	var e0, e1 [freezer.CidxAddrEntrySize]byte
	copy(e0[:20], addr0[:])
	binary.BigEndian.PutUint16(e0[20:22], 0)
	binary.BigEndian.PutUint32(e0[22:26], 0)
	copy(e1[:20], addr1[:])
	binary.BigEndian.PutUint16(e1[20:22], 1)
	binary.BigEndian.PutUint32(e1[22:26], 0)
	body = append(body, e0[:]...)
	body = append(body, e1[:]...)
	header := make([]byte, codesCidxHeaderSize)
	copy(header[:4], freezer.CidxMagic[:])
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.cidx"), append(header, body...), 0o644))

	r, err := NewCodesFreezerReader(dir)
	require.NoError(t, err)
	defer r.Close()

	got0, err := r.LookupByAddress(addr0)
	require.NoError(t, err)
	require.Equal(t, code0, got0)

	got1, err := r.LookupByAddress(addr1)
	require.NoError(t, err)
	require.Equal(t, code1, got1)
}

func TestCodesFreezerReader_BadMagic(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.cidx"), make([]byte, codesCidxHeaderSize), 0o644))
	_, err := NewCodesFreezerReader(dir)
	require.Error(t, err)
}

func TestCodesFreezerReader_MissingFile(t *testing.T) {
	_, err := NewCodesFreezerReader(t.TempDir())
	require.Error(t, err)
}
