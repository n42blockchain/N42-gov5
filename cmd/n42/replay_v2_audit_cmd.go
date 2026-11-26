// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	rootCmd = append(rootCmd, replayV2AuditCommand)
}

var replayV2AuditCommand = &cli.Command{
	Name:  "replay-v2-p1-audit",
	Usage: "Audit a profile-1 replay target as a follower: header codecs, profile-1 header rules, CommitQC and seal, committee evidence (read-only)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "Replay target datadir (opened read-only)", Required: true},
		&cli.StringFlag{Name: "p1-genesis", Usage: "The profile-1 genesis JSON the target was replayed on", Required: true},
		&cli.Uint64Flag{Name: "to", Usage: "Last block to audit (0 = head)"},
		&cli.IntFlag{Name: "workers", Usage: "Concurrent auditors, each on its own block range", Value: 1},
		&cli.Uint64Flag{Name: "chunk", Usage: "Blocks per audit range", Value: 20000},
	},
	Action: runReplayV2Audit,
}

func runReplayV2Audit(cliCtx *cli.Context) error {
	g, err := loadReplayProfile1Genesis(cliCtx.String("p1-genesis"))
	if err != nil {
		return err
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(cliCtx.String("target") + "/chaindata").Label(kv.ChainDB).
		MapSize(2 * datasize.TB).Readonly().Accede().Open(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	var (
		genesis *block.Header
		head    uint64
	)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		genesis = rawdb.ReadHeaderByNumber(tx, 0)
		if genesis == nil {
			return fmt.Errorf("target has no genesis")
		}
		head = cliCtx.Uint64("to")
		if head == 0 {
			if h := rawdb.ReadCurrentBlockNumber(tx); h != nil {
				head = *h
			}
		}
		return nil
	}); err != nil {
		return err
	}
	ap, err := replay.NewP1AuditPool(g.Config)
	if err != nil {
		return err
	}
	workers, chunk := max(1, cliCtx.Int("workers")), max(uint64(1), cliCtx.Uint64("chunk"))
	type rangeResult struct {
		from, to, txs, maxGap, headTime uint64
		err                             error
	}
	ranges := make(chan [2]uint64)
	results := make(chan rangeResult)
	start := time.Now()
	var done atomic.Uint64
	for w := 0; w < workers; w++ {
		go func() {
			for r := range ranges {
				res := rangeResult{from: r[0], to: r[1]}
				res.err = db.View(context.Background(), func(tx kv.Tx) error {
					audit := replay.NewP1AuditorWithPool(g.Config, genesis, ap)
					var prev uint64
					if r[0] > 1 {
						var seed []*block.Header
						for n := r[0] - min(r[0]-1, 16); n < r[0]; n++ {
							h := rawdb.ReadHeaderByNumber(tx, n)
							if h == nil {
								return fmt.Errorf("block %d header missing", n)
							}
							seed = append(seed, h)
						}
						audit.Resume(seed...)
						prev = seed[len(seed)-1].Time
					}
					for n := r[0]; n <= r[1]; n++ {
						blk, err := rawdb.ReadBlockByNumber(tx, n)
						if err != nil || blk == nil {
							return fmt.Errorf("block %d missing: %v", n, err)
						}
						ce, err := rawdb.ReadConsensusEvidence(tx, n)
						if err != nil {
							return fmt.Errorf("block %d evidence: %w", n, err)
						}
						if err := audit.CheckBlock(blk, ce); err != nil {
							return err
						}
						res.txs += uint64(len(blk.Transactions()))
						if t := blk.Time(); prev != 0 && t-prev > res.maxGap {
							res.maxGap = t - prev
						}
						prev = blk.Time()
						if d := done.Add(1); d%1000000 == 0 {
							log.Info("audit progress", "blocks", d, "of", head, "elapsed", time.Since(start).Round(time.Second))
						}
					}
					res.headTime = prev
					return nil
				})
				results <- res
			}
		}()
	}
	go func() {
		for f := uint64(1); f <= head; f += chunk {
			ranges <- [2]uint64{f, min(head, f+chunk-1)}
		}
		close(ranges)
	}()
	var txs, maxGap, headTime uint64
	var firstErr error
	for f := uint64(1); f <= head; f += chunk {
		r := <-results
		if r.err != nil && (firstErr == nil) {
			firstErr = fmt.Errorf("FAIL in range %d-%d: %w", r.from, r.to, r.err)
		}
		txs += r.txs
		maxGap = max(maxGap, r.maxGap)
		if r.to == head {
			headTime = r.headTime
		}
	}
	if firstErr != nil {
		return firstErr
	}
	pc := g.Config.HotStuff.CommitteePool
	fmt.Printf("PASS: %d blocks (%d txs) audited in %s with %d workers: codecs, profile-1 header rules, vote gate, CommitQC + seal, body commitments, committee evidence (pool %d, committee %d); max slot gap %ds; genesis %s head time %d\n",
		head, txs, time.Since(start).Round(time.Second), workers, pc.PoolSize, pc.CommitteeSize, maxGap, genesis.Hash().Hex(), headTime)
	return nil
}
