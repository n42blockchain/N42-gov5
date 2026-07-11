// block-stats: find the ETH-mainnet block with the most transactions and the
// block with the largest body, from local data — WITHOUT a full body scan.
//
//   - max-tx: read per-block tx counts straight from the txindex V2 dat
//     (Elias-Fano of cumulative counts) — no body decode.
//   - max-size: scan the geth ancient bodies.cidx for the largest COMPRESSED
//     per-block delta (a proxy), keep the top-K, then decompress just those
//     (+ the max-tx block) to report the exact RLP body size and tx count.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient chain dir")
	txdir := flag.String("txdir", "d:/n42-eth1/chain/freezer", "dir with txindex (cscompact)")
	topK := flag.Int("topk", 12, "decompress this many largest-compressed blocks for exact raw size")
	flag.Parse()

	fz, err := freezer.NewReadOnly(*ancient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open ancient: %v\n", err)
		os.Exit(1)
	}
	defer fz.Close()
	frozen := fz.Frozen()
	fmt.Printf("geth ancient frozen = %d\n", frozen)

	// ---- max tx, from txindex V2 Elias-Fano per-block counts ----
	maxTxBlock, maxTx := scanMaxTx(*txdir)
	fmt.Printf("\n[max-tx] block %d : %d txs (from txindex EF, covers its range)\n", maxTxBlock, maxTx)

	// ---- top-K largest compressed bodies, from geth bodies.cidx deltas ----
	cands := topCompressedBodies(*ancient+"/bodies.cidx", *topK)

	// ---- decompress candidates (+ max-tx block) for exact raw size + tx count ----
	type stat struct {
		block   uint64
		rawSize int
		txCount int
	}
	report := func(block uint64) stat {
		data, err := fz.Ancient(freezer.TableBodies, block)
		if err != nil {
			return stat{block, -1, -1}
		}
		n := -1
		if body, err := ethel.DecodeGethBody(data); err == nil {
			n = len(body.Transactions)
		}
		return stat{block, len(data), n}
	}

	fmt.Printf("\n[top-%d largest compressed bodies → exact raw RLP size]\n", *topK)
	var best stat
	var stats []stat
	for _, b := range cands {
		s := report(b)
		stats = append(stats, s)
		if s.rawSize > best.rawSize {
			best = s
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].rawSize > stats[j].rawSize })
	for _, s := range stats {
		fmt.Printf("  block %-9d raw body %d B (%.1f KB), %d txs\n", s.block, s.rawSize, float64(s.rawSize)/1024, s.txCount)
	}

	mt := report(maxTxBlock)
	fmt.Printf("\n=== RESULTS ===\n")
	fmt.Printf("most-tx block : %d — %d txs, body %d B (%.1f KB)\n", mt.block, mt.txCount, mt.rawSize, float64(mt.rawSize)/1024)
	fmt.Printf("largest body  : %d — %d B (%.1f KB), %d txs\n", best.block, best.rawSize, float64(best.rawSize)/1024, best.txCount)
}

// scanMaxTx reads each txindex segment's V2 dat (EFD2 magic + blockCount +
// txCount + Elias-Fano cumulative counts) and returns the block with the most
// txs. Per-block count = ef.Get(i+1) - ef.Get(i).
func scanMaxTx(dir string) (uint64, uint64) {
	store, err := cscompact.OpenSegmentStore(dir, "txindex")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open txindex: %v\n", err)
		return 0, 0
	}
	defer store.Close()
	const segSize = 1_000_000
	var bestBlock, bestTx uint64
	for s := uint64(0); s < store.SegmentCount(); s++ {
		data, err := store.ReadSegmentData(s)
		if err != nil || len(data) < 16 || string(data[:4]) != "EFD2" {
			continue
		}
		blockCount := uint64(binary.LittleEndian.Uint32(data[4:8]))
		txCount := binary.LittleEndian.Uint64(data[8:16])
		if blockCount == 0 || txCount == 0 || len(data) < 32 {
			continue
		}
		ef, _ := eliasfano32.ReadEliasFano(data[16:])
		if ef == nil {
			continue
		}
		prev := ef.Get(0)
		for i := uint64(1); i <= blockCount; i++ {
			cur := ef.Get(i)
			if n := cur - prev; n > bestTx {
				bestTx = n
				bestBlock = s*segSize + (i - 1)
			}
			prev = cur
		}
	}
	return bestBlock, bestTx
}

// topCompressedBodies streams the geth freezer bodies.cidx (6-byte entries:
// [2B fileNum LE][4B offset LE]) and returns the topK block numbers by
// compressed size delta.
func topCompressedBodies(cidxPath string, topK int) []uint64 {
	f, err := os.Open(cidxPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open cidx: %v\n", err)
		return nil
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)

	type cand struct {
		block uint64
		size  int64
	}
	top := make([]cand, 0, topK+1)
	insert := func(c cand) {
		if len(top) < topK {
			top = append(top, c)
		} else if c.size > top[len(top)-1].size {
			top[len(top)-1] = c
		} else {
			return
		}
		sort.Slice(top, func(i, j int) bool { return top[i].size > top[j].size })
	}

	var ent [6]byte
	var prevFile uint16
	var prevOff uint32
	first := true
	var block uint64
	for {
		n, err := readFull(r, ent[:])
		if n < 6 || err != nil {
			break
		}
		fn := binary.LittleEndian.Uint16(ent[0:2])
		off := binary.LittleEndian.Uint32(ent[2:6])
		if first {
			prevFile, prevOff, first = fn, off, false
			continue
		}
		var size int64
		if fn == prevFile {
			size = int64(off) - int64(prevOff)
		} else {
			size = int64(off) // item starts at 0 of the new file
		}
		insert(cand{block: block, size: size})
		prevFile, prevOff = fn, off
		block++
	}
	out := make([]uint64, len(top))
	for i, c := range top {
		out[i] = c.block
	}
	return out
}

func readFull(r *bufio.Reader, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := r.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
