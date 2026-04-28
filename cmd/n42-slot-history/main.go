// n42-slot-history scans the n42 storcs freezer for a specific (addr, slot)
// pair across a block range and prints each matching entry with its
// pre-block (oldVal) and post-block (newVal) values.
//
// Mirrors cmd/reth-slot-history but reads our compact storcs blob format
// instead of reth's MDBX StorageChangeSets table.
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
	freezerDir := flag.String("freezer", `d:\N42-1703m\chain\freezer`, "n42 freezer dir (contains storcs)")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 0, "end block (inclusive, 0 = unbounded)")
	addrHex := flag.String("addr", "", "20-byte address (hex)")
	slotHex := flag.String("slot", "", "32-byte slot (hex)")
	flag.Parse()

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr:", err, "len:", len(addr))
		os.Exit(1)
	}
	// Empty slot = match every slot the addr touched in the range.
	var slot []byte
	if *slotHex != "" {
		s, err := hex.DecodeString(strings.TrimPrefix(*slotHex, "0x"))
		if err != nil || len(s) != 32 {
			fmt.Fprintln(os.Stderr, "bad slot:", err, "len:", len(s))
			os.Exit(1)
		}
		slot = s
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

	slotLabel := "any"
	if slot != nil {
		slotLabel = fmt.Sprintf("%x", slot)
	}
	fmt.Printf("scanning n42 storcs [%d, %d] addr=%x slot=%s\n", *fromBlock, end, addr, slotLabel)
	matches := 0
	for blk := *fromBlock; blk <= end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil || len(data) < 2 {
			continue
		}
		for _, e := range findEntries(data, addr, slot) {
			matches++
			oldHex := hex.EncodeToString(e.old)
			if oldHex == "" {
				oldHex = "0"
			}
			newHex := hex.EncodeToString(e.new)
			if newHex == "" {
				newHex = "0"
			}
			fmt.Printf("blk=%d slot=%x oldVal=0x%s newVal=0x%s\n", blk, e.slot, oldHex, newHex)
		}
	}
	fmt.Printf("done: %d match(es)\n", matches)
}

type slotEntry struct {
	slot []byte
	old  []byte
	new  []byte
}

// findEntries scans an n42 storcs blob and returns every slot entry
// that matches wantAddr. With wantSlot == nil, returns every slot the
// address touched. Values are raw BE-minimal bytes (length 0 = zero/absent).
func findEntries(data, wantAddr, wantSlot []byte) []slotEntry {
	var out []slotEntry
	if len(data) < 2 {
		return out
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			return out
		}
		addrMatches := equal20(data[pos:pos+20], wantAddr)
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				return out
			}
			slotPos := pos
			pos += 32
			oldLen := int(data[pos])
			pos++
			if oldLen > 32 || pos+oldLen+1 > len(data) {
				return out
			}
			oldStart := pos
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if newLen > 32 || pos+newLen > len(data) {
				return out
			}
			newStart := pos
			pos += newLen
			if !addrMatches {
				continue
			}
			if wantSlot != nil && !equal32(data[slotPos:slotPos+32], wantSlot) {
				continue
			}
			out = append(out, slotEntry{
				slot: append([]byte(nil), data[slotPos:slotPos+32]...),
				old:  append([]byte(nil), data[oldStart:oldStart+oldLen]...),
				new:  append([]byte(nil), data[newStart:newStart+newLen]...),
			})
		}
	}
	return out
}

func equal20(a, b []byte) bool {
	if len(a) != 20 || len(b) != 20 {
		return false
	}
	for i := 0; i < 20; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equal32(a, b []byte) bool {
	if len(a) != 32 || len(b) != 32 {
		return false
	}
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
