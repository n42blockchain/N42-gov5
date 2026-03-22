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
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Deposit contract addresses to skip during replay.
var skipAddresses = map[types.Address]bool{
	types.HexToAddress("0x85A5E24ef94fe5bDD5055133E6bd00DcEA25F37D"): true, // DepositContract
	types.HexToAddress("0xF762E4Aa8Da0B9FC8113ECBFf6c84B3a6B7B5544"): true, // DepositNFTContract
	types.HexToAddress("0x8018c0ba6717FE077cB37Db5D6187B400ee76Eeb"): true, // DepositFUJIContract
}

func init() {
	rootCmd = append(rootCmd, replayCommand)
}

var replayCommand = &cli.Command{
	Name:  "replay",
	Usage: "Replay old chain transactions and export to JSON (lossy — skips incompatible txs)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "source",
			Usage:    "Source chain data directory (old mainnet)",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "output",
			Usage: "Output JSON file path (default: replay_result.json)",
			Value: "replay_result.json",
		},
		&cli.Uint64Flag{
			Name:  "from",
			Usage: "Start block number (default: 1)",
			Value: 1,
		},
		&cli.Uint64Flag{
			Name:  "to",
			Usage: "End block number (0 = latest)",
			Value: 0,
		},
		&cli.IntFlag{
			Name:  "batch",
			Usage: "Blocks per progress report",
			Value: 10000,
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
	// NOTE: Do NOT call modules.N42Init() or set kv.ChaindataTablesCfg here.
	// The source database may be an older version without newer tables (e.g.,
	// BlobSidecars). Opening without table config lets MDBX read existing
	// tables without trying to create missing ones.

	sourceDir := ctx.String("source")
	outputFile := ctx.String("output")
	fromBlock := ctx.Uint64("from")
	toBlock := ctx.Uint64("to")
	batchSize := ctx.Int("batch")

	// Validate source directory.
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
			// Find latest block.
			hash, rerr := rawdb.ReadCanonicalHash(tx, 0)
			if rerr != nil {
				return fmt.Errorf("cannot read genesis hash: %w", rerr)
			}
			if hash == (types.Hash{}) {
				return fmt.Errorf("source database has no genesis block")
			}
			// Binary search for latest block.
			low, high := uint64(0), uint64(100_000_000)
			for low < high {
				mid := (low + high + 1) / 2
				h, _ := rawdb.ReadCanonicalHash(tx, mid)
				if h == (types.Hash{}) {
					high = mid - 1
				} else {
					low = mid
				}
			}
			toBlock = low
		}
		stats.ToBlock = toBlock
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("Replaying blocks %d → %d (%d blocks)\n\n", fromBlock, toBlock, toBlock-fromBlock+1)

	// Process blocks.
	var replayedBlocks []ReplayedBlock
	balances := make(map[types.Address]*uint256.Int)

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
				replayedBlocks = append(replayedBlocks, rb)
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
	if err != nil {
		return fmt.Errorf("replay failed: %w", err)
	}

	stats.Duration = time.Since(start).String()

	// Convert balances to hex strings for output.
	for addr, bal := range balances {
		stats.Accounts[addr.Hex()] = bal.Hex()
	}

	// Write output.
	output := map[string]interface{}{
		"stats":  stats,
		"blocks": replayedBlocks,
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal output: %w", err)
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

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
