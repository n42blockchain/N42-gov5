// bench-merkle measures the cost of ONE incremental Merkle pass (staged-catchup
// model) over blocks [from,to]: build a RetainList from the range's changesets,
// then CalcTrieRoot (nil collectors = compute only, no TrieOf* write). Read-only,
// so it coexists with a running eth-el writer. Compare the per-block-equivalent
// to the per-block path's ~82ms/block dRoot to see whether batching amortizes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	dir := flag.String("dir", "", "MDBX chaindata dir")
	from := flag.Uint64("from", 0, "from block (inclusive)")
	to := flag.Uint64("to", 0, "to block (inclusive)")
	perBlockMs := flag.Int("per-block-ms", 82, "per-block dRoot baseline (ms) for comparison")
	mapsizeGB := flag.Int("mapsize-gb", 4096, "mapsize cap")
	flag.Parse()
	if *dir == "" || *to < *from {
		fmt.Fprintln(os.Stderr, "--dir required, --to >= --from")
		os.Exit(2)
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*mapsizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "begin ro: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	nblk := *to - *from + 1

	t0 := time.Now()
	rl, nAcc, nSto, err := commitment.BuildRetainListFromChangesets(tx, *from, *to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build retain list: %v\n", err)
		os.Exit(1)
	}
	tBuild := time.Since(t0)

	t1 := time.Now()
	loader := trie.NewFlatDBTrieLoader("bench-merkle", rl, nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CalcTrieRoot: %v\n", err)
		os.Exit(1)
	}
	tCalc := time.Since(t1)

	total := tBuild + tCalc
	perBlock := total / time.Duration(nblk)
	baseline := time.Duration(*perBlockMs) * time.Millisecond * time.Duration(nblk)
	fmt.Printf("range=[%d,%d] blocks=%d uniqAccKeys=%d uniqStoKeys=%d root=0x%x\n",
		*from, *to, nblk, nAcc, nSto, root)
	fmt.Printf("tBuildRetainList=%s tCalcTrieRoot=%s total=%s\n",
		tBuild.Round(time.Millisecond), tCalc.Round(time.Millisecond), total.Round(time.Millisecond))
	fmt.Printf("staged per-block-equiv = %s/blk\n", perBlock.Round(time.Microsecond))
	fmt.Printf("per-block baseline (%dms/blk) = %s for %d blocks → staged speedup = %.2fx\n",
		*perBlockMs, baseline.Round(time.Millisecond), nblk, float64(baseline)/float64(total))
}
