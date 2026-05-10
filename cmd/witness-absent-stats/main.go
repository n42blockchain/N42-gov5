// witness-absent-stats walks a window of block_witness entries and
// counts reads that returned "absent" (len=0). Witness format is the
// length-prefixed stream produced by WitnessStateReader: every state
// read becomes one [len:1B][value:lenB] tuple in exact execution order.
//
// len=0 means EITHER account-doesn't-exist OR storage-slot-is-zero/
// uninitialized. Both cases are EVM-legal and produce 0 in semantics —
// they're exactly the queries that would be "phantom" against a
// RecSplit-MPHF snapshot built from PlainAccount/StorageState (only
// extant rows are in the MPHF; absent rows have no ordinal).
//
// Reports total read count and absent fraction across the sampled
// blocks. Used to size whether the .val fingerprint is essential.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	witnessDir := flag.String("dir", "", "freezer dir with block_witness")
	start := flag.Uint64("start", 0, "first block")
	count := flag.Uint64("count", 1000, "number of blocks to sample")
	flag.Parse()
	if *witnessDir == "" {
		fmt.Fprintln(os.Stderr, "usage: --dir D --start N --count K")
		os.Exit(1)
	}
	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*witnessDir, freezer.TableBlockWitness, "c")
	if err != nil {
		panic(err)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	var totalReads, absentReads, totalBytes, absentBlobs uint64
	var blocks uint64
	end := *start + *count
	for n := *start; n < end; n++ {
		data, err := tbl.Retrieve(n)
		if err != nil || len(data) == 0 {
			continue
		}
		blocks++
		pos := 0
		blkAbsent := uint64(0)
		blkReads := uint64(0)
		for pos < len(data) {
			l := int(data[pos])
			pos++
			pos += l
			if l == 0 {
				blkAbsent++
			}
			blkReads++
		}
		totalReads += blkReads
		absentReads += blkAbsent
		totalBytes += uint64(len(data))
		if blkReads > 0 && blkAbsent == blkReads {
			absentBlobs++
		}
	}

	if totalReads == 0 {
		fmt.Println("no reads")
		return
	}
	fmt.Printf("=== witness absent-read stats ===\n")
	fmt.Printf("blocks sampled : %d (range %d..%d)\n", blocks, *start, end-1)
	fmt.Printf("total reads    : %d (%.0f / block)\n", totalReads, float64(totalReads)/float64(blocks))
	fmt.Printf("absent reads   : %d (%.2f%%)\n", absentReads, float64(absentReads)*100/float64(totalReads))
	fmt.Printf("present reads  : %d (%.2f%%)\n", totalReads-absentReads, float64(totalReads-absentReads)*100/float64(totalReads))
	fmt.Printf("witness bytes  : %d (%.0f B/block avg)\n", totalBytes, float64(totalBytes)/float64(blocks))
}
