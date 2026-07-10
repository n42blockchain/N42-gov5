// Command qs-canon-probe prints, for one or more chaindata dirs, the canonical
// hash + header presence at a given height range — used to diagnose canonical
// divergence across stopped HotStuff validator node DBs (same-height competing
// blocks after view changes).
//
//	qs-canon-probe -from 13013137 -to 13013141 E:/qs-node0/chaindata E:/qs-node1/chaindata ...
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	from := flag.Uint64("from", 0, "first height")
	to := flag.Uint64("to", 0, "last height (inclusive)")
	walk := flag.Bool("walk", false, "walk parent chain down from HeadBlockHash; print derived vs canonical hash over [from,to]")
	flag.Parse()
	if flag.NArg() == 0 || *to < *from {
		fmt.Fprintln(os.Stderr, "usage: qs-canon-probe -from N -to M <chaindata>...")
		os.Exit(1)
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	for _, dir := range flag.Args() {
		db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
			MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
		if err != nil {
			fmt.Printf("%s: open: %v\n", dir, err)
			continue
		}
		tx, err := db.BeginRo(context.Background())
		if err != nil {
			fmt.Printf("%s: begin: %v\n", dir, err)
			db.Close()
			continue
		}
		head := rawdb.ReadCurrentBlockNumber(tx)
		headStr := "?"
		if head != nil {
			headStr = fmt.Sprintf("%d", *head)
		}
		fmt.Printf("== %s  head=%s\n", dir, headStr)
		for n := *from; n <= *to; n++ {
			ch, _ := rawdb.ReadCanonicalHash(tx, n)
			hdr := rawdb.ReadHeader(tx, ch, n)
			ceStr := "-"
			if ce, cerr := rawdb.ReadConsensusEvidence(tx, n); cerr == nil && ce != nil {
				br := ce.BeaconRoot()
				ceStr = fmt.Sprintf("%x", br[:6])
			}
			if hdr != nil {
				fmt.Printf("  %d canon=%x root=%x ce=%s\n", n, ch[:8], hdr.Root[:6], ceStr)
			} else {
				fmt.Printf("  %d canon=%x (no header) ce=%s\n", n, ch[:8], ceStr)
			}
		}
		// -walk: derive the TRUE chain by walking parent hashes down from
		// HeadBlockHash to -from, then print derived vs canonical-row hash for
		// the [from,to] window — shows exactly what the startup linkage repair
		// should rewrite and where it would stop.
		if *walk {
			hh := rawdb.ReadHeadBlockHash(tx)
			hn := rawdb.ReadHeaderNumber(tx, hh)
			if hn == nil {
				fmt.Printf("  walk: HeadBlockHash %x has no HeaderNumber row\n", hh[:8])
			} else {
				num, cur := *hn, hh
				derived := map[uint64][8]byte{}
				stop := ""
				for num >= *from {
					if num <= *to {
						var h8 [8]byte
						copy(h8[:], cur[:8])
						derived[num] = h8
					}
					hdr := rawdb.ReadHeader(tx, cur, num)
					if hdr == nil {
						stop = fmt.Sprintf("HEADER MISSING at %d %x", num, cur[:8])
						break
					}
					if len(hdr.Extra) < 4 || string(hdr.Extra[:4]) != "N42H" {
						stop = fmt.Sprintf("replay boundary (no N42H) at %d", num)
						break
					}
					cur = hdr.ParentHash
					num--
				}
				fmt.Printf("  walk: head=%d %x stop=%q\n", *hn, hh[:8], stop)
				for n := *from; n <= *to; n++ {
					ch, _ := rawdb.ReadCanonicalHash(tx, n)
					d, ok := derived[n]
					dStr := "unreached"
					if ok {
						dStr = fmt.Sprintf("%x", d[:])
					}
					match := "MISMATCH"
					if ok && fmt.Sprintf("%x", d[:]) == fmt.Sprintf("%x", ch[:8]) {
						match = "ok"
					}
					fmt.Printf("  %d derived=%s canon=%x %s\n", n, dStr, ch[:8], match)
				}
			}
		}
		tx.Rollback()
		db.Close()
	}
}
