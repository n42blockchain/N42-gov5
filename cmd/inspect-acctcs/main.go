// inspect-acctcs: scan acctcs to find entries with codeHash bit set.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	src := flag.String("acctcs-dir", "", "freezer dir")
	from := flag.Uint64("from", 0, "start block")
	to := flag.Uint64("to", 0, "end block exclusive, 0 = items")
	limit := flag.Int("limit", 10, "max entries with codeHash bit to print")
	flag.Parse()

	tbl, _ := freezer.NewFreezerTableCompressedReadOnly(*src, freezer.TableAccountChanges, "c")
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	end := *to
	if end == 0 {
		end = tbl.Items()
	}

	var oldHasCH, newHasCH, oldEmpty, newEmpty, total, contractDeleted, oldHasCHNonempty uint64
	var found int
	for b := *from; b < end; b++ {
		data, _ := tbl.Retrieve(b)
		if len(data) == 0 {
			continue
		}
		entries, err := ethel.DecodeAccountChanges(data)
		if err != nil {
			continue
		}
		for _, e := range entries {
			total++
			oldBits := byte(0)
			newBits := byte(0)
			if len(e.OldValue) > 0 {
				oldBits = e.OldValue[0]
			} else {
				oldEmpty++
			}
			if len(e.NewValue) > 0 {
				newBits = e.NewValue[0]
			} else {
				newEmpty++
			}
			if oldBits&8 != 0 {
				oldHasCH++
				if len(e.OldValue) >= 1+1+32 {
					oldHasCHNonempty++
				}
			}
			if newBits&8 != 0 {
				newHasCH++
			}
			if oldBits&8 != 0 && len(e.NewValue) == 0 {
				contractDeleted++
			}
			if oldBits&8 != 0 && found < *limit {
				fmt.Printf("blk=%d addr=%x old(bits=%02x len=%d)=%x new(bits=%02x len=%d)=%x\n",
					b, e.Address[:], oldBits, len(e.OldValue), e.OldValue, newBits, len(e.NewValue), e.NewValue)
				found++
			}
		}
		if b%500000 == 0 && b > *from {
			fmt.Fprintf(os.Stderr, "  blk=%d total=%d oldHasCH=%d newHasCH=%d oldEmpty=%d newEmpty=%d\n",
				b, total, oldHasCH, newHasCH, oldEmpty, newEmpty)
		}
	}
	fmt.Printf("\n=== summary blocks [%d, %d) ===\n", *from, end)
	fmt.Printf("total entries:   %d\n", total)
	fmt.Printf("old has codeHash bit (bit 3): %d\n", oldHasCH)
	fmt.Printf("new has codeHash bit (bit 3): %d\n", newHasCH)
	fmt.Printf("old empty (deleted-before):   %d\n", oldEmpty)
	fmt.Printf("new empty (deleted-after):    %d\n", newEmpty)
	fmt.Printf("contract-deleted (old CH set ∧ new empty): %d\n", contractDeleted)
}
