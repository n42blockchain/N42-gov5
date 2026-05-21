// n42-mpt-migrate consolidates the per-table MDBX outputs from Phase A
// (D:\n42-mpt\accounts-mptcache + storage-mptcache) into a single
// unified chaindata MDBX env (D:\n42-chaindata) per the storage-tier
// layout decision: AccountsTrie + StoragesTrie must share an env so
// per-block updates are atomic and MVCC 256-block diff layers work.
//
// Read path: open each source RO, iterate the trie bucket with a
// cursor (entries are already sorted by nibble path).
//
// Write path: open dest RW with AccountsTrie + StoragesTrie + Meta
// buckets. Use cursor.Append for each entry — since source is sorted,
// dest gets the fastest path-fill (~95% page utilization).
//
// Meta: preserve state_root + built_at from each source under
// prefixed keys ("accounts:state_root", "storage:state_root", etc.)
// so the unified env knows which root belongs to which trie.
//
// Output (when both sources merged):
//   <dst>/mdbx.dat   one env, two trie buckets, one Meta bucket
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const metaTable = "Meta"

type migration struct {
	srcDir, srcTable string
	dstTable         string
	metaPrefix       string
}

func main() {
	srcAcct := flag.String("src-accounts", `D:\n42-mpt\accounts-mptcache`, "source MDBX dir for accounts trie")
	srcStor := flag.String("src-storage", `D:\n42-mpt\storage-mptcache`, "source MDBX dir for storage trie")
	dst := flag.String("dst", `D:\n42-chaindata`, "destination MDBX dir (will be created)")
	srcMapGB := flag.Int("src-mapsize-gb", 64, "source MapSize cap")
	dstMapGB := flag.Int("dst-mapsize-gb", 128, "destination MapSize cap")
	dryRun := flag.Bool("dry-run", false, "scan + size-check sources, don't write dest")
	flag.Parse()

	if err := os.MkdirAll(*dst, 0o755); err != nil {
		fatal("mkdir dst: %v", err)
	}

	migrations := []migration{
		{srcDir: *srcAcct, srcTable: "AccountsTrie", dstTable: "AccountsTrie", metaPrefix: "accounts"},
		{srcDir: *srcStor, srcTable: "StoragesTrie", dstTable: "StoragesTrie", metaPrefix: "storage"},
	}

	// Validate sources exist before opening dest (no half-mutation).
	for _, m := range migrations {
		mdbxPath := filepath.Join(m.srcDir, "mdbx.dat")
		if st, err := os.Stat(mdbxPath); err != nil {
			fatal("source %s missing: %v", mdbxPath, err)
		} else if st.Size() == 0 {
			fatal("source %s is empty", mdbxPath)
		}
	}

	logger := log.New()
	t0 := time.Now()

	// Open destination RW (with both trie buckets + Meta).
	var dstDB kv.RwDB
	if !*dryRun {
		var err error
		dstDB, err = mdbxkv.NewMDBX(logger).
			Path(*dst).
			Label(kv.ChainDB).
			PageSize(4096).
			MapSize(datasize.ByteSize(*dstMapGB) * datasize.GB).
			WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
				for _, m := range migrations {
					d[m.dstTable] = kv.TableCfgItem{}
				}
				d[metaTable] = kv.TableCfgItem{}
				return d
			}).
			Open(context.Background())
		if err != nil {
			fatal("open dst: %v", err)
		}
		defer dstDB.Close()
	}

	// Process each migration: read entries + meta from source,
	// AppendDup into dest, copy meta with prefix.
	for _, m := range migrations {
		fmt.Printf("\n=== migrating %s → %s ===\n", m.srcDir, m.dstTable)
		mt0 := time.Now()
		copied, bytes, err := migrateTable(logger, *srcMapGB, dstDB, m, *dryRun)
		if err != nil {
			fatal("migrate %s: %v", m.srcTable, err)
		}
		fmt.Printf("  entries copied:    %d\n", copied)
		fmt.Printf("  bytes written:     %.2f GB\n", float64(bytes)/1e9)
		fmt.Printf("  elapsed:           %s\n", time.Since(mt0).Truncate(time.Second))
		if !*dryRun {
			rate := float64(copied) / time.Since(mt0).Seconds()
			fmt.Printf("  rate:              %.0f entries/sec\n", rate)
		}
	}

	// Final summary.
	fmt.Println()
	fmt.Println("=== migration complete ===")
	fmt.Printf("  destination:       %s\n", *dst)
	fmt.Printf("  total elapsed:     %s\n", time.Since(t0).Truncate(time.Second))

	if !*dryRun {
		fmt.Println()
		fmt.Println("  per-bucket sizes (real data, leaf+branch+overflow pages × 4KB):")
		tx, err := dstDB.BeginRo(context.Background())
		if err != nil {
			fatal("post-tx: %v", err)
		}
		defer tx.Rollback()
		mtx := tx.(*mdbxkv.MdbxTx)
		var totalBucket uint64
		for _, m := range migrations {
			st, err := mtx.BucketStat(m.dstTable)
			if err != nil {
				fmt.Printf("    %s: stat err %v\n", m.dstTable, err)
				continue
			}
			used := (st.LeafPages + st.BranchPages + st.OverflowPages) * 4096
			totalBucket += used
			fmt.Printf("    %-15s entries=%-12d used=%.2f GB\n",
				m.dstTable, st.Entries, float64(used)/1e9)
		}
		fmt.Printf("    %-15s %.2f GB\n", "(total used)", float64(totalBucket)/1e9)

		if st, err := os.Stat(filepath.Join(*dst, "mdbx.dat")); err == nil {
			fmt.Printf("    mdbx.dat file:  %.2f GB  (MapSize preallocation)\n",
				float64(st.Size())/1e9)
		}

		fmt.Println()
		fmt.Println("  state roots stored in Meta bucket:")
		for _, m := range migrations {
			k := []byte(m.metaPrefix + ":state_root")
			v, _ := tx.GetOne(metaTable, k)
			if len(v) == 32 {
				fmt.Printf("    %-30s 0x%s\n", m.metaPrefix+":state_root", hex.EncodeToString(v))
			}
		}
	}
}

