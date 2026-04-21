// storcs-diag — extract and inspect a single block's storcs entry from a
// compressed freezer, walking the encoded bytes the same way DecodeStorageChanges
// does so we can pinpoint exactly where the decode fails.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	dir := flag.String("dir", "", "freezer directory")
	table := flag.String("table", "storcs", "table name (storcs / acctcs)")
	block := flag.Uint64("block", 0, "block number")
	dump := flag.Bool("dump", false, "dump the raw bytes as hex")
	scanAll := flag.Bool("scan-all", false, "scan every block, report any with >32B slot values")
	scanFrom := flag.Uint64("scan-from", 0, "scan-all start block (default 0)")
	scanTo := flag.Uint64("scan-to", 0, "scan-all end block (0 = all)")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: storcs-diag --dir <path> --block N [--table storcs] [--dump]")
		fmt.Fprintln(os.Stderr, "   or: storcs-diag --dir <path> --scan-all [--scan-from N] [--scan-to M]")
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableReadOnly(*dir, *table, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)
	tbl.SetCompressed(true)

	items := tbl.Items()
	fmt.Printf("table=%s items=%d batchSize=%d\n", *table, items, freezer.BatchSize)

	// Scan mode: walk a range of blocks, report decode errors only.
	// Useful for isolating which block(s) in a range have corrupt entries.
	if *scanAll {
		from := *scanFrom
		to := *scanTo
		if to == 0 || to > items {
			to = items
		}
		if *table != "storcs" {
			fmt.Fprintln(os.Stderr, "--scan-all only supports --table storcs")
			os.Exit(1)
		}
		fmt.Printf("scanning blocks [%d, %d) for decode errors...\n", from, to)
		bad := 0
		for i := from; i < to; i++ {
			d, err := tbl.Retrieve(i)
			if err != nil {
				fmt.Printf("  block %d: retrieve ERROR %v\n", i, err)
				bad++
				continue
			}
			if e := tryDecodeStorage(d); e != nil {
				fmt.Printf("  block %d: decode ERROR (%d bytes): %v\n", i, len(d), e)
				bad++
			}
		}
		fmt.Printf("done: %d bad / %d scanned\n", bad, to-from)
		return
	}

	if *block >= items {
		fmt.Fprintf(os.Stderr, "block %d >= items %d\n", *block, items)
		os.Exit(1)
	}

	batchStart := (*block / uint64(freezer.BatchSize)) * uint64(freezer.BatchSize)
	batchEnd := batchStart + uint64(freezer.BatchSize)
	if batchEnd > items {
		batchEnd = items
	}
	fmt.Printf("block %d lives in batch [%d..%d)\n", *block, batchStart, batchEnd)

	data, err := tbl.Retrieve(*block)
	if err != nil {
		fmt.Fprintln(os.Stderr, "retrieve:", err)
		os.Exit(1)
	}
	fmt.Printf("retrieved %d bytes for block %d\n", len(data), *block)

	if *dump {
		fmt.Printf("\nhex dump:\n%s\n", hexDump(data))
	}

	// Find all 20-byte address-like prefixes that appear on likely group
	// boundaries (preceded by a slot-count that fits).
	fmt.Printf("\nscanning for structural anomalies:\n")
	scanForAddressRepeats(data)

	if *table == "storcs" {
		walkStorageChanges(data)
	}

	// Also retrieve every entry in this batch and report length.
	fmt.Printf("\nper-block sizes in batch:\n")
	for i := batchStart; i < batchEnd; i++ {
		d, err := tbl.Retrieve(i)
		if err != nil {
			fmt.Printf("  block %d: ERROR %v\n", i, err)
			continue
		}
		status := "ok"
		if *table == "storcs" {
			if e := tryDecodeStorage(d); e != nil {
				status = "DECODE-ERR: " + e.Error()
			}
		}
		fmt.Printf("  block %d: %d bytes (%s)\n", i, len(d), status)
	}
}

