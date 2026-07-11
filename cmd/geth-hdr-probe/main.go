// geth-hdr-probe: read-only probe of geth ancient headers — prints stateRoot,
// parentHash, number at given blocks, and verifies parentHash chain continuity
// over a window. Used to verify geth source canonicity before regenerating
// derived eth-el data. Strictly read-only (freezer.NewReadOnly).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient chain dir")
	blocks := flag.String("blocks", "", "comma-separated block numbers to print stateRoot for")
	chainFrom := flag.Uint64("chain-from", 0, "verify parentHash chain continuity from this block")
	chainN := flag.Uint64("chain-n", 0, "...for this many blocks (chain-from..chain-from+chain-n)")
	flag.Parse()

	fz, err := freezer.NewReadOnly(*ancient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open ancient: %v\n", err)
		os.Exit(1)
	}
	defer fz.Close()
	fmt.Printf("geth ancient frozen = %d\n", fz.Frozen())

	readHdr := func(n uint64) (root, parent, hash string, num uint64, err error) {
		data, e := fz.Ancient(freezer.TableHeaders, n)
		if e != nil {
			return "", "", "", 0, e
		}
		h, e := ethel.DecodeGethHeader(data)
		if e != nil {
			return "", "", "", 0, e
		}
		return fmt.Sprintf("0x%x", h.Root), fmt.Sprintf("0x%x", h.ParentHash), fmt.Sprintf("0x%x", h.Hash()), h.Number.Uint64(), nil
	}

	if *blocks != "" {
		for _, s := range strings.Split(*blocks, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			n, _ := strconv.ParseUint(s, 10, 64)
			root, parent, hash, num, err := readHdr(n)
			if err != nil {
				fmt.Printf("block %d: ERROR %v\n", n, err)
				continue
			}
			fmt.Printf("block %d: number=%d hash=%s parent=%s stateRoot=%s\n", n, num, hash, parent, root)
		}
	}

	if *chainN > 0 {
		fmt.Printf("\nchain continuity %d..%d:\n", *chainFrom, *chainFrom+*chainN)
		var prevHash string
		ok := true
		for n := *chainFrom; n <= *chainFrom+*chainN; n++ {
			_, parent, hash, _, err := readHdr(n)
			if err != nil {
				fmt.Printf("  block %d: ERROR %v\n", n, err)
				ok = false
				break
			}
			if prevHash != "" && parent != prevHash {
				fmt.Printf("  BREAK at %d: parent=%s != prevHash=%s\n", n, parent, prevHash)
				ok = false
			}
			prevHash = hash
		}
		if ok {
			fmt.Printf("  OK: all parentHash links consistent\n")
		}
	}
}
