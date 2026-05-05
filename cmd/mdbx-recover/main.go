// mdbx-recover: open an MDBX in read-write Accede mode and immediately
// commit-and-close. MDBX runs its WAL replay on R/W open, so this
// "no-op tx" is enough to clear MDBX_WANNA_RECOVERY and let later
// read-only consumers (witness-replay, ethexec --readonly, etc) open
// the database without complaint.
//
// Use: build\bin\mdbx-recover.exe <datadir>
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mdbx-recover <datadir>")
		os.Exit(1)
	}
	datadir := os.Args[1]

	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		Accede().
		MapSize(4 * datasize.TB).
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "begin rw: %v\n", err)
		os.Exit(1)
	}
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "commit: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK: %s recovered\n", datadir)
}
