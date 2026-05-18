// storcs-bytes-breakdown: sample storcs blocks and break down the
// physical bytes into: framing, addr key, slot key, oldVal, newVal.
// Reports per-component byte totals so we can answer
// "what % of storcs's 260 GB is key vs value?".
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	frDir := flag.String("freezer", "", "freezer dir with storcs")
	samples := flag.Int("samples", 5000, "number of blocks to sample")
	startBlk := flag.Uint64("start", 0, "start block (default 0)")
	endBlk := flag.Uint64("end", 0, "end block (default head)")
	seed := flag.Int64("seed", 0, "RNG seed (0=time)")
	flag.Parse()

	if *frDir == "" {
		fmt.Fprintln(os.Stderr, "usage: storcs-bytes-breakdown --freezer <dir> [--samples N --start B --end B]")
		os.Exit(1)
	}
	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	fr, err := freezer.NewReadOnly(*frDir)
	check(err, "open freezer")
	defer fr.Close()
	tbl, err := fr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	check(err, "open storcs")
	if *endBlk == 0 || *endBlk > tbl.Items() {
		*endBlk = tbl.Items()
	}

	var (
		totalRawBytes  uint64 // decoded codec bytes (pre-cdat-zstd)
		framingBytes   uint64 // 2B addrCount + 2B/group slotCount + 1B oldLen + 1B newLen
		addrBytes      uint64 // 20B × addrCount per block
		slotBytes      uint64 // 32B × entries
		oldValBytes    uint64
		newValBytes    uint64
		entries        uint64
		addrCount      uint64
		zeroOldCount   uint64
		zeroNewCount   uint64
		oldNonZeroSum  uint64
		newNonZeroSum  uint64
		blocksSampled  uint64
		blocksWithData uint64
	)
	oldLenHist := make([]uint64, 33) // 0..32
	newLenHist := make([]uint64, 33)

	t0 := time.Now()
	for i := 0; i < *samples; i++ {
		blk := *startBlk + uint64(rng.Int63n(int64(*endBlk-*startBlk)))
		data, err := tbl.Retrieve(blk)
		if err != nil {
			continue
		}
		blocksSampled++
		totalRawBytes += uint64(len(data))
		if len(data) == 0 {
			continue
		}
		blocksWithData++
		changes, err := ethel.DecodeStorageChanges(data)
		if err != nil {
			continue
		}
		// Need per-block addr count for proper bytes accounting
		seenAddrs := make(map[[20]byte]bool, 16)
		for _, c := range changes {
			var a [20]byte
			copy(a[:], c.CompositeKey[:20])
			seenAddrs[a] = true
			slotBytes += 32
			ol, nl := len(c.OldValue), len(c.NewValue)
			oldValBytes += uint64(ol)
			newValBytes += uint64(nl)
			framingBytes += 2 // oldLen + newLen
			if ol == 0 {
				zeroOldCount++
			} else {
				oldNonZeroSum += uint64(ol)
				oldLenHist[ol]++
			}
			if nl == 0 {
				zeroNewCount++
			} else {
				newNonZeroSum += uint64(nl)
				newLenHist[nl]++
			}
			entries++
		}
		addrCount += uint64(len(seenAddrs))
		addrBytes += uint64(len(seenAddrs)) * 20
		framingBytes += 2 + uint64(len(seenAddrs))*2 // addrCount + slotCount per group
	}
	t := time.Since(t0)

	decodedBytes := framingBytes + addrBytes + slotBytes + oldValBytes + newValBytes
	fmt.Printf("=== Sample (seed=%d, %d blocks, %d had data) ===\n", *seed, blocksSampled, blocksWithData)
	fmt.Printf("Elapsed: %s\n\n", t.Truncate(time.Millisecond))
	fmt.Printf("Total raw cdat bytes read (post-zstd-decoded): %d\n", totalRawBytes)
	fmt.Printf("Sum of decoded field bytes:                    %d  (sanity, should == above)\n", decodedBytes)
	fmt.Printf("\nEntries: %d (%.1f per block-with-data, %.1f per addr-group)\n",
		entries, float64(entries)/float64(blocksWithData), float64(entries)/float64(addrCount))
	fmt.Printf("Addr groups: %d\n", addrCount)
	fmt.Println()

	pct := func(x uint64) float64 { return float64(x) * 100 / float64(decodedBytes) }
	per := func(x uint64) float64 {
		if entries == 0 {
			return 0
		}
		return float64(x) / float64(entries)
	}

	fmt.Printf("%-20s  %14s   %6s   %7s\n", "Component", "Bytes", "% raw", "B/entry")
	fmt.Printf("%-20s  %14d   %5.1f%%   %7.2f\n", "framing", framingBytes, pct(framingBytes), per(framingBytes))
	fmt.Printf("%-20s  %14d   %5.1f%%   %7.2f\n", "addr key (20B amort)", addrBytes, pct(addrBytes), per(addrBytes))
	fmt.Printf("%-20s  %14d   %5.1f%%   %7.2f\n", "slot key (32B)", slotBytes, pct(slotBytes), per(slotBytes))
	fmt.Printf("%-20s  %14d   %5.1f%%   %7.2f\n", "OldValue", oldValBytes, pct(oldValBytes), per(oldValBytes))
	fmt.Printf("%-20s  %14d   %5.1f%%   %7.2f\n", "NewValue", newValBytes, pct(newValBytes), per(newValBytes))
	fmt.Printf("%-20s  %14d  ------   %7.2f\n", "TOTAL", decodedBytes, per(decodedBytes))

	fmt.Println()
	fmt.Printf("OldValue zero/empty: %d (%.1f%%)  non-zero avg %.2f B\n",
		zeroOldCount, float64(zeroOldCount)*100/float64(entries),
		float64(oldNonZeroSum)/float64(entries-zeroOldCount))
	fmt.Printf("NewValue zero/empty: %d (%.1f%%)  non-zero avg %.2f B\n",
		zeroNewCount, float64(zeroNewCount)*100/float64(entries),
		float64(newNonZeroSum)/float64(entries-zeroNewCount))

	fmt.Println("\nNewValue length distribution (non-zero):")
	for l := 1; l <= 32; l++ {
		c := newLenHist[l]
		if c == 0 {
			continue
		}
		fmt.Printf("  len %2d: %12d  (%.2f%%)\n", l, c, float64(c)*100/float64(entries-zeroNewCount))
	}

	// Estimate: redundancy savings if we dropped NewValue
	withoutNew := framingBytes + addrBytes + slotBytes + oldValBytes
	fmt.Printf("\nWithout NewValue: %d bytes (%.1f%% of current)\n",
		withoutNew, float64(withoutNew)*100/float64(decodedBytes))
}

func check(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", what, err)
		os.Exit(1)
	}
}
