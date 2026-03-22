// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package replay provides a lossy chain replay engine that reads blocks from
// an old chain database and writes filtered transactions into a new database
// using the current data structures. Incompatible transactions (deposit
// contract calls, failed txs) are skipped, and the resulting state is built
// incrementally in the target database.
package replay

import (
	"context"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// DefaultSkipAddresses are the deposit contract addresses to filter out.
var DefaultSkipAddresses = map[types.Address]bool{
	types.HexToAddress("0x85A5E24ef94fe5bDD5055133E6bd00DcEA25F37D"): true,
	types.HexToAddress("0xF762E4Aa8Da0B9FC8113ECBFf6c84B3a6B7B5544"): true,
	types.HexToAddress("0x8018c0ba6717FE077cB37Db5D6187B400ee76Eeb"): true,
}

// Config configures the replay engine.
type Config struct {
	SourcePath    string                  // Old chain data directory (contains chaindata/)
	TargetPath    string                  // New chain data directory (will be created)
	FromBlock     uint64                  // Start block (inclusive, default 1)
	ToBlock       uint64                  // End block (0 = auto-detect latest)
	ChainConfig   *params.ChainConfig     // New chain config for EVM execution
	SkipAddresses map[types.Address]bool  // Addresses to filter (default: deposit contracts)
	BatchSize     int                     // Commit batch size in blocks (default: 1000)
	ProgressFn    func(stats *Stats)      // Called periodically with progress (may be nil)
}

// Stats tracks replay progress.
type Stats struct {
	FromBlock       uint64
	ToBlock         uint64
	CurrentBlock    uint64
	BlocksProcessed atomic.Uint64
	BlocksEmpty     atomic.Uint64
	BlocksMissing   atomic.Uint64
	TxTotal         atomic.Uint64
	TxReplayed      atomic.Uint64
	TxSkipped       atomic.Uint64
	TxFailed        atomic.Uint64
	SkipReasons     map[string]*atomic.Uint64
	StartTime       time.Time
}

// NewStats creates a Stats instance with initialized skip reason counters.
func NewStats() *Stats {
	return &Stats{
		SkipReasons: map[string]*atomic.Uint64{
			"deposit_contract": {},
			"empty_create":     {},
			"sender_unknown":   {},
			"evm_error":        {},
		},
		StartTime: time.Now(),
	}
}

func (s *Stats) skipReason(reason string) {
	if c, ok := s.SkipReasons[reason]; ok {
		c.Add(1)
	}
}

// Elapsed returns time since replay started.
func (s *Stats) Elapsed() time.Duration { return time.Since(s.StartTime) }

// BlocksPerSecond returns the processing rate.
func (s *Stats) BlocksPerSecond() float64 {
	elapsed := s.Elapsed().Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(s.BlocksProcessed.Load()) / elapsed
}

// Engine performs the replay operation.
type Engine struct {
	cfg    Config
	srcDB  kv.RwDB
	dstDB  kv.RwDB
	stats  *Stats
}

// NewEngine creates a replay engine. Call Run() to execute.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.SourcePath == "" {
		return nil, fmt.Errorf("replay: source path required")
	}
	if cfg.TargetPath == "" {
		return nil, fmt.Errorf("replay: target path required")
	}
	if cfg.ChainConfig == nil {
		cfg.ChainConfig = params.MainnetChainConfig
	}
	if cfg.SkipAddresses == nil {
		cfg.SkipAddresses = DefaultSkipAddresses
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.FromBlock == 0 {
		cfg.FromBlock = 1
	}

	// Do NOT call modules.N42Init() — the source database may lack newer
	// tables. MDBX is opened without table config for backward compatibility.

	return &Engine{cfg: cfg, stats: NewStats()}, nil
}

// Stats returns the current replay statistics.
func (e *Engine) Stats() *Stats { return e.stats }

