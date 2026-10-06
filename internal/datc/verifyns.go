// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// verifyns.go — check the exact storage records through the READER.
//
// derive-ns compares what it computes with the storage-root history before it
// writes. That says nothing about what a reader finds afterwards: a bucket
// mix-up, a bad merge or a stale ladder would pass it. verify-ns takes listed
// contracts and blocks where their storage root is known (rows of the
// per-block storage-root history), assembles the root the way a proof does —
// floor records, the levels above them, a fold where the ladder says so, the
// birth partitions at early heights — and compares.
package datc

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// exactRootAt is the contract's storage root at n as the exact ladder yields
// it, without the storage-root history shortcut nodeHashAt takes.
func (q *querier) exactRootAt(domain []byte, n uint64) (types.Hash, bool, error) {
	slots, nKids, usable, err := q.branchSlotsAt(domain, nil, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if !usable {
		return q.foldAt(domain, nil, n)
	}
	if nKids == 0 {
		return types.Hash{}, false, nil
	}
	return branch17Hash(slots), true, nil
}

func runVerifyNS(args []string) {
	fs := flag.NewFlagSet("verify-ns", flag.ExitOnError)
	out := fs.String("out", "", "archive")
	contracts := fs.Int("contracts", 200, "listed contracts to sample")
	perContract := fs.Int("per-contract", 8, "blocks per contract, spread over its life (every growth stage gets some)")
	from := fs.Uint64("from", 0, "only blocks >= this one (after a weekly extension: the previous head)")
	seed := fs.Int64("seed", 1, "sampling seed")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	modulesInit()
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()
	q, head, err := loadQuerier(tx, *out, 0)
	if err != nil {
		die("%v", err)
	}
	defer q.Close()
	if q.exact == nil || q.segSR == nil {
		die("%s has no exact ladder (ns.ladders) or no storage-root segments", *out)
	}
	doms := make([]string, 0, len(q.exact.m))
	for d := range q.exact.m {
		doms = append(doms, d)
	}
	sort.Strings(doms)
	rng := rand.New(rand.NewSource(*seed))
	rng.Shuffle(len(doms), func(i, j int) { doms[i], doms[j] = doms[j], doms[i] })
	if len(doms) > *contracts {
		doms = doms[:*contracts]
	}

	checked, bad := 0, 0
	byDepth := map[int]int{}
	for _, d := range doms {
		dom := []byte(d)
		// The contract's storage-root rows, reservoir-sampled per growth stage
		// so the short early stages are not drowned by the last one.
		rungs := q.exact.m[d]
		bounds := make([]uint64, len(rungs))
		for i, r := range rungs {
			bounds[i] = r.from
		}
		type row struct {
			block uint64
			root  []byte
		}
		picked := make([][]row, len(bounds)+1)
		seen := make([]int, len(bounds)+1)
		per := *perContract/(len(bounds)+1) + 1
		c := q.segSR.Cursor()
		for k, v, err := c.Seek(dom); k != nil; k, v, err = c.Next() {
			if err != nil {
				die("sr: %v", err)
			}
			if len(k) != stoDomainLen+blkLen || !bytes.Equal(k[:stoDomainLen], dom) {
				break
			}
			blk := uint64(binary.BigEndian.Uint32(k[stoDomainLen:]))
			if blk < *from || blk >= head {
				continue
			}
			g := stageOf(bounds, blk)
			seen[g]++
			r := row{blk, append([]byte(nil), v...)}
			if len(picked[g]) < per {
				picked[g] = append(picked[g], r)
			} else if j := rng.Intn(seen[g]); j < per {
				picked[g][j] = r
			}
		}
		for g := range picked {
			for _, r := range picked[g] {
				got, exists, err := q.exactRootAt(dom, r.block)
				if err != nil {
					die("contract %x at %d: %v", dom[:6], r.block, err)
				}
				want := len(r.root) == 32
				if exists != want || (exists && !bytes.Equal(got[:], r.root)) {
					bad++
					fmt.Printf("MISMATCH contract %x block %d (stage %d, depth %d): ladder root %x exists=%v, storage-root history %x\n",
						dom[:6], r.block, g, q.exact.depthAt(dom, r.block), got[:8], exists, r.root)
				}
				checked++
				byDepth[q.exact.depthAt(dom, r.block)]++
			}
		}
	}
	fmt.Printf("verify-ns: %d contracts, %d roots assembled through the ladder (by depth in force: %v), %d mismatches\n", len(doms), checked, byDepth, bad)
	if bad > 0 {
		os.Exit(2)
	}
}
