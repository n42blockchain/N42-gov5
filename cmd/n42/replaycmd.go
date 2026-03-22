// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// skipAddresses aliases the canonical list from the replay package to
// avoid maintaining a duplicate copy.
var skipAddresses = replay.DefaultSkipAddresses

func init() {
	rootCmd = append(rootCmd, replayCommand)
}

var replayCommand = &cli.Command{
	Name:  "replay",
	Usage: "Replay old chain transactions into a new database (lossy — skips incompatible txs)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "source",
			Usage:    "Source chain data directory (old mainnet)",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "target",
			Usage: "Target chain data directory for new database (if set, writes blocks to new DB)",
		},
		&cli.StringFlag{
			Name:  "output",
			Usage: "Output JSON stats file (default: replay_result.json)",
			Value: "replay_result.json",
		},
		&cli.Uint64Flag{
			Name:  "from",
			Usage: "Start block number (default: 0)",
			Value: 0,
		},
		&cli.Uint64Flag{
			Name:  "to",
			Usage: "End block number (0 = latest)",
			Value: 0,
		},
		&cli.IntFlag{
			Name:  "batch",
			Usage: "Blocks per progress report",
			Value: 500000,
		},
	},
	Action: runReplay,
}

// ReplayStats tracks replay progress and filtering statistics.
type ReplayStats struct {
	FromBlock       uint64            `json:"from_block"`
	ToBlock         uint64            `json:"to_block"`
	BlocksProcessed uint64            `json:"blocks_processed"`
	BlocksEmpty     uint64            `json:"blocks_empty"`
	BlocksMissing   uint64            `json:"blocks_missing"`
	TxTotal         uint64            `json:"tx_total"`
	TxReplayed      uint64            `json:"tx_replayed"`
	TxSkipped       uint64            `json:"tx_skipped"`
	TxFailed        uint64            `json:"tx_failed"`
	SkipReasons     map[string]uint64 `json:"skip_reasons"`
	Accounts        map[string]string `json:"accounts"` // address → final balance (hex)
	Duration        string            `json:"duration"`
}

// ReplayedBlock holds the replayed data for a single block.
type ReplayedBlock struct {
	Number    uint64   `json:"number"`
	Hash      string   `json:"hash"`
	Timestamp uint64   `json:"timestamp"`
	Coinbase  string   `json:"coinbase"`
	TxCount   int      `json:"tx_count"`
	GasUsed   uint64   `json:"gas_used"`
	TxHashes  []string `json:"tx_hashes,omitempty"`
}