func walkStorageChanges(data []byte) {
	fmt.Printf("\nwalking storage changeset (mirrors DecodeStorageChanges):\n")
	if len(data) == 0 {
		fmt.Println("  empty")
		return
	}
	if len(data) < 2 {
		fmt.Printf("  truncated header: len=%d\n", len(data))
		return
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	fmt.Printf("  addrCount=%d\n", addrCount)
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			fmt.Printf("  ABORT at group %d: pos=%d need 22, have %d\n", g, pos, len(data)-pos)
			return
		}
		addr := data[pos : pos+20]
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		fmt.Printf("  group %d: addr=%x slotCount=%d pos=%d\n", g, addr, slotCount, pos)
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				fmt.Printf("    ABORT slot %d: pos=%d need 34, have %d\n", s, pos, len(data)-pos)
				return
			}
			slotKey := data[pos : pos+32]
			pos += 32
			oldLen := int(data[pos])
			pos++
			if pos+oldLen+1 > len(data) {
				fmt.Printf("    ABORT slot %d: pos=%d oldLen=%d remaining=%d\n", s, pos, oldLen, len(data)-pos)
				return
			}
			oldVal := data[pos : pos+oldLen]
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if pos+newLen > len(data) {
				fmt.Printf("    *** TRUNCATED slot new value *** group=%d slot=%d pos=%d newLen=%d remaining=%d\n",
					g, s, pos, newLen, len(data)-pos)
				fmt.Printf("    context: addr=%x slot=%x oldLen=%d oldVal=%x\n", addr, slotKey, oldLen, oldVal)
				fmt.Printf("    last 64 bytes of blob: %x\n", data[maxInt(0, len(data)-64):])
				return
			}
			newVal := data[pos : pos+newLen]
			pos += newLen
			if s < 3 || s >= slotCount-3 {
				fmt.Printf("    slot %d: key=%x oldLen=%d oldVal=%x newLen=%d newVal=%x\n",
					s, slotKey, oldLen, oldVal, newLen, newVal)
			} else if s == 3 {
				fmt.Printf("    ... (%d slots omitted) ...\n", slotCount-6)
			}
		}
	}
	fmt.Printf("  OK: decoded fully, pos=%d of %d\n", pos, len(data))
}

func tryDecodeStorage(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) < 2 {
		return fmt.Errorf("truncated header")
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			return fmt.Errorf("truncated addr group %d", g)
		}
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				return fmt.Errorf("truncated slot %d in group %d", s, g)
			}
			pos += 32
			oldLen := int(data[pos])
			pos++
			if pos+oldLen+1 > len(data) {
				return fmt.Errorf("truncated slot old value")
			}
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if pos+newLen > len(data) {
				return fmt.Errorf("truncated slot new value at group=%d slot=%d", g, s)
			}
			pos += newLen
		}
	}
	return nil
}

func hexDump(b []byte) string {
	const rowLen = 32
	out := ""
	for i := 0; i < len(b); i += rowLen {
		end := i + rowLen
		if end > len(b) {
			end = len(b)
		}
		out += fmt.Sprintf("  %08x: %x\n", i, b[i:end])
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// scanForAddressRepeats looks for 20-byte sequences that appear multiple
// times in the blob. In a well-formed storage changeset each address
// appears exactly once as a group prefix — repeats suggest something
// like multiple blocks' changesets concatenated into one entry.
func scanForAddressRepeats(data []byte) {
	// Build a map of every 20-byte window's count.
	counts := make(map[[20]byte]int)
	positions := make(map[[20]byte][]int)
	for i := 0; i+20 <= len(data); i++ {
		var w [20]byte
		copy(w[:], data[i:i+20])
		counts[w]++
		if len(positions[w]) < 5 {
			positions[w] = append(positions[w], i)
		}
	}
	// Print the top-10 most repeated windows (only interesting if >1).
	type hit struct {
		w   [20]byte
		cnt int
	}
	var hits []hit
	for w, c := range counts {
		if c >= 3 {
			hits = append(hits, hit{w, c})
		}
	}
	// Sort top 20.
	for i := range hits {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].cnt > hits[i].cnt {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	if len(hits) > 20 {
		hits = hits[:20]
	}
	fmt.Printf("  total 20B windows with count>=3: %d (showing top 20)\n", len(hits))
	for _, h := range hits {
		fmt.Printf("    count=%d window=%x positions=%v\n", h.cnt, h.w[:], positions[h.w])
	}
}
