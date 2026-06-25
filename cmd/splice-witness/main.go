// splice-witness — fill the witness freezer's resume-gap [25101824,25101865]
// (42 empty items) from a clean source freezer (F:/ethdata, items 25101867,
// pre-gap) using the same directory-based, batch-aligned splice as splice-cs.
//
// The gap is inside one batch (392216). The splice dir holds only segments
// NN..last + a full cidx — a drop-in replacement for the primary's NN..last.
// Read-only on both sources.
//
// Usage:
//
//	splice-witness --primary D:/N42-eth1177/chain/freezer \
//	               --source  F:/ethdata \
//	               --out     D:/N42-eth1177-witness-spliced
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	gapLo          = 25101824 // batch-aligned (392216*64)
	batchSize      = 64
	cidxHeaderSize = 16
	indexEntrySize = 6
	table          = "witness"
)

var cidxMagic = [4]byte{'N', 'C', 'I', 'X'}

// rawSource — raw cidx/cdat access (from splice-cs / splice-leaves).
type rawSource struct {
	dir   string
	cidx  []byte
	files map[uint16]*os.File
	sizes map[uint16]int64
	items uint64
}

func openRawSource(dir string) (*rawSource, error) {
	cidx, err := os.ReadFile(filepath.Join(dir, table+".cidx"))
	if err != nil {
		return nil, err
	}
	if len(cidx) >= cidxHeaderSize && cidx[0] == cidxMagic[0] && cidx[1] == cidxMagic[1] &&
		cidx[2] == cidxMagic[2] && cidx[3] == cidxMagic[3] {
		cidx = cidx[cidxHeaderSize:]
	}
	usable := (len(cidx) / indexEntrySize) * indexEntrySize
	return &rawSource{dir: dir, cidx: cidx[:usable],
		files: map[uint16]*os.File{}, sizes: map[uint16]int64{}, items: uint64(usable) / indexEntrySize}, nil
}

func (s *rawSource) close() {
	for _, f := range s.files {
		f.Close()
	}
}

func (s *rawSource) indexEntry(item uint64) (uint16, uint32) {
	off := item * indexEntrySize
	return binary.BigEndian.Uint16(s.cidx[off : off+2]), binary.BigEndian.Uint32(s.cidx[off+2 : off+6])
}

func (s *rawSource) openFile(fn uint16) (*os.File, int64, error) {
	if f, ok := s.files[fn]; ok {
		return f, s.sizes[fn], nil
	}
	f, err := os.Open(filepath.Join(s.dir, fmt.Sprintf("%s.%04d.cdat", table, fn)))
	if err != nil {
		return nil, 0, err
	}
	fi, _ := f.Stat()
	s.files[fn] = f
	s.sizes[fn] = fi.Size()
	return f, fi.Size(), nil
}

func (s *rawSource) readBatchBlob(b uint64) ([]byte, error) {
	startItem := b * batchSize
	fn, off := s.indexEntry(startItem)
	f, fsize, err := s.openFile(fn)
	if err != nil {
		return nil, err
	}
	var end uint32
	if next := startItem + batchSize; next < s.items {
		nfn, noff := s.indexEntry(next)
		if nfn == fn {
			end = noff
		} else {
			end = uint32(fsize)
		}
	} else {
		end = uint32(fsize)
	}
	blob := make([]byte, end-off)
	if _, err := f.ReadAt(blob, int64(off)); err != nil {
		return nil, err
	}
	return blob, nil
}

func copyByteRange(srcPath, dstPath string, n int64) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.CopyN(out, in, n); err != nil {
		return err
	}
	return out.Sync()
}