func runReplay(ctx *cli.Context) error {
	sourceDir := ctx.String("source")
	targetDir := ctx.String("target")
	outputFile := ctx.String("output")
	fromBlock := ctx.Uint64("from")
	toBlock := ctx.Uint64("to")
	batchSize := ctx.Int("batch")

	// If --target is set, use the replay.Engine to write into a new DB.
	if targetDir != "" {
		return runReplayWithTarget(ctx.Context, sourceDir, targetDir, outputFile, fromBlock, toBlock, batchSize)
	}

	// Read-only analysis mode (no --target).
	chaindataPath := filepath.Join(sourceDir, "chaindata")
	if _, err := os.Stat(chaindataPath); err != nil {
		return fmt.Errorf("source chaindata not found: %s", chaindataPath)
	}

	fmt.Println("========================================")
	fmt.Println("N42 Chain Replay Tool (Lossy)")
	fmt.Println("========================================")
	fmt.Printf("Source:    %s\n", chaindataPath)
	fmt.Printf("Output:    %s\n", outputFile)
	fmt.Printf("Range:     %d → %d (0=latest)\n", fromBlock, toBlock)
	fmt.Println()

	// Open source database read-only. Use Accede() mode which tells MDBX to
	// only open tables that already exist in the database, silently skipping
	// any missing ones. This is critical for old databases that lack newer
	// tables like BlobSidecars or AccountHistoryKeys.
	srcDB, err := mdbx.NewMDBX(log2.New()).
		Path(chaindataPath).
		MapSize(2 * datasize.TB).
		Accede().
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open source database: %w", err)
	}
	defer srcDB.Close()

	start := time.Now()

	stats := &ReplayStats{
		FromBlock:   fromBlock,
		SkipReasons: make(map[string]uint64),
		Accounts:    make(map[string]string),
	}

	// Determine end block.
	err = srcDB.View(context.Background(), func(tx kv.Tx) error {
		if toBlock == 0 {
			// Verify genesis exists.
			hash, rerr := rawdb.ReadCanonicalHash(tx, 0)
			if rerr != nil {
				return fmt.Errorf("cannot read genesis hash: %w", rerr)
			}
			if hash == (types.Hash{}) {
				return fmt.Errorf("source database has no genesis block")
			}
			toBlock = replay.FindLatestBlock(tx)
		}
		stats.ToBlock = toBlock
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("Replaying blocks %d → %d (%d blocks)\n\n", fromBlock, toBlock, toBlock-fromBlock+1)

	// Process blocks. Stream JSON output to file to avoid OOM on large chains.
	balances := make(map[types.Address]*uint256.Int)

	outF, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outF.Close()

	// We write a JSON-Lines style block array to a temp file first,
	// then assemble the final JSON output with stats + blocks.
	// This keeps memory usage constant regardless of chain length.
	blocksFile, err := os.CreateTemp("", "replay-blocks-*.jsonl")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	blocksPath := blocksFile.Name()
	defer os.Remove(blocksPath)
	blocksEnc := json.NewEncoder(blocksFile)

	var blockCount uint64

	err = srcDB.View(context.Background(), func(tx kv.Tx) error {
		for num := fromBlock; num <= toBlock; num++ {
			blk, berr := rawdb.ReadBlockByNumber(tx, num)
			if berr != nil || blk == nil {
				stats.BlocksMissing++
				continue
			}

			stats.BlocksProcessed++
			txs := blk.Transactions()

			if len(txs) == 0 {
				stats.BlocksEmpty++
				continue
			}

			rb := ReplayedBlock{
				Number:    num,
				Hash:      blk.Hash().Hex(),
				Timestamp: blk.Time(),
				Coinbase:  blk.Coinbase().Hex(),
				GasUsed:   blk.GasUsed(),
			}

			for _, txn := range txs {
				stats.TxTotal++

				// Filter: skip deposit contract interactions.
				if to := txn.To(); to != nil && skipAddresses[*to] {
					stats.TxSkipped++
					stats.SkipReasons["deposit_contract"]++
					continue
				}

				// Filter: skip zero-value contract creations with no data.
				if txn.To() == nil && len(txn.Data()) == 0 {
					stats.TxSkipped++
					stats.SkipReasons["empty_create"]++
					continue
				}

				// Track the transaction.
				stats.TxReplayed++
				rb.TxHashes = append(rb.TxHashes, txn.Hash().Hex())
				rb.TxCount++

				// Track balance changes (simplified: just record value transfers).
				value := txn.Value()
				if value != nil && !value.IsZero() {
					if to := txn.To(); to != nil {
						if balances[*to] == nil {
							balances[*to] = uint256.NewInt(0)
						}
						balances[*to].Add(balances[*to], value)
					}
				}
			}

			if rb.TxCount > 0 {
				if err := blocksEnc.Encode(rb); err != nil {
					return fmt.Errorf("failed to write block %d: %w", num, err)
				}
				blockCount++
			}

			// Progress report.
			if num > 0 && uint64(batchSize) > 0 && (num-fromBlock)%uint64(batchSize) == 0 {
				elapsed := time.Since(start)
				progress := float64(num-fromBlock) / float64(toBlock-fromBlock) * 100
				bps := float64(num-fromBlock) / elapsed.Seconds()
				fmt.Printf("  Block #%-10d  %5.1f%%  |  %d tx replayed, %d skipped  |  %.0f blocks/s\n",
					num, progress, stats.TxReplayed, stats.TxSkipped, bps)
			}
		}
		return nil
	})
	blocksFile.Close()
	if err != nil {
		return fmt.Errorf("replay failed: %w", err)
	}

	stats.Duration = time.Since(start).String()

	// Convert balances to hex strings for output.
	for addr, bal := range balances {
		stats.Accounts[addr.Hex()] = bal.Hex()
	}

	// Assemble final JSON: stats header, then stream blocks from temp file.
	outF.Seek(0, 0)
	outF.Truncate(0)
	fmt.Fprintf(outF, "{\n  \"stats\": ")
	statsData, _ := json.MarshalIndent(stats, "  ", "  ")
	outF.Write(statsData)
	fmt.Fprintf(outF, ",\n  \"blocks\": [\n")

	// Copy blocks from temp file into the JSON array.
	if blockCount > 0 {
		bf, _ := os.Open(blocksPath)
		if bf != nil {
			scanner := json.NewDecoder(bf)
			var written uint64
			for scanner.More() {
				var raw json.RawMessage
				if err := scanner.Decode(&raw); err != nil {
					break
				}
				if written > 0 {
					fmt.Fprintf(outF, ",\n")
				}
				fmt.Fprintf(outF, "    ")
				outF.Write(raw)
				written++
			}
			bf.Close()
		}
	}
	fmt.Fprintf(outF, "\n  ]\n}\n")

	// Print summary.
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("REPLAY COMPLETE")
	fmt.Println("========================================")
	fmt.Printf("Duration:           %s\n", stats.Duration)
	fmt.Printf("Blocks processed:   %d\n", stats.BlocksProcessed)
	fmt.Printf("Blocks empty:       %d\n", stats.BlocksEmpty)
	fmt.Printf("Blocks missing:     %d\n", stats.BlocksMissing)
	fmt.Println("----------------------------------------")
	fmt.Printf("Transactions total: %d\n", stats.TxTotal)
	fmt.Printf("Replayed:           %d\n", stats.TxReplayed)
	fmt.Printf("Skipped:            %d\n", stats.TxSkipped)
	fmt.Println("Skip reasons:")
	for reason, count := range stats.SkipReasons {
		fmt.Printf("  %-20s %d\n", reason, count)
	}
	fmt.Printf("Unique accounts:    %d\n", len(stats.Accounts))
	fmt.Println("----------------------------------------")
	fmt.Printf("Output:             %s\n", outputFile)
	fmt.Println("========================================")

	return nil
}

// runReplayWithTarget uses internal/replay.Engine to read old blocks and write
// filtered transactions into a new target database.
func runReplayWithTarget(ctx context.Context, sourceDir, targetDir, outputFile string, fromBlock, toBlock uint64, batchSize int) error {
	fmt.Println("========================================")
	fmt.Println("N42 Chain Replay → New Database")
	fmt.Println("========================================")
	fmt.Printf("Source:    %s\n", sourceDir)
	fmt.Printf("Target:    %s\n", targetDir)
	fmt.Printf("Output:    %s\n", outputFile)
	fmt.Printf("Range:     %d → %d (0=latest)\n", fromBlock, toBlock)
	fmt.Println()

	engine, err := replay.NewEngine(replay.Config{
		SourcePath: sourceDir,
		TargetPath: targetDir,
		FromBlock:  fromBlock,
		ToBlock:    toBlock,
		BatchSize:  batchSize,
		ProgressFn: func(s *replay.Stats) {
			total := s.ToBlock - s.FromBlock + 1
			pct := float64(s.CurrentBlock-s.FromBlock) / float64(total) * 100
			fmt.Printf("  Block #%-10d  %5.1f%%  |  %d tx replayed, %d skipped  |  %.0f blocks/s\n",
				s.CurrentBlock, pct,
				s.TxReplayed.Load(), s.TxSkipped.Load(),
				s.BlocksPerSecond())
		},
	})
	if err != nil {
		return err
	}

	stats, err := engine.Run(ctx)
	if err != nil {
		fmt.Printf("\nReplay stopped: %v\n", err)
	}

	// Print summary.
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("REPLAY TO NEW DB COMPLETE")
	fmt.Println("========================================")
	fmt.Printf("Duration:           %s\n", stats.Elapsed())
	fmt.Printf("Blocks processed:   %d\n", stats.BlocksProcessed.Load())
	fmt.Printf("Blocks empty:       %d\n", stats.BlocksEmpty.Load())
	fmt.Printf("Blocks missing:     %d\n", stats.BlocksMissing.Load())
	fmt.Println("----------------------------------------")
	fmt.Printf("Transactions total: %d\n", stats.TxTotal.Load())
	fmt.Printf("Replayed:           %d\n", stats.TxReplayed.Load())
	fmt.Printf("Skipped:            %d\n", stats.TxSkipped.Load())
	fmt.Printf("Failed:             %d\n", stats.TxFailed.Load())
	fmt.Println("Skip reasons:")
	for reason, count := range stats.SkipReasons {
		fmt.Printf("  %-20s %d\n", reason, count.Load())
	}
	fmt.Printf("Speed:              %.0f blocks/s\n", stats.BlocksPerSecond())
	fmt.Println("----------------------------------------")
	fmt.Printf("Target DB:          %s/chaindata\n", targetDir)
	fmt.Println("========================================")

	return err
}
