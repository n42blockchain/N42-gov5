// n42-history-build: transpose N42 acctcs.cdat / storcs.cdat
// (block-major changesets) into per-key history files for the cold
// tier.
//
// Pipeline (per domain):
//
//	Phase 1  stream CS freezer block-by-block
//	         → for each (key, oldValue): ETL.Collect(key, blockBE || valueLen || value)
//	         ETL handles disk spill + merge sort.
//	Phase 2  ETL.Load gives entries sorted by key (then by blockBE inside collected value).
//	         Group by key, build packed history blob, append to history.Writer.
//
// Output (per domain):
//   <out>/<domain>.kv  + <out>/<domain>.idx
//
// Design: docs/ethel/history-build-v1-design.md
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/lib/etl"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

var logger = log.New()

func main() {
	frDir := flag.String("freezer", "", "freezer dir containing acctcs.cidx / storcs.cidx")
	outDir := flag.String("out", "", "output dir for history files")
	domain := flag.String("domain", "both", "account / storage / both")
	startBlk := flag.Uint64("start", 0, "starting block (inclusive)")
	endBlk := flag.Uint64("end", 0, "ending block (exclusive; 0 = head)")
	tmpDir := flag.String("tmpdir", "", "ETL spill dir (default: <out>/etl)")
	etlBufMB := flag.Uint64("etl-buf-mb", 4096, "ETL buffer size (MB)")
	flag.Parse()

	if *frDir == "" || *outDir == "" {
		fmt.Fprintf(os.Stderr, "usage: n42-history-build --freezer <dir> --out <dir> [--domain X] [--start B] [--end B]\n")
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0755); err != nil {
		fatal("mkdir out: %v", err)
	}
	if *tmpDir == "" {
		*tmpDir = *outDir + "/etl"
	}
	if err := os.MkdirAll(*tmpDir, 0755); err != nil {
		fatal("mkdir tmpdir: %v", err)
	}

	fr, err := freezer.NewReadOnly(*frDir)
	must(err, "open freezer")
	defer fr.Close()

	switch *domain {
	case "account", "accounts":
		buildHistory(fr, *outDir, *tmpDir, "account", freezer.TableAccountChanges, 20, *startBlk, *endBlk, *etlBufMB)
	case "storage":
		buildHistory(fr, *outDir, *tmpDir, "storage", freezer.TableStorageChanges, 52, *startBlk, *endBlk, *etlBufMB)
	case "both", "":
		buildHistory(fr, *outDir, *tmpDir, "account", freezer.TableAccountChanges, 20, *startBlk, *endBlk, *etlBufMB)
		buildHistory(fr, *outDir, *tmpDir, "storage", freezer.TableStorageChanges, 52, *startBlk, *endBlk, *etlBufMB)
	default:
		fatal("unknown domain: %s", *domain)
	}
}

