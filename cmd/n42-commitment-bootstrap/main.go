// n42-commitment-bootstrap walks a reth HashedAccounts/HashedStorages
// snapshot, pushes every entry through HexPatriciaHashed.Process,
// and persists the resulting BranchData updates into a destination
// CommitmentBranches MDBX table.
//
// Output footprint target (per HA-1..HA-4 plan): ~3-5 GB at full
// mainnet state, vs 38 GB for reth's compact trie tables.
//
// Usage:
//
//	n42-commitment-bootstrap \
//	  --reth-db D:\reth\db \
//	  --dst    D:\n42-commitment \
//	  --tmpdir D:\tmp\commitment-etl \
//	  --account-limit 10000 \
//	  --storage-limit 100000
//
// --account-limit / --storage-limit cap the walk for development
// iteration; set 0 (default) to walk the whole table.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	rethPlainAccountStateTable = "PlainAccountState"
	rethPlainStorageStateTable = "PlainStorageState"
)

func main() {
	rethDB := flag.String("reth-db", "", "path to reth's mdbx.dat dir (read-only)")
	dst := flag.String("dst", "", "destination dir for CommitmentBranches MDBX env")
	tmpdir := flag.String("tmpdir", "", "scratch dir for ETL spill (default: --dst/etl-tmp)")
	rethMapGB := flag.Int("reth-mapsize-gb", 4096, "reth env mapsize (GB)")
	dstMapGB := flag.Int("dst-mapsize-gb", 256, "destination env mapsize (GB)")
	acctLimit := flag.Int("account-limit", 0, "bound account count (0 = no limit)")
	storLimit := flag.Int("storage-limit", 0, "bound storage entry count (0 = no limit)")
	zstdLevel := flag.Int("zstd-level", 0, "zstd compression level for CommitmentBranches values (0 = off, 3 = recommended)")
	flag.Parse()

	if *rethDB == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "usage: --reth-db DIR --dst DIR")
		os.Exit(2)
	}
	if *tmpdir == "" {
		*tmpdir = *dst + "/etl-tmp"
	}
	if err := os.MkdirAll(*tmpdir, 0o755); err != nil {
		fail("mkdir tmpdir: %v", err)
	}

	fmt.Printf("n42-commitment-bootstrap starting: reth_db=%s dst=%s tmpdir=%s acct_limit=%d stor_limit=%d\n",
		*rethDB, *dst, *tmpdir, *acctLimit, *storLimit)
	logger := log.New()
	_ = logger

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Open reth as plain MDBX (read-only) ---
	rethSrc, err := mptproof.NewRethHashedLeafSource(*rethDB, *rethMapGB)
	if err != nil {
		fail("open reth: %v", err)
	}
	defer rethSrc.Close()
	reader := mptproof.NewRethBackedReader(rethSrc)

	// --- Open destination CommitmentBranches env ---
	dstDB, err := mdbxkv.NewMDBX(logger).
		Path(*dst).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*dstMapGB) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[commitment.CommitmentBranchesTable] = kv.TableCfgItem{}
			return d
		}).Open(ctx)
	if err != nil {
		fail("open dst: %v", err)
	}
	defer dstDB.Close()

	tx, err := dstDB.BeginRw(ctx)
	if err != nil {
		fail("BeginRw: %v", err)
	}

	// --- Wire HPH + PersistentPatriciaContext ---
	pctx := commitment.NewPersistentPatriciaContext(reader, reader)
	pctx.SetWriteTx(tx)
	if *zstdLevel > 0 {
		if err := pctx.SetZstdLevel(*zstdLevel); err != nil {
			fail("SetZstdLevel: %v", err)
		}
		fmt.Printf("zstd compression enabled at level %d\n", *zstdLevel)
	}
	hph := commitment.NewHexPatriciaHashed(int16(length.Addr), pctx)

	// Plain keys are real (unhashed) addresses / addr||slot from
	// reth's PlainAccountState / PlainStorageState tables. HPH
	// hashes them internally via the canonical hasher, yielding
	// canonical ETH state roots.
	updates := commitment.NewUpdates(commitment.ModeDirect, *tmpdir, commitment.KeyToHexNibbleHash)
	defer updates.Close()

	// --- Walk reth HashedAccounts → TouchPlainKey ---
	t0 := time.Now()
	acctCount, err := touchAccounts(ctx, rethSrc, updates, *acctLimit, logger)
	if err != nil {
		fail("touch accounts: %v", err)
	}
	fmt.Printf("accounts touched: count=%d elapsed=%s\n", acctCount, time.Since(t0))

	// --- Walk reth HashedStorages → TouchPlainKey ---
	t1 := time.Now()
	storCount, err := touchStorages(ctx, rethSrc, updates, *storLimit, logger)
	if err != nil {
		fail("touch storages: %v", err)
	}
	fmt.Printf("storages touched: count=%d elapsed=%s\n", storCount, time.Since(t1))

	// --- Run HPH.Process ---
	t2 := time.Now()
	fmt.Println("running HPH.Process — this is the long part")
	lastReport := time.Now()
	onProgress := func(p *commitment.CommitProgress) {
		now := time.Now()
		if now.Sub(lastReport) < 30*time.Second {
			return
		}
		lastReport = now
		elapsed := now.Sub(t2)
		var pct float64
		if p.UpdateCount > 0 {
			pct = 100 * float64(p.KeyIndex) / float64(p.UpdateCount)
		}
		var eta time.Duration
		if p.KeyIndex > 0 {
			eta = time.Duration(float64(elapsed) * float64(p.UpdateCount-p.KeyIndex) / float64(p.KeyIndex))
		}
		fmt.Printf("  HPH.Process progress: %d/%d (%.1f%%) elapsed=%s ETA=%s\n",
			p.KeyIndex, p.UpdateCount, pct, elapsed.Truncate(time.Second), eta.Truncate(time.Second))
	}
	root, err := hph.Process(ctx, updates, "bootstrap", onProgress, commitment.WarmupConfig{})
	if err != nil {
		fail("Process: %v", err)
	}
	fmt.Printf("HPH.Process complete: root=%x elapsed=%s\n", root, time.Since(t2))

	// --- Commit MDBX ---
	if err := tx.Commit(); err != nil {
		fail("commit: %v", err)
	}

	// --- Inspect persisted size ---
	roTx, _ := dstDB.BeginRo(ctx)
	entries, valueBytes, _ := commitment.CommitmentBranchesSize(ctx, roTx)
	roTx.Rollback()
	fmt.Printf("CommitmentBranches persisted: entries=%d value_bytes=%d value_mb=%.2f\n",
		entries, valueBytes, float64(valueBytes)/1024/1024)
	fmt.Printf("bootstrap done: state_root=%x accounts=%d storages=%d total_elapsed=%s\n",
		root, acctCount, storCount, time.Since(t0))
}

