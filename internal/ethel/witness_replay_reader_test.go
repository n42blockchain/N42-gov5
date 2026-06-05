// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_reader_test.go — regression guards for the tiered code source:
// the cold codes-freezer is a snapshot at its coverage height, so a contract
// deployed/redeployed past that height must NOT fail loud on a keccak mismatch
// — the reader falls through to the hot tier (codeFetch / MDBX). This was the
// Phase E out-of-range failure: the verifier path was stricter than the
// full-node readers and rejected legit post-coverage codes.

package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// writeTinyCodesFreezer writes a one-file address-indexed codes-freezer
// (codes.cidx + codes.0000.cdat) mirroring cmd/code-import2fz, plus the
// codes.coverage sidecar. entries maps address → raw bytecode.
func writeTinyCodesFreezer(t *testing.T, dir string, entries map[types.Address][]byte, coverage uint64) {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	// Sort addresses ascending (binary order) — the reader binary-searches.
	addrs := make([]types.Address, 0, len(entries))
	for a := range entries {
		addrs = append(addrs, a)
	}
	for i := 0; i < len(addrs); i++ {
		for j := i + 1; j < len(addrs); j++ {
			less := false
			for b := 0; b < 20; b++ {
				if addrs[j][b] != addrs[i][b] {
					less = addrs[j][b] < addrs[i][b]
					break
				}
			}
			if less {
				addrs[i], addrs[j] = addrs[j], addrs[i]
			}
		}
	}

	var cdat []byte
	type ie struct {
		addr   types.Address
		offset uint32
	}
	var index []ie
	for _, a := range addrs {
		index = append(index, ie{addr: a, offset: uint32(len(cdat))})
		cdat = append(cdat, enc.EncodeAll(entries[a], nil)...)
	}
	if err := os.WriteFile(filepath.Join(dir, "codes.0000.cdat"), cdat, 0o644); err != nil {
		t.Fatal(err)
	}

	hdr := make([]byte, 16)
	copy(hdr[0:4], freezer.CidxMagic[:])
	hdr[4] = 1                                  // version
	hdr[5] = 0x01 | freezer.CidxFlagAddrIndex   // compressed | addr-indexed
	cidx := hdr
	for _, e := range index {
		entry := make([]byte, freezer.CidxAddrEntrySize)
		copy(entry[0:20], e.addr[:])
		binary.BigEndian.PutUint16(entry[20:22], 0) // fileNum 0
		binary.BigEndian.PutUint32(entry[22:26], e.offset)
		cidx = append(cidx, entry...)
	}
	if err := os.WriteFile(filepath.Join(dir, "codes.cidx"), cidx, 0o644); err != nil {
		t.Fatal(err)
	}
	if coverage > 0 {
		if err := freezer.WriteCodesCoverage(dir, coverage); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCodesFreezerCoverageRoundTrip: the sidecar height written by the builder
// is read back by the reader, and a missing sidecar reports (0,false).
func TestCodesFreezerCoverageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	addr := types.Address{0xaa}
	writeTinyCodesFreezer(t, dir, map[types.Address][]byte{addr: {0x60, 0x00}}, 25208529)

	r, err := NewCodesFreezerReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got, ok := r.Coverage(); !ok || got != 25208529 {
		t.Fatalf("Coverage() = (%d,%v), want (25208529,true)", got, ok)
	}
	if r.ContractCount() != 1 {
		t.Fatalf("ContractCount() = %d, want 1", r.ContractCount())
	}

	// No-sidecar dir → (0,false).
	if _, ok := freezer.ReadCodesCoverage(t.TempDir()); ok {
		t.Fatal("ReadCodesCoverage on empty dir should report absent")
	}
}

// TestWitnessReplayCodeMismatchFallsThrough: the freezer holds OLD bytecode for
// an address (a redeploy / post-coverage deployment), so its keccak != the
// requested codeHash. The reader must fall through to the hot tier (codeFetch
// here) and return the correct current code — NOT fail loud.
func TestWitnessReplayCodeMismatchFallsThrough(t *testing.T) {
	prev := GlobalBytecodeCache
	GlobalBytecodeCache = nil // isolate from process-wide cache
	defer func() { GlobalBytecodeCache = prev }()

	dir := t.TempDir()
	addr := types.Address{0xbe, 0xef}
	oldCode := []byte{0x60, 0x01, 0x60, 0x02} // what the snapshot froze
	newCode := []byte{0x60, 0x0a, 0x60, 0x0b} // what the contract holds now
	writeTinyCodesFreezer(t, dir, map[types.Address][]byte{addr: oldCode}, 24000000)

	codes, err := NewCodesFreezerReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer codes.Close()

	r := NewWitnessReplayReader(nil, nil)
	r.SetCodesFreezer(codes)
	if cov, ok := codes.Coverage(); ok {
		r.SetFreezerCoverage(cov)
	}
	fetched := 0
	newHash := crypto.Keccak256Hash(newCode)
	r.SetCodeFetcher(func(h types.Hash) ([]byte, error) {
		fetched++
		if h == newHash {
			return newCode, nil
		}
		return nil, nil
	})

	got, err := r.ReadAccountCode(addr, newHash)
	if err != nil {
		t.Fatalf("ReadAccountCode fell loud on a post-coverage code: %v", err)
	}
	if string(got) != string(newCode) {
		t.Fatalf("got %x, want hot-tier code %x", got, newCode)
	}
	if fetched != 1 {
		t.Fatalf("codeFetch called %d times, want 1 (mismatch must fall through)", fetched)
	}

	// Sanity: a matching freezer entry is still served from the cold tier
	// without touching the fetcher.
	fetched = 0
	oldHash := crypto.Keccak256Hash(oldCode)
	got, err = r.ReadAccountCode(addr, oldHash)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(oldCode) {
		t.Fatalf("cold-tier hit returned %x, want %x", got, oldCode)
	}
	if fetched != 0 {
		t.Fatalf("cold-tier hit should not call codeFetch, called %d", fetched)
	}
}
