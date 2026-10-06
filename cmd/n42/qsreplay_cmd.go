// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// qs-replay is the single-node offline bench for the follower import path
// (docs/QS_REPLAN_2026-09-09.md section 4.0). It rewinds the applied QMDB
// state N blocks through the undo window, then re-imports those blocks one at
// a time through InsertChainAuthorized -- the same path a follower takes for a
// block pushed by the leader -- and reports the wall time per block. The
// per-phase breakdown comes from the "blockimport phases" log line; run with
// N42_SLOW_BLOCK_MS=0 to get it for every block.
//
// The datadir is mutated (rewind + re-execute) but ends where it started; use
// it on a copy (cp --reflink on xfs is instant), never on a fleet member's
// live datadir.
var qsReplayCommand = &cli.Command{
	Name:  "qs-replay",
	Usage: "Rewind the last N blocks through the QMDB undo window and re-import them (offline follower-path bench)",
	Flags: []cli.Flag{
		DataDirFlag,
		ChainFlag,
		ProfileFlag,
		&cli.IntFlag{Name: "replay.depth", Value: 60, Usage: "blocks to rewind and re-import (must be within the 256-block undo window)"},
		&cli.IntFlag{Name: "replay.repeat", Value: 1, Usage: "rewind+replay cycles"},
		&cli.IntFlag{Name: "replay.min-txs", Value: 100000, Usage: "blocks with at least this many transactions form the reported set"},
		&cli.BoolFlag{Name: "replay.scan", Usage: "only list the last replay.depth blocks (number, txs, gas) and the applied marker"},
		&cli.IntFlag{Name: "pprof.maxcpu", Value: 0, Usage: "GOMAXPROCS for the replay (0 = all)", Destination: &DefaultConfig.PprofCfg.MaxCpu},
	},
	Action: qsReplay,
}

