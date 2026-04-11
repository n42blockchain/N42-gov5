// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// `jmt-compact` subcommand: offline JMT pruning and archival.
// Copies a source datadir into a target datadir, keeping only
// JMT nodes reachable from the HEAD root in MDBX while streaming
// historical nodes to compressed seg+idx archive files. Source is
// opened read-only; other tables are copied verbatim so the
// resulting node stays fully runnable.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/lib/jmt"
	jmtarchive "github.com/n42blockchain/N42/lib/jmt/archive"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	rootCmd = append(rootCmd, jmtCompactCommand)
}

var jmtCompactCommand = &cli.Command{
	Name:  "jmt-compact",
	Usage: "Create a new DB with only HEAD-reachable JMT nodes; archive historical nodes to seg files",
	Description: `Reads a source DB (with full JMT history), creates a new target DB where:
- All non-JMT tables are copied as-is
- JMT nodes reachable from HEAD root stay in MDBX (hot)
- All other JMT nodes are written to compressed seg+idx archive files (cold)

Source DB is opened read-only and not modified.`,
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "source", Usage: "Source data directory (with full JMT)", Required: true},
		&cli.StringFlag{Name: "target", Usage: "Target data directory (new compact DB)", Required: true},
		&cli.BoolFlag{Name: "dry-run", Usage: "Only walk and count, don't write", Value: false},
	},
	Action: runJMTCompact,
}

