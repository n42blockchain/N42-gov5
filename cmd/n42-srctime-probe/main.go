// Command n42-srctime-probe prints source block timestamps + the gap-fill count
// that replay-v2 would synthesize between consecutive blocks, to tell real sparse
// gaps apart from a timestamp misread (units/field).
package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func blkTime(b block.IBlock) uint64 { return b.Header().(*block.Header).Time }

func main() {
	src := flag.String("source", "D:/mainnet/mainnet", "source chain datadir")
	period := flag.Uint64("period", 8, "gap-fill period (s)")
	tol := flag.Uint64("tol", 15, "gap tolerance (s)")
	flag.Parse()

	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(filepath.Join(*src, "chaindata")).
		Label(kv.ChainDB).MapSize(2 * datasize.TB).Accede().Readonly().Open(context.Background())
	if err != nil {
		panic(err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	nums := []uint64{0, 1, 2, 3, 4, 5, 10, 100, 1000, 5000}
	var prev uint64
	fmt.Printf("%-8s %-14s %-12s %-10s\n", "block", "timestamp", "delta(s)", "fillBlocks")
	for _, n := range nums {
		b, _ := rawdb.ReadBlockByNumber(tx, n)
		if b == nil {
			fmt.Printf("%-8d <nil>\n", n)
			continue
		}
		t := blkTime(b)
		delta := int64(t) - int64(prev)
		fill := uint64(0)
		if prev != 0 && int64(t) > int64(prev) && uint64(delta) > *tol {
			fill = (uint64(delta) - 1) / *period
		}
		fmt.Printf("%-8d %-14d %-12d %-10d\n", n, t, delta, fill)
		prev = t
	}
	// Exhaustive: sum the gap-fill over EVERY consecutive pair 0..upto, exactly as
	// the engine would (prevTime updated to each block's real time).
	upto := flag.Uint64("dummy", 0, "")
	_ = upto
	scanTo := uint64(5000)
	var prevT uint64
	var totalFill, bigGaps uint64
	var maxGap uint64
	maxGapAt := uint64(0)
	for n := uint64(0); n <= scanTo; n++ {
		b, _ := rawdb.ReadBlockByNumber(tx, n)
		if b == nil {
			continue
		}
		ct := blkTime(b)
		if prevT != 0 && ct > prevT {
			d := ct - prevT
			if d > *tol {
				f := (d - 1) / *period
				totalFill += f
				if f > 10 {
					bigGaps++
				}
				if d > maxGap {
					maxGap = d
					maxGapAt = n
				}
			}
		}
		prevT = ct
	}
	fmt.Printf("\nEXHAUSTIVE 0..%d (prevTime tracked per block, period=%d tol=%d):\n", scanTo, *period, *tol)
	fmt.Printf("  total fill blocks = %d   (gaps>10 blocks: %d)   maxGap=%ds at block %d (=%d fill)\n",
		totalFill, bigGaps, maxGap, maxGapAt, (maxGap-1) / *period)
	fmt.Printf("  => head if filled = %d + %d real = %d\n", totalFill, scanTo, totalFill+scanTo)
}
