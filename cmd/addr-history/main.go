// addr-history scans acctcs for an address's account changes.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	dir := flag.String("dir", "", "freezer directory containing acctcs.cidx")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 0, "end block (inclusive)")
	addrHex := flag.String("addr", "", "20-byte address (hex, no 0x prefix)")
	flag.Parse()

	if *dir == "" || *addrHex == "" {
		fmt.Fprintln(os.Stderr, "usage: addr-history --dir <freezer> --from N --to M --addr <20B hex>")
		os.Exit(1)
	}

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr")
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableReadOnly(*dir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)
	tbl.SetCompressed(true)

	items := tbl.Items()
	if *toBlock == 0 || *toBlock >= items {
		*toBlock = items - 1
	}

	fmt.Printf("scanning acctcs [%d, %d] for addr=%x\n", *fromBlock, *toBlock, addr)
	matches := 0
	for b := *fromBlock; b <= *toBlock; b++ {
		data, err := tbl.Retrieve(b)
		if err != nil {
			continue
		}
		if len(data) < 2 {
			continue
		}
		count := int(binary.LittleEndian.Uint16(data[0:2]))
		pos := 2
		for i := 0; i < count; i++ {
			if pos+20 > len(data) {
				break
			}
			entryAddr := data[pos : pos+20]
			pos += 20
			if pos+1 > len(data) {
				break
			}
			oldLen := int(data[pos])
			pos++
			if pos+oldLen+1 > len(data) {
				break
			}
			oldVal := data[pos : pos+oldLen]
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if pos+newLen > len(data) {
				break
			}
			newVal := data[pos : pos+newLen]
			pos += newLen

			if string(entryAddr) == string(addr) {
				fmt.Printf("block=%d oldLen=%d old=0x%x newLen=%d new=0x%x\n",
					b, oldLen, oldVal, newLen, newVal)
				matches++
			}
		}
	}
	fmt.Printf("done: %d match(es)\n", matches)
}
