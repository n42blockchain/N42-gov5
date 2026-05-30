// n42-stateless-bench compares, per real block, the three data sizes that decide
// whether MPT-stateless verification is worth shipping for the minimal client:
//   - witness      (eth-el v1 = changed leaf values + changeset deltas)
//   - block        (header+body; real if datadir has the block tables, else ref)
//   - MPT stateless(multiproof: TrieAccount/TrieStorage nodes on changed paths)
//
// It reads the REAL per-block changeset that yesterday's catch-up wrote into
// D:/N42-hashed (AccountChangeSet/StorageChangeSet, hashed keys), so the
// changed-key set per block is exact. Reports avg + p50/p90/p99/max and the
// per-block bandwidth under "every block" vs "every-100-block anchor".
//
//	n42-stateless-bench --dir D:/N42-hashed/chaindata --blocks 100
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	tAccCS    = "AccountChangeSet"
	tStorCS   = "StorageChangeSet"
	tTrieAcc  = "TrieAccount"
	tTrieSto  = "TrieStorage"
	tHdrCanon = "CanonicalHeader"
	tHeader   = "Header"
	tBody     = "BlockBody"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tAccCS] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tTrieAcc] = kv.TableCfgItem{}
	d[tTrieSto] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tHdrCanon] = kv.TableCfgItem{}
	d[tHeader] = kv.TableCfgItem{}
	d[tBody] = kv.TableCfgItem{}
	return d
}

func toNibbles(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = x >> 4
		out[2*i+1] = x & 0x0f
	}
	return out
}

type proof struct {
	nodes map[string][]byte
	naive int
}

func newProof() *proof { return &proof{nodes: map[string][]byte{}} }

func (p *proof) collect(tx kv.Tx, table string, prefix, nib []byte) {
	buf := make([]byte, 0, len(prefix)+len(nib))
	for l := 0; l <= len(nib); l++ {
		buf = append(buf[:0], prefix...)
		buf = append(buf, nib[:l]...)
		v, err := tx.GetOne(table, buf)
		if err != nil || v == nil {
			continue
		}
		p.naive += len(v)
		if _, ok := p.nodes[string(buf)]; !ok {
			p.nodes[string(buf)] = append([]byte(nil), v...)
		}
	}
}

func (p *proof) dedup() int {
	t := 0
	for _, v := range p.nodes {
		t += len(v)
	}
	return t
}

func zstdLen(raw []byte) int {
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	z := enc.EncodeAll(raw, nil)
	enc.Close()
	return len(z)
}

func readBlockSize(tx kv.Tx, blk uint64) int {
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blk)
	h, _ := tx.GetOne(tHdrCanon, bk[:])
	if len(h) != 32 {
		return 0
	}
	key := append(append([]byte(nil), bk[:]...), h...)
	hdr, _ := tx.GetOne(tHeader, key)
	body, _ := tx.GetOne(tBody, key)
	return len(hdr) + len(body)
}

func pct(s []float64, p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[int(p*float64(len(s)-1))]
}

func changedAccounts(tx kv.Tx, blk uint64) ([][]byte, int) {
	c, _ := tx.CursorDupSort(tAccCS)
	defer c.Close()
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blk)
	var keys [][]byte
	total := 0
	for k, v, e := c.Seek(bk[:]); k != nil && e == nil; k, v, e = c.Next() {
		if len(k) < 8 || binary.BigEndian.Uint64(k[:8]) != blk {
			break
		}
		if len(v) >= 32 {
			keys = append(keys, append([]byte(nil), v[:32]...))
			total += len(v)
		}
	}
	return keys, total
}

