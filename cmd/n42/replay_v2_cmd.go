// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/params"
)

func init() {
	rootCmd = append(rootCmd, replayV2Command)
}

var replayV2Command = &cli.Command{
	Name:  "replay-v2",
	Usage: "Full chain replay with JMT + LtHash state commitment, gap filling, and EraE export",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "source", Usage: "Source chain data directory", Required: true},
		&cli.StringFlag{Name: "target", Usage: "Target chain data directory", Required: true},
		&cli.StringFlag{Name: "chain", Usage: "Chain config name", Value: "mainnet_v2"},
		&cli.BoolFlag{Name: "jmt", Usage: "Enable JMT state commitment", Value: true},
		&cli.BoolFlag{Name: "lthash", Usage: "Enable LtHash digest", Value: true},
		&cli.BoolFlag{Name: "no-gc", Usage: "Disable JMT GC (full history)", Value: true},
		&cli.BoolFlag{Name: "fill-gaps", Usage: "Fill timeline gaps with empty blocks", Value: true},
		&cli.Uint64Flag{Name: "gap-period", Usage: "Gap fill period (seconds)", Value: 8},
		&cli.Uint64Flag{Name: "gap-tolerance", Usage: "Gap fill tolerance (seconds)", Value: 15},
		&cli.BoolFlag{Name: "snapshot-at-end", Usage: "Create snapshot after replay", Value: true},
		&cli.BoolFlag{Name: "export-era", Usage: "Export EraE segments after replay", Value: false},
		&cli.Uint64Flag{Name: "era-segment-size", Usage: "Blocks per EraE segment", Value: 8192},
		&cli.IntFlag{Name: "batch", Usage: "Blocks per MDBX commit", Value: 10000},
		&cli.Uint64Flag{Name: "from", Usage: "Start block number", Value: 0},
		&cli.Uint64Flag{Name: "to", Usage: "End block number (0=auto)", Value: 0},
		&cli.StringFlag{Name: "output", Usage: "Stats output file", Value: "replay_v2_stats.json"},
	},
	Action: runReplayV2,
}

func runReplayV2(cliCtx *cli.Context) error {
	cfg := replay.DefaultConfigV2()
	cfg.SourcePath = cliCtx.String("source")
	cfg.TargetPath = cliCtx.String("target")

	// Resolve chain config
	chainName := cliCtx.String("chain")
	if cc := params.ChainConfigByChainName(chainName); cc != nil {
		cfg.ChainConfig = cc
	}

	cfg.EnableJMT = cliCtx.Bool("jmt")
	cfg.EnableLtHash = cliCtx.Bool("lthash")
	cfg.DisableGC = cliCtx.Bool("no-gc")
	cfg.FillGaps = cliCtx.Bool("fill-gaps")
	cfg.GapPeriod = cliCtx.Uint64("gap-period")
	cfg.GapTolerance = cliCtx.Uint64("gap-tolerance")
	cfg.SnapshotAtEnd = cliCtx.Bool("snapshot-at-end")
	cfg.ExportEraE = cliCtx.Bool("export-era")
	cfg.EraESegmentSize = cliCtx.Uint64("era-segment-size")
	cfg.BatchSize = cliCtx.Int("batch")
	cfg.FromBlock = cliCtx.Uint64("from")
	cfg.ToBlock = cliCtx.Uint64("to")

	cfg.ProgressFn = func(current, total uint64, bps float64) {
		pct := float64(current) / float64(total) * 100
		fmt.Printf("\r● Replay  %6.2f%%  #%d / %d  ▸ %.0f blk/s", pct, current, total, bps)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT gracefully
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nInterrupted — saving progress...")
		cancel()
	}()

	engine, err := replay.NewEngineV2(cfg)
	if err != nil {
		return err
	}

	start := time.Now()
	stats, err := engine.Run(ctx)
	elapsed := time.Since(start)

	fmt.Printf("\n\nReplay completed in %s\n", elapsed.Truncate(time.Second))
	if stats != nil {
		fmt.Printf("Blocks: %d processed, %d empty, %d missing\n",
			stats.BlocksProcessed.Load(), stats.BlocksEmpty.Load(), stats.BlocksMissing.Load())
		fmt.Printf("Txs: %d total, %d replayed, %d skipped\n",
			stats.TxTotal.Load(), stats.TxReplayed.Load(), stats.TxSkipped.Load())
	}

	if err != nil {
		return err
	}

	// Post-replay export
	if exportErr := engine.RunPostExport(ctx); exportErr != nil {
		fmt.Printf("Warning: post-export failed: %v\n", exportErr)
	}

	// Write stats
	if stats != nil {
		output := cliCtx.String("output")
		data, _ := json.MarshalIndent(stats, "", "  ")
		os.WriteFile(output, data, 0644)
		fmt.Printf("Stats written to %s\n", output)
	}

	return nil
}