func qsReplay(cliCtx *cli.Context) error {
	depth := uint64(cliCtx.Int("replay.depth"))
	repeat := cliCtx.Int("replay.repeat")
	minTxs := cliCtx.Int("replay.min-txs")
	if depth == 0 || depth > 256 {
		return fmt.Errorf("replay.depth must be 1..256 (undo window), got %d", depth)
	}

	stack, err := node.NewNode(cliCtx, &DefaultConfig)
	if err != nil {
		return err
	}
	defer stack.Close()
	bc, ok := stack.BlockChain().(*internal.BlockChain)
	if !ok {
		return fmt.Errorf("qs-replay needs the native BlockChain, got %T", stack.BlockChain())
	}
	db := stack.Database()
	// A fleet member wires the reward function when it authorizes the miner;
	// Finalize needs it on the import path too (the first replay stopped at
	// "reward function not set"). Same delegate as authorizeMiningEngine.
	if hs, ok := stack.Engine().(*hotstuff.HotStuff); ok {
		hs.SetRewardFunc(func(chainCfg *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, chain consensus.N42ChainHeaderReader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
			return apos.DoReward(chainCfg, ibs, header, chain)
		})
	}

	// The range is the last <depth> blocks of the STORED chain. The applied
	// marker may already sit below the head (a previous replay that stopped
	// after its unwind): then the unwind is skipped, not repeated -- the undo
	// window is 256 blocks from the head, and a second unwind below it fails.
	head, headHash, err := qsReplayStoredHead(cliCtx, db)
	if err != nil {
		return err
	}
	applied, appliedHash, err := qsReplayAppliedHead(cliCtx, db)
	if err != nil {
		return err
	}
	if head < depth+1 {
		return fmt.Errorf("stored head %d too low for depth %d", head, depth)
	}
	base := head - depth
	if applied < base {
		return fmt.Errorf("applied marker %d is below base %d: choose replay.depth >= %d", applied, base, head-applied)
	}
	baseHash, err := qsReplayCanonical(cliCtx, db, base)
	if err != nil {
		return err
	}

	// Only hashes and transaction counts are held across the run: a decoded
	// full block is ~160 MB, and holding the whole range under the fleet's
	// 10 GiB GOMEMLIMIT turned the first replay into a GC thrash (import wall
	// climbing 2.2 -> 7.9 s over eleven blocks). Each block is re-read from
	// the store right before its import, as a follower decodes a pushed body.
	type ref struct {
		n    uint64
		hash types.Hash
		txs  int
	}
	refs := make([]ref, 0, depth)
	for n := base + 1; n <= head; n++ {
		blk, err := bc.GetBlockByNumber(uint256.NewInt(n))
		if err != nil || blk == nil {
			return fmt.Errorf("block %d not readable: %v", n, err)
		}
		refs = append(refs, ref{n: n, hash: blk.Hash(), txs: len(blk.Transactions())})
		if cliCtx.Bool("replay.scan") {
			h := blk.Hash()
			fmt.Printf("  %d txs=%d gas=%d root=%x hash=%x\n", n, len(blk.Transactions()), blk.GasUsed(), blk.StateRoot().Bytes()[:8], h[:8])
		}
	}
	if last := refs[len(refs)-1].hash; last != headHash {
		return fmt.Errorf("canonical block %d is %x but the stored head is %x", head, last[:8], headHash[:8])
	}
	fmt.Printf("qs-replay: stored head %d (%x), applied %d (%x), base %d (%x), %d blocks\n",
		head, headHash[:8], applied, appliedHash[:8], base, baseHash[:8], len(refs))
	if cliCtx.Bool("replay.scan") {
		return nil
	}

	// The unwind refuses to revert a 2-chain-committed block. Offline, the
	// consensus marker is meaningless; pin the floor at the base so the range
	// above it is revertible. (On the copy only -- this is why the tool must
	// never run against a live datadir.)
	if err := db.Update(cliCtx.Context, func(tx kv.RwTx) error {
		return rawdb.WriteHotStuffCommittedHead(tx, baseHash)
	}); err != nil {
		return fmt.Errorf("pin committed floor at %d: %w", base, err)
	}

	type sample struct {
		n    uint64
		txs  int
		wall time.Duration
	}
	for cycle := 1; cycle <= repeat; cycle++ {
		tUnwind := time.Now()
		// HasAppliedBlock answers "on the applied lineage"; the skip needs
		// "the applied head IS the base" (cycle 2 of the first run skipped its
		// unwind because the base was an ancestor of the head).
		if !bc.AppliedHeadIsExactly(baseHash, base) {
			if err := bc.AlignAppliedBranch(base+1, baseHash); err != nil {
				return fmt.Errorf("cycle %d: unwind to %d: %w", cycle, base, err)
			}
		}
		dUnwind := time.Since(tUnwind)
		if !bc.AppliedHeadIsExactly(baseHash, base) {
			return fmt.Errorf("cycle %d: unwind did not land on %d", cycle, base)
		}
		fmt.Printf("cycle %d: applied state at %d after %s\n", cycle, base, dUnwind.Round(time.Millisecond))

		samples := make([]sample, 0, len(refs))
		tAll := time.Now()
		for _, r := range refs {
			blk, err := bc.GetBlockByNumber(uint256.NewInt(r.n))
			if err != nil || blk == nil || blk.Hash() != r.hash {
				return fmt.Errorf("cycle %d: block %d changed under the replay: %v", cycle, r.n, err)
			}
			t := time.Now()
			if _, err := bc.InsertChainAuthorized([]block.IBlock{blk}); err != nil {
				return fmt.Errorf("cycle %d: import %d: %w", cycle, r.n, err)
			}
			d := time.Since(t)
			if !bc.HasAppliedBlock(r.hash, r.n) {
				return fmt.Errorf("cycle %d: block %d imported but not applied", cycle, r.n)
			}
			samples = append(samples, sample{n: r.n, txs: r.txs, wall: d})
			fmt.Printf("  %d txs=%d wall=%s\n", r.n, r.txs, d.Round(time.Millisecond))
		}
		dAll := time.Since(tAll)

		// Report: the full-block set (>= min-txs) is what the fleet's B legs
		// measure; per-transaction cost is the number to compare across builds.
		var full []time.Duration
		var fullTxs int
		for _, s := range samples {
			if s.txs >= minTxs {
				full = append(full, s.wall)
				fullTxs += s.txs
			}
		}
		fmt.Printf("cycle %d: %d blocks in %s (%.0f ms/block)\n", cycle, len(samples), dAll.Round(time.Millisecond), float64(dAll.Milliseconds())/float64(len(samples)))
		if len(full) > 0 {
			sort.Slice(full, func(i, j int) bool { return full[i] < full[j] })
			var sum time.Duration
			for _, d := range full {
				sum += d
			}
			mean := sum / time.Duration(len(full))
			fmt.Printf("cycle %d: full blocks (>=%d txs): %d, mean %s, median %s, p90 %s, %.2f us/tx\n",
				cycle, minTxs, len(full), mean.Round(time.Millisecond), full[len(full)/2].Round(time.Millisecond),
				full[len(full)*9/10].Round(time.Millisecond), float64(sum.Microseconds())/float64(fullTxs))
		}
	}
	fmt.Fprintln(os.Stderr, "qs-replay: done; per-phase lines are the \"blockimport phases\" entries in the node log")
	return nil
}

func qsReplayAppliedHead(cliCtx *cli.Context, db kv.RwDB) (uint64, types.Hash, error) {
	var (
		num  uint64
		hash types.Hash
	)
	err := db.View(cliCtx.Context, func(tx kv.Tx) error {
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no QMDB applied marker: not a QMDB datadir")
		}
		num, hash = n, h
		return nil
	})
	return num, hash, err
}

func qsReplayStoredHead(cliCtx *cli.Context, db kv.RwDB) (uint64, types.Hash, error) {
	var (
		num  uint64
		hash types.Hash
	)
	err := db.View(cliCtx.Context, func(tx kv.Tx) error {
		hn := rawdb.ReadCurrentBlockNumber(tx)
		if hn == nil {
			return fmt.Errorf("no stored head")
		}
		h, err := rawdb.ReadCanonicalHash(tx, *hn)
		if err != nil {
			return err
		}
		num, hash = *hn, h
		return nil
	})
	return num, hash, err
}

func qsReplayCanonical(cliCtx *cli.Context, db kv.RwDB, n uint64) (types.Hash, error) {
	var h types.Hash
	err := db.View(cliCtx.Context, func(tx kv.Tx) error {
		var err error
		h, err = rawdb.ReadCanonicalHash(tx, n)
		return err
	})
	if err == nil && h == (types.Hash{}) {
		err = fmt.Errorf("no canonical hash at %d", n)
	}
	return h, err
}
