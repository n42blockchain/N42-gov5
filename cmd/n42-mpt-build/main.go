// n42-mpt-build: Phase A production tool — build a reth-compatible
// MPT into a local MDBX directory.
//
// MDBX (not flat file) chosen because the resulting trie must support
// per-block updates during catch-up sync and 12-second live sync,
// plus MVCC for 256-block reorg-safe diff layers. AppendDup gives us
// the fastest sorted-insert path with ~95% page fill, so initial bulk
// build matches flat-file write throughput while keeping live update
// capability intact.
//
// Heavy lifting lives in internal/mptbuild; this binary is the CLI
// wiring + progress reporting.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

func main() {
	var (
		srcDB     = flag.String("db", `D:\reth2k\db`, "source MDBX dir (readonly)")
		srcTable  = flag.String("table", "PlainAccountState", "source: PlainAccountState | PlainStorageState")
		outDir    = flag.String("out", `D:\n42-mpt`, "output base directory")
		tmpDir    = flag.String("tmp", "", "ETL spill dir (default <out>/etl-tmp)")
		bufMB     = flag.Uint64("etl-buf-mb", 4096, "ETL buffer size MB per collector")
		mapSizeGB = flag.Int("mapsize-gb", 4096, "source DB MapSize cap")
		outMapGB  = flag.Int("out-mapsize-gb", 64, "output DB MapSize cap")
		verify    = flag.String("verify-root", "", "optional expected root hash (hex) to assert")
		maxRows   = flag.Int64("max-rows", 0, "0=full table; else stop after N rows (smoke testing)")
		emitDense   = flag.Bool("emit-dense", false, "ALSO emit Phase G1 dense (V1) form to AccountsDense/StoragesDense")
		emitDenseV2 = flag.Bool("emit-dense-v2", false, "ALSO emit Phase G2 dense V2 (plain-key referencing) form to AccountsDenseV2/StoragesDenseV2 — 85% smaller than V1 on real data")
	)
	flag.Parse()

	if *tmpDir == "" {
		*tmpDir = filepath.Join(*outDir, "etl-tmp")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal("mkdir out: %v", err)
	}
	if err := os.MkdirAll(*tmpDir, 0o755); err != nil {
		fatal("mkdir tmp: %v", err)
	}

	prefix, outTable, extractor := tableMapping(*srcTable)
	dbDir := mptbuild.AbsoluteOutPath(*outDir, prefix)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		fatal("mkdir output mdbx dir: %v", err)
	}

	logger := log.New()
	t0 := time.Now()

	source := &mptbuild.MDBXSource{
		DBPath:    *srcDB,
		Table:     *srcTable,
		MapSizeGB: *mapSizeGB,
		MaxRows:   *maxRows,
		Logger:    logger,
	}
	target := &mptbuild.MDBXTarget{
		DBPath:    dbDir,
		Table:     outTable,
		MapSizeGB: *outMapGB,
		Logger:    logger,
	}
	defer target.Close()

	// Phase G1 / G2 dense ETL collector. Exactly one of V1 / V2 may be
	// active. Stored to the corresponding {Accounts,Storages}Dense or
	// {Accounts,Storages}DenseV2 table in the same env.
	var (
		denseColl  *etl.Collector
		denseTable string
		denseRows  int64
		denseBytes int64
		denseV2    = *emitDenseV2
	)
	if *emitDense && *emitDenseV2 {
		fatal("--emit-dense and --emit-dense-v2 are mutually exclusive")
	}
	if *emitDense || *emitDenseV2 {
		switch outTable {
		case "AccountsTrie":
			if denseV2 {
				denseTable = mpttrie.AccountsDenseV2Table
			} else {
				denseTable = mpttrie.AccountsDenseTable
			}
		case "StoragesTrie":
			if denseV2 {
				denseTable = mpttrie.StoragesDenseV2Table
			} else {
				denseTable = mpttrie.StoragesDenseTable
			}
		default:
			fatal("--emit-dense{,-v2}: unknown output table %s", outTable)
		}
		denseColl = etl.NewCollector(
			"mptbuild-dense-"+outTable,
			*tmpDir,
			etl.NewSortableBuffer(datasize.ByteSize(*bufMB)*datasize.MB),
			logger,
		)
		defer denseColl.Close()
	}

	fmt.Printf("source     %s/%s\n", *srcDB, *srcTable)
	fmt.Printf("target     %s/%s (MDBX AppendDup)\n", dbDir, outTable)
	fmt.Printf("etl tmp    %s  (buf=%d MB)\n", *tmpDir, *bufMB)
	if *maxRows > 0 {
		fmt.Printf("max-rows   %d (smoke)\n", *maxRows)
	}
	fmt.Println()

	lastLog := time.Now()
	opts := mptbuild.Opts{
		Source:    source,
		Target:    target,
		Extractor: extractor,
		TmpDir:    *tmpDir,
		BufMB:     *bufMB,
		Logger:    logger,
		Progress: func(rows int64) {
			if time.Since(lastLog) > 5*time.Second {
				rate := float64(rows) / time.Since(t0).Seconds()
				fmt.Fprintf(os.Stderr, "  pass-1 collected %d rows (%.0f k/s)\n",
					rows, rate/1000)
				lastLog = time.Now()
			}
		},
	}
	if denseColl != nil {
		var encBuf []byte
		opts.DenseBranchSink = func(keyHex []byte, stateMask, treeMask, extMask uint16, slotData []byte) error {
			if denseV2 {
				encBuf = trie.MarshalTrieNodeDenseV2(stateMask, treeMask, extMask, slotData, encBuf[:0])
			} else {
				encBuf = trie.MarshalTrieNodeDense(stateMask, treeMask, slotData, encBuf[:0])
			}
			keyCopy := make([]byte, len(keyHex))
			copy(keyCopy, keyHex)
			valCopy := make([]byte, len(encBuf))
			copy(valCopy, encBuf)
			denseRows++
			denseBytes += int64(len(valCopy))
			return denseColl.Collect(keyCopy, valCopy)
		}
	}
	res, err := mptbuild.Build(context.Background(), opts)
	if err != nil {
		fatal("Build: %v", err)
	}

	// Phase G1 dense write: target must close first to release the
	// MDBX env (single writer), then we reopen with the dense table
	// declared and Load the collected entries.
	if denseColl != nil {
		fmt.Printf("\n=== Phase G1 dense write ===\n")
		target.Close()
		t1 := time.Now()
		denseDB, err := mdbxkv.NewMDBX(logger).
			Path(dbDir).
			Label(kv.ChainDB).
			PageSize(4096).
			MapSize(datasize.ByteSize(*outMapGB) * datasize.GB).
			WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
				d[outTable] = kv.TableCfgItem{}
				d[denseTable] = kv.TableCfgItem{}
				d["Meta"] = kv.TableCfgItem{}
				return d
			}).
			Open(context.Background())
		if err != nil {
			fatal("dense reopen: %v", err)
		}
		denseTx, err := denseDB.BeginRw(context.Background())
		if err != nil {
			denseDB.Close()
			fatal("dense begin: %v", err)
		}
		if err := denseTx.ClearBucket(denseTable); err != nil {
			denseTx.Rollback()
			denseDB.Close()
			fatal("dense clear: %v", err)
		}
		if err := denseColl.Load(denseTx, denseTable, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
			denseTx.Rollback()
			denseDB.Close()
			fatal("dense load: %v", err)
		}
		if err := denseTx.Commit(); err != nil {
			denseDB.Close()
			fatal("dense commit: %v", err)
		}
		denseDB.Close()
		fmt.Printf("  dense rows         %d\n", denseRows)
		fmt.Printf("  dense bytes        %.2f GB\n", float64(denseBytes)/1e9)
		fmt.Printf("  dense write        %s\n", time.Since(t1).Truncate(time.Second))
	}

	rootHex := hex.EncodeToString(res.StateRoot[:])
	fmt.Println()
	fmt.Println("=== n42-mpt-build complete ===")
	fmt.Printf("  source table       %s\n", *srcTable)
	fmt.Printf("  target bucket      %s/%s\n", dbDir, outTable)
	fmt.Printf("  leaves             %d\n", res.Leaves)
	fmt.Printf("  branches           %d\n", res.Branches)
	if res.Leaves > 0 {
		fmt.Printf("  bytes/leaf         %.2f\n", float64(res.BranchBytes)/float64(res.Leaves))
	}
	fmt.Printf("  branch total bytes %.2f GB\n", float64(res.BranchBytes)/1e9)
	used := target.BucketSize()
	if used > 0 {
		fmt.Printf("  bucket used        %.2f GB  (real data, sums leaf+branch+overflow pages × 4KB)\n",
			float64(used)/1e9)
	}
	if st, err := os.Stat(filepath.Join(dbDir, "mdbx.dat")); err == nil {
		fmt.Printf("  mdbx.dat file size %.2f GB  (MDBX MapSize preallocation; not real data)\n",
			float64(st.Size())/1e9)
	}
	fmt.Printf("  state root         0x%s\n", rootHex)
	fmt.Println()
	fmt.Printf("  pass-1 (scan+sort) %s\n", res.Pass1.Truncate(time.Second))
	fmt.Printf("  pass-2 (HashBuilder) %s\n", res.Pass2.Truncate(time.Second))
	fmt.Printf("  pass-3 (AppendDup) %s\n", res.Pass3.Truncate(time.Second))
	fmt.Printf("  total elapsed      %s\n", time.Since(t0).Truncate(time.Second))

	if *verify != "" {
		want := strings.TrimPrefix(strings.TrimSpace(*verify), "0x")
		wantB, err := hex.DecodeString(want)
		if err != nil || len(wantB) != 32 {
			fatal("--verify-root: bad hex (expected 32 bytes)")
		}
		if hex.EncodeToString(wantB) != rootHex {
			fatal("ROOT MISMATCH:\n  got  0x%s\n  want 0x%s", rootHex, hex.EncodeToString(wantB))
		}
		fmt.Println("  verify-root        ✓ matches")
	}
}

func tableMapping(srcTable string) (prefix, outTable string, ex mptbuild.Extractor) {
	switch srcTable {
	case "PlainAccountState", "Account":
		return "accounts", "AccountsTrie", mptbuild.NewAccountExtractor()
	case "PlainStorageState", "Storage":
		return "storage", "StoragesTrie", mptbuild.NewStorageExtractor()
	default:
		fatal("unknown source table: %s (expected PlainAccountState | PlainStorageState)", srcTable)
		return "", "", nil
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
