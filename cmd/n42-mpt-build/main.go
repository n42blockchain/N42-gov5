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

	"github.com/n42blockchain/N42/internal/mptbuild"
	log "github.com/n42blockchain/N42/lib/log/v3"
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

	fmt.Printf("source     %s/%s\n", *srcDB, *srcTable)
	fmt.Printf("target     %s/%s (MDBX AppendDup)\n", dbDir, outTable)
	fmt.Printf("etl tmp    %s  (buf=%d MB)\n", *tmpDir, *bufMB)
	if *maxRows > 0 {
		fmt.Printf("max-rows   %d (smoke)\n", *maxRows)
	}
	fmt.Println()

	lastLog := time.Now()
	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
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
	})
	if err != nil {
		fatal("Build: %v", err)
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
