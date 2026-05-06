// senders-probe: read senders for a single block from the freezer
// senders table and print the 20-byte address(es). Validates whether
// loadSenders is returning the right sender for the right block.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	dir := flag.String("dir", "", "freezer dir with senders.cidx + senders.NNNN.cdat")
	block := flag.Uint64("block", 0, "block to read")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: senders-probe --dir <freezer> --block N")
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*dir, freezer.TableSenders, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	fmt.Printf("senders table items=%d\n", tbl.Items())

	for _, b := range []uint64{*block - 1, *block, *block + 1} {
		if b >= tbl.Items() {
			continue
		}
		data, err := tbl.Retrieve(b)
		if err != nil {
			fmt.Printf("block %d: retrieve err=%v\n", b, err)
			continue
		}
		fmt.Printf("block %d: %d bytes (%d senders)\n", b, len(data), len(data)/20)
		for i := 0; i+20 <= len(data); i += 20 {
			fmt.Printf("  sender[%d/%d]: 0x%x\n", i/20, len(data)/20, data[i:i+20])
		}
	}
}
