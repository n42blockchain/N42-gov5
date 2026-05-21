// n42-mpt-hashedindex bootstraps the hashed-key fast-path index for
// the MPT proof system. Reads reth's PlainAccountState +
// PlainStorageState and emits two MDBX tables into the existing
// chaindata env:
//
//	HashedAccount       key=keccak(addr)            val=accountRLP
//	HashedStorageRef    key=keccak(addr)||keccak(slot)
//	                    val=addr20||slot32  (Option A: re-fetch value
//	                    via plain state at proof time)
//
// Replaces the 4-min-per-prefix ScanAccounts hot-path with a ~5 ms
// MDBX cursor seek. Expected end-to-end proof speedup: ~10,000x for
// USDC.
//
// Pipeline: parallel scan (account + storage) into separate ETL
// collectors, then serial AppendDup load (single MDBX writer per env).
// Total: ~max(account_scan, storage_scan) + load.
//
// Usage:
//
//	n42-mpt-hashedindex \
//	  --reth        D:\reth2k\db \
//	  --chaindata   D:\n42-chaindata \
//	  --tmp         D:\n42-mpt\etl-tmp \
//	  --etl-buf-mb  4096
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	var (
		rethDB        = flag.String("reth", `D:\reth2k\db`, "reth source DB (readonly)")
		acctTable     = flag.String("acct-table", "PlainAccountState", "source account table")
		storTable     = flag.String("stor-table", "PlainStorageState", "source storage table")
		chaindataDir  = flag.String("chaindata", `D:\n42-chaindata`, "destination chaindata env")
		tmpDir        = flag.String("tmp", "", "ETL spill dir (default <chaindata>/etl-tmp)")
		bufMB         = flag.Uint64("etl-buf-mb", 4096, "ETL buffer size MB per collector")
		mapSizeGB     = flag.Int("reth-mapsize-gb", 4096, "reth source DB MapSize cap")
		dstMapSizeGB  = flag.Int("dst-mapsize-gb", 4096, "destination env MapSize cap")
		onlyAccount   = flag.Bool("only-account", false, "build HashedAccount only (skip storage)")
		onlyStorage   = flag.Bool("only-storage", false, "build HashedStorageRef only (skip account)")
		maxRows       = flag.Int64("max-rows", 0, "0=full table; else stop after N rows per table (smoke)")
	)
	flag.Parse()

	if *tmpDir == "" {
		*tmpDir = filepath.Join(*chaindataDir, "etl-tmp")
	}
	if err := os.MkdirAll(*tmpDir, 0o755); err != nil {
		fatal("mkdir tmp: %v", err)
	}

	logger := log.New()
	t0 := time.Now()

	fmt.Printf("source     %s\n", *rethDB)
	fmt.Printf("destination %s\n", *chaindataDir)
	fmt.Printf("etl tmp    %s  (buf=%d MB)\n", *tmpDir, *bufMB)
	if *maxRows > 0 {
		fmt.Printf("max-rows   %d (smoke mode)\n", *maxRows)
	}
	fmt.Println()

	// Open the reth source ONCE — MDBX disallows opening the same env
	// from two handles in the same process. Both goroutines share it.
	srcDB, err := mdbxkv.NewMDBX(logger).
		Path(*rethDB).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[*acctTable] = kv.TableCfgItem{}
			d[*storTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		fatal("open reth source: %v", err)
	}
	defer srcDB.Close()

	// ----- Phase 1: parallel scan + ETL fill -----

	var (
		acctRes = make(chan collectResult, 1)
		storRes = make(chan collectResult, 1)
		wg      sync.WaitGroup
	)

	if !*onlyStorage {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := scanAndCollect(scanOpts{
				logger: logger, label: "account", srcDB: srcDB, srcTable: *acctTable,
				maxRows: *maxRows,
				tmpDir:  filepath.Join(*tmpDir, "account"), bufMB: *bufMB,
				transform: transformAccount,
			})
			acctRes <- r
		}()
	} else {
		acctRes <- collectResult{}
	}

	if !*onlyAccount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := scanAndCollect(scanOpts{
				logger: logger, label: "storage", srcDB: srcDB, srcTable: *storTable,
				maxRows: *maxRows,
				tmpDir:  filepath.Join(*tmpDir, "storage"), bufMB: *bufMB,
				transform: transformStorage,
			})
			storRes <- r
		}()
	} else {
		storRes <- collectResult{}
	}

	wg.Wait()
	ar := <-acctRes
	sr := <-storRes

	if ar.err != nil {
		fatal("account scan: %v", ar.err)
	}
	if sr.err != nil {
		fatal("storage scan: %v", sr.err)
	}
	t1 := time.Now()
	fmt.Printf("\nphase-1 done in %s (account: %d rows %d MB, storage: %d rows %d MB)\n",
		t1.Sub(t0).Truncate(time.Second),
		ar.rows, ar.bytes>>20, sr.rows, sr.bytes>>20)

	// ----- Phase 2: serial AppendDup load -----

	dstDB, err := mdbxkv.NewMDBX(logger).
		Path(*chaindataDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*dstMapSizeGB) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[mptproof.HashedAccountTable] = kv.TableCfgItem{}
			d[mptproof.HashedStorageRefTable] = kv.TableCfgItem{}
			// Existing tables — declare so MDBX doesn't try to drop them
			// (re-declaring an existing table is a no-op).
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		fatal("open dst: %v", err)
	}
	defer dstDB.Close()

	if ar.coll != nil {
		fmt.Printf("phase-2 loading %d HashedAccount rows...\n", ar.rows)
		if err := loadIntoTable(dstDB, mptproof.HashedAccountTable, ar.coll); err != nil {
			fatal("load HashedAccount: %v", err)
		}
		ar.coll.Close()
	}
	if sr.coll != nil {
		fmt.Printf("phase-2 loading %d HashedStorageRef rows...\n", sr.rows)
		if err := loadIntoTable(dstDB, mptproof.HashedStorageRefTable, sr.coll); err != nil {
			fatal("load HashedStorageRef: %v", err)
		}
		sr.coll.Close()
	}
	t2 := time.Now()

	fmt.Printf("\n✓ done in %s (phase-1 %s scan+sort, phase-2 %s load)\n",
		t2.Sub(t0).Truncate(time.Second),
		t1.Sub(t0).Truncate(time.Second),
		t2.Sub(t1).Truncate(time.Second))
}

