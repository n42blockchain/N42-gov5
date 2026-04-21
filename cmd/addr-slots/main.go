// addr-slots: for a specific block, print all storage changes for an address.
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
	dir := flag.String("dir", "", "freezer directory")
	block := flag.Uint64("block", 0, "block number")
	addrHex := flag.String("addr", "", "20-byte address (hex, no 0x)")
	flag.Parse()

	addr, _ := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	tbl, _ := freezer.NewFreezerTableReadOnly(*dir, "storcs", "c")
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)
	tbl.SetCompressed(true)

	data, err := tbl.Retrieve(*block)
	if err != nil {
		fmt.Fprintln(os.Stderr, "retrieve:", err)
		os.Exit(1)
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
		match := string(groupAddr) == string(addr)
		if match {
			fmt.Printf("== addr %x has %d slot changes in block %d ==\n", groupAddr, slotCount, *block)
		}
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				break
			}
			slotKey := data[pos : pos+32]
			pos += 32
			oldLen := int(data[pos])
			pos++
			oldVal := data[pos : pos+oldLen]
			pos += oldLen
			newLen := int(data[pos])
			pos++
			newVal := data[pos : pos+newLen]
			pos += newLen
			if match {
				fmt.Printf("  slot=%x old=0x%x new=0x%x\n", slotKey, oldVal, newVal)
			}
		}
	}
}
