// storcs-grep: grep a hex byte pattern across all storcs freezer blobs.
// Prints every block whose decoded blob contains the pattern.
//
// Use case: locate where a corrupted-looking value first appeared
// (was it stored elsewhere as someone's slot/value before?).
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	freezerDir := flag.String("freezer", `d:\N42-ethverify\chain\freezer`, "n42 freezer dir")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 0, "end block (inclusive, 0 = unbounded)")
	patternHex := flag.String("pattern", "", "byte pattern to search (hex, no 0x)")
	maxHits := flag.Int("max", 200, "stop after this many hits")
	flag.Parse()

	pattern, err := hex.DecodeString(strings.TrimPrefix(*patternHex, "0x"))
	if err != nil || len(pattern) == 0 {
		fmt.Fprintln(os.Stderr, "bad --pattern:", err)
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableReadOnly(*freezerDir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)
	tbl.SetCompressed(true)

	end := *toBlock
	if end == 0 {
		end = tbl.Items() - 1
	}
	fmt.Printf("scanning n42 storcs [%d, %d] for pattern=%x (%dB)\n",
		*fromBlock, end, pattern, len(pattern))

	hits := 0
	for blk := *fromBlock; blk <= end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil || len(data) < 2 {
			continue
		}
		if idx := bytes.Index(data, pattern); idx >= 0 {
			hits++
			fmt.Printf("blk=%d  blob_len=%d  match_offset=%d\n", blk, len(data), idx)
			if hits >= *maxHits {
				fmt.Printf("(stopping at --max=%d)\n", *maxHits)
				break
			}
		}
	}
	fmt.Printf("done: %d match(es)\n", hits)
}
