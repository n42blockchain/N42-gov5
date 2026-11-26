// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Command replay-p1-scan reads a replay source chain (chain 94) read-only and
// measures what re-deriving its headers to the mixed production standard,
// profile 1, would change: the 8 s slot grid (AM27), a fixed gas limit (A23),
// the EIP-1559 base fee from the parent header with deferred gasUsed (B14,
// A10), the coinbase = leader rule (B3) and the block size bound (B15). It
// executes nothing; tx fee checks use the source gasUsed as the execution
// gasUsed, which replay reproduces for all but ~0.02% of blocks.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func main() {
	src := flag.String("source", "", "source datadir (read-only)")
	to := flag.Uint64("to", 0, "last source block (0 = head)")
	interval := flag.Uint64("interval", 8, "slot interval in seconds")
	gasLimit := flag.Uint64("gaslimit", 0, "fixed gas limit (0 = max source gasLimit, two passes)")
	flag.Parse()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(*src + "/chaindata").Label(kv.ChainDB).
		MapSize(2 * datasize.TB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	cfg := params.MainnetQMDBStaggeredChainConfig
	err = db.View(context.Background(), func(tx kv.Tx) error {
		head := *to
		if head == 0 {
			h := rawdb.ReadCurrentBlockNumber(tx)
			if h == nil {
				return fmt.Errorf("no head")
			}
			head = *h
		}
		g, _ := rawdb.ReadBlockByNumber(tx, 0)
		if g == nil {
			return fmt.Errorf("no genesis")
		}
		gh := g.Header().(*block.Header)
		G := gh.Time

		L := *gasLimit
		var maxLimit, minLimit, maxUsed uint64 = 0, ^uint64(0), 0
		if L == 0 {
			for n := uint64(1); n <= head; n++ {
				h := rawdb.ReadHeaderByNumber(tx, n)
				if h == nil {
					continue
				}
				hh := h
				maxLimit = max(maxLimit, hh.GasLimit)
				minLimit = min(minLimit, hh.GasLimit)
				maxUsed = max(maxUsed, hh.GasUsed)
			}
			L = maxLimit
		}
		fmt.Printf("source head %d genesis time %d gasLimit min %d max %d, gasUsed max %d; fixed L=%d\n", head, G, minLimit, maxLimit, maxUsed, L)

		var (
			slot, maxDrift, lastTime, lastSrc   uint64
			driftBlocks, missing                uint64
			txs, txBlocks, legacyLow, dynLow    uint64
			dynPriceChanged, maxSize, overLimit uint64
			baseLow                             uint64 // source baseFee < new baseFee
			gasUsedPrev, gasUsedPrev2           uint64
			driftAtMillion                      []uint64
			coinbases                           = map[[20]byte]uint64{}
		)
		baseFee := new(big.Int).SetUint64(params.InitialBaseFee)
		if gh.BaseFee != nil {
			baseFee = gh.BaseFee.ToBig()
		}
		minBase := new(big.Int).Set(baseFee)
		parentGasField := gh.GasUsed // header 1 carries genesis's gasUsed (A10)
		_ = gasUsedPrev2
		for n := uint64(1); n <= head; n++ {
			blk, _ := rawdb.ReadBlockByNumber(tx, n)
			if blk == nil {
				missing++
				continue
			}
			h := blk.Header().(*block.Header)
			// Slot grid: s = max(sP+1, ceil((t-G)/I)), stamped G + s*I.
			want := uint64(0)
			if h.Time > G {
				want = (h.Time - G + *interval - 1) / *interval
			}
			slot = max(slot+1, want)
			st := G + slot**interval
			if st > h.Time {
				d := st - h.Time
				driftBlocks++
				maxDrift = max(maxDrift, d)
			}
			lastTime, lastSrc = st, h.Time
			if n%1000000 == 0 {
				driftAtMillion = append(driftAtMillion, st-min(st, h.Time))
			}
			// Base fee from the parent header as written: parent's gasUsed field is
			// the grandparent's execution under k=1.
			if n > 1 {
				parent := &block.Header{Number: uint256.NewInt(n - 1), GasLimit: L, GasUsed: parentGasField, BaseFee: uint256.MustFromBig(baseFee)}
				baseFee = misc.CalcBaseFee(cfg, parent)
				parentGasField = gasUsedPrev
			}
			if baseFee.Cmp(minBase) < 0 {
				minBase.Set(baseFee)
			}
			if h.BaseFee != nil && h.BaseFee.ToBig().Cmp(baseFee) < 0 {
				baseLow++
			}
			gasUsedPrev = h.GasUsed
			bt := blk.Transactions()
			if len(bt) > 0 {
				txBlocks++
				coinbases[h.Coinbase]++
			}
			for _, t := range bt {
				txs++
				switch t.Type() {
				case 2, 3, 4:
					if t.GasFeeCap().ToBig().Cmp(baseFee) < 0 {
						dynLow++
					}
					if h.BaseFee != nil {
						old := new(big.Int).Add(h.BaseFee.ToBig(), t.GasTipCap().ToBig())
						old = bigMin(old, t.GasFeeCap().ToBig())
						nw := bigMin(new(big.Int).Add(baseFee, t.GasTipCap().ToBig()), t.GasFeeCap().ToBig())
						if old.Cmp(nw) != 0 {
							dynPriceChanged++
						}
					}
				default:
					if t.GasPrice().ToBig().Cmp(baseFee) < 0 {
						legacyLow++
					}
				}
			}
			if h.GasUsed > L {
				overLimit++
			}
			if len(bt) > 0 {
				if enc, err := rlp.EncodeToBytes(blk); err == nil && uint64(len(enc)) > maxSize {
					maxSize = uint64(len(enc))
				}
			}
		}
		fmt.Printf("grid I=%ds: blocks stamped after their source time %d, max drift %ds, head slot time %d vs source %d (drift %ds)\n",
			*interval, driftBlocks, maxDrift, lastTime, lastSrc, int64(lastTime)-int64(lastSrc))
		fmt.Printf("drift at each 1M: %v\n", driftAtMillion)
		fmt.Printf("txs %d in %d blocks (distinct coinbases %d); missing blocks %d\n", txs, txBlocks, len(coinbases), missing)
		fmt.Printf("base fee: final %s min %s; txs below new baseFee: legacy/type1 %d, dynamic %d; dynamic effective price changed %d; source baseFee < new %d blocks\n",
			baseFee, minBase, legacyLow, dynLow, dynPriceChanged, baseLow)
		fmt.Printf("blocks gasUsed > L: %d; max block size %d bytes\n", overLimit, maxSize)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func bigMin(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}
