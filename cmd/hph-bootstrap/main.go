// hph-bootstrap runs ethel.BootstrapHPHBatched against an already-populated
// PlainState (Account/Storage) to fill the four tables the eth-el wire path's
// TrieRootComputer reads: HashedAccounts, HashedStorage, TrieOfAccounts,
// TrieOfStorage. No block replay — assumes PlainState is already at the target
// state (e.g. migrated from reth). Prints the computed state root.
//
// This is the standalone equivalent of `ethexec rebuild-state --persist-trie`
// minus the replay phase.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dir := flag.String("datadir", `D:/N42-eth1177-test/chaindata`, "chaindata MDBX path (PlainState must be populated)")
	dirtyGB := flag.Uint64("dirty-space-gb", 48, "MDBX dirty-page pool size (GB) for the single-tx CalcTrieRoot flush")
	tmpdir := flag.String("tmpdir", `D:/tmp/hph-bootstrap-etl`, "ETL spill dir for sorted hashing (~30 GB at 25M state)")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(datasize.ByteSize(*dirtyGB) * datasize.GB)).
		Accede().
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := os.MkdirAll(*tmpdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir tmpdir:", err)
		os.Exit(1)
	}

	t0 := time.Now()
	logger.Info("hph-bootstrap starting (ETL sorted)", "datadir", *dir, "dirtyGB", *dirtyGB, "tmpdir", *tmpdir)
	root, err := ethel.BootstrapHPHFastETL(context.Background(), db, *tmpdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "BootstrapHPHFastETL:", err)
		os.Exit(1)
	}
	fmt.Printf("HPH bootstrap complete: root=%s elapsed=%s\n", root.Hex(), time.Since(t0).Truncate(time.Second))
}
