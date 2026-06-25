// verify-splice — independent cross-check of a splice-cs output dir against the
// source changeset freezer: sampled non-gap items must be byte-identical (raw
// batch copy), gap items must be non-empty in the splice and empty in the source.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	gapLo = 25101824
	gapHi = 25101866
)

func count2(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	return int(binary.LittleEndian.Uint16(b[:2]))
}

func main() {
	_ = context.Background
	srcDir := "D:/N42-eth1177/chain/freezer"
	splDir := "D:/N42-eth1177-cs-spliced"
	if len(os.Args) >= 3 {
		srcDir, splDir = os.Args[1], os.Args[2]
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	for _, table := range []string{"acctcs", "storcs"} {
		src, err := freezer.NewFreezerTableCompressedReadOnly(srcDir, table, "c")
		if err != nil {
			fmt.Printf("%s open src: %v\n", table, err)
			os.Exit(1)
		}
		spl, err := freezer.NewFreezerTableCompressedReadOnly(splDir, table, "c")
		if err != nil {
			fmt.Printf("%s open spl: %v\n", table, err)
			os.Exit(1)
		}
		maxItems := src.Items()
		if spl.Items() != maxItems {
			fmt.Printf("%s ITEMS MISMATCH src=%d spl=%d\n", table, maxItems, spl.Items())
			os.Exit(1)
		}

		// 1) Gap items: spliced non-empty, source empty (count==0).
		gapFilled, gapSrcEmpty := 0, 0
		for n := uint64(gapLo); n <= gapHi; n++ {
			sb, _ := spl.Retrieve(n)
			ob, _ := src.Retrieve(n)
			if count2(sb) > 0 {
				gapFilled++
			}
			if count2(ob) == 0 {
				gapSrcEmpty++
			}
		}

		// 2) Sampled non-gap items across [gapHi+1, maxItems): must be byte-identical.
		mism, checked := 0, 0
		step := (maxItems - (gapHi + 1)) / 5000
		if step == 0 {
			step = 1
		}
		for n := uint64(gapHi + 1); n < maxItems; n += step {
			a, e1 := src.Retrieve(n)
			b, e2 := spl.Retrieve(n)
			if e1 != nil || e2 != nil {
				fmt.Printf("%s retrieve %d: %v / %v\n", table, n, e1, e2)
				os.Exit(1)
			}
			checked++
			if len(a) != len(b) {
				mism++
				continue
			}
			for i := range a {
				if a[i] != b[i] {
					mism++
					break
				}
			}
		}
		// Also the exact last item + a few just after the gap batch.
		for _, n := range []uint64{gapHi + 1, gapHi + 2, 25101888, maxItems - 1} {
			a, _ := src.Retrieve(n)
			b, _ := spl.Retrieve(n)
			checked++
			same := len(a) == len(b)
			if same {
				for i := range a {
					if a[i] != b[i] {
						same = false
						break
					}
				}
			}
			if !same {
				mism++
			}
		}

		src.Close()
		spl.Close()
		status := "OK"
		if mism != 0 || gapFilled != 43 || gapSrcEmpty != 43 {
			status = "FAIL"
		}
		fmt.Printf("[%s] %s  items=%d  gap: spliced-filled=%d/43 source-empty=%d/43  non-gap: checked=%d mismatch=%d\n",
			status, table, maxItems, gapFilled, gapSrcEmpty, checked, mism)
		if status == "FAIL" {
			os.Exit(1)
		}
	}
	fmt.Println("VERIFY-SPLICE PASS")
}
