// n42-mpt-clear-hashed clears the legacy HashedAccount +
// HashedStorageRef tables from a chaindata MDBX env.
//
// These tables were created by a misguided Phase F attempt to build
// our own hashed-key index. We now use reth's existing HashedAccounts
// / HashedStorages instead (zero extra storage), and will eventually
// replace both with an Erigon-style commitment domain.
//
// ClearBucket frees the pages but does NOT shrink mdbx.dat. To
// reclaim physical disk, run `mdbx_copy --compact` offline or wait
// until those pages get reused by future writes.
//
// Usage:
//
//	n42-mpt-clear-hashed --chaindata D:\n42-chaindata
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	hashedAccountTable    = "HashedAccount"
	hashedStorageRefTable = "HashedStorageRef"
)

func main() {
	var (
		chaindataDir = flag.String("chaindata", `D:\n42-chaindata`, "destination chaindata env")
		mapSizeGB    = flag.Int("mapsize-gb", 4096, "env MapSize cap")
		dryRun       = flag.Bool("dry-run", false, "report only, no clear")
	)
	flag.Parse()

	logger := log.New()

	db, err := mdbxkv.NewMDBX(logger).
		Path(*chaindataDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[hashedAccountTable] = kv.TableCfgItem{}
			d[hashedStorageRefTable] = kv.TableCfgItem{}
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		fatal("open chaindata: %v", err)
	}
	defer db.Close()

	// Pre-clear counts to confirm what we're killing.
	roTx, _ := db.BeginRo(context.Background())
	for _, t := range []string{hashedAccountTable, hashedStorageRefTable, "AccountsTrie", "StoragesTrie", "Meta"} {
		n, err := tableCount(roTx, t)
		if err != nil {
			fmt.Printf("  %-22s count error: %v\n", t, err)
			continue
		}
		fmt.Printf("  %-22s rows=%d\n", t, n)
	}
	roTx.Rollback()

	if *dryRun {
		fmt.Println("\n--dry-run set; no changes made")
		return
	}

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fatal("begin rw: %v", err)
	}
	defer tx.Rollback()

	for _, t := range []string{hashedAccountTable, hashedStorageRefTable} {
		t0 := time.Now()
		if err := tx.ClearBucket(t); err != nil {
			fatal("clear %s: %v", t, err)
		}
		fmt.Printf("  cleared %-22s in %s\n", t, time.Since(t0).Truncate(time.Millisecond))
	}

	t0 := time.Now()
	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}
	fmt.Printf("  commit in %s\n", time.Since(t0).Truncate(time.Millisecond))

	fmt.Println("\n✓ cleared (pages freed for reuse; .dat size unchanged)")
}

func tableCount(tx kv.Tx, table string) (int64, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	var n int64
	for k, _, err := c.First(); err == nil && k != nil; k, _, err = c.Next() {
		n++
	}
	return n, nil
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
