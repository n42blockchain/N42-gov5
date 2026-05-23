// n42-read-ethel-progress prints all known progress markers in an
// MDBX env so we know where executor / rebuild stopped.
//
//   DbInfo/ethel_progress              — set ONLY at end of
//                                        RebuildState. Stale if
//                                        the rebuild crashed.
//   SyncStageProgress/ethel-last-block — set per-batch by the
//                                        executor. Authoritative.
//
// Read-only. Single-shot.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dir := flag.String("dir", "", "MDBX dir")
	mapsizeGB := flag.Int("mapsize-gb", 1024, "mapsize cap")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "--dir required")
		os.Exit(2)
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*mapsizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["DbInfo"] = kv.TableCfgItem{}
			d[kv.SyncStageProgress] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	fmt.Printf("dir: %s\n\n", *dir)

	// 1. DbInfo/ethel_progress (rebuild end marker, stale on crash)
	if v, err := tx.GetOne("DbInfo", []byte("ethel_progress")); err == nil && len(v) == 8 {
		p := binary.BigEndian.Uint64(v)
		fmt.Printf("DbInfo/ethel_progress              = %d  (set ONLY at RebuildState end; STALE if rebuild crashed)\n", p)
	} else {
		fmt.Printf("DbInfo/ethel_progress              = ABSENT\n")
	}

	// 2. SyncStageProgress/ethel-last-block (executor per-batch, authoritative)
	if v, err := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block")); err == nil && len(v) == 8 {
		p := binary.BigEndian.Uint64(v)
		fmt.Printf("SyncStageProgress/ethel-last-block = %d  (executor per-batch — AUTHORITATIVE)\n", p)
	} else {
		fmt.Printf("SyncStageProgress/ethel-last-block = ABSENT\n")
	}

	// 3. CS remainder presence
	for _, name := range []string{"acctcs", "storcs", "witness"} {
		k := []byte("ethel-cs-remainder-" + name)
		if v, err := tx.GetOne(kv.SyncStageProgress, k); err == nil && len(v) > 0 {
			fmt.Printf("ethel-cs-remainder-%s            = %d bytes (unflushed since last batch)\n", name, len(v))
		}
	}
}
