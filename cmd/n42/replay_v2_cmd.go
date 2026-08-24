// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// `replay-v2` subcommand: full-chain re-execution with JMT commitment.
// Reads blocks from a source datadir and replays them into a target
// database through internal/replay with JMT + LtHash state
// commitment, optional gap filling for missing epochs and an EraE
// export step. Flags select tree type (jmt / bmt / mpt / trie) and
// control snapshot creation at the end of the run.

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/modules/rawdb"
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
		&cli.StringFlag{Name: "tree", Usage: "Tree type: jmt, bmt, qmdb, mpt, or trie", Value: "jmt"},
		&cli.IntFlag{Name: "qmdb-undo-window", Usage: "qmdb only: keep per-block undo records for the last N blocks (recent-height eth_getProof); 0 disables", Value: 64},
		&cli.BoolFlag{Name: "qmdb-history", Usage: "qmdb only: journal the full-history layer (death stamps, key versions, top band) for any-height proofs; forces archival entry retention", Value: false},
		&cli.BoolFlag{Name: "jmt", Usage: "Enable JMT state commitment", Value: true},
		&cli.BoolFlag{Name: "lthash", Usage: "Enable LtHash digest", Value: true},
		&cli.BoolFlag{Name: "no-gc", Usage: "Disable JMT GC (full history)", Value: true},
		&cli.BoolFlag{Name: "fill-gaps", Usage: "Fill timeline gaps with empty blocks", Value: true},
		&cli.Uint64Flag{Name: "gap-period", Usage: "Gap fill period (seconds)", Value: 8},
		&cli.Uint64Flag{Name: "gap-tolerance", Usage: "Gap fill tolerance (seconds)", Value: 15},
		&cli.Uint64Flag{Name: "gap-max", Usage: "Cap synthetic empty blocks per gap (0=unlimited); guards OOM on a huge startup/outage gap", Value: 10000},
		&cli.BoolFlag{Name: "compact-headers", Usage: "Write target headers in the compact storage codec (~4x smaller; read path accepts both formats)", Value: true},
		&cli.BoolFlag{Name: "compact-txs", Usage: "Write target transactions in the compact storage codec (unsupported types fall back to proto; read path accepts both)", Value: true},
		&cli.BoolFlag{Name: "compact-receipts", Usage: "Write target receipts in the compact storage codec (consensus fields + logs; Bloom recomputed on read; read path accepts both)", Value: true},
		&cli.BoolFlag{Name: "compact-logs", Usage: "Write the target transaction-log table in the compact storage codec (address, topics, data; context fields come from the key; read path accepts both)", Value: true},
		&cli.BoolFlag{Name: "compact-bodies", Usage: "Serialize freezer bodies in the compact storage codec instead of proto (read path accepts both)", Value: true},
		&cli.BoolFlag{Name: "virtual-td", Usage: "Skip all-zero TD rows (PoS TD=0); ReadTd synthesizes 0 for known headers via a DatabaseInfo marker", Value: true},
		&cli.BoolFlag{Name: "snapshot-at-end", Usage: "Create snapshot after replay", Value: true},
		&cli.BoolFlag{Name: "export-era", Usage: "Export EraE segments after replay", Value: false},
		&cli.Uint64Flag{Name: "era-segment-size", Usage: "Blocks per EraE segment", Value: 8192},
		&cli.IntFlag{Name: "batch", Usage: "Blocks per MDBX commit (larger = fewer commits, more memory)", Value: 100000},
		&cli.Uint64Flag{Name: "from", Usage: "Start block number", Value: 0},
		&cli.Uint64Flag{Name: "to", Usage: "End block number (0=auto)", Value: 0},
		&cli.StringFlag{Name: "output", Usage: "Stats output file", Value: "replay_v2_stats.json"},
		&cli.StringFlag{Name: "log", Usage: "Structured log file (empty=stderr only)", Value: ""},
		&cli.StringFlag{Name: "leaf-journal", Usage: "Leaf change journal file for tree building (empty=disabled)", Value: ""},
		&cli.BoolFlag{Name: "verify-mpt", Usage: "Per-block: rebuild MPT root from PlainState and verify (slow)", Value: false},
		&cli.BoolFlag{Name: "auto-topup", Usage: "Diagnostic: credit the exact deficit to any sender that hits 'insufficient funds' and retry, so it executes instead of skipping; reports the cumulative minimum genesis balance per address", Value: false},
		&cli.StringFlag{Name: "topup-dump", Usage: "auto-topup: write the per-address minimum-topup table to this file", Value: ""},
		&cli.BoolFlag{Name: "bls-reseal", Usage: "Re-seal every block with a simulated mobile-voter BLS committee QC (written to ConsensusEvidence)", Value: false},
		&cli.StringFlag{Name: "bls-seed", Usage: "32-byte hex master seed for the BLS voter pool (must match the generated pool)"},
		&cli.IntFlag{Name: "bls-pool-size", Usage: "Total mobile-voter pool size", Value: 200000},
		&cli.IntFlag{Name: "bls-committee", Usage: "Per-block committee size (ETH sync-committee reference)", Value: 512},
		&cli.Uint64Flag{Name: "bls-ramp-blocks", Usage: "Blocks over which the active pool ramps from one committee up to pool-size", Value: 1000000},
		&cli.IntFlag{Name: "pprof.port", Usage: "If >0, serve net/http/pprof on this port for profiling", Value: 0},
	},
	Action: runReplayV2,
}

