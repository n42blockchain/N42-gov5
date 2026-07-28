// Command qs-block-rlp exports one canonical block in the exact native gov5
// HotStuff RLP wire form used by block gossip and direct push.
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
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	number := flag.Uint64("number", 0, "canonical block number")
	out := flag.String("out", "", "binary output path")
	flag.Parse()
	if flag.NArg() != 1 || *number == 0 || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: qs-block-rlp -number N -out FILE <chaindata>")
		os.Exit(2)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(flag.Arg(0)).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal(err)
	}
	defer tx.Rollback()
	hash, err := rawdb.ReadCanonicalHash(tx, *number)
	if err != nil {
		fatal(err)
	}
	blk := rawdb.ReadBlock(tx, hash, *number)
	if blk == nil {
		fatal(fmt.Errorf("canonical block %d is missing", *number))
	}
	encoded, err := rlp.EncodeToBytes(blk)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, encoded, 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("block=%d hash=%s bytes=%d out=%s\n", *number, hash.Hex(), len(encoded), *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
