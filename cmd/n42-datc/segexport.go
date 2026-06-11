// n42-datc segexport — measures the static-segment-file potential of the DATC
// tables: streams each table in key order into a prefix-delta-coded flat
// stream, compresses with zstd (fast + max), and runs a hash-zeroed control to
// split the size into "compressible structure" vs "incompressible hash
// entropy" (the theoretical floor).
//
//	n42-datc segexport --out D:/n42-datc-proto [--write-dir D:/n42-datc-seg]
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// segStats aggregates one table's export measurements.
type segStats struct {
	rows                 uint64
	keyBytes, valBytes   uint64
	rawStream            uint64
	zstdFast, zstdMax    uint64
	zstdMaxHashZeroed    uint64
	fullRecs, diffRecs   uint64
	tombstones           uint64
	hashBytes            uint64 // 32B child-hash payload inside values
	streamSample         []byte // first frame retained for ratio sanity (unused beyond len)
}

func runSegExport(args []string) {
	fs := flag.NewFlagSet("segexport", flag.ExitOnError)
	out := fs.String("out", "", "DATC MDBX dir")
	writeDir := fs.String("write-dir", "", "optionally write the zstd-max segments to this dir")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	tables := []struct {
		name   string
		isNode bool
	}{
		{tDatcAccNode, true},
		{tDatcStoNode, true},
		{tDatcAccChg, false},
		{tDatcStoChg, false},
		{tDatcLeafA, false},
		{tDatcLeafS, false},
	}

	fmt.Printf("%-14s %10s %12s %12s %12s %12s %14s\n",
		"table", "rows", "key+val", "raw-stream", "zstd-fast", "zstd-max", "zmax-hash0")
	var totZ, totZ0, totRaw uint64
	for _, t := range tables {
		st, err := exportTable(tx, t.name, t.isNode, *writeDir)
		if err != nil {
			die("%s: %v", t.name, err)
		}
		if st.rows == 0 {
			continue
		}
		fmt.Printf("%-14s %10d %12s %12s %12s %12s %14s\n",
			t.name, st.rows,
			hb(st.keyBytes+st.valBytes), hb(st.rawStream),
			hb(st.zstdFast), hb(st.zstdMax), hb(st.zstdMaxHashZeroed))
		if t.isNode {
			fmt.Printf("               full=%d diff=%d tomb=%d hashPayload=%s (%.0f%% of zstd-max)\n",
				st.fullRecs, st.diffRecs, st.tombstones, hb(st.hashBytes),
				100*float64(st.hashBytes)/float64(max(st.zstdMax, 1)))
		}
		totRaw += st.rawStream
		totZ += st.zstdMax
		totZ0 += st.zstdMaxHashZeroed
	}
	fmt.Printf("\nTOTAL raw-stream=%s  zstd-max=%s  hash-zeroed=%s\n", hb(totRaw), hb(totZ), hb(totZ0))
	fmt.Printf("→ incompressible hash entropy ≈ %s; compressible structure ≈ %s\n",
		hb(totZ-totZ0), hb(totZ0))
}

func hb(n uint64) string { return datasize.ByteSize(n).HumanReadable() }

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// exportTable streams a table in key order into a prefix-delta flat stream:
// varint(shared) varint(suffixLen) suffix varint(valLen) value.
func exportTable(tx kv.Tx, table string, isNode bool, writeDir string) (segStats, error) {
	var st segStats
	c, err := tx.Cursor(table)
	if err != nil {
		return st, err
	}
	defer c.Close()

	var stream, streamZeroed []byte
	var prevKey []byte
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return st, err
		}
		st.rows++
		st.keyBytes += uint64(len(k))
		st.valBytes += uint64(len(v))

		shared := 0
		for shared < len(k) && shared < len(prevKey) && k[shared] == prevKey[shared] {
			shared++
		}
		emit := func(dst []byte, val []byte) []byte {
			dst = binary.AppendUvarint(dst, uint64(shared))
			dst = binary.AppendUvarint(dst, uint64(len(k)-shared))
			dst = append(dst, k[shared:]...)
			dst = binary.AppendUvarint(dst, uint64(len(val)))
			dst = append(dst, val...)
			return dst
		}
		stream = emit(stream, v)
		streamZeroed = emit(streamZeroed, zeroHashes(v, isNode, table))
		prevKey = append(prevKey[:0], k...)

		if isNode {
			switch {
			case len(v) == 0:
				st.tombstones++
			case v[0] == nodeRecFull:
				st.fullRecs++
				_, _, hasHash, hashes, _ := trie.UnmarshalTrieNode(v[1:])
				_ = hasHash
				st.hashBytes += uint64(len(hashes))
			case v[0] == nodeRecDiff:
				st.diffRecs++
				if len(v) > 9 {
					st.hashBytes += uint64(len(v) - 9)
				}
			}
		}
	}
	st.rawStream = uint64(len(stream))

	// zstd fast (level 3-ish default) and max (best compression).
	fast, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	maxw, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	st.zstdFast = uint64(len(fast.EncodeAll(stream, nil)))
	zmax := maxw.EncodeAll(stream, nil)
	st.zstdMax = uint64(len(zmax))
	st.zstdMaxHashZeroed = uint64(len(maxw.EncodeAll(streamZeroed, nil)))

	if writeDir != "" && st.rows > 0 {
		_ = os.MkdirAll(writeDir, 0o755)
		if err := os.WriteFile(filepath.Join(writeDir, table+".seg.zst"), zmax, 0o644); err != nil {
			return st, err
		}
	}
	return st, nil
}

// zeroHashes blanks the 32-byte hash regions of a record so the control
// compression isolates the incompressible entropy.
func zeroHashes(v []byte, isNode bool, table string) []byte {
	if len(v) == 0 {
		return v
	}
	out := append([]byte{}, v...)
	switch {
	case isNode && v[0] == nodeRecFull:
		// MarshalTrieNode: masks(6) then hashes (+ optional rootHash) — zero
		// everything after the masks.
		if len(out) > 7 {
			for i := 7; i < len(out); i++ {
				out[i] = 0
			}
		}
	case isNode && v[0] == nodeRecDiff:
		for i := 9; i < len(out); i++ {
			out[i] = 0
		}
	case table == tDatcLeafA || table == tDatcLeafS:
		// Leaf values: account encodings / slot values — entropy mixed; zero
		// nothing (they are part of the structure side for this experiment).
	}
	return out
}