func main() {
	primaryDir := flag.String("primary", "D:/N42-eth1177/chain/freezer", "primary witness freezer (with the gap)")
	sourceDir := flag.String("source", "F:/ethdata", "clean source witness freezer (covers the gap)")
	outDir := flag.String("out", "D:/N42-eth1177-witness-spliced", "splice output dir")
	flag.Parse()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	if gapLo%batchSize != 0 {
		fmt.Fprintln(os.Stderr, "gapLo not batch-aligned")
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if fi, err := os.Stat(filepath.Join(*outDir, table+".cidx")); err == nil && fi.Size() > 0 {
		fmt.Fprintln(os.Stderr, "out already has witness.cidx; remove first")
		os.Exit(1)
	}

	prim, err := freezer.NewFreezerTableCompressedReadOnly(*primaryDir, table, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open primary:", err)
		os.Exit(1)
	}
	defer prim.Close()
	src, err := freezer.NewFreezerTableCompressedReadOnly(*sourceDir, table, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open source:", err)
		os.Exit(1)
	}
	defer src.Close()
	raw, err := openRawSource(*primaryDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "raw:", err)
		os.Exit(1)
	}
	defer raw.close()

	maxItems := raw.items
	srcItems := src.Items()
	NN, gapBatchOffset := raw.indexEntry(gapLo)
	fmt.Printf("primary items=%d  source items=%d  gap batch=%d  NN=%04d\n", maxItems, srcItems, gapLo/batchSize, NN)

	// Seed: cidx prefix + segment NN prefix.
	if err := copyByteRange(filepath.Join(*primaryDir, table+".cidx"),
		filepath.Join(*outDir, table+".cidx"), cidxHeaderSize+int64(gapLo)*indexEntrySize); err != nil {
		fmt.Fprintln(os.Stderr, "seed cidx:", err)
		os.Exit(1)
	}
	if err := copyByteRange(filepath.Join(*primaryDir, fmt.Sprintf("%s.%04d.cdat", table, NN)),
		filepath.Join(*outDir, fmt.Sprintf("%s.%04d.cdat", table, NN)), int64(gapBatchOffset)); err != nil {
		fmt.Fprintln(os.Stderr, "seed cdat:", err)
		os.Exit(1)
	}

	dst, err := freezer.NewFreezerTableCompressed(*outDir, table, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst:", err)
		os.Exit(1)
	}
	defer dst.Close()
	if dst.Items() != gapLo {
		fmt.Fprintf(os.Stderr, "seeded dst items=%d want %d\n", dst.Items(), gapLo)
		os.Exit(1)
	}

	// Re-encode the gap batch: empty-in-primary items pulled from the clean source.
	filled, stillEmpty := 0, 0
	var filledItems []uint64
	var payload []byte
	for i := uint64(gapLo); i < uint64(gapLo)+batchSize; i++ {
		item, _ := prim.Retrieve(i)
		// Within the clean source's coverage, prefer the source whenever the
		// primary DIFFERS from it (empty OR a backfill-corrupted boundary value,
		// e.g. block 25101866 whose changeset was also gapped). Where primary
		// already equals the source it is a no-op. Beyond the source's coverage,
		// keep the primary unchanged.
		if i < srcItems {
			if fi, _ := src.Retrieve(i); len(fi) > 0 && !bytes.Equal(item, fi) {
				item = fi
				filled++
				filledItems = append(filledItems, i)
			} else if len(item) == 0 {
				stillEmpty++
			}
		} else if len(item) == 0 {
			stillEmpty++
		}
		var lp [4]byte
		binary.LittleEndian.PutUint32(lp[:], uint32(len(item)))
		payload = append(payload, lp[:]...)
		payload = append(payload, item...)
	}
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	if err := dst.AppendBatchBlob(gapLo, batchSize, enc.EncodeAll(payload, nil)); err != nil {
		fmt.Fprintln(os.Stderr, "append gap batch:", err)
		os.Exit(1)
	}
	fmt.Printf("[gap batch %d] filled %d items from source, %d still empty (uncovered)\n",
		gapLo/batchSize, filled, stillEmpty)

	// Raw-copy every later batch verbatim.
	gapBatch := uint64(gapLo) / batchSize
	lastFull := maxItems / batchSize
	t0 := time.Now()
	for b := gapBatch + 1; b < lastFull; b++ {
		blob, err := raw.readBatchBlob(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read batch %d: %v\n", b, err)
			os.Exit(1)
		}
		if err := dst.AppendBatchBlob(b*batchSize, batchSize, blob); err != nil {
			fmt.Fprintf(os.Stderr, "append batch %d: %v\n", b, err)
			os.Exit(1)
		}
	}
	if rem := maxItems - lastFull*batchSize; rem > 0 {
		blob, _ := raw.readBatchBlob(lastFull)
		if err := dst.AppendBatchBlob(lastFull*batchSize, int(rem), blob); err != nil {
			fmt.Fprintln(os.Stderr, "append final batch:", err)
			os.Exit(1)
		}
	}
	if err := dst.Sync(); err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		os.Exit(1)
	}
	if dst.Items() != maxItems {
		fmt.Fprintf(os.Stderr, "dst items=%d want %d\n", dst.Items(), maxItems)
		os.Exit(1)
	}
	fmt.Printf("[splice] copied %d batches in %s; dir holds witness.%04d..last\n",
		lastFull-gapBatch, time.Since(t0).Round(time.Millisecond), NN)

	// Verify: reopen, gap items non-empty + byte-match source, non-gap byte-match primary.
	v, err := freezer.NewFreezerTableCompressedReadOnly(*outDir, table, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify open:", err)
		os.Exit(1)
	}
	defer v.Close()
	if v.Items() != maxItems {
		fmt.Fprintf(os.Stderr, "VERIFY items=%d want %d\n", v.Items(), maxItems)
		os.Exit(1)
	}
	bad := 0
	for _, i := range filledItems {
		sb, _ := v.Retrieve(i)
		fb, _ := src.Retrieve(i)
		if len(sb) == 0 || len(sb) != len(fb) {
			bad++
			continue
		}
		for k := range sb {
			if sb[k] != fb[k] {
				bad++
				break
			}
		}
	}
	// Non-gap spot-check (byte-identical to primary).
	checked, mism := 0, 0
	step := (maxItems - (uint64(gapLo) + batchSize)) / 4000
	if step == 0 {
		step = 1
	}
	for i := uint64(gapLo) + batchSize; i < maxItems; i += step {
		a, _ := prim.Retrieve(i)
		b, _ := v.Retrieve(i)
		checked++
		if len(a) != len(b) {
			mism++
			continue
		}
		for k := range a {
			if a[k] != b[k] {
				mism++
				break
			}
		}
	}
	status := "PASS"
	if bad != 0 || mism != 0 {
		status = "FAIL"
	}
	fmt.Printf("[verify] %s  gap-filled=%d match-source-bad=%d  non-gap checked=%d mismatch=%d\n",
		status, len(filledItems), bad, checked, mism)
	if status == "FAIL" {
		os.Exit(1)
	}
	fmt.Println("SPLICE-WITNESS DONE")
}