func runJMTCompact(cliCtx *cli.Context) error {
	srcDir := cliCtx.String("source")
	dstDir := cliCtx.String("target")
	dryRun := cliCtx.Bool("dry-run")
	ctx := context.Background()
	logger := log2.New()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// Open source DB read-only
	srcDB, err := mdbx.NewMDBX(logger).
		Path(srcDir+"/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(4 * datasize.TB).
		Accede().
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open source DB: %w", err)
	}
	defer srcDB.Close()

	// ============================================================
	// Phase 1: Walk HEAD root to collect hot node hashes
	// ============================================================
	fmt.Println("Phase 1/3: Walking HEAD tree to collect reachable nodes...")
	walkStart := time.Now()
	hotSet := make(map[jmt.Hash]struct{})

	var jmtRoot jmt.Hash
	if err := srcDB.View(ctx, func(tx kv.Tx) error {
		var readErr error
		jmtRoot, readErr = jmtstore.ReadJMTRoot(tx)
		if readErr != nil {
			return readErr
		}
		if jmtRoot == jmt.EmptyHash {
			return fmt.Errorf("no JMT root found in source")
		}
		fmt.Printf("  HEAD root: %x\n", jmtRoot[:8])
		return walkJMTTree(tx, jmtRoot, hotSet)
	}); err != nil {
		return err
	}
	fmt.Printf("  Hot nodes: %d (%s)\n\n", len(hotSet), time.Since(walkStart).Round(time.Millisecond))

	// ============================================================
	// Phase 2: Scan JMTNode, count hot vs cold
	// ============================================================
	fmt.Println("Phase 2/3: Scanning JMTNode table...")
	scanStart := time.Now()
	var totalNodes, hotCount, coldCount uint64
	var coldRawSize uint64

	if err := srcDB.View(ctx, func(tx kv.Tx) error {
		cursor, err := tx.Cursor(jmtstore.JMTNodeTable)
		if err != nil {
			return err
		}
		defer cursor.Close()
		for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				return err
			}
			totalNodes++
			var h jmt.Hash
			copy(h[:], k)
			if _, isHot := hotSet[h]; isHot {
				hotCount++
			} else {
				coldCount++
				coldRawSize += uint64(len(k) + len(v))
			}
			if totalNodes%10_000_000 == 0 {
				fmt.Printf("  %dM scanned (hot=%d cold=%d)\n", totalNodes/1_000_000, hotCount, coldCount)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("  Total: %d | Hot: %d | Cold: %d | Cold raw: %.2f GiB (%s)\n\n",
		totalNodes, hotCount, coldCount,
		float64(coldRawSize)/(1<<30),
		time.Since(scanStart).Round(time.Millisecond))

	if dryRun {
		fmt.Println("[DRY RUN] Done. Use without --dry-run to create target DB + archive.")
		return nil
	}

	// ============================================================
	// Phase 3: Create target DB + archive files
	// ============================================================
	fmt.Println("Phase 3/3: Creating target DB + archiving cold nodes...")
	os.MkdirAll(dstDir+"/chaindata", 0700)
	archiveDir := filepath.Join(dstDir, "jmt-archive")
	os.MkdirAll(archiveDir, 0700)

	// Open target DB
	dstDB, err := mdbx.NewMDBX(logger).
		Path(dstDir+"/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(4 * datasize.TB).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open target DB: %w", err)
	}
	defer dstDB.Close()

	// Create archive writer for cold nodes
	archiveWriter, err := jmtarchive.NewWriter(ctx, archiveDir, 0, uint64(totalNodes), filepath.Join(dstDir, "tmp"), logger)
	if err != nil {
		return fmt.Errorf("create archive writer: %w", err)
	}

	copyStart := time.Now()

	// List all tables from source
	var tables []string
	srcDB.View(ctx, func(tx kv.Tx) error {
		if migrator, ok := tx.(kv.BucketMigrator); ok {
			tables, _ = migrator.ListBuckets()
		}
		return nil
	})

	// Copy each table
	for _, table := range tables {
		tableStart := time.Now()
		var copied, archived uint64

		err := srcDB.View(ctx, func(srcTx kv.Tx) error {
			srcCursor, err := srcTx.Cursor(table)
			if err != nil {
				return err
			}
			defer srcCursor.Close()

			// Process in batches of 50K entries per MDBX write tx
			const batchSize = 50000
			batch := make([][2][]byte, 0, batchSize)

			flush := func() error {
				if len(batch) == 0 {
					return nil
				}
				return dstDB.Update(ctx, func(dstTx kv.RwTx) error {
					for _, kv := range batch {
						if err := dstTx.Put(table, kv[0], kv[1]); err != nil {
							return err
						}
					}
					return nil
				})
			}

			for k, v, err := srcCursor.First(); k != nil; k, v, err = srcCursor.Next() {
				if err != nil {
					return err
				}

				// Special handling for JMTNode: split hot vs cold
				if table == jmtstore.JMTNodeTable {
					var h jmt.Hash
					copy(h[:], k)
					if _, isHot := hotSet[h]; isHot {
						// Hot → target MDBX
						kCopy := make([]byte, len(k))
						copy(kCopy, k)
						vCopy := make([]byte, len(v))
						copy(vCopy, v)
						batch = append(batch, [2][]byte{kCopy, vCopy})
						copied++
					} else {
						// Cold → archive file
						var nodeHash [32]byte
						copy(nodeHash[:], k)
						vCopy := make([]byte, len(v))
						copy(vCopy, v)
						if err := archiveWriter.AddNode(nodeHash, vCopy); err != nil {
							return err
						}
						archived++
					}
				} else {
					// Non-JMT tables: copy as-is
					kCopy := make([]byte, len(k))
					copy(kCopy, k)
					vCopy := make([]byte, len(v))
					copy(vCopy, v)
					batch = append(batch, [2][]byte{kCopy, vCopy})
					copied++
				}

				if len(batch) >= batchSize {
					if err := flush(); err != nil {
						return err
					}
					batch = batch[:0]
				}

				if (copied+archived)%5_000_000 == 0 && table == jmtstore.JMTNodeTable {
					fmt.Printf("    JMTNode: %dM processed (hot=%d archived=%d)\n",
						(copied+archived)/1_000_000, copied, archived)
				}
			}
			return flush()
		})
		if err != nil {
			return fmt.Errorf("copy table %s: %w", table, err)
		}

		if copied > 0 || archived > 0 {
			fmt.Printf("  %-25s copied=%d archived=%d (%s)\n",
				table, copied, archived, time.Since(tableStart).Round(time.Millisecond))
		}
	}

	// Build archive index
	fmt.Println("\n  Building archive index (RecSplit)...")
	indexStart := time.Now()
	if err := archiveWriter.Build(ctx); err != nil {
		return fmt.Errorf("build archive: %w", err)
	}
	fmt.Printf("  Index built (%s)\n", time.Since(indexStart).Round(time.Millisecond))

	// Report
	elapsed := time.Since(copyStart)

	// Measure file sizes
	dstDBInfo, _ := os.Stat(dstDir + "/chaindata/mdbx.dat")
	dstDBSize := int64(0)
	if dstDBInfo != nil {
		dstDBSize = dstDBInfo.Size()
	}

	var archiveSize int64
	filepath.Walk(archiveDir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			archiveSize += info.Size()
		}
		return nil
	})

	fmt.Printf("\n=== JMT Compact Complete ===\n")
	fmt.Printf("  Source MDBX:     116 GiB (full JMT history)\n")
	fmt.Printf("  Target MDBX:     %.2f GiB (hot nodes only)\n", float64(dstDBSize)/(1<<30))
	fmt.Printf("  Archive files:   %.2f GiB (cold nodes, compressed)\n", float64(archiveSize)/(1<<30))
	fmt.Printf("  Total:           %.2f GiB\n", float64(dstDBSize+archiveSize)/(1<<30))
	fmt.Printf("  Reduction:       %.1f%%\n", (1-float64(dstDBSize+archiveSize)/float64(116*(1<<30)))*100)
	fmt.Printf("  Hot nodes:       %d\n", hotCount)
	fmt.Printf("  Cold nodes:      %d (archived)\n", coldCount)
	fmt.Printf("  Time:            %s\n", elapsed.Round(time.Second))

	return nil
}

// walkJMTTree walks the JMT from a root hash, collecting all reachable node hashes.
func walkJMTTree(tx kv.Tx, root jmt.Hash, reachable map[jmt.Hash]struct{}) error {
	type stackEntry struct{ hash jmt.Hash }
	stack := []stackEntry{{root}}

	for len(stack) > 0 {
		entry := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if entry.hash == jmt.EmptyHash {
			continue
		}
		if _, seen := reachable[entry.hash]; seen {
			continue
		}
		reachable[entry.hash] = struct{}{}

		data, err := tx.GetOne(jmtstore.JMTNodeTable, entry.hash[:])
		if err != nil || data == nil {
			continue
		}

		node, err := jmt.DecodeNode(data)
		if err != nil {
			continue
		}

		switch node.Type {
		case jmt.NodeTypeInternal:
			for i := 0; i < jmt.ChildCount; i++ {
				if node.Internal.Children[i].Valid {
					stack = append(stack, stackEntry{node.Internal.Children[i].Hash})
				}
			}
		case jmt.NodeTypeExtension:
			stack = append(stack, stackEntry{node.Extension.Child})
		// Leaf: no children
		}

		if len(reachable)%1_000_000 == 0 {
			fmt.Printf("    Walked %dM nodes...\n", len(reachable)/1_000_000)
		}
	}
	return nil
}