// ----------------------------------------------------------------------------
// Scan + collect phase
// ----------------------------------------------------------------------------

type scanOpts struct {
	logger    log.Logger
	label     string
	srcDB     kv.RoDB
	srcTable  string
	maxRows   int64
	tmpDir    string
	bufMB     uint64
	transform func(k, v []byte) (outKey, outVal []byte, err error)
}

type collectResult struct {
	coll  *etl.Collector
	rows  int64
	bytes int64
	err   error
}

func scanAndCollect(opts scanOpts) collectResult {
	if err := os.MkdirAll(opts.tmpDir, 0o755); err != nil {
		return collectResult{err: fmt.Errorf("mkdir %s: %w", opts.tmpDir, err)}
	}

	tx, err := opts.srcDB.BeginRo(context.Background())
	if err != nil {
		return collectResult{err: err}
	}
	defer tx.Rollback()
	c, err := tx.Cursor(opts.srcTable)
	if err != nil {
		return collectResult{err: err}
	}
	defer c.Close()

	coll := etl.NewCollectorWithAllocator(
		opts.label,
		opts.tmpDir,
		datasize.ByteSize(opts.bufMB)*datasize.MB,
		opts.logger,
	)

	var (
		rows    int64
		nBytes  int64
		lastLog = time.Now()
	)
	t0 := time.Now()
	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		outK, outV, terr := opts.transform(k, v)
		if terr != nil {
			coll.Close()
			return collectResult{err: fmt.Errorf("transform row %d: %w", rows, terr)}
		}
		if outK == nil {
			continue
		}
		if cerr := coll.Collect(outK, outV); cerr != nil {
			coll.Close()
			return collectResult{err: fmt.Errorf("collect row %d: %w", rows, cerr)}
		}
		rows++
		nBytes += int64(len(outK) + len(outV))
		if opts.maxRows > 0 && rows >= opts.maxRows {
			break
		}
		if time.Since(lastLog) > 5*time.Second {
			rate := float64(rows) / time.Since(t0).Seconds()
			fmt.Fprintf(os.Stderr, "  %s collected %d rows (%.0f k/s, %d MB)\n",
				opts.label, rows, rate/1000, nBytes>>20)
			lastLog = time.Now()
		}
	}
	return collectResult{coll: coll, rows: rows, bytes: nBytes}
}

// ----------------------------------------------------------------------------
// Transforms
// ----------------------------------------------------------------------------

// transformAccount: reth PlainAccountState key is addr20, value is the
// account RLP. We emit (keccak(addr), value).
func transformAccount(k, v []byte) ([]byte, []byte, error) {
	if len(k) != 20 {
		return nil, nil, nil // skip non-account keys (defensive)
	}
	h := keccak(k)
	out := make([]byte, len(v))
	copy(out, v)
	return h, out, nil
}

// transformStorage: reth PlainStorageState is DupSort; cursor.Next
// returns key=addr20 + value=slot32||u256bytes for each dup. We emit
// (keccak(addr)||keccak(slot), addr20||slot32) — the value-by-reference
// form (Option A).
func transformStorage(k, v []byte) ([]byte, []byte, error) {
	if len(k) != 20 || len(v) < 32 {
		return nil, nil, nil
	}
	hAddr := keccak(k)
	hSlot := keccak(v[:32])

	outK := make([]byte, 64)
	copy(outK[:32], hAddr)
	copy(outK[32:], hSlot)

	outV := make([]byte, 52)
	copy(outV[:20], k)
	copy(outV[20:], v[:32])

	return outK, outV, nil
}

// ----------------------------------------------------------------------------
// Load phase
// ----------------------------------------------------------------------------

func loadIntoTable(db kv.RwDB, table string, coll *etl.Collector) error {
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Truncate the table if it already has data (idempotent rebuild).
	if err := tx.ClearBucket(table); err != nil {
		return fmt.Errorf("clear %s: %w", table, err)
	}

	if err := coll.Load(tx, table, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		return fmt.Errorf("etl load: %w", err)
	}
	return tx.Commit()
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func keccak(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
