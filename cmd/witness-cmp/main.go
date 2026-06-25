// witness-cmp — compare two witness freezers on overlapping NON-gap blocks.
// If F:/ethdata matches D:/N42-eth1177 byte-for-byte on the blocks both have
// (outside the gap), they are the same witness generation and F:/ethdata's
// gap-block witnesses are the trustworthy originals.
package main

import (
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	gapLo = 25101824
	gapHi = 25101865
)

func main() {
	aDir, bDir := "D:/N42-eth1177/chain/freezer", "F:/ethdata"
	if len(os.Args) >= 3 {
		aDir, bDir = os.Args[1], os.Args[2]
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	a, err := freezer.NewFreezerTableCompressedReadOnly(aDir, "witness", "c")
	if err != nil {
		fmt.Println("open A:", err)
		os.Exit(1)
	}
	defer a.Close()
	b, err := freezer.NewFreezerTableCompressedReadOnly(bDir, "witness", "c")
	if err != nil {
		fmt.Println("open B:", err)
		os.Exit(1)
	}
	defer b.Close()

	overlap := a.Items()
	if b.Items() < overlap {
		overlap = b.Items()
	}
	fmt.Printf("A=%s items=%d\nB=%s items=%d\noverlap=[0,%d)\n", aDir, a.Items(), bDir, b.Items(), overlap)

	// Sample non-gap blocks across the overlap, with a dense cluster near the gap.
	pts := []uint64{}
	step := overlap / 3000
	if step == 0 {
		step = 1
	}
	for n := uint64(0); n < overlap; n += step {
		pts = append(pts, n)
	}
	for n := uint64(gapLo - 30); n <= gapHi+1 && n < overlap; n++ {
		pts = append(pts, n) // dense around the gap edges (skip the gap itself in A)
	}

	checked, mism, bothEmpty, skippedGap := 0, 0, 0, 0
	var firstMism int64 = -1
	for _, n := range pts {
		if n >= gapLo && n <= gapHi {
			skippedGap++
			continue // A's gap is empty by definition
		}
		av, _ := a.Retrieve(n)
		bv, _ := b.Retrieve(n)
		checked++
		if len(av) == 0 && len(bv) == 0 {
			bothEmpty++
			continue
		}
		same := len(av) == len(bv)
		if same {
			for i := range av {
				if av[i] != bv[i] {
					same = false
					break
				}
			}
		}
		if !same {
			mism++
			if firstMism < 0 {
				firstMism = int64(n)
			}
		}
	}
	status := "SAME-GENERATION"
	if mism != 0 {
		status = "DIFFERENT (mismatch)"
	}
	fmt.Printf("[%s] checked=%d mismatch=%d bothEmpty=%d skippedGap=%d firstMism=%d\n",
		status, checked, mism, bothEmpty, skippedGap, firstMism)
	if mism != 0 {
		os.Exit(1)
	}
}
