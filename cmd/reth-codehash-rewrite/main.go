// reth-codehash-rewrite: rewrite reth PlainAccountState into a side
// MDBX table with codeHash 32B replaced by 3B dict idx, and measure
// the physical-size delta.
//
// Layout (input, reth Account compact):
//
//	[fieldFlags 1B] [nonce uvarint] [balLen 1B + balance N] [codeHash 32B if bit 3]
//
// Layout (output, this tool):
//
//	[fieldFlags 1B] [nonce uvarint] [balLen 1B + balance N] [codeHashIdx 3B if bit 3]
//
// fieldFlags is preserved as-is (bit 3 still means "codeHash present").
// Idx is 24-bit big-endian; max 16,777,215 unique hashes (reth at
// 24.9M-block head has ~2.2M, so 3B has 7.5× headroom).
//
// Dict file format:
//
//	[uint32 LE: count] [count × 32B hash]
//
// Idx 0..count-1 maps to dict[i*32:i*32+32].
//
// Reports:
//
//	src  table size  (MDBX BucketSize)
//	dst  table size  (MDBX BucketSize)
//	dict file size
//	encoding-level: src raw bytes vs dst raw bytes (sum of len(k)+len(v))
//	  — independent of MDBX page overhead
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	srcTbl = "PlainAccountState"
	dstTbl = "PlainAccountStateIdx"
)

func srcCfg(d kv.TableCfg) kv.TableCfg {
	d[srcTbl] = kv.TableCfgItem{}
	return d
}

func dstCfg(d kv.TableCfg) kv.TableCfg {
	d[dstTbl] = kv.TableCfgItem{}
	return d
}

