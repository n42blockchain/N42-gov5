// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// `era` subcommand: EraE history archive tools.
// Registers eraCommand with export / import / verify actions that
// package blocks into fixed-size (segment-size) .era files for
// sharing via HTTP / BitTorrent. Uses internal/sync/torrentsync
// and modules/rawdb helpers to read blocks and write era segments
// that OtterSync can later fetch.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	rootCmd = append(rootCmd, eraCommand)
}

var eraCommand = &cli.Command{
	Name:  "era",
	Usage: "EraE history archive commands",
	Subcommands: []*cli.Command{
		{
			Name:  "export",
			Usage: "Export blocks to EraE archive files",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "source", Usage: "Source data directory", Required: true},
				&cli.StringFlag{Name: "output", Usage: "Output directory for .era files", Required: true},
				&cli.Uint64Flag{Name: "from", Usage: "Start block", Value: 0},
				&cli.Uint64Flag{Name: "to", Usage: "End block (0=latest)", Value: 0},
				&cli.Uint64Flag{Name: "segment-size", Usage: "Blocks per .era file", Value: 8192},
			},
			Action: runEraExport,
		},
		{
			Name:  "import",
			Usage: "Import blocks from EraE archive files",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "input", Usage: "Directory containing .era files", Required: true},
				&cli.StringFlag{Name: "target", Usage: "Target data directory", Required: true},
			},
			Action: runEraImport,
		},
	},
}

func runEraExport(ctx *cli.Context) error {
	sourceDir := ctx.String("source")
	outputDir := ctx.String("output")
	from := ctx.Uint64("from")
	to := ctx.Uint64("to")
	segmentSize := ctx.Uint64("segment-size")

	chaindataPath := filepath.Join(sourceDir, "chaindata")
	if _, err := os.Stat(chaindataPath); err != nil {
		return fmt.Errorf("source chaindata not found: %s", chaindataPath)
	}

	fmt.Println("========================================")
	fmt.Println("N42 EraE Export")
	fmt.Println("========================================")
	fmt.Printf("Source:        %s\n", chaindataPath)
	fmt.Printf("Output:        %s\n", outputDir)
	fmt.Printf("Segment size:  %d blocks\n", segmentSize)

	// Open source database read-only with Accede mode.
	srcDB, err := mdbx.NewMDBX(log2.New()).
		Path(chaindataPath).
		MapSize(2 * datasize.TB).
		Accede().
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open source database: %w", err)
	}
	defer srcDB.Close()

	// Determine the end block if not specified.
	if to == 0 {
		if err := srcDB.View(ctx.Context, func(tx kv.Tx) error {
			hash, rerr := rawdb.ReadCanonicalHash(tx, 0)
			if rerr != nil {
				return fmt.Errorf("cannot read genesis hash: %w", rerr)
			}
			if hash == (types.Hash{}) {
				return fmt.Errorf("source database has no genesis block")
			}
			// Find latest block by reading head block number.
			num := rawdb.ReadCurrentBlockNumber(tx)
			if num != nil {
				to = *num
			}
			return nil
		}); err != nil {
			return err
		}
		if to == 0 {
			return fmt.Errorf("could not determine latest block number")
		}
	}

	fmt.Printf("Range:         %d -> %d (%d blocks)\n\n", from, to, to-from+1)

	start := time.Now()
	exporter := torrentsync.NewExporter(srcDB, outputDir, segmentSize)
	segments, err := exporter.ExportRange(ctx.Context, from, to)
	if err != nil {
		return fmt.Errorf("export failed: %w", err)
	}
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("EXPORT COMPLETE")
	fmt.Println("========================================")
	fmt.Printf("Duration:    %s\n", elapsed)
	fmt.Printf("Segments:    %d\n", len(segments))
	var totalBlocks uint64
	for _, seg := range segments {
		totalBlocks += seg.BlockCount
		fmt.Printf("  %s  blocks %d-%d  (%d blocks, %d bytes)\n",
			seg.FileName, seg.FromBlock, seg.ToBlock, seg.BlockCount, seg.Size)
	}
	fmt.Printf("Total blocks: %d\n", totalBlocks)
	fmt.Println("========================================")

	return nil
}

func runEraImport(ctx *cli.Context) error {
	inputDir := ctx.String("input")
	targetDir := ctx.String("target")

	if _, err := os.Stat(inputDir); err != nil {
		return fmt.Errorf("input directory not found: %s", inputDir)
	}

	chaindataPath := filepath.Join(targetDir, "chaindata")
	if err := os.MkdirAll(chaindataPath, 0755); err != nil {
		return fmt.Errorf("failed to create target chaindata dir: %w", err)
	}

	fmt.Println("========================================")
	fmt.Println("N42 EraE Import")
	fmt.Println("========================================")
	fmt.Printf("Input:   %s\n", inputDir)
	fmt.Printf("Target:  %s\n", chaindataPath)
	fmt.Println()

	// Open target database for writing.
	targetDB, err := mdbx.NewMDBX(log2.New()).
		Path(chaindataPath).
		MapSize(2 * datasize.TB).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open target database: %w", err)
	}
	defer targetDB.Close()

	start := time.Now()
	importer := torrentsync.NewImporter(targetDB, inputDir, true)
	totalBlocks, err := importer.ImportAll(ctx.Context, inputDir)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("IMPORT COMPLETE")
	fmt.Println("========================================")
	fmt.Printf("Duration:      %s\n", elapsed)
	fmt.Printf("Total blocks:  %d\n", totalBlocks)
	if elapsed.Seconds() > 0 {
		fmt.Printf("Speed:         %.0f blocks/s\n", float64(totalBlocks)/elapsed.Seconds())
	}
	fmt.Println("========================================")

	return nil
}
