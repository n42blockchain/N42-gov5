// cidx-inspect — STRICTLY READ-ONLY inspection of a single cidx + its
// associated cdat segments.
//
// Does NOT use any freezer.go / table.go API. Opens files only with
// os.O_RDONLY. Never writes. Never truncates. Never modifies metadata.
// Designed for forensic recovery: tells you exactly what's on disk and
// nothing else.
//
// Output:
//   - cidx file size + implied entry count under each plausible layout
//     (legacy 6B-per-entry vs N42 16B-header + 6B-per-entry)
//   - first / mid / last cidx entries decoded as (fileNum, offset)
//   - unique (fileNum, offset) pairs (= compressed batch count)
//   - per-cdat segment file sizes
//   - first 32 bytes of each cdat segment (zstd magic 28 B5 2F FD = batch start)
//   - rough batch-size heuristic from offset gaps
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	dir := flag.String("dir", "", "freezer directory containing the cidx file")
	name := flag.String("name", "", "table name (e.g. senders, bodies)")
	ext := flag.String("ext", "c", "cidx extension letter (default 'c')")
	flag.Parse()

	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: cidx-inspect --dir <freezer> --name <table>")
		os.Exit(2)
	}

	cidxPath := filepath.Join(*dir, fmt.Sprintf("%s.%sidx", *name, *ext))
	if err := inspectCidx(cidxPath); err != nil {
		fmt.Fprintln(os.Stderr, "cidx:", err)
		os.Exit(1)
	}
	if err := inspectCdats(*dir, *name, *ext); err != nil {
		fmt.Fprintln(os.Stderr, "cdat:", err)
		os.Exit(1)
	}
}

func inspectCidx(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	fmt.Printf("=== cidx ===\npath: %s\nsize: %d bytes\n", path, size)

	// Probe first 16 bytes for an N42 header (magic 'CIDX').
	var hdr [16]byte
	n, _ := f.ReadAt(hdr[:], 0)
	hasHeader := n >= 4 && string(hdr[0:4]) == "CIDX"
	fmt.Printf("first 16B: %x\n", hdr[:n])
	if hasHeader {
		fmt.Printf("layout:    N42 header present  (version=%d flags=0x%02x batchSize=%d entrySize=%d)\n",
			hdr[4], hdr[5], hdr[6], hdr[7])
	} else {
		fmt.Printf("layout:    legacy / no header  (assumes 6B entries from byte 0)\n")
	}

	// Compute entry count under both interpretations.
	const entrySize = 6
	if hasHeader {
		dataBytes := size - 16
		fmt.Printf("entries (post-header @ 6B each): %d\n", dataBytes/entrySize)
		if rem := dataBytes % entrySize; rem != 0 {
			fmt.Printf("  WARNING: %d trailing bytes don't fit a full 6B entry\n", rem)
		}
	}
	fmt.Printf("entries (legacy @ 6B each from byte 0): %d\n", size/entrySize)
	if rem := size % entrySize; rem != 0 {
		fmt.Printf("  (legacy interp: %d trailing bytes don't fit)\n", rem)
	}

	// Read entries (use legacy interpretation: just byte 0..size, 6B/entry).
	entries := readAll6BEntries(f, 0, size)
	fmt.Printf("\n=== sample entries (legacy 6B/entry interpretation) ===\n")
	dumpEntry(entries, 0, "first")
	if len(entries) > 1 {
		dumpEntry(entries, 1, "second")
	}
	if len(entries) > 64 {
		dumpEntry(entries, 63, "63 (= last of 1st batch if BatchSize=64)")
		dumpEntry(entries, 64, "64 (= first of 2nd batch if BatchSize=64)")
	}
	if mid := len(entries) / 2; mid > 0 && mid != 1 && mid != 64 {
		dumpEntry(entries, mid, "mid")
	}
	if len(entries) > 0 {
		dumpEntry(entries, len(entries)-1, "last")
	}

	// Count unique (fileNum, offset) pairs == distinct compressed batches.
	uniq := make(map[[6]byte]uint64)
	for _, e := range entries {
		var k [6]byte
		copy(k[:], e[:])
		uniq[k]++
	}
	fmt.Printf("\nunique (fileNum,offset) pairs (= compressed batch count): %d\n", len(uniq))

	// Implied per-batch size (in cidx entries) = entries / unique.
	if len(uniq) > 0 {
		fmt.Printf("avg entries per batch:                                   %.2f\n",
			float64(len(entries))/float64(len(uniq)))
	}

	// Distribution of multiplicity (= batch sizes seen).
	mult := make(map[uint64]int)
	for _, c := range uniq {
		mult[c]++
	}
	if len(mult) <= 20 {
		fmt.Printf("\nbatch-size distribution (count → how many batches share that count):\n")
		keys := make([]uint64, 0, len(mult))
		for k := range mult {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			fmt.Printf("  %d × %d batches\n", k, mult[k])
		}
	}

	// Offset jumps (compressed batch sizes per segment) in first 100 unique batches.
	type entry struct {
		fileNum uint16
		offset  uint32
	}
	var seq []entry
	seen := make(map[[6]byte]bool)
	for _, e := range entries {
		var k [6]byte
		copy(k[:], e[:])
		if seen[k] {
			continue
		}
		seen[k] = true
		seq = append(seq, entry{
			fileNum: binary.BigEndian.Uint16(e[0:2]),
			offset:  binary.BigEndian.Uint32(e[2:6]),
		})
		if len(seq) >= 100 {
			break
		}
	}
	if len(seq) >= 2 {
		fmt.Printf("\nfirst %d unique-batch offset progression:\n", len(seq))
		for i := 1; i < len(seq) && i < 16; i++ {
			gap := int64(seq[i].offset) - int64(seq[i-1].offset)
			file := ""
			if seq[i].fileNum != seq[i-1].fileNum {
				file = fmt.Sprintf("  (rotated to file %d)", seq[i].fileNum)
				gap = int64(seq[i].offset) // first batch in new file
			}
			fmt.Printf("  batch %d→%d: file=%d offset=%d  Δ=%d B%s\n",
				i-1, i, seq[i].fileNum, seq[i].offset, gap, file)
		}
	}
	return nil
}

