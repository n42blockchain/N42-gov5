// n42-history-expiry: EIP-4444-style recent-window retention for the eth-el
// full node. Computes the cold/hot segment boundary for a tip + window, exports
// cold body segments to EraE archive files (era-FROM-TO.era, zstd, random
// access), and publishes a torrentsync manifest (per-file SHA256 + block range)
// for 1-of-N availability via archive/seeder nodes.
//
// The hot bodyc store keeps only recent segments + the full bodyc.cidx
// (trimmed-store; cold reads return ethel.ErrBodyTrimmed and are resolved
// against the manifest). This tool exports + manifests; it does not delete from
// the hot store (relocation is byte-level, proven by the post-merge extraction).
//
// Body blob codec (per block): varint(nTx) [varint(len) tx.Marshal()]...
// varint(nWithdrawals) [withdrawal]... varint(nUncles) [varint(len) rlp]...
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/historyexpiry"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/modules/rawdb/era"
)

func putUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

func encodeBody(blk *ethel.DecodedBlock) ([]byte, error) {
	var b []byte
	b = putUvarint(b, uint64(len(blk.Txs)))
	for _, tx := range blk.Txs {
		m, err := tx.Marshal()
		if err != nil {
			return nil, err
		}
		b = putUvarint(b, uint64(len(m)))
		b = append(b, m...)
	}
	b = putUvarint(b, uint64(len(blk.Withdrawals)))
	for _, w := range blk.Withdrawals {
		b = putUvarint(b, w.Index)
		b = putUvarint(b, w.Validator)
		b = append(b, w.Address[:]...)
		b = putUvarint(b, w.Amount)
	}
	b = putUvarint(b, uint64(len(blk.UncleRLP)))
	for _, u := range blk.UncleRLP {
		b = putUvarint(b, uint64(len(u)))
		b = append(b, u...)
	}
	return b, nil
}

