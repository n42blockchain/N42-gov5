// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// benchplan.go — stratified storage-proof queries for `bench --queries`.
//
// Sampling the changesets uniformly by block draws what the chain does most:
// mature heights of whatever contracts are busy. The cases a ladder is judged
// by are rare there — a giant contract, a mid-size one just under the record
// threshold, the first blocks of a contract that later became huge, an early
// height. bench-plan draws a fixed number of queries per (contract class,
// phase of the contract's life) from the storage leaf history itself, so each
// stratum gets its own percentiles.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/types"
)

// planPhases are points of a contract's life [first, last] a query is placed
// at. infancy is the case a depth sized for the contract's final key count
// serves worst: almost nothing exists yet below the recorded level.
var planPhases = []struct {
	name string
	at   float64
}{{"infancy", 0.002}, {"early", 0.10}, {"mid", 0.50}, {"late", 0.95}}

func runBenchPlan(args []string) {
	fs := flag.NewFlagSet("bench-plan", flag.ExitOnError)
	out := fs.String("out", "", "archive (reads the storage leaf history)")
	list := fs.String("contracts", "", "file of '<addrHash> <class> <firstBlock> <lastBlock>' lines")
	perPhase := fs.Int("per-phase", 2, "queries per contract and phase")
	seed := fs.Int64("seed", 1, "sampling seed")
	jsonOut := fs.String("json", "", "write the queries here")
	_ = fs.Parse(args)
	if *out == "" || *list == "" || *jsonOut == "" {
		die("--out, --contracts and --json required")
	}
	f, err := os.Open(*list)
	if err != nil {
		die("contracts: %v", err)
	}
	defer f.Close()
	set, ok, err := openLeafSegSet(*out, segTabLeafS, newFrameLRUSize(64))
	if err != nil || !ok {
		die("open storage leaf history: ok=%v err=%v", ok, err)
	}
	defer set.Close()
	rng := rand.New(rand.NewSource(*seed))

	var queries []benchSample
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		txt := strings.TrimSpace(sc.Text())
		if txt == "" || txt[0] == '#' {
			continue
		}
		p := strings.Fields(txt)
		if len(p) != 4 {
			die("%s:%d: want <addrHash> <class> <firstBlock> <lastBlock>", *list, line)
		}
		dom, err := hex.DecodeString(p[0])
		first, err1 := strconv.ParseUint(p[2], 10, 64)
		last, err2 := strconv.ParseUint(p[3], 10, 64)
		if err != nil || len(dom) != stoDomainLen || err1 != nil || err2 != nil || last < first {
			die("%s:%d: bad line", *list, line)
		}
		var ah types.Hash
		copy(ah[:], dom)
		for _, ph := range planPhases {
			height := first + uint64(float64(last-first)*ph.at)
			for i := 0; i < *perPhase; i++ {
				// A slot of this contract near a random point of its key space.
				// It may not exist yet at `height` — an exclusion proof walks
				// the same path.
				seek := append(append([]byte{}, dom...), make([]byte, 32)...)
				rng.Read(seek[stoDomainLen:])
				k, _, err := set.Cursor().Seek(seek)
				if err != nil {
					die("seek: %v", err)
				}
				if k == nil || !bytes.HasPrefix(k, dom) {
					if k, _, err = set.Cursor().Seek(dom); err != nil || k == nil || !bytes.HasPrefix(k, dom) {
						die("%s:%d: contract has no storage rows", *list, line)
					}
				}
				var sh types.Hash
				copy(sh[:], k[stoDomainLen:stoDomainLen+32])
				a := ah
				queries = append(queries, benchSample{
					AddrHash:   &a,
					SlotHashes: []types.Hash{sh},
					Touched:    uint64(binary.BigEndian.Uint32(k[stoDomainLen+32:])),
					Height:     height,
					Class:      p[1] + "/" + ph.name,
				})
			}
		}
	}
	if err := sc.Err(); err != nil {
		die("contracts: %v", err)
	}
	enc, _ := json.MarshalIndent(queries, "", " ")
	if err := os.WriteFile(*jsonOut, enc, 0o644); err != nil {
		die("write: %v", err)
	}
	fmt.Printf("planned %d queries\n", len(queries))
}