func readAll6BEntries(f *os.File, start int64, size int64) [][6]byte {
	const entrySize = 6
	count := (size - start) / entrySize
	if count <= 0 {
		return nil
	}
	buf := make([]byte, count*entrySize)
	if _, err := f.ReadAt(buf, start); err != nil {
		return nil
	}
	out := make([][6]byte, count)
	for i := int64(0); i < count; i++ {
		copy(out[i][:], buf[i*entrySize:(i+1)*entrySize])
	}
	return out
}

func dumpEntry(entries [][6]byte, idx int, label string) {
	if idx < 0 || idx >= len(entries) {
		return
	}
	e := entries[idx]
	fileNum := binary.BigEndian.Uint16(e[0:2])
	offset := binary.BigEndian.Uint32(e[2:6])
	fmt.Printf("  entry[%-9d] = %s  → fileNum=%d offset=%d  (%s)\n",
		idx, hex.EncodeToString(e[:]), fileNum, offset, label)
}

func inspectCdats(dir, name, ext string) error {
	pattern := name + "."
	suffix := "." + ext + "dat"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type seg struct {
		num  int
		path string
		size int64
		head [32]byte
	}
	var segs []seg
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		n := de.Name()
		if !strings.HasPrefix(n, pattern) || !strings.HasSuffix(n, suffix) {
			continue
		}
		core := strings.TrimSuffix(strings.TrimPrefix(n, pattern), suffix)
		var num int
		if _, err := fmt.Sscanf(core, "%d", &num); err != nil {
			continue
		}
		p := filepath.Join(dir, n)
		fi, err := de.Info()
		if err != nil {
			continue
		}
		s := seg{num: num, path: p, size: fi.Size()}
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err == nil {
			f.ReadAt(s.head[:], 0)
			f.Close()
		}
		segs = append(segs, s)
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].num < segs[j].num })

	fmt.Printf("\n=== cdat segments ===\n")
	if len(segs) == 0 {
		fmt.Println("  (none)")
		return nil
	}
	var total int64
	for _, s := range segs {
		zstdMagic := s.head[0] == 0x28 && s.head[1] == 0xB5 && s.head[2] == 0x2F && s.head[3] == 0xFD
		marker := ""
		if zstdMagic {
			marker = "  (zstd)"
		}
		fmt.Printf("  %04d  size=%-12d head=%x%s\n", s.num, s.size, s.head[:8], marker)
		total += s.size
	}
	fmt.Printf("  ----\n  total: %d bytes (%.2f GiB across %d segments)\n",
		total, float64(total)/(1024*1024*1024), len(segs))
	return nil
}
