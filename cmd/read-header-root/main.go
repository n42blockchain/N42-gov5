// read-header-root prints header.Root (stateRoot) for a block from an MDBX
// chaindata env. Read-only, single-shot — used to get the trusted wire root
// for a synced (post-migration) block.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	dir := flag.String("dir", "", "MDBX chaindata dir")
	block := flag.Uint64("block", 0, "block number")
	mapsizeGB := flag.Int("mapsize-gb", 4096, "mapsize cap")
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
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	hash, err := rawdb.ReadCanonicalHash(tx, *block)
	if err != nil || hash == (types.Hash{}) {
		fmt.Fprintf(os.Stderr, "no canonical hash at %d (err=%v)\n", *block, err)
		os.Exit(1)
	}
	hdr := rawdb.ReadHeader(tx, hash, *block)
	if hdr == nil {
		fmt.Fprintf(os.Stderr, "no header at %d (hash=%s)\n", *block, hash.Hex())
		os.Exit(1)
	}
	fmt.Printf("block=%d hash=%s stateRoot=%s gasUsed=%d gasLimit=%d\n",
		*block, hash.Hex(), hdr.Root.Hex(), hdr.GasUsed, hdr.GasLimit)
}