func changedStorage(tx kv.Tx, blk uint64) ([][]byte, int) {
	c, _ := tx.CursorDupSort(tStorCS)
	defer c.Close()
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blk)
	var keys [][]byte
	total := 0
	for k, v, e := c.Seek(bk[:]); k != nil && e == nil; k, v, e = c.Next() {
		if len(k) < 8 || binary.BigEndian.Uint64(k[:8]) != blk {
			break
		}
		var composite []byte
		switch {
		case len(k) >= 40 && len(v) >= 32: // key=blk+addr, val=slot+val
			composite = append(append([]byte(nil), k[8:40]...), v[:32]...)
		case len(v) >= 64: // key=blk, val=addr+slot+val
			composite = append([]byte(nil), v[:64]...)
		default:
			continue
		}
		keys = append(keys, composite)
		total += len(v)
	}
	return keys, total
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 catch-up chaindata")
	nBlocks := flag.Int("blocks", 100, "number of blocks to bench")
	startBlk := flag.Uint64("start", 0, "start block (0 = firstChangeset+1000)")
	blockRefKB := flag.Float64("block-kb", 130, "block size ref KB when datadir has no block tables")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	// detect first changeset block
	var first uint64
	{
		c, _ := tx.Cursor(tAccCS)
		k, _, _ := c.First()
		if len(k) >= 8 {
			first = binary.BigEndian.Uint64(k[:8])
		}
		c.Close()
	}
	if first == 0 {
		fmt.Println("no AccountChangeSet data in this datadir")
		return
	}
	start := *startBlk
	if start == 0 {
		start = first + 1000
	}
	fmt.Printf("changeset firstBlock=%d, benching [%d, %d)\n\n", first, start, start+uint64(*nBlocks))

	var wArr, mptArr, blkArr, ratioArr []float64
	var sumW, sumMpt, sumBlk float64
	realBlk := 0
	for i := 0; i < *nBlocks; i++ {
		blk := start + uint64(i)
		accts, accBytes := changedAccounts(tx, blk)
		stor, storBytes := changedStorage(tx, blk)

		witnessKB := float64(accBytes+storBytes) / 1024

		ap := newProof()
		for _, ha := range accts {
			ap.collect(tx, tTrieAcc, nil, toNibbles(ha))
		}
		sp := newProof()
		for _, sk := range stor {
			if len(sk) >= 64 {
				sp.collect(tx, tTrieSto, sk[:32], toNibbles(sk[32:64]))
			}
		}
		dedup := ap.dedup() + sp.dedup()
		var raw []byte
		keys := make([]string, 0, len(ap.nodes)+len(sp.nodes))
		tmp := map[string][]byte{}
		for k, v := range ap.nodes {
			tmp["a"+k] = v
			keys = append(keys, "a"+k)
		}
		for k, v := range sp.nodes {
			tmp["s"+k] = v
			keys = append(keys, "s"+k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			raw = append(raw, tmp[k]...)
		}
		zb := zstdLen(raw)
		mptKB := float64(dedup) / 1024

		blkBytes := readBlockSize(tx, blk)
		var blockKB float64
		if blkBytes > 0 {
			blockKB = float64(blkBytes) / 1024
			realBlk++
		} else {
			blockKB = *blockRefKB
		}

		if i%10 == 0 || mptKB > 600 {
			fmt.Printf("blk=%-10d accts=%-4d stor=%-4d | witness=%6.1fKB  MPT=%7.1fKB (zstd %6.1fKB)  block=%6.1fKB  %.1fx\n",
				blk, len(accts), len(stor), witnessKB, mptKB, float64(zb)/1024, blockKB, mptKB/blockKB)
		}
		wArr = append(wArr, witnessKB)
		mptArr = append(mptArr, mptKB)
		blkArr = append(blkArr, blockKB)
		ratioArr = append(ratioArr, mptKB/blockKB)
		sumW += witnessKB
		sumMpt += mptKB
		sumBlk += blockKB
	}
	sort.Float64s(wArr)
	sort.Float64s(mptArr)
	sort.Float64s(blkArr)
	sort.Float64s(ratioArr)
	n := float64(*nBlocks)
	src := "real header+body"
	if realBlk == 0 {
		src = fmt.Sprintf("REFERENCE %.0fKB", *blockRefKB)
	} else if realBlk < *nBlocks {
		src = fmt.Sprintf("%d real / %d ref", realBlk, *nBlocks-realBlk)
	}
	fmt.Printf("\n=== SUMMARY %d blocks [%d,%d) ===\n", *nBlocks, start, start+uint64(*nBlocks))
	fmt.Printf("block size source: %s\n", src)
	fmt.Printf("%-18s %9s %9s %9s %9s\n", "metric", "avg", "p50", "p90", "p99/max")
	fmt.Printf("%-18s %9.1f %9.1f %9.1f %9.1f\n", "witness KB", sumW/n, pct(wArr, .5), pct(wArr, .9), pct(wArr, .99))
	fmt.Printf("%-18s %9.1f %9.1f %9.1f %9.1f\n", "MPT stateless KB", sumMpt/n, pct(mptArr, .5), pct(mptArr, .9), pct(mptArr, .99))
	fmt.Printf("%-18s %9.1f %9.1f %9.1f %9.1f\n", "block KB", sumBlk/n, pct(blkArr, .5), pct(blkArr, .9), pct(blkArr, .99))
	fmt.Printf("%-18s %8.1fx %8.1fx %8.1fx %8.1fx\n", "MPT/block", sumMpt/sumBlk, pct(ratioArr, .5), pct(ratioArr, .9), pct(ratioArr, .99))
	fmt.Printf("MPT/witness avg = %.1fx\n\n", (sumMpt/n)/(sumW/n+1e-9))
	fmt.Printf("per-block bandwidth if adopted:\n")
	fmt.Printf("  every block MPT : witness %.1f + MPT %.1f = %.1f KB/block\n", sumW/n, sumMpt/n, sumW/n+sumMpt/n)
	fmt.Printf("  every-100 anchor: witness %.1f + MPT/100 %.2f = %.1f KB/block (amortized)\n", sumW/n, sumMpt/n/100, sumW/n+sumMpt/n/100)
}
