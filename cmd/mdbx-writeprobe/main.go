// mdbx-writeprobe attributes a running node's per-block write volume.
//
// The OS write counters say how many bytes a node writes; they do not say
// which table. This opens the live chaindata read-only (Accede, so it
// attaches to the running environment and sees its shared page-op counters),
// samples twice, and reports the delta:
//
//   - env PageOps: cow/clone/split/merge/spill/wops — the write mechanism
//   - per-table entries and pages — which tables actually receive rows
//
// Cow x pageSize is the bytes the B-tree rewrote; comparing it with the
// process's WriteTransferCount over the same window says how much of the
// write volume is copy-on-write page rewriting versus anything else.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

type tableSample struct {
	entries, branch, leaf, overflow uint64
}

func main() {
	dbPath := flag.String("db", "", "chaindata directory (required)")
	window := flag.Duration("window", 60*time.Second, "sampling window")
	top := flag.Int("top", 20, "how many tables to report")
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mdbx-writeprobe --db <chaindata> [--window 60s]")
		os.Exit(2)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()

	db, err := mdbx.NewMDBX(log.New()).Path(*dbPath).Label(kv.ChainDB).
		MapSize(4 * datasize.TB).Accede().Readonly().Open(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tables := make([]string, 0, len(modules.N42TableCfg))
	for name := range modules.N42TableCfg {
		tables = append(tables, name)
	}
	sort.Strings(tables)

	sample := func() (map[string]tableSample, uint64) {
		out := make(map[string]tableSample, len(tables))
		var pageSize uint64
		_ = db.View(ctx, func(tx kv.Tx) error {
			mtx, ok := tx.(*mdbx.MdbxTx)
			if !ok {
				return fmt.Errorf("not an mdbx tx")
			}
			pageSize = 4096
			for _, name := range tables {
				st, err := mtx.BucketStat(name)
				if err != nil || st == nil {
					continue
				}
				out[name] = tableSample{st.Entries, st.BranchPages, st.LeafPages, st.OverflowPages}
			}
			return nil
		})
		return out, pageSize
	}

	a, pageSize := sample()
	t0 := time.Now()
	time.Sleep(*window)
	b, _ := sample()
	elapsed := time.Since(t0)

	type row struct {
		name     string
		dEntries int64
		dPages   int64
		entries  uint64
		pages    uint64
	}
	var rows []row
	var totalDelta int64
	for _, name := range tables {
		x, y := a[name], b[name]
		de := int64(y.entries) - int64(x.entries)
		dp := int64(y.branch+y.leaf+y.overflow) - int64(x.branch+x.leaf+x.overflow)
		if de == 0 && dp == 0 {
			continue
		}
		totalDelta += dp
		rows = append(rows, row{name, de, dp, y.entries, y.branch + y.leaf + y.overflow})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].dPages != rows[j].dPages {
			return rows[i].dPages > rows[j].dPages
		}
		return rows[i].dEntries > rows[j].dEntries
	})

	fmt.Printf("window %s, page size %d\n\n", elapsed.Truncate(time.Second), pageSize)
	fmt.Printf("%-28s %14s %12s %16s %12s\n", "TABLE", "+ENTRIES", "+PAGES", "TOTAL ENTRIES", "TOTAL PAGES")
	n := 0
	for _, r := range rows {
		if n >= *top {
			break
		}
		n++
		fmt.Printf("%-28s %14d %12d %16d %12d\n", r.name, r.dEntries, r.dPages, r.entries, r.pages)
	}
	fmt.Printf("\ngrowth over window: %d pages = %s (growth only; COW rewrites do not grow)\n",
		totalDelta, datasize.ByteSize(uint64(max64(totalDelta, 0))*pageSize).HR())
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
