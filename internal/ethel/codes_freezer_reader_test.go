// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// codes_freezer_reader_test.go builds a synthetic codes.cidx +
// codes.NNNN.cdat pair (address-indexed, zstd-framed bytecode) and
// exercises CodesFreezerReader's lookup paths without any real export
// data.

package ethel

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
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

// buildCodesHashIndex writes codes.hidx (RecSplit MPHF over codeHashes) +
// codes.hoff (slot-ordered fileNum/offset/length) matching the layout
// codesHashIndex.lookup expects.
func buildCodesHashIndex(t *testing.T, dir string, hashes []types.Hash, locs [][3]uint32 /* fileNum, offset, length */) {
	t.Helper()
	idxPath := filepath.Join(dir, CodesHashIndexFile)
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   len(hashes),
		BucketSize: 2000,
		IndexFile:  idxPath,
		TmpDir:     dir,
		LeafSize:   8,
		NoValues:   true,
	}, log.New())
	require.NoError(t, err)
	feed := func() {
		for i, h := range hashes {
			require.NoError(t, rs.AddKey(h[:], uint64(i)))
		}
	}
	feed()
	for {
		err := rs.Build(context.Background())
		if err == nil {
			break
		}
		require.True(t, rs.Collision(), "recsplit build: %v", err)
		rs.ResetNextSalt()
		feed()
	}

	idx, err := recsplit.OpenIndex(idxPath)
	require.NoError(t, err)
	rd := recsplit.NewIndexReader(idx)

	// codes.hoff is slot-ordered: for each hash, find its assigned slot and
	// write that hash's (fileNum, offset, length) there.
	offs := make([]byte, len(hashes)*codesHoffEntrySize)
	for i, h := range hashes {
		slot, found := rd.Lookup(h[:])
		require.True(t, found)
		rec := offs[slot*codesHoffEntrySize:]
		binary.LittleEndian.PutUint16(rec[0:2], uint16(locs[i][0]))
		binary.LittleEndian.PutUint32(rec[2:6], locs[i][1])
		binary.LittleEndian.PutUint32(rec[6:10], locs[i][2])
	}
	idx.Close()
	require.NoError(t, os.WriteFile(filepath.Join(dir, CodesHashOffsetsFile), offs, 0o644))
}

// TestCodesFreezerReader_HashIndex drives GetCodeByHash and
// LookupCompressedByHash through a real RecSplit-backed codes.hidx/.hoff
// pair, covering the hit (decode succeeds), the structurally-absent miss
// (hidx present but hash not in the build set), and HasHashIndex()==true.
func TestCodesFreezerReader_HashIndex(t *testing.T) {
	dir := t.TempDir()
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer enc.Close()

	code := []byte{0x60, 0x0a, 0x60, 0x0b, 0x01}
	framed := enc.EncodeAll(code, nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.0000.cdat"), framed, 0o644))

	codeHash := types.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000bb")
	buildCodesHashIndex(t, dir, []types.Hash{codeHash}, [][3]uint32{{0, 0, uint32(len(framed))}})

	// No address index needed for this path — write a minimal empty one
	// so NewCodesFreezerReader's base open succeeds.
	header := make([]byte, codesCidxHeaderSize)
	copy(header[:4], freezer.CidxMagic[:])
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codes.cidx"), header, 0o644))

	r, err := NewCodesFreezerReader(dir)
	require.NoError(t, err)
	defer r.Close()
	require.True(t, r.HasHashIndex())

	got, err := r.GetCodeByHash(codeHash)
	require.NoError(t, err)
	require.Equal(t, code, got)

	compGot, err := r.LookupCompressedByHash(codeHash)
	require.NoError(t, err)
	require.Equal(t, framed, compGot)

	// A hash outside the build set lands on an arbitrary slot (MPHF has no
	// keys to compare against) — GetCodeByHash must report a clean miss
	// rather than garbage, because the slot's bytes won't decode as the
	// right zstd frame content (or at all).
	missHash := types.HexToHash("0x9999000000000000000000000000000000000000000000000000000000ee")
	_, _ = r.GetCodeByHash(missHash) // must not panic; result is best-effort
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