// decodeBodyTxCount returns (#txs, #withdrawals) by parsing the blob — enough to
// verify the round-trip without rebuilding full Transaction objects.
func decodeBodyCounts(b []byte) (nTx, nWd int, err error) {
	pos := 0
	rdUvarint := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, fmt.Errorf("bad varint at %d", pos)
		}
		pos += n
		return v, nil
	}
	nt, err := rdUvarint()
	if err != nil {
		return 0, 0, err
	}
	for i := uint64(0); i < nt; i++ {
		l, err := rdUvarint()
		if err != nil {
			return 0, 0, err
		}
		pos += int(l)
		if pos > len(b) {
			return 0, 0, fmt.Errorf("tx %d overruns blob", i)
		}
	}
	nw, err := rdUvarint()
	if err != nil {
		return 0, 0, err
	}
	return int(nt), int(nw), nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func main() {
	dir := flag.String("dir", "", "hot bodyc freezer dir (source)")
	out := flag.String("out", "D:/n42-cold-era", "cold era archive output dir")
	window := flag.Uint64("window", historyexpiry.DefaultWindowBlocks, "hot retention window in blocks (~1yr default)")
	chainID := flag.Uint64("chainid", 1, "chain id for the manifest")
	limit := flag.Int("limit", 2, "max cold units to process (prototype; 0=all)")
	mode := flag.String("mode", "cdat", "cold unit: 'cdat' (relocate columnar segments, ~2.5x tighter, recommended) | 'era' (re-encode to EraE, interop/receipts)")
	dryrun := flag.Bool("dryrun", false, "only print the plan + sizes, do not export")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-history-expiry --dir <bodyc> [--out D] [--window N] [--limit K]")
		os.Exit(1)
	}

	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open bodyc:", err)
		os.Exit(1)
	}
	defer br.Close()

	tip := br.MaxBlock()
	segs := uint64(br.Segments())
	plan := historyexpiry.Compute(tip, *window, historyexpiry.SegSize)

	// Find first available segment (post-merge store does not start at 0).
	firstSeg := uint64(0)
	for s := uint64(0); s < segs; s++ {
		if _, err := br.ReadBody(s * historyexpiry.SegSize); err == nil {
			firstSeg = s
			break
		}
	}
	cold := plan.ColdSegs(firstSeg, segs-1)

	fmt.Printf("tip≈%d  segments=%d  firstAvailSeg=%d  window=%d\n", tip, segs, firstSeg, *window)
	fmt.Printf("plan: HotFromBlock=%d HotFromSeg=%d ColdUntilSeg=%d  → %d cold segments available\n",
		plan.HotFromBlock, plan.HotFromSeg, plan.ColdUntilSeg, len(cold))
	if len(cold) == 0 {
		fmt.Println("nothing to offload (chain within window or store starts above boundary)")
		return
	}
	fmt.Printf("cold range: seg %d (block %d) .. seg %d (block %d)\n",
		cold[0].Seg, cold[0].FirstBlock, cold[len(cold)-1].Seg, cold[len(cold)-1].LastBlock)

	if *mode == "cdat" {
		runCdat(*dir, *out, *chainID, plan, segs, *limit, *dryrun)
		return
	}
	if *dryrun {
		return
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir out:", err)
		os.Exit(1)
	}

	toExport := cold
	if *limit > 0 && len(toExport) > *limit {
		toExport = toExport[:*limit]
		fmt.Printf("prototype: exporting first %d of %d cold segments\n", *limit, len(cold))
	}

	var segInfos []torrentsync.SegmentInfo
	var rawBodies, eraBytes int64
	t0 := time.Now()
	for _, sr := range toExport {
		name := torrentsync.EraFileName(sr.FirstBlock, sr.LastBlock)
		path := filepath.Join(*out, name)
		w, err := era.NewWriter(path, *chainID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "era new:", err)
			os.Exit(1)
		}
		var srcBytes int64
		nBlocks := uint64(0)
		for n := sr.FirstBlock; n <= sr.LastBlock; n++ {
			blk, err := br.ReadBody(n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read %d: %v\n", n, err)
				os.Exit(1)
			}
			blob, err := encodeBody(blk)
			if err != nil {
				fmt.Fprintf(os.Stderr, "encode %d: %v\n", n, err)
				os.Exit(1)
			}
			srcBytes += int64(len(blob))
			if err := w.Append(n, blob, nil); err != nil { // receipts deferred (bodies-only store)
				fmt.Fprintln(os.Stderr, "append:", err)
				os.Exit(1)
			}
			nBlocks++
		}
		if err := w.Finalize(); err != nil {
			fmt.Fprintln(os.Stderr, "finalize:", err)
			os.Exit(1)
		}
		sum, size, err := sha256File(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sha256:", err)
			os.Exit(1)
		}
		rawBodies += srcBytes
		eraBytes += size
		segInfos = append(segInfos, torrentsync.SegmentInfo{
			FromBlock:  sr.FirstBlock,
			ToBlock:    sr.LastBlock,
			FileName:   name,
			Size:       size,
			BlockCount: nBlocks,
			SHA256:     sum,
		})
		fmt.Printf("  exported %s: %d blocks, body-blob %d B → era %d B (%.2f×), sha256 %s…\n",
			name, nBlocks, srcBytes, size, float64(srcBytes)/float64(size), sum[:12])
	}

	manifest := &torrentsync.Manifest{ChainID: *chainID, Segments: segInfos, UpdatedAt: time.Unix(0, 0)}
	manifestPath := filepath.Join(*out, "manifest.json")
	if err := torrentsync.SaveManifest(manifest, manifestPath); err != nil {
		fmt.Fprintln(os.Stderr, "save manifest:", err)
		os.Exit(1)
	}
	fmt.Printf("manifest: %s (%d segments)  build %s\n", manifestPath, len(segInfos), time.Since(t0).Truncate(time.Millisecond))

	// === Round-trip verify: manifest.FindSegment → era.ReadRecord → counts match source ===
	loaded, err := torrentsync.LoadManifest(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load manifest:", err)
		os.Exit(1)
	}
	checked, mismatch := 0, 0
	for _, sr := range toExport {
		// Sample a few blocks per segment.
		for _, n := range []uint64{sr.FirstBlock, (sr.FirstBlock + sr.LastBlock) / 2, sr.LastBlock} {
			si := loaded.FindSegment(n)
			if si == nil {
				mismatch++
				continue
			}
			rd, err := era.OpenReader(filepath.Join(*out, si.FileName))
			if err != nil {
				mismatch++
				continue
			}
			blob, _, err := rd.ReadRecord(n)
			rd.Close()
			if err != nil {
				mismatch++
				continue
			}
			gotTx, gotWd, err := decodeBodyCounts(blob)
			if err != nil {
				mismatch++
				continue
			}
			src, err := br.ReadBody(n)
			if err != nil {
				mismatch++
				continue
			}
			checked++
			if gotTx != len(src.Txs) || gotWd != len(src.Withdrawals) {
				mismatch++
				fmt.Printf("  MISMATCH block %d: era(tx=%d,wd=%d) vs bodyc(tx=%d,wd=%d)\n",
					n, gotTx, gotWd, len(src.Txs), len(src.Withdrawals))
			}
		}
	}
	fmt.Println("=== round-trip verify (manifest resolve → era read → counts vs bodyc) ===")
	fmt.Printf("  checked=%d mismatch=%d → %s\n", checked, mismatch, passStr(mismatch == 0))
	fmt.Printf("=== sizes ===\n  cold body-blob raw %d B → era %d B (%.2f× zstd)\n",
		rawBodies, eraBytes, float64(rawBodies)/float64(max1(eraBytes)))
}

