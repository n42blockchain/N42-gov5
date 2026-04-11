// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// `migrate-tiers` subcommand: split datadir into storage tiers.
// Moves chaindata / tmp onto Hot (NVMe), snapshot domain / accessor
// onto Warm, and history / indices / freezer onto Cold paths. Uses
// lib/common/datadir to resolve logical directories and writes a
// conf.StorageTierCfg entry that subsequent runs consume.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/common/datadir"
)

func init() {
	rootCmd = append(rootCmd, migrateTiersCommand)
}

var migrateTiersCommand = &cli.Command{
	Name:  "migrate-tiers",
	Usage: "Move data files from a flat datadir to tiered storage paths (NVMe/HDD split)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "datadir",
			Usage:    "Current flat data directory",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "hot",
			Usage: "Hot tier path (NVMe) — chaindata, tmp",
		},
		&cli.StringFlag{
			Name:  "warm",
			Usage: "Warm tier path (NVMe/SSD) — snapshots domain/accessor",
		},
		&cli.StringFlag{
			Name:  "cold",
			Usage: "Cold tier path (HDD) — history, indices, freezer",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Show what would be moved without actually moving",
		},
	},
	Action: runMigrateTiers,
}

// moveEntry describes a single directory move operation.
type moveEntry struct {
	Desc string // human-readable description
	Src  string // source path
	Dst  string // destination path
}

func runMigrateTiers(ctx *cli.Context) error {
	datadirPath := ctx.String("datadir")
	hotPath := ctx.String("hot")
	warmPath := ctx.String("warm")
	coldPath := ctx.String("cold")
	dryRun := ctx.Bool("dry-run")

	if hotPath == "" && warmPath == "" && coldPath == "" {
		return fmt.Errorf("at least one tier path (--hot, --warm, --cold) must be specified")
	}

	// Validate source datadir exists.
	if _, err := os.Stat(datadirPath); err != nil {
		return fmt.Errorf("datadir not found: %s", datadirPath)
	}

	// Validate tier config.
	tierCfg := conf.StorageTierCfg{
		Enabled:  true,
		HotPath:  hotPath,
		WarmPath: warmPath,
		ColdPath: coldPath,
	}
	if err := tierCfg.Validate(); err != nil {
		return err
	}

	// Build the current flat dirs.
	srcDirs := datadir.New(datadirPath)

	// Build the target tiered dirs.
	dstDirs := datadir.New(datadirPath)
	datadir.ApplyTierOverrides(&dstDirs, hotPath, warmPath, coldPath, "")

	// Determine which directories need to move.
	var moves []moveEntry

	addMove := func(desc, src, dst string) {
		if src == dst {
			return // same path, no move needed
		}
		if _, err := os.Stat(src); os.IsNotExist(err) {
			return // source doesn't exist, skip
		}
		moves = append(moves, moveEntry{Desc: desc, Src: src, Dst: dst})
	}

	addMove("Chaindata (MDBX)", srcDirs.Chaindata, dstDirs.Chaindata)
	addMove("Temp", srcDirs.Tmp, dstDirs.Tmp)
	addMove("Snapshots", srcDirs.Snap, dstDirs.Snap)
	addMove("Snapshot Domain", srcDirs.SnapDomain, dstDirs.SnapDomain)
	addMove("Snapshot Accessors", srcDirs.SnapAccessors, dstDirs.SnapAccessors)
	addMove("Snapshot History", srcDirs.SnapHistory, dstDirs.SnapHistory)
	addMove("Snapshot Indices", srcDirs.SnapIdx, dstDirs.SnapIdx)
	addMove("Downloader", srcDirs.Downloader, dstDirs.Downloader)

	if len(moves) == 0 {
		fmt.Println("Nothing to move — all paths are already at their target locations.")
		return nil
	}

	// Print plan.
	fmt.Println("========================================")
	if dryRun {
		fmt.Println("STORAGE TIER MIGRATION (DRY RUN)")
	} else {
		fmt.Println("STORAGE TIER MIGRATION")
	}
	fmt.Println("========================================")
	fmt.Printf("Source:  %s\n", datadirPath)
	if hotPath != "" {
		fmt.Printf("Hot:     %s\n", hotPath)
	}
	if warmPath != "" {
		fmt.Printf("Warm:    %s\n", warmPath)
	}
	if coldPath != "" {
		fmt.Printf("Cold:    %s\n", coldPath)
	}
	fmt.Println("----------------------------------------")

	for i, m := range moves {
		fmt.Printf("  [%d] %s\n", i+1, m.Desc)
		fmt.Printf("      %s\n", m.Src)
		fmt.Printf("   →  %s\n", m.Dst)
	}
	fmt.Printf("\nTotal: %d directories to move\n", len(moves))

	if dryRun {
		fmt.Println("\n(dry run — no files moved)")
		return nil
	}

	// Execute moves.
	fmt.Println("\nMoving...")
	start := time.Now()

	for i, m := range moves {
		fmt.Printf("  [%d/%d] %s ... ", i+1, len(moves), m.Desc)

		// Ensure destination parent exists.
		if err := os.MkdirAll(filepath.Dir(m.Dst), 0700); err != nil {
			fmt.Printf("FAILED (mkdir: %v)\n", err)
			return fmt.Errorf("failed to create destination directory for %s: %w", m.Desc, err)
		}

		// Move via rename (same filesystem = instant, cross-filesystem = error).
		if err := os.Rename(m.Src, m.Dst); err != nil {
			fmt.Printf("FAILED (rename: %v)\n", err)
			fmt.Println("\nNote: os.Rename fails across filesystems. For cross-device moves,")
			fmt.Println("use system tools (cp -a + rm) or rsync manually, then update config.")
			return fmt.Errorf("failed to move %s: %w", m.Desc, err)
		}

		// Create a symlink from old location to new for backward compatibility.
		_ = os.Symlink(m.Dst, m.Src)

		fmt.Println("OK")
	}

	elapsed := time.Since(start)
	fmt.Println("----------------------------------------")
	fmt.Printf("Migration complete in %s\n", elapsed)
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Add to your config file:")
	fmt.Println("     storage_tier:")
	fmt.Println("       enabled: true")
	if hotPath != "" {
		fmt.Printf("       hot_path: %q\n", hotPath)
	}
	if warmPath != "" {
		fmt.Printf("       warm_path: %q\n", warmPath)
	}
	if coldPath != "" {
		fmt.Printf("       cold_path: %q\n", coldPath)
	}
	fmt.Println("  2. Start the node — it will use the tiered paths automatically.")
	fmt.Println("========================================")

	return nil
}