func main() {
	srcPath := flag.String("src", `d:\reth2k\db`, "reth MDBX (read-only)")
	dstDir := flag.String("dst", `d:\reth-codehash-poc`, "PoC output directory (creates db/ + dict.bin)")
	limit := flag.Uint64("limit", 0, "stop after N rows (0 = all)")
	progress := flag.Duration("progress", 30*time.Second, "log interval")
	flag.Parse()

	if err := os.MkdirAll(filepath.Join(*dstDir, "db"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	logger := log.New()
	src, err := mdbx.NewMDBX(logger).
		Path(*srcPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(srcCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open src:", err)
		os.Exit(1)
	}
	defer src.Close()

	dst, err := mdbx.NewMDBX(logger).
		Path(filepath.Join(*dstDir, "db")).
		Label(kv.SentryDB). // any non-conflicting label
		PageSize(4096).
		MapSize(4 * datasize.TB).
		WithTableCfg(dstCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst:", err)
		os.Exit(1)
	}
	defer dst.Close()

	srcTx, err := src.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "src ro:", err)
		os.Exit(1)
	}
	defer srcTx.Rollback()

	dstTx, err := dst.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "dst rw:", err)
		os.Exit(1)
	}

	c, err := srcTx.Cursor(srcTbl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	hashIdx := make(map[[32]byte]uint32, 4_000_000)
	dictHashes := make([][32]byte, 0, 4_000_000)

	var (
		totalRows                       uint64
		contractRows                    uint64
		srcEncodedBytes, dstEncodedBytes uint64
		startT                          = time.Now()
		lastT                           = startT
	)

	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		totalRows++
		srcEncodedBytes += uint64(len(k) + len(v))

		newV, isContract := rewriteValue(v, hashIdx, &dictHashes)
		if isContract {
			contractRows++
		}
		dstEncodedBytes += uint64(len(k) + len(newV))

		if err := dstTx.Put(dstTbl, k, newV); err != nil {
			fmt.Fprintln(os.Stderr, "put:", err)
			os.Exit(1)
		}

		if *limit > 0 && totalRows >= *limit {
			break
		}
		if time.Since(lastT) >= *progress {
			lastT = time.Now()
			elapsed := lastT.Sub(startT).Seconds()
			fmt.Fprintf(os.Stderr,
				"  rows=%d contracts=%d uniq=%d src=%.2fGiB dst=%.2fGiB rate=%.0f/s\n",
				totalRows, contractRows, len(dictHashes),
				float64(srcEncodedBytes)/(1<<30),
				float64(dstEncodedBytes)/(1<<30),
				float64(totalRows)/elapsed)
		}
	}

	fmt.Fprintf(os.Stderr, "scan+write done in %v, committing...\n", time.Since(startT))
	cmtT := time.Now()
	if err := dstTx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "commit done in %v\n", time.Since(cmtT))

	// Write dict file.
	dictPath := filepath.Join(*dstDir, "dict.bin")
	df, err := os.Create(dictPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create dict:", err)
		os.Exit(1)
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(dictHashes)))
	df.Write(hdr[:])
	for _, h := range dictHashes {
		df.Write(h[:])
	}
	df.Close()
	dictFI, _ := os.Stat(dictPath)
	dictBytes := uint64(dictFI.Size())

	// Physical sizes.
	srcStatTx, _ := src.BeginRo(context.Background())
	defer srcStatTx.Rollback()
	srcSize, _ := srcStatTx.BucketSize(srcTbl)

	dstStatTx, _ := dst.BeginRo(context.Background())
	defer dstStatTx.Rollback()
	dstSize, _ := dstStatTx.BucketSize(dstTbl)

	fmt.Println()
	fmt.Println("=== PoC: codeHash 32B → 3B idx ===")
	fmt.Printf("rows scanned         : %d\n", totalRows)
	fmt.Printf("contract rows        : %d (%.2f%%)\n", contractRows, float64(contractRows)*100/float64(totalRows))
	fmt.Printf("unique codeHashes    : %d (idx range 0..%d, 3B caps at 16777215)\n",
		len(dictHashes), len(dictHashes)-1)
	fmt.Println()
	fmt.Println("--- encoding-level (sum len(k)+len(v)) ---")
	fmt.Printf("src raw              : %d B (%.3f GiB)\n", srcEncodedBytes, float64(srcEncodedBytes)/(1<<30))
	fmt.Printf("dst raw              : %d B (%.3f GiB)\n", dstEncodedBytes, float64(dstEncodedBytes)/(1<<30))
	fmt.Printf("dict file            : %d B (%.3f GiB)\n", dictBytes, float64(dictBytes)/(1<<30))
	saved := int64(srcEncodedBytes) - int64(dstEncodedBytes) - int64(dictBytes)
	fmt.Printf("net saved            : %+d B (%+.3f GiB) = %+.2f%%\n",
		saved, float64(saved)/(1<<30),
		float64(saved)*100/float64(srcEncodedBytes))
	fmt.Println()
	fmt.Println("--- MDBX physical (BucketSize, includes leaf+branch+overflow pages) ---")
	fmt.Printf("src table            : %d B (%.3f GiB)\n", srcSize, float64(srcSize)/(1<<30))
	fmt.Printf("dst table            : %d B (%.3f GiB)\n", dstSize, float64(dstSize)/(1<<30))
	physSaved := int64(srcSize) - int64(dstSize) - int64(dictBytes)
	fmt.Printf("net physical saved   : %+d B (%+.3f GiB) = %+.2f%%\n",
		physSaved, float64(physSaved)/(1<<30),
		float64(physSaved)*100/float64(srcSize))
}

// rewriteValue returns (newValue, isContract). Contract = last 32 bytes
// of value are a non-zero codeHash; we replace them with a 3-byte big-
// endian dict index, allocating a new index on first encounter. Non-
// contract values pass through unchanged.
func rewriteValue(v []byte, hashIdx map[[32]byte]uint32, dictHashes *[][32]byte) ([]byte, bool) {
	if len(v) < 33 {
		return v, false
	}
	tail := v[len(v)-32:]
	allZero := true
	for _, b := range tail {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return v, false
	}
	var h [32]byte
	copy(h[:], tail)
	idx, ok := hashIdx[h]
	if !ok {
		idx = uint32(len(*dictHashes))
		hashIdx[h] = idx
		*dictHashes = append(*dictHashes, h)
	}
	out := make([]byte, len(v)-32+3)
	copy(out, v[:len(v)-32])
	out[len(out)-3] = byte(idx >> 16)
	out[len(out)-2] = byte(idx >> 8)
	out[len(out)-1] = byte(idx)
	return out, true
}