func buildHistory(fr *freezer.Freezer, outDir, tmpDir, prefix, tableName string, keyLen int, startBlk, endBlk uint64, etlBufMB uint64) {
	fmt.Printf("\n=== %s history (table=%s, keyLen=%d) ===\n", prefix, tableName, keyLen)
	t0 := time.Now()

	tbl, err := fr.EnsureTableCompressed(tableName, "c")
	must(err, "open table "+tableName)
	maxItems := tbl.Items()
	if endBlk == 0 || endBlk > maxItems {
		endBlk = maxItems
	}
	fmt.Printf("  range: [%d, %d)  table.Items=%d\n", startBlk, endBlk, maxItems)

	// Phase 1: stream CS, ETL.Collect(key, blockBE+valueLen+value)
	fmt.Printf("  [phase 1] streaming CS → ETL collector...\n")
	t1 := time.Now()

	bufSize := datasize.ByteSize(etlBufMB) * datasize.MB
	coll := etl.NewCollector(prefix+"-history", tmpDir,
		etl.NewSortableBuffer(bufSize), logger)
	defer coll.Close()

	var emitted, blocksProcessed uint64
	scratch := make([]byte, 0, 64)

	for blk := startBlk; blk < endBlk; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil {
			continue
		}
		if len(data) == 0 {
			continue
		}
		blocksProcessed++

		switch prefix {
		case "account":
			changes, err := ethel.DecodeAccountChanges(data)
			if err != nil {
				continue
			}
			for _, c := range changes {
				scratch = encodeEntry(scratch[:0], blk, c.OldValue)
				if err := coll.Collect(c.Address[:], scratch); err != nil {
					fatal("ETL.Collect acct at blk %d: %v", blk, err)
				}
				emitted++
			}
		case "storage":
			changes, err := ethel.DecodeStorageChanges(data)
			if err != nil {
				continue
			}
			for _, c := range changes {
				scratch = encodeEntry(scratch[:0], blk, c.OldValue)
				if err := coll.Collect(c.CompositeKey, scratch); err != nil {
					fatal("ETL.Collect stor at blk %d: %v", blk, err)
				}
				emitted++
			}
		}

		if blk%100_000 == 0 && blk > startBlk {
			elapsed := time.Since(t1).Seconds()
			rate := float64(blk-startBlk) / elapsed
			fmt.Fprintf(os.Stderr, "    blk=%d  blocks=%d  emitted=%d  (%.0f blk/s)\n",
				blk, blocksProcessed, emitted, rate)
		}
	}
	fmt.Printf("  phase 1 done: %d emitted entries from %d blocks, %s\n",
		emitted, blocksProcessed, time.Since(t1).Truncate(time.Second))

	// Phase 2: ETL.Load → group by key → coldstore history.Writer
	fmt.Printf("  [phase 2] ETL.Load + group + write coldstore...\n")
	t2 := time.Now()

	w, err := history.NewWriter(outDir, prefix, keyLen, 64)
	must(err, "history.NewWriter")

	var (
		curKey  []byte
		curHist []history.Change
		uniq    uint64
	)
	flush := func() error {
		if curKey == nil {
			return nil
		}
		packed := history.PackHistory(nil, curHist)
		if err := w.Append(curKey, packed); err != nil {
			return fmt.Errorf("Append %x: %w", curKey, err)
		}
		uniq++
		if uniq%1_000_000 == 0 {
			elapsed := time.Since(t2).Seconds()
			fmt.Fprintf(os.Stderr, "    written %dM keys (%.0f keys/s)\n",
				uniq/1_000_000, float64(uniq)/elapsed)
		}
		curKey = curKey[:0]
		curHist = curHist[:0]
		return nil
	}

	err = coll.Load(nil, "", func(k, v []byte, table etl.CurrentTableReader, next etl.LoadNextFunc) error {
		if curKey == nil || !bytes.Equal(curKey, k) {
			if err := flush(); err != nil {
				return err
			}
			curKey = append(curKey[:0], k...)
		}
		block, value, err := decodeEntry(v)
		if err != nil {
			return fmt.Errorf("decodeEntry: %w", err)
		}
		curHist = append(curHist, history.Change{Block: block, Value: append([]byte(nil), value...)})
		return nil
	}, etl.TransformArgs{})
	if err != nil {
		fatal("ETL.Load: %v", err)
	}
	if err := flush(); err != nil {
		fatal("final flush: %v", err)
	}
	if err := w.Close(); err != nil {
		fatal("history.Writer.Close: %v", err)
	}

	stats := w.Stats()
	fmt.Printf("  phase 2 done: %d unique keys, %d pages, %s\n",
		stats.KeyCount, stats.PageCount, time.Since(t2).Truncate(time.Second))

	// Summary
	idxSize := uint64(history.HeaderLen) + stats.PageCount*uint64(keyLen+8)
	kvSize := uint64(history.HeaderLen) + stats.TotalKvSize
	total := idxSize + kvSize
	bytesPerEntry := float64(0)
	if emitted > 0 {
		bytesPerEntry = float64(total) / float64(emitted)
	}
	bytesPerKey := float64(0)
	if stats.KeyCount > 0 {
		bytesPerKey = float64(total) / float64(stats.KeyCount)
	}

	fmt.Printf("\n  SUMMARY (%s history):\n", prefix)
	fmt.Printf("    Source entries  : %d\n", emitted)
	fmt.Printf("    Unique keys     : %d\n", stats.KeyCount)
	fmt.Printf("    .idx size       : %s\n", humanBytes(idxSize))
	fmt.Printf("    .kv size        : %s (compressed)\n", humanBytes(kvSize))
	fmt.Printf("    .val raw        : %s (uncompressed packed)\n", humanBytes(stats.TotalValSize))
	fmt.Printf("    TOTAL on disk   : %s\n", humanBytes(total))
	fmt.Printf("    bytes / entry   : %.2f\n", bytesPerEntry)
	fmt.Printf("    bytes / key     : %.2f\n", bytesPerKey)
	fmt.Printf("    Total time      : %s\n", time.Since(t0).Truncate(time.Second))
}

// encodeEntry packs (block, value) for ETL transit.
// Layout: 8B blockBE || 2B valueLen LE || value
func encodeEntry(dst []byte, block uint64, value []byte) []byte {
	var buf [10]byte
	binary.BigEndian.PutUint64(buf[0:8], block)
	binary.LittleEndian.PutUint16(buf[8:10], uint16(len(value)))
	dst = append(dst, buf[:]...)
	dst = append(dst, value...)
	return dst
}

// decodeEntry unpacks. Returns aliased value slice; copy if needed.
func decodeEntry(data []byte) (block uint64, value []byte, err error) {
	if len(data) < 10 {
		return 0, nil, fmt.Errorf("entry too short: %d B", len(data))
	}
	block = binary.BigEndian.Uint64(data[0:8])
	vlen := binary.LittleEndian.Uint16(data[8:10])
	if 10+int(vlen) > len(data) {
		return 0, nil, fmt.Errorf("entry truncated value")
	}
	return block, data[10 : 10+int(vlen)], nil
}

func humanBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/KB)
	}
	return fmt.Sprintf("%d B", b)
}

func must(err error, what string) {
	if err != nil {
		fatal("%s: %v", what, err)
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

var _ = context.Background // future use
