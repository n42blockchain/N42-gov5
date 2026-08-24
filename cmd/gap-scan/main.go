// gap-scan — check block-keyed freezer tables for empty-item gaps like the
// acctcs/storcs [25101824,25101866] resume-gap. Reports empty items in the known
// boundary window plus a scattered sample across the whole table.
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
	gapHi = 25101866
)

func openTable(dir, name string) (*freezer.FreezerTable, error) {
	// Try NCIX compressed (witness/acctcs/storcs); fall back to plain.
	if t, err := freezer.NewFreezerTableCompressedReadOnly(dir, name, "c"); err == nil {
		return t, nil
	}
	return freezer.NewFreezerTableReadOnly(dir, name, "c")
}

func main() {
	dir := "D:/N42-eth1177/chain/freezer"
	if len(os.Args) >= 2 {
		dir = os.Args[1]
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	for _, name := range []string{"witness", "senders", "acctcs", "storcs"} {
		t, err := openTable(dir, name)
		if err != nil {
			fmt.Printf("%-8s OPEN FAIL: %v\n", name, err)
			continue
		}
		items := t.Items()

		// 1) Dense scan of the known boundary window.
		emptyInGap := 0
		var firstE, lastE int64 = -1, -1
		for n := uint64(gapLo - 8); n <= gapHi+8 && n < items; n++ {
			b, e := t.Retrieve(n)
			if e != nil {
				continue
			}
			if len(b) == 0 {
				emptyInGap++
				if firstE < 0 {
					firstE = int64(n)
				}
				lastE = int64(n)
			}
		}

		// 2) Scattered sample across the whole table: count empties, flag clusters.
		const samples = 4000
		step := items / samples
		if step == 0 {
			step = 1
		}
		emptySample, checked := 0, 0
		for n := uint64(0); n < items; n += step {
			b, e := t.Retrieve(n)
			if e != nil {
				continue
			}
			checked++
			if len(b) == 0 {
				emptySample++
			}
		}
		t.Close()
		fmt.Printf("%-8s items=%d  gap-window[%d,%d] empties=%d (first=%d last=%d)  sample(%d) empties=%d\n",
			name, items, gapLo, gapHi, emptyInGap, firstE, lastE, checked, emptySample)
	}
}