func runReplayV2(cliCtx *cli.Context) error {
	if port := cliCtx.Int("pprof.port"); port > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		go func() {
			fmt.Printf("pprof listening on http://%s/debug/pprof/\n", addr)
			_ = http.ListenAndServe(addr, nil)
		}()
	}
	cfg := replay.DefaultConfigV2()
	cfg.SourcePath = cliCtx.String("source")
	cfg.TargetPath = cliCtx.String("target")

	// Resolve chain config
	chainName := cliCtx.String("chain")
	cfg.ChainName = chainName
	if cc := params.ChainConfigByChainName(chainName); cc != nil {
		cfg.ChainConfig = cc
	}
	if cfg.ChainConfig == nil {
		return fmt.Errorf("unknown replay target chain %q", chainName)
	}
	if err := validateReplayTargetNetworkBinding(cfg.TargetPath, chainName, cfg.ChainConfig); err != nil {
		return fmt.Errorf("validate replay target network binding: %w", err)
	}

	cfg.TreeType = cliCtx.String("tree")
	cfg.QMDBUndoWindow = cliCtx.Int("qmdb-undo-window")
	cfg.QMDBHistory = cliCtx.Bool("qmdb-history")
	cfg.EnableJMT = cliCtx.Bool("jmt")
	cfg.EnableLtHash = cliCtx.Bool("lthash")
	cfg.DisableGC = cliCtx.Bool("no-gc")
	cfg.FillGaps = cliCtx.Bool("fill-gaps")
	cfg.GapPeriod = cliCtx.Uint64("gap-period")
	cfg.GapTolerance = cliCtx.Uint64("gap-tolerance")
	cfg.GapMaxBlocks = cliCtx.Uint64("gap-max")
	rawdb.CompactHeaderWrites = cliCtx.Bool("compact-headers")
	rawdb.CompactTxWrites = cliCtx.Bool("compact-txs")
	rawdb.CompactReceiptWrites = cliCtx.Bool("compact-receipts")
	rawdb.CompactLogWrites = cliCtx.Bool("compact-logs")
	rawdb.CompactBodyWrites = cliCtx.Bool("compact-bodies")
	cfg.VirtualTd = cliCtx.Bool("virtual-td")
	cfg.SnapshotAtEnd = cliCtx.Bool("snapshot-at-end")
	cfg.ExportEraE = cliCtx.Bool("export-era")
	cfg.EraESegmentSize = cliCtx.Uint64("era-segment-size")
	cfg.BatchSize = cliCtx.Int("batch")
	cfg.FromBlock = cliCtx.Uint64("from")
	cfg.ToBlock = cliCtx.Uint64("to")
	cfg.LogFile = cliCtx.String("log")
	cfg.LeafJournal = cliCtx.String("leaf-journal")
	cfg.StatsFile = cliCtx.String("output")
	cfg.VerifyMPT = cliCtx.Bool("verify-mpt")
	cfg.AutoTopup = cliCtx.Bool("auto-topup")
	cfg.TopupDumpFile = cliCtx.String("topup-dump")

	cfg.BLSReseal = cliCtx.Bool("bls-reseal")
	if cfg.BLSReseal {
		seedHex := strings.TrimPrefix(cliCtx.String("bls-seed"), "0x")
		seed, err := hex.DecodeString(seedHex)
		if err != nil || len(seed) != 32 {
			return fmt.Errorf("--bls-seed must be 32-byte hex (got %d bytes)", len(seed))
		}
		copy(cfg.BLSSeed[:], seed)
		cfg.BLSPoolSize = cliCtx.Int("bls-pool-size")
		cfg.BLSCommitteeSize = cliCtx.Int("bls-committee")
		cfg.BLSRampBlocks = cliCtx.Uint64("bls-ramp-blocks")
	}

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
	defer engine.Close()

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
		return fmt.Errorf("post-export: %w", exportErr)
	}
	if bindErr := persistReplayTargetNetworkBinding(cfg.TargetPath, chainName, cfg.ChainConfig); bindErr != nil {
		return fmt.Errorf("persist replay target network binding: %w", bindErr)
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

func replayTargetNetworkConfig(targetPath, chainName string) (*params.ChainConfig, *conf.Config, *types.Hash, error) {
	chainCfg := params.ChainConfigByChainName(chainName)
	if chainCfg == nil {
		return nil, nil, nil, fmt.Errorf("unknown chain %q", chainName)
	}
	preset, err := params.ResolveNetworkPreset(chainName, "")
	if err != nil {
		return nil, nil, nil, err
	}
	genesisHash := params.GenesisHashByChainName(chainName)
	if genesisHash == nil {
		return nil, nil, nil, fmt.Errorf("missing genesis hash for chain %q", chainName)
	}
	cfg := DefaultConfig
	cfg.NodeCfg.DataDir = targetPath
	cfg.NodeCfg.Chain = preset.Chain
	cfg.NodeCfg.Profile = preset.Profile.String()
	cfg.NodeCfg.JMTCommitment = preset.Commitment == params.StateCommitmentPresetJMT
	cfg.ChainCfg = chainCfg
	return chainCfg, &cfg, genesisHash, nil
}

func validateReplayTargetNetworkBinding(targetPath, chainName string, chainCfg *params.ChainConfig) error {
	_, cfg, genesisHash, err := replayTargetNetworkConfig(targetPath, chainName)
	if err != nil {
		return err
	}
	return node.ValidateDataDirNetworkBinding(cfg, chainCfg, genesisHash)
}

func persistReplayTargetNetworkBinding(targetPath, chainName string, chainCfg *params.ChainConfig) error {
	_, cfg, genesisHash, err := replayTargetNetworkConfig(targetPath, chainName)
	if err != nil {
		return err
	}
	return node.PersistDataDirNetworkBinding(cfg, chainCfg, *genesisHash)
}
