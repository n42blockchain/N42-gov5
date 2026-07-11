// -csgrep: scan the Account/Storage changesets of a store for a specific
// address, printing every block that recorded a pre-value for it. Answers
// "did THIS key's writes go through the changeset-recording writer?" — the
// load-bearing question when a PlainState row survives an unwind (the unwind
// can only revert keys the changesets know about).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func csGrep(dir string, addrHex string) {
	target := types.HexToAddress(addrHex)
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Printf("begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	an, _, _, _ := rawdb.ReadQMDBApplied(tx)
	from, _ := changeset.AvailableFrom(tx)
	fmt.Printf("== %s addr=%x marker=%d accountCS from=%d\n", dir, target, an, from)

	hits := 0
	if err := changeset.ForRange(tx, modules.AccountChangeSet, 0, an+512, func(bn uint64, k, v []byte) error {
		if len(k) >= 20 && types.BytesToAddress(k[:20]) == target {
			hits++
			if hits <= 40 {
				fmt.Printf("  acctCS block=%d preLen=%d pre=%x\n", bn, len(v), v)
			}
		}
		return nil
	}); err != nil {
		fmt.Printf("account scan: %v\n", err)
	}
	fmt.Printf("account changeset rows for %x: %d\n", target, hits)

	shits := 0
	if err := changeset.ForRange(tx, modules.StorageChangeSet, 0, an+512, func(bn uint64, k, v []byte) error {
		if len(k) >= 20 && types.BytesToAddress(k[:20]) == target {
			shits++
			if shits <= 20 {
				slot := ""
				if len(k) >= 52 {
					slot = fmt.Sprintf("%x", k[20:52][:8])
				}
				fmt.Printf("  storCS block=%d slot=%s preLen=%d pre=%x\n", bn, slot, len(v), v)
			}
		}
		return nil
	}); err != nil {
		fmt.Printf("storage scan: %v\n", err)
	}
	fmt.Printf("storage changeset rows for %x: %d\n", target, shits)
}
