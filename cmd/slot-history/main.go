// slot-history scans storcs freezer entries for a block range and prints
// every block where a specific (addr, slot) pair was written, including
// old and new values.
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
	dir := flag.String("dir", "", "freezer directory containing storcs.cidx")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 0, "end block (inclusive)")
	addrHex := flag.String("addr", "", "20-byte address (hex, no 0x prefix)")
	slotHex := flag.String("slot", "", "32-byte slot key (hex, no 0x prefix)")
	flag.Parse()

	if *dir == "" || *addrHex == "" || *slotHex == "" {
		fmt.Fprintln(os.Stderr, "usage: slot-history --dir <freezer> --from N --to M --addr <20B hex> --slot <32B hex>")
		os.Exit(1)
	}

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr:", err, "len:", len(addr))
		os.Exit(1)
	}
	slot, err := hex.DecodeString(strings.TrimPrefix(*slotHex, "0x"))
	if err != nil || len(slot) != 32 {
		fmt.Fprintln(os.Stderr, "bad slot:", err, "len:", len(slot))
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableReadOnly(*dir, "storcs", "c")
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

	fmt.Printf("scanning storcs [%d, %d] for addr=%x slot=%x\n", *fromBlock, *toBlock, addr, slot)
	matches := 0
	for b := *fromBlock; b <= *toBlock; b++ {
		data, err := tbl.Retrieve(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "block %d retrieve err: %v\n", b, err)
			continue
		}
		if len(data) < 2 {
			continue
		}
		addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
		pos := 2
		for g := 0; g < addrCount; g++ {
			if pos+22 > len(data) {
				break
			}
			groupAddr := data[pos : pos+20]
			pos += 20
			slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
			pos += 2
			addrMatch := string(groupAddr) == string(addr)
			for s := 0; s < slotCount; s++ {
				if pos+34 > len(data) {
					break
				}
				slotKey := data[pos : pos+32]
				pos += 32
				oldLen := int(data[pos])
				pos++
				if oldLen > 32 || pos+oldLen+1 > len(data) {
					break
				}
				oldVal := data[pos : pos+oldLen]
				pos += oldLen
				newLen := int(data[pos])
				pos++
				if newLen > 32 || pos+newLen > len(data) {
					break
				}
				newVal := data[pos : pos+newLen]
				pos += newLen

				if addrMatch && string(slotKey) == string(slot) {
					fmt.Printf("block=%d old=0x%x new=0x%x\n", b, oldVal, newVal)
					matches++
				}
			}
		}
	}
	fmt.Printf("done: %d match(es) in [%d, %d]\n", matches, *fromBlock, *toBlock)
}