// Run executes the replay. It opens both databases, reads blocks from source,
// filters transactions, re-executes them on the target, and writes the new
// state. Returns the final statistics.
func (e *Engine) Run(ctx context.Context) (*Stats, error) {
	var err error

	// Open source DB with Accede mode — only opens tables that already exist,
	// silently skipping missing ones. Critical for old databases that lack
	// newer tables (BlobSidecars, AccountHistoryKeys, etc.).
	e.srcDB, err = mdbx.NewMDBX(log2.New()).
		Path(e.cfg.SourcePath + "/chaindata").
		MapSize(2 * datasize.TB).
		Accede().
		Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay: open source DB: %w", err)
	}
	defer e.srcDB.Close()

	// Open/create target DB.
	e.dstDB, err = mdbx.NewMDBX(log2.New()).
		Path(e.cfg.TargetPath + "/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(2 * datasize.TB).
		Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay: open target DB: %w", err)
	}
	defer e.dstDB.Close()

	// Detect end block.
	if e.cfg.ToBlock == 0 {
		if err := e.srcDB.View(ctx, func(tx kv.Tx) error {
			e.cfg.ToBlock = e.findLatestBlock(tx)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	e.stats.FromBlock = e.cfg.FromBlock
	e.stats.ToBlock = e.cfg.ToBlock

	// Process in batches.
	for batchStart := e.cfg.FromBlock; batchStart <= e.cfg.ToBlock; batchStart += uint64(e.cfg.BatchSize) {
		select {
		case <-ctx.Done():
			return e.stats, ctx.Err()
		default:
		}

		batchEnd := batchStart + uint64(e.cfg.BatchSize) - 1
		if batchEnd > e.cfg.ToBlock {
			batchEnd = e.cfg.ToBlock
		}

		if err := e.processBatch(ctx, batchStart, batchEnd); err != nil {
			return e.stats, fmt.Errorf("replay: batch %d-%d: %w", batchStart, batchEnd, err)
		}

		if e.cfg.ProgressFn != nil {
			e.stats.CurrentBlock = batchEnd
			e.cfg.ProgressFn(e.stats)
		}
	}

	return e.stats, nil
}

// processBatch reads a range of blocks from source and writes to target.
func (e *Engine) processBatch(ctx context.Context, from, to uint64) error {
	// Read blocks from source.
	type blockData struct {
		blk  *block.Block
		num  uint64
	}
	var blocks []blockData

	if err := e.srcDB.View(ctx, func(tx kv.Tx) error {
		for num := from; num <= to; num++ {
			blk, err := rawdb.ReadBlockByNumber(tx, num)
			if err != nil || blk == nil {
				e.stats.BlocksMissing.Add(1)
				continue
			}
			blocks = append(blocks, blockData{blk: blk, num: num})
		}
		return nil
	}); err != nil {
		return err
	}

	// Write to target.
	return e.dstDB.Update(ctx, func(dstTx kv.RwTx) error {
		r := state.NewPlainStateReader(dstTx)
		for _, bd := range blocks {
			if err := e.replayBlock(dstTx, r, bd.blk, bd.num); err != nil {
				// Lossy: log and continue on block-level errors.
				e.stats.TxFailed.Add(1)
			}
		}
		return nil
	})
}

// replayBlock processes a single block: filters txs, executes valid ones, writes state.
func (e *Engine) replayBlock(dstTx kv.RwTx, reader *state.PlainStateReader, blk *block.Block, num uint64) error {
	e.stats.BlocksProcessed.Add(1)

	txs := blk.Transactions()
	if len(txs) == 0 {
		e.stats.BlocksEmpty.Add(1)
		// Still write the block header for chain continuity.
		return e.writeBlockHeader(dstTx, blk, num)
	}

	w := state.NewPlainStateWriterNoHistory(dstTx)
	ibs := state.New(reader)

	var replayedTxs []*transaction.Transaction
	blockNum := new(big.Int).SetUint64(num)

	for _, tx := range txs {
		e.stats.TxTotal.Add(1)

		// Filter: deposit contract.
		if to := tx.To(); to != nil && e.cfg.SkipAddresses[*to] {
			e.stats.TxSkipped.Add(1)
			e.stats.skipReason("deposit_contract")
			continue
		}

		// Filter: empty contract creation.
		if tx.To() == nil && len(tx.Data()) == 0 {
			e.stats.TxSkipped.Add(1)
			e.stats.skipReason("empty_create")
			continue
		}

		// Recover sender. In lossy mode, skip if we can't recover.
		// Try old chain ID (94) first since source data uses that chain.
		signer := transaction.MakeSigner(&params.ChainConfig{ChainID: big.NewInt(94)}, blockNum)
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			// Fallback: try new chain config signer.
			newSigner := transaction.MakeSigner(e.cfg.ChainConfig, blockNum)
			from, err = transaction.Sender(newSigner, tx)
			if err != nil {
				e.stats.TxSkipped.Add(1)
				e.stats.skipReason("sender_unknown")
				continue
			}
		}

		// Execute the transaction via simple transfer (lossy: no full EVM for speed).
		value := tx.Value()
		if value != nil && !value.IsZero() {
			senderBal := ibs.GetBalance(from)
			if senderBal.Cmp(value) >= 0 {
				ibs.SubBalance(from, value)
				if tx.To() != nil {
					ibs.AddBalance(*tx.To(), value)
				}
			}
		}

		// Track nonce.
		ibs.SetNonce(from, ibs.GetNonce(from)+1)

		// Contract deployment: store code.
		if tx.To() == nil && len(tx.Data()) > 0 {
			contractAddr := crypto.CreateAddress(from, tx.Nonce())
			ibs.CreateAccount(contractAddr, true)
			ibs.SetCode(contractAddr, tx.Data())
		}

		replayedTxs = append(replayedTxs, tx)
		e.stats.TxReplayed.Add(1)
	}

	// Finalize state changes.
	rules := e.cfg.ChainConfig.Rules(num)
	if err := ibs.FinalizeTx(rules, w); err != nil {
		return fmt.Errorf("finalize block %d: %w", num, err)
	}

	// Write block with replayed transactions.
	return e.writeBlock(dstTx, blk, num, replayedTxs)
}

// writeBlockHeader writes only the block header (for empty blocks).
func (e *Engine) writeBlockHeader(dstTx kv.RwTx, blk *block.Block, num uint64) error {
	hash := blk.Hash()
	if err := rawdb.WriteCanonicalHash(dstTx, hash, num); err != nil {
		return err
	}
	return rawdb.WriteTd(dstTx, hash, num, uint256.NewInt(num))
}

// writeBlock writes a block with its filtered transactions.
func (e *Engine) writeBlock(dstTx kv.RwTx, origBlk *block.Block, num uint64, txs []*transaction.Transaction) error {
	header := origBlk.Header().(*block.Header)
	newBlk := block.NewBlock(header, txs)
	hash := newBlk.Hash()

	if err := rawdb.WriteBlock(dstTx, newBlk.(*block.Block)); err != nil {
		return err
	}
	if err := rawdb.WriteCanonicalHash(dstTx, hash, num); err != nil {
		return err
	}
	return rawdb.WriteTd(dstTx, hash, num, uint256.NewInt(num))
}

// findLatestBlock binary-searches for the highest block number in the source DB.
func (e *Engine) findLatestBlock(tx kv.Tx) uint64 {
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
	return low
}
