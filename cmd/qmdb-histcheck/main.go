// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// qmdb-histcheck — end-to-end audit of the QMDB full-history layer on a real
// replayed chain: reload the tree, then for sampled (key, height) pairs run
// ProofAtHeight and check the reconstructed root against the block HEADER's
// state root (the consensus oracle) and the proof against that root.
//
//	qmdb-histcheck --db D:/qmdb-hist-smoke/chaindata --heights 1000,25000,49999 --keys 20
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	db := flag.String("db", "", "chaindata dir of a --tree qmdb --qmdb-history replay target")
	heightsCSV := flag.String("heights", "", "comma-separated heights to audit (default: 8 spread over the recorded range)")
	nKeys := flag.Int("keys", 12, "sampled keys per height (from the version index)")
	mapGB := flag.Int("map.gb", 512, "MDBX map size GB")
	flag.Parse()
	if *db == "" {
		die("--db required")
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	kdb, err := mdbxkv.NewMDBX(log.New()).Path(*db).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer kdb.Close()
	tx, err := kdb.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	rc := commitment.NewQMDBRootComputer()
	t0 := time.Now()
	if err := rc.LoadFrom(tx); err != nil {
		die("LoadFrom: %v", err)
	}
	rc.SetCold(tx)
	rc.EnableHistory(tx) // read-only: ProofAtHeight only
	fmt.Printf("tree reloaded in %v: liveKeys=%d twigs=%d nextSlot=%d\n",
		time.Since(t0).Round(time.Millisecond), rc.Tree().LiveCount(), rc.Tree().NumTwigs(), rc.Tree().NextSlot())

	// Height set.
	var heights []uint64
	if *heightsCSV != "" {
		for _, s := range strings.Split(*heightsCSV, ",") {
			var h uint64
			fmt.Sscanf(strings.TrimSpace(s), "%d", &h)
			heights = append(heights, h)
		}
	} else {
		// Spread over the recorded blockPos range.
		lo, hi, ok := blockPosRange(tx)
		if !ok {
			die("no qmdbBlockPos records — was the replay run with --qmdb-history?")
		}
		for i := 0; i < 8; i++ {
			heights = append(heights, lo+(hi-lo)*uint64(i)/7)
		}
	}

	// Key sample: first N distinct keys from the version index.
	keys := sampleKeys(tx, *nKeys)
	fmt.Printf("auditing %d heights × up to %d keys\n", len(heights), len(keys))

	totalPass, totalFail := 0, 0
	for _, h := range heights {
		hdr := rawdb.ReadHeaderByNumber(tx, h)
		if hdr == nil {
			fmt.Printf("h=%d: no header — skip\n", h)
			continue
		}
		want := qmdb.Hash(hdr.Root)
		t1 := time.Now()
		pass, fail, found := 0, 0, 0
		for _, kh := range keys {
			proof, root, ok, err := rc.ProofAtHeight(kh, h)
			if err != nil {
				fmt.Printf("  h=%d key=%x: ERR %v\n", h, kh[:6], err)
				fail++
				continue
			}
			if root != want {
				fmt.Printf("  h=%d key=%x: ROOT MISMATCH recon=%x header=%x\n", h, kh[:6], root[:8], want[:8])
				fail++
				continue
			}
			if ok {
				found++
				if !qmdb.VerifyProof(want, proof) {
					fmt.Printf("  h=%d key=%x: proof does not verify\n", h, kh[:6])
					fail++
					continue
				}
			}
			pass++
		}
		totalPass += pass
		totalFail += fail
		fmt.Printf("h=%-9d pass=%d fail=%d (found=%d) in %v\n", h, pass, fail, found, time.Since(t1).Round(time.Millisecond))
	}
	fmt.Printf("TOTAL pass=%d fail=%d\n", totalPass, totalFail)
	if totalFail > 0 {
		os.Exit(1)
	}
}

func blockPosRange(tx kv.Tx) (lo, hi uint64, ok bool) {
	c, err := tx.Cursor(modules.QMDBBlockPos)
	if err != nil {
		return 0, 0, false
	}
	defer c.Close()
	fk, _, _ := c.First()
	lk, _, _ := c.Last()
	if fk == nil || lk == nil {
		return 0, 0, false
	}
	return be8dec(fk), be8dec(lk), true
}

func be8dec(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

func sampleKeys(tx kv.Tx, n int) []qmdb.Hash {
	c, err := tx.Cursor(modules.QMDBKeyVers)
	if err != nil {
		return nil
	}
	defer c.Close()
	var out []qmdb.Hash
	// Stride through the table to sample diverse keys, not just the first N.
	k, _, _ := c.First()
	for k != nil && len(out) < n {
		var kh qmdb.Hash
		copy(kh[:], k)
		out = append(out, kh)
		// jump ahead: bump the top bytes to skip a chunk of keyspace
		seek := append([]byte{}, k...)
		seek[1] += 16
		if seek[1] < 16 { // wrapped
			seek[0]++
		}
		k, _, _ = c.Seek(seek)
	}
	return out
}