func touchAccounts(ctx context.Context, src *mptproof.RethHashedLeafSource,
	updates *commitment.Updates, limit int, logger log.Logger) (int, error) {

	tx, err := src.RoTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(rethPlainAccountStateTable)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	last := time.Now()
	count := 0
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return count, err
		}
		// plainKey = 32-byte addrHash (per RethBackedReader convention).
		updates.TouchPlainKeyNoDedup(string(k), nil, updates.TouchAccount)
		count++
		if limit > 0 && count >= limit {
			break
		}
		if count%100_000 == 0 && time.Since(last) > 10*time.Second {
			fmt.Printf("  touching accounts: count=%d\n", count)
			last = time.Now()
		}
	}
	return count, nil
}

func touchStorages(ctx context.Context, src *mptproof.RethHashedLeafSource,
	updates *commitment.Updates, limit int, logger log.Logger) (int, error) {

	tx, err := src.RoTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethPlainStorageStateTable)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	composite := make([]byte, 52) // 20-byte addr + 32-byte slot
	last := time.Now()
	count := 0
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return count, err
		}
		if len(v) < 32 {
			continue
		}
		copy(composite[:20], k)
		copy(composite[20:], v[:32])
		updates.TouchPlainKeyNoDedup(string(composite), nil, updates.TouchStorage)
		count++
		if limit > 0 && count >= limit {
			break
		}
		if count%1_000_000 == 0 && time.Since(last) > 30*time.Second {
			fmt.Printf("  touching storages: count=%d\n", count)
			last = time.Now()
		}
	}
	return count, nil
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