// migrateTable opens one source RO, streams its trie table into the
// dest's bucket via cursor.Append (fastest sorted-insert path), and
// copies its Meta entries with the migration's prefix.
//
// Returns (entries copied, bytes written from source).
func migrateTable(logger log.Logger, srcMapGB int, dstDB kv.RwDB, m migration, dryRun bool) (uint64, uint64, error) {
	srcDB, err := mdbxkv.NewMDBX(logger).
		Path(m.srcDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(srcMapGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[m.srcTable] = kv.TableCfgItem{}
			d[metaTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("open src: %w", err)
	}
	defer srcDB.Close()

	srcTx, err := srcDB.BeginRo(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("src tx: %w", err)
	}
	defer srcTx.Rollback()

	// Get source state_root + built_at from Meta for later transfer.
	stateRoot, _ := srcTx.GetOne(metaTable, []byte("state_root"))
	builtAt, _ := srcTx.GetOne(metaTable, []byte("built_at"))

	srcCursor, err := srcTx.Cursor(m.srcTable)
	if err != nil {
		return 0, 0, fmt.Errorf("src cursor: %w", err)
	}
	defer srcCursor.Close()

	var (
		copied  uint64
		bytes   uint64
		dstTx   kv.RwTx
		dstCur  kv.RwCursor
		lastLog = time.Now()
	)

	if !dryRun {
		dstTx, err = dstDB.BeginRw(context.Background())
		if err != nil {
			return 0, 0, fmt.Errorf("dst tx: %w", err)
		}
		// Truncate any prior content in dest bucket — re-runs become idempotent.
		if err := dstTx.ClearBucket(m.dstTable); err != nil {
			dstTx.Rollback()
			return 0, 0, fmt.Errorf("clear dst bucket: %w", err)
		}
		dstCur, err = dstTx.RwCursor(m.dstTable)
		if err != nil {
			dstTx.Rollback()
			return 0, 0, fmt.Errorf("dst cursor: %w", err)
		}
	}

	for k, v, err := srcCursor.First(); err == nil && k != nil; k, v, err = srcCursor.Next() {
		bytes += uint64(len(k) + len(v))
		copied++
		if dstCur != nil {
			if err := dstCur.Append(k, v); err != nil {
				dstCur.Close()
				dstTx.Rollback()
				return copied, bytes, fmt.Errorf("dst Append entry %d: %w", copied, err)
			}
		}
		if time.Since(lastLog) > 5*time.Second {
			fmt.Fprintf(os.Stderr, "  copying %s: %d entries (%.1f GB)\n",
				m.srcTable, copied, float64(bytes)/1e9)
			lastLog = time.Now()
		}
	}

	if !dryRun {
		dstCur.Close()
		// Persist meta entries under prefix.
		if len(stateRoot) == 32 {
			if err := dstTx.Put(metaTable, []byte(m.metaPrefix+":state_root"), stateRoot); err != nil {
				dstTx.Rollback()
				return copied, bytes, fmt.Errorf("put state_root: %w", err)
			}
		}
		if len(builtAt) > 0 {
			if err := dstTx.Put(metaTable, []byte(m.metaPrefix+":built_at"), builtAt); err != nil {
				dstTx.Rollback()
				return copied, bytes, fmt.Errorf("put built_at: %w", err)
			}
		}
		if err := dstTx.Put(metaTable, []byte(m.metaPrefix+":migrated_at"),
			[]byte(time.Now().UTC().Format(time.RFC3339))); err != nil {
			dstTx.Rollback()
			return copied, bytes, fmt.Errorf("put migrated_at: %w", err)
		}
		if err := dstTx.Commit(); err != nil {
			return copied, bytes, fmt.Errorf("dst commit: %w", err)
		}
	}

	return copied, bytes, nil
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
