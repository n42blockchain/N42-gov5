// code-import2fz exports the Reth/Erigon MDBX Code table to freezer format.
//
// cidx uses address as the key (not sequential index). The cidx header
// has a custom type byte indicating "address-indexed" layout.
//
// Each code entry in cdat is individually zstd max-compressed.
//
// cidx layout:
//
//	[16B header: "NCIX" + ver=1 + flags=0x09 (compressed|addrIndexed) + batch=0 + entry=26]
//	[entry₀: address:20B + fileNum:2B BE + offset:4B BE]
//	[entry₁: ...]
//	...
//	Entries sorted by address for binary search lookup.
//
// cdat layout:
//
//	[zstd(code₀)][zstd(code₁)]...
//	Each code independently compressed. Retrieve by reading
//	[offset_i, offset_{i+1}) from the cdat file.
//
// Usage:
//
//	code-import2fz --db d:\reth2k\db --outdir D:\output
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Format identity (magic / address-indexed flag / per-entry width) lives
// in modules/rawdb/freezer so the reader (codes_freezer_reader.go) and
// this writer share a single source of truth.
const (
	cidxHeaderSize     = 16
	cidxVersion        = 1
	cidxFlagCompressed = 0x01
	cidxFlagAddrIndex  = freezer.CidxFlagAddrIndex
	addrEntrySize      = freezer.CidxAddrEntrySize
	maxFileSize        = 2_000_000_000 // 2GB per cdat file
)

var cidxMagic = string(freezer.CidxMagic[:])

// rethRawCode extracts the original deployed bytecode from a reth Compact-encoded
// revm Bytecode value: [u32 BE padded_len][padded bytecode][0x02][u64 BE
// original_len][jumptable]. Returns padded[:original_len] (the code keccak256
// hashes to its codeHash), or padded[:padded_len] when there is no trailer
// (LegacyRaw), or nil if the value is malformed.
func rethRawCode(v []byte) []byte {
	if len(v) < 4 {
		return nil
	}
	pl := int(uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3]))
	if pl < 0 || 4+pl > len(v) {
		return nil
	}
	if len(v) >= 4+pl+9 { // [variant u8][u64 BE original_len]
		ol := int(uint64(v[4+pl+1])<<56 | uint64(v[4+pl+2])<<48 | uint64(v[4+pl+3])<<40 |
			uint64(v[4+pl+4])<<32 | uint64(v[4+pl+5])<<24 | uint64(v[4+pl+6])<<16 |
			uint64(v[4+pl+7])<<8 | uint64(v[4+pl+8]))
		if ol >= 0 && ol <= pl {
			return v[4 : 4+ol]
		}
	}
	return v[4 : 4+pl]
}

