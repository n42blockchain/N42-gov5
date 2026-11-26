// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"

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
	return db.View(context.Background(), func(tx kv.Tx) error {
		genesis := rawdb.ReadHeaderByNumber(tx, 0)
		if genesis == nil {
			return fmt.Errorf("target has no genesis")
		}
		audit, err := replay.NewP1Auditor(g.Config, genesis)
		if err != nil {
			return err
		}
		head := cliCtx.Uint64("to")
		if head == 0 {
			if h := rawdb.ReadCurrentBlockNumber(tx); h != nil {
				head = *h
			}
		}
		start := time.Now()
		var txs, maxGap uint64
		var prev uint64
		for n := uint64(1); n <= head; n++ {
			blk, err := rawdb.ReadBlockByNumber(tx, n)
			if err != nil || blk == nil {
				return fmt.Errorf("block %d missing: %v", n, err)
			}
			ce, err := rawdb.ReadConsensusEvidence(tx, n)
			if err != nil {
				return fmt.Errorf("block %d evidence: %w", n, err)
			}
			if err := audit.CheckBlock(blk, ce); err != nil {
				return fmt.Errorf("FAIL after %d blocks: %w", n-1, err)
			}
			txs += uint64(len(blk.Transactions()))
			if t := blk.Time(); prev != 0 && t-prev > maxGap {
				maxGap = t - prev
			}
			prev = blk.Time()
		}
		fmt.Printf("PASS: %d blocks (%d txs) audited in %s: codecs, profile-1 header rules, vote gate, CommitQC + seal, committee evidence (pool %d, committee %d, active at head %d); max slot gap %ds; genesis %s head time %d\n",
			head, txs, time.Since(start).Round(time.Second), g.Config.HotStuff.CommitteePool.PoolSize,
			g.Config.HotStuff.CommitteePool.CommitteeSize, audit.ActiveAt(head), maxGap, genesis.Hash().Hex(), prev)
		return nil
	})
}
