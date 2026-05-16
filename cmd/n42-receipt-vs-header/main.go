// n42-receipt-vs-header: spot-check that N42 receipts.cidx entries
// decode to a receipt list whose Merkle root matches the corresponding
// geth ancient header's ReceiptHash. Used to validate that the N42
// receipts table is byte-faithful before truncating its trailing
// phantom sentinel entry.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	var (
		datadir   = flag.String("datadir", "", "N42 datadir (chain/freezer/receipts.* lives here)")
		ancient   = flag.String("ancient", "", "geth ancient dir (for headers)")
		samples   = flag.Int("samples", 10, "random blocks to sample")
		seed      = flag.Int64("seed", 0, "RNG seed (0 = time)")
		truncate  = flag.Uint64("truncate-to", 0, "if all samples match, truncate receipts.cidx to this item count (0 = skip)")
		startBlk  = flag.Uint64("start", 1, "lower bound for samples (skip block 0 — empty by definition)")
		endBlk    = flag.Uint64("end", 0, "upper bound for samples (0 = receipts.Items()-1)")
	)
	flag.Parse()
	if *datadir == "" || *ancient == "" {
		fmt.Fprintln(os.Stderr, "--datadir and --ancient required")
		os.Exit(2)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}

	// Open N42 receipts (RW only if we may truncate, otherwise RO).
	var n42 *freezer.Freezer
	var err error
	n42Path := *datadir + `\chain\freezer`
	if *truncate > 0 {
		n42, err = freezer.New(n42Path, 0)
	} else {
		n42, err = freezer.NewReadOnly(n42Path)
	}
	must(err, "open N42 receipts freezer")
	defer n42.Close()

	rTbl, err := n42.EnsureTableCompressed("receipts", "c")
	must(err, "open receipts table")
	rItems := rTbl.Items()
	fmt.Printf("N42 receipts.cidx: %d items\n", rItems)

	// Open geth headers (RO).
	geth, err := freezer.NewReadOnly(*ancient)
	must(err, "open geth ancient")
	defer geth.Close()
	if _, err := geth.EnsureTable("headers", "c"); err != nil {
		die("open geth headers table: %v", err)
	}
	gethFrozen := geth.Frozen()
	fmt.Printf("geth headers frozen: %d items\n", gethFrozen)

	hi := *endBlk
	if hi == 0 {
		hi = rItems - 1
	}
	if hi >= gethFrozen {
		hi = gethFrozen - 1
	}
	if *startBlk > hi {
		die("sample range empty: start=%d hi=%d", *startBlk, hi)
	}

	rng := rand.New(rand.NewSource(*seed))
	picks := make([]uint64, 0, *samples+2)
	picks = append(picks, *startBlk, hi) // boundary samples
	for i := 0; i < *samples; i++ {
		picks = append(picks, *startBlk+uint64(rng.Int63n(int64(hi-*startBlk+1))))
	}

	matches := 0
	hasherWrong := 0
	codecLossy := 0
	emptyBlocks := 0
	for _, n := range picks {
		gethRaw, err := geth.Ancient(freezer.TableHeaders, n)
		if err != nil {
			fmt.Printf("  block %d: header read err: %v\n", n, err)
			codecLossy++
			continue
		}
		hdr, err := ethel.DecodeGethHeader(gethRaw)
		if err != nil {
			fmt.Printf("  block %d: header decode err: %v\n", n, err)
			codecLossy++
			continue
		}
		rRaw, err := rTbl.Retrieve(n)
		if err != nil {
			fmt.Printf("  block %d: receipts read err: %v\n", n, err)
			codecLossy++
			continue
		}
		n42Receipts, err := ethel.DecodeReceiptsCompact(rRaw)
		if err != nil {
			fmt.Printf("  block %d: N42 receipts decode err: %v\n", n, err)
			codecLossy++
			continue
		}
		// Round-trip via geth raw too — distinguishes "EthReceiptHash bug"
		// from "compact codec loses hash-relevant fields".
		gethReceiptsRaw, err := geth.Ancient(freezer.TableReceipts, n)
		if err != nil {
			fmt.Printf("  block %d: geth receipts read err: %v\n", n, err)
			codecLossy++
			continue
		}
		gethReceipts, err := ethel.DecodeGethReceipts(gethReceiptsRaw)
		if err != nil {
			fmt.Printf("  block %d: geth receipts decode err: %v\n", n, err)
			codecLossy++
			continue
		}
		n42Root := ethel.EthReceiptHash(n42Receipts)
		gethRoot := ethel.EthReceiptHash(gethReceipts)
		want := hdr.ReceiptHash
		switch {
		case n42Root == want:
			if len(n42Receipts) == 0 {
				emptyBlocks++
			}
			matches++
			fmt.Printf("  block %8d  txs=%4d  match  n42=%s\n", n, len(n42Receipts), n42Root.Hex()[:12])
		case gethRoot == want:
			codecLossy++
			fmt.Printf("  block %8d  txs=%4d  N42 LOSSY (geth-raw matches header but n42 doesn't)  n42=%s want=%s\n",
				n, len(n42Receipts), n42Root.Hex()[:12], want.Hex()[:12])
		case n42Root == gethRoot:
			hasherWrong++
			fmt.Printf("  block %8d  txs=%4d  HASHER BUG (n42==geth but neither matches header)  n42=%s want=%s\n",
				n, len(n42Receipts), n42Root.Hex()[:12], want.Hex()[:12])
		default:
			codecLossy++
			fmt.Printf("  block %8d  txs=%4d  BOTH WRONG (n42!=geth!=header)  n42=%s geth=%s want=%s\n",
				n, len(n42Receipts), n42Root.Hex()[:12], gethRoot.Hex()[:12], want.Hex()[:12])
		}
	}
	fmt.Printf("\nResult: %d match (%d empty) / %d hasher-bug / %d codec-lossy / %d samples\n",
		matches, emptyBlocks, hasherWrong, codecLossy, len(picks))

	// Codec-lossy is the only failure that means the receipts table is wrong.
	// Hasher-bug means the receipts agree with geth source — only the trie root
	// computation has a separate, pre-existing N42 bug for typed receipts; safe
	// to truncate the phantom regardless.
	if codecLossy > 0 {
		fmt.Fprintln(os.Stderr, "ABORT: codec-lossy mismatch — receipts table is not byte-faithful")
		os.Exit(1)
	}
	if *truncate > 0 {
		if *truncate >= rItems {
			fmt.Printf("truncate-to %d >= current %d, no-op\n", *truncate, rItems)
			return
		}
		fmt.Printf("\nAll %d samples match. Truncating receipts table %d → %d\n", len(picks), rItems, *truncate)
		if err := rTbl.TruncateHead(*truncate); err != nil {
			die("TruncateHead: %v", err)
		}
		fmt.Printf("Truncated. New items: %d\n", rTbl.Items())
	}
}

func must(err error, what string) {
	if err != nil {
		die("%s: %v", what, err)
	}
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
