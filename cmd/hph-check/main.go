// hph-check: inspect HPH tables + ethel_progress marker in an MDBX datadir.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "--datadir" {
		fmt.Fprintln(os.Stderr, "usage: hph-check --datadir PATH")
		os.Exit(1)
	}
	datadir := os.Args[2]

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).Path(datadir).Label(kv.ChainDB).Readonly().Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	tables := []string{"Account", "Storage", "HashedAccount", "HashedStorage", "TrieAccount", "TrieStorage"}
	for _, name := range tables {
		c, err := tx.Cursor(name)
		if err != nil {
			fmt.Printf("%-16s ERROR: %v\n", name, err)
			continue
		}
		k, _, err := c.First()
		c.Close()
		if err != nil {
			fmt.Printf("%-16s ERROR: %v\n", name, err)
			continue
		}
		status := "EMPTY"
		if k != nil {
			status = "populated"
		}
		fmt.Printf("%-16s %s\n", name, status)
	}

	// Progress markers.
	fmt.Println("--- progress markers ---")
	if v, err := tx.GetOne("DbInfo", []byte("ethel_progress")); err == nil && len(v) == 8 {
		fmt.Printf("DbInfo/ethel_progress = %d\n", binary.BigEndian.Uint64(v))
	} else {
		fmt.Printf("DbInfo/ethel_progress = <missing>\n")
	}
	if v, err := tx.GetOne(kv.SyncStageProgress, []byte("ethel-last-block")); err == nil && len(v) == 8 {
		fmt.Printf("SyncStageProgress/ethel-last-block = %d\n", binary.BigEndian.Uint64(v))
	} else {
		fmt.Printf("SyncStageProgress/ethel-last-block = <missing>\n")
	}
}
