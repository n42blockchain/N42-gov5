// copy-code copies the Code table from one MDBX to another.
// Usage: copy-code --src D:\path\src --dst D:\path\dst
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	if len(os.Args) < 5 || os.Args[1] != "--src" || os.Args[3] != "--dst" {
		fmt.Fprintln(os.Stderr, "usage: copy-code --src SRC_MDBX_DIR --dst DST_MDBX_DIR")
		os.Exit(1)
	}
	srcDir, dstDir := os.Args[2], os.Args[4]
	logger := log2.New()

	srcDB, err := mdbx.NewMDBX(logger).Path(srcDir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open src:", err)
		os.Exit(1)
	}
	defer srcDB.Close()

	dstDB, err := mdbx.NewMDBX(logger).Path(dstDir).Label(kv.ChainDB).Accede().Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst:", err)
		os.Exit(1)
	}
	defer dstDB.Close()

	srcTx, _ := srcDB.BeginRo(context.Background())
	defer srcTx.Rollback()

	dstTx, _ := dstDB.BeginRw(context.Background())

	cursor, err := srcTx.Cursor(modules.Code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}

	count := 0
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iterate:", err)
			os.Exit(1)
		}
		if err := dstTx.Put(modules.Code, k, v); err != nil {
			fmt.Fprintln(os.Stderr, "put:", err)
			os.Exit(1)
		}
		count++
		if count%100000 == 0 {
			fmt.Fprintf(os.Stderr, "  copied %d code entries\n", count)
		}
	}

	if err := dstTx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "commit:", err)
		os.Exit(1)
	}
	fmt.Printf("Copied %d code entries\n", count)
}