// cdatInfo groups the segments packed into one bodyc.NNNN.cdat file.
type cdatInfo struct {
	fileNum        uint16
	minSeg, maxSeg uint64
	sizeBytes      int64
}

// runCdat is the RECOMMENDED cold path: cold unit = the bodyc.NNNN.cdat columnar
// segment file itself (relocated byte-level, ~2.5x tighter than EraE re-encode).
// It reads bodyc.cidx to map segments→cdat, classifies each cdat cold/boundary/
// hot vs the retention plan, and reports the boundary fileNum + per-class sizes.
// For --limit cold cdat it computes SHA256 and writes a torrentsync manifest.
func runCdat(dir, outDir string, chainID uint64, plan historyexpiry.Plan, segs uint64, limit int, dryrun bool) {
	cidxPath := filepath.Join(dir, "bodyc.cidx")
	cidx, err := os.ReadFile(cidxPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read cidx:", err)
		os.Exit(1)
	}
	// Group segments by cdat fileNum (cidx entry = 8B: fileNum LE u16[0:2]).
	byFile := map[uint16]*cdatInfo{}
	var order []uint16
	for s := uint64(0); s < segs; s++ {
		off := s * 8
		if int(off)+8 > len(cidx) {
			break
		}
		fn := binary.LittleEndian.Uint16(cidx[off : off+2])
		ci, ok := byFile[fn]
		if !ok {
			ci = &cdatInfo{fileNum: fn, minSeg: s, maxSeg: s}
			byFile[fn] = ci
			order = append(order, fn)
		}
		if s < ci.minSeg {
			ci.minSeg = s
		}
		if s > ci.maxSeg {
			ci.maxSeg = s
		}
	}
	// Stat each cdat file.
	for fn, ci := range byFile {
		if fi, err := os.Stat(filepath.Join(dir, fmt.Sprintf("bodyc.%04d.cdat", fn))); err == nil {
			ci.sizeBytes = fi.Size()
		}
	}

	// Classify: cold = entirely below ColdUntilSeg; boundary = straddles; hot = at/above.
	var coldFiles, boundaryFiles, hotFiles []*cdatInfo
	var coldGB, boundaryGB, hotGB float64
	const giB = 1 << 30
	for _, fn := range order {
		ci := byFile[fn]
		if ci.sizeBytes == 0 {
			continue // absent in this (trimmed) store — e.g. pre-merge cdat in a post-merge-only dir
		}
		switch {
		case ci.maxSeg < plan.ColdUntilSeg:
			coldFiles = append(coldFiles, ci)
			coldGB += float64(ci.sizeBytes) / giB
		case ci.minSeg >= plan.ColdUntilSeg:
			hotFiles = append(hotFiles, ci)
			hotGB += float64(ci.sizeBytes) / giB
		default:
			boundaryFiles = append(boundaryFiles, ci)
			boundaryGB += float64(ci.sizeBytes) / giB
		}
	}

	fmt.Println("=== mode=cdat: cold/hot split by bodyc.NNNN.cdat (recommended; ~2.5× tighter than era) ===")
	if len(boundaryFiles) > 0 {
		b := boundaryFiles[0]
		fmt.Printf("  HOT BOUNDARY: cdat %04d straddles hot-from-seg %d (covers seg %d..%d, block %d..%d) → kept HOT\n",
			b.fileNum, plan.ColdUntilSeg, b.minSeg, b.maxSeg, b.minSeg*historyexpiry.SegSize, b.maxSeg*historyexpiry.SegSize+historyexpiry.SegSize-1)
	}
	fmt.Printf("  COLD cdat: %d files, %.1f GB  (relocate + manifest + seed)\n", len(coldFiles), coldGB)
	fmt.Printf("  HOT  cdat: %d files, %.1f GB  (the ~1yr window — what a Full node keeps)\n", len(hotFiles)+len(boundaryFiles), hotGB+boundaryGB)
	if len(hotFiles) > 0 || len(boundaryFiles) > 0 {
		// First/last hot fileNum.
		firstHot := byFile[order[0]]
		for _, fn := range order {
			ci := byFile[fn]
			if ci.minSeg >= plan.ColdUntilSeg || (ci.minSeg < plan.ColdUntilSeg && ci.maxSeg >= plan.ColdUntilSeg) {
				firstHot = ci
				break
			}
		}
		last := byFile[order[len(order)-1]]
		fmt.Printf("  HOT range: cdat %04d .. %04d (block ~%d .. %d)\n",
			firstHot.fileNum, last.fileNum, firstHot.minSeg*historyexpiry.SegSize, last.maxSeg*historyexpiry.SegSize+historyexpiry.SegSize-1)
	}

	if dryrun {
		return
	}
	if len(coldFiles) == 0 {
		return
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir out:", err)
		os.Exit(1)
	}
	toHash := coldFiles
	if limit > 0 && len(toHash) > limit {
		toHash = toHash[:limit]
		fmt.Printf("prototype: hashing+manifesting first %d of %d cold cdat\n", limit, len(coldFiles))
	}
	var segInfos []torrentsync.SegmentInfo
	for _, ci := range toHash {
		name := fmt.Sprintf("bodyc.%04d.cdat", ci.fileNum)
		sum, size, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			fmt.Fprintln(os.Stderr, "sha256:", err)
			os.Exit(1)
		}
		segInfos = append(segInfos, torrentsync.SegmentInfo{
			FromBlock:  ci.minSeg * historyexpiry.SegSize,
			ToBlock:    ci.maxSeg*historyexpiry.SegSize + historyexpiry.SegSize - 1,
			FileName:   name,
			Size:       size,
			BlockCount: (ci.maxSeg - ci.minSeg + 1) * historyexpiry.SegSize,
			SHA256:     sum,
		})
		fmt.Printf("  manifested %s: seg %d..%d, %.2f GB, sha256 %s…\n",
			name, ci.minSeg, ci.maxSeg, float64(size)/giB, sum[:12])
	}
	m := &torrentsync.Manifest{ChainID: chainID, Segments: segInfos, UpdatedAt: time.Unix(0, 0)}
	mp := filepath.Join(outDir, "manifest-cdat.json")
	if err := torrentsync.SaveManifest(m, mp); err != nil {
		fmt.Fprintln(os.Stderr, "save manifest:", err)
		os.Exit(1)
	}
	// Verify FindSegment resolves a cold block to the right cdat.
	loaded, _ := torrentsync.LoadManifest(mp)
	ok := 0
	for _, ci := range toHash {
		probe := ci.minSeg*historyexpiry.SegSize + 1
		if si := loaded.FindSegment(probe); si != nil && si.FileName == fmt.Sprintf("bodyc.%04d.cdat", ci.fileNum) {
			ok++
		}
	}
	fmt.Printf("manifest: %s (%d cold cdat); FindSegment resolve %d/%d → %s\n",
		mp, len(segInfos), ok, len(toHash), passStr(ok == len(toHash)))
}

func passStr(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}
func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