func main() {
	if len(os.Args) < 5 || os.Args[1] != "--db" || os.Args[3] != "--outdir" {
		fmt.Fprintln(os.Stderr, "usage: code-import2fz --db RETH_MDBX_PATH --outdir OUTPUT_DIR")
		os.Exit(1)
	}
	dbPath := os.Args[2]
	outdir := os.Args[4]

	// Optional --coverage-block N: the height of the source state this code store
	// is built from. Recorded to codes.coverage so a verifier can check the store
	// covers a block before trusting it (content-addressed code has no per-entry
	// height; this single value is the store's coverage boundary).
	var coverageBlock uint64
	for i := 5; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--coverage-block" {
			coverageBlock, _ = strconv.ParseUint(os.Args[i+1], 10, 64)
		}
	}

	if err := os.MkdirAll(outdir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		Accede().
		Readonly().
		WithTableCfg(func(defaults kv.TableCfg) kv.TableCfg {
			defaults["PlainCodeHash"] = kv.TableCfgItem{}
			defaults["Code"] = kv.TableCfgItem{}
			defaults["Bytecodes"] = kv.TableCfgItem{}
			return defaults
		}).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Phase 1: read all (address, code) pairs.
	type codeEntry struct {
		addr [20]byte
		code []byte
	}

	t0 := time.Now()
	fmt.Fprintf(os.Stderr, "Reading Code table...\n")

	// Pick the table that actually holds data. Both "Bytecodes" (Reth) and "Code"
	// (N42/Erigon) are declared in WithTableCfg, so Cursor() succeeds on either even
	// when empty — selecting purely by open-success would wrongly use an empty
	// "Bytecodes" on an N42 schema and export 0 codes. Probe First() and prefer the
	// non-empty one; "Bytecodes" wins ties (its values need reth-decode).
	tableName := ""
	var cursor kv.Cursor
	for _, name := range []string{"Bytecodes", "Code"} {
		c, cerr := tx.Cursor(name)
		if cerr != nil {
			continue
		}
		k, _, _ := c.First()
		if k != nil {
			tableName = name
			cursor = c
			break
		}
		c.Close()
	}
	if cursor == nil {
		fmt.Fprintln(os.Stderr, "no non-empty Bytecodes/Code table found")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Using table: %s\n", tableName)

	var entries []codeEntry
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iterate:", err)
			os.Exit(1)
		}
		if len(v) == 0 {
			continue
		}
		var a [20]byte
		if len(k) >= 20 {
			copy(a[:], k[:20])
		} else {
			copy(a[:], k)
		}
		// Reth's "Bytecodes" table stores Compact-encoded revm Bytecode, NOT raw
		// bytecode: [u32 BE padded_len][padded bytecode][0x02][u64 BE original_len]
		// [jumptable]. keccak256(whole value) != codeHash; the DEPLOYED code is
		// padded_bytecode[:original_len]. (LegacyAnalyzed pads the analyzed bytecode;
		// original_len recovers the real length — verified keccak==key on 5000/5000
		// sampled entries.) The N42 "Code" table stores raw bytecode, no decode.
		raw := v
		if tableName == "Bytecodes" {
			raw = rethRawCode(v)
			if raw == nil {
				fmt.Fprintf(os.Stderr, "  WARN undecodable reth bytecode (valLen=%d) for key %x — skipping\n", len(v), k[:min(len(k), 8)])
				continue
			}
		}
		code := make([]byte, len(raw))
		copy(code, raw)
		entries = append(entries, codeEntry{addr: a, code: code})
		if len(entries)%100000 == 0 {
			fmt.Fprintf(os.Stderr, "  read %d  key_len=%d\n", len(entries), len(k))
		}
	}
	cursor.Close()
	fmt.Fprintf(os.Stderr, "Read %d codes in %v\n", len(entries), time.Since(t0).Truncate(time.Millisecond))

	if len(entries) > 0 {
		fmt.Fprintf(os.Stderr, "  first key (hex): %x\n", entries[0].addr)
	}

	// Sort by address (binary order) for deterministic cidx + binary search.
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].addr[:], entries[j].addr[:]) < 0
	})

	// Phase 2: write cdat files (per-entry zstd max compression).
	fmt.Fprintf(os.Stderr, "Compressing + writing cdat...\n")
	t1 := time.Now()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		fmt.Fprintln(os.Stderr, "zstd encoder:", err)
		os.Exit(1)
	}
	defer enc.Close()

	type indexEntry struct {
		addr    [20]byte
		fileNum uint16
		offset  uint32
	}
	index := make([]indexEntry, len(entries))

	var (
		curFileNum uint16
		curOffset  int64
		curFile    *os.File
	)

	openDataFile := func(num uint16) error {
		if curFile != nil {
			curFile.Close()
		}
		name := filepath.Join(outdir, fmt.Sprintf("codes.%04d.cdat", num))
		f, err := os.Create(name)
		if err != nil {
			return err
		}
		curFile = f
		curFileNum = num
		curOffset = 0
		return nil
	}

	if err := openDataFile(0); err != nil {
		fmt.Fprintln(os.Stderr, "create cdat:", err)
		os.Exit(1)
	}

	totalRaw, totalComp := 0, 0
	for i, e := range entries {
		compressed := enc.EncodeAll(e.code, make([]byte, 0, len(e.code)/2))

		// Rotate file if needed.
		if curOffset+int64(len(compressed)) > maxFileSize && curOffset > 0 {
			if err := openDataFile(curFileNum + 1); err != nil {
				fmt.Fprintln(os.Stderr, "rotate cdat:", err)
				os.Exit(1)
			}
		}

		index[i] = indexEntry{
			addr:    e.addr,
			fileNum: curFileNum,
			offset:  uint32(curOffset),
		}

		if _, err := curFile.Write(compressed); err != nil {
			fmt.Fprintln(os.Stderr, "write cdat:", err)
			os.Exit(1)
		}
		curOffset += int64(len(compressed))
		totalRaw += len(e.code)
		totalComp += len(compressed)

		if (i+1)%100000 == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d (%.1f%%)\n", i+1, len(entries),
				float64(totalComp)/float64(totalRaw)*100)
		}
	}
	if curFile != nil {
		curFile.Close()
	}

	// Phase 3: write cidx (address-indexed).
	cidxPath := filepath.Join(outdir, "codes.cidx")
	cidxFile, err := os.Create(cidxPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create cidx:", err)
		os.Exit(1)
	}

	// Header: 16 bytes.
	var hdr [cidxHeaderSize]byte
	copy(hdr[0:4], cidxMagic)
	hdr[4] = cidxVersion
	hdr[5] = cidxFlagCompressed | cidxFlagAddrIndex
	hdr[6] = 0 // batchSize=0 (not batch mode)
	hdr[7] = addrEntrySize
	// bytes [8:16] reserved
	cidxFile.Write(hdr[:])

	// Entries: sorted by address.
	for _, ie := range index {
		var entry [addrEntrySize]byte
		copy(entry[0:20], ie.addr[:])
		binary.BigEndian.PutUint16(entry[20:22], ie.fileNum)
		binary.BigEndian.PutUint32(entry[22:26], ie.offset)
		cidxFile.Write(entry[:])
	}
	cidxFile.Close()

	elapsed := time.Since(t1).Truncate(time.Millisecond)
	if totalRaw > 0 {
		ratio := float64(totalComp) / float64(totalRaw) * 100
		fmt.Printf("Done: %d codes\n", len(entries))
		fmt.Printf("  raw:        %d bytes (%.1f MB)\n", totalRaw, float64(totalRaw)/1e6)
		fmt.Printf("  compressed: %d bytes (%.1f MB, %.1f%%)\n", totalComp, float64(totalComp)/1e6, ratio)
	} else {
		fmt.Printf("Done: %d codes (no data)\n", len(entries))
	}
	fmt.Printf("  cidx:       %d bytes (%d entries × %d)\n",
		cidxHeaderSize+len(index)*addrEntrySize, len(index), addrEntrySize)
	fmt.Printf("  elapsed:    %v\n", elapsed)
	fmt.Printf("  output:     %s/codes.cidx + codes.*.cdat\n", outdir)
	if coverageBlock > 0 {
		if err := freezer.WriteCodesCoverage(outdir, coverageBlock); err != nil {
			fmt.Fprintln(os.Stderr, "write coverage:", err)
		} else {
			fmt.Printf("  coverage:   block %d (codes.coverage)\n", coverageBlock)
		}
	} else {
		fmt.Printf("  coverage:   NOT recorded (pass --coverage-block N = source state height)\n")
	}
}
