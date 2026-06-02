// bodyc-trim-probe: open a bodyc store (which may be a TRIMMED post-merge-only
// store whose bodyc.NNNN.cdat files do not start at 0000) and read one block,
// distinguishing a real decode from a clean ErrBodyTrimmed (segment cdat absent).
//
// Usage:
//
//	bodyc-trim-probe --dir <freezer-with-bodyc.cidx> --block N
//
// Exit 0 = decoded; exit 3 = ErrBodyTrimmed (expected for pruned segments);
// exit 1 = any other error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/coldresolve"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func main() {
	dir := flag.String("dir", "", "freezer dir containing bodyc.cidx + some bodyc.NNNN.cdat")
	block := flag.Uint64("block", 0, "block to read")
	manifest := flag.String("manifest", "", "cold manifest JSON (enables auto-fetch of trimmed segments)")
	coldDir := flag.String("colddir", "", "local cold dir holding offloaded bodyc.NNNN.cdat")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: bodyc-trim-probe --dir <freezer> --block N [--manifest M --colddir D]")
		os.Exit(1)
	}

	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open bodyc:", err)
		os.Exit(1)
	}
	defer br.Close()
	fmt.Printf("bodyc segments=%d max_block=%d\n", br.Segments(), br.MaxBlock())

	if *manifest != "" && *coldDir != "" {
		m, err := torrentsync.LoadManifest(*manifest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "load manifest:", err)
			os.Exit(1)
		}
		res := coldresolve.New(m, coldresolve.LocalDirFetcher{ColdDir: *coldDir}, true)
		br.SetColdResolver(res)
		fmt.Printf("cold resolver: manifest %d segments, colddir %s (sha256-verified)\n", len(m.Segments), *coldDir)
	}

	body, err := br.ReadBody(*block)
	if errors.Is(err, ethel.ErrBodyTrimmed) {
		fmt.Printf("block %d: TRIMMED (not in this store) — %v\n", *block, err)
		os.Exit(3)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "read body %d: %v\n", *block, err)
		os.Exit(1)
	}
	fmt.Printf("block %d: DECODED txs=%d uncles=%d withdrawals=%d\n",
		*block, len(body.Txs), len(body.UncleRLP), len(body.Withdrawals))
}
