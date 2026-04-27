// segidx-rebuild — rebuild a missing/truncated 8-byte segmented cidx
// from intact cdat segments. Used for bodies and headers tables produced
// by internal/ethel/{body,header}_compact.go.
//
// Format (matches encodeBodyIdx / encodeHeaderIdx):
//   - cidx entry: [fileNum:2 LE][reserved:2][offset:4 LE]   (8 bytes)
//   - cdat frame: [size:4 LE][bytes...]                     (1 frame == 1 segment)
//
// Each segment covers HeaderSegmentSize=8192 blocks. We do NOT decode
// the inner zstd; the size prefix alone delimits frames.
//
// SAFETY: cdat opened O_RDONLY. Output written to a NEW path (default
// {name}.cidx.recovered) — operator manually swaps after verifying.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", "", "freezer directory containing cdat segments")
	name := flag.String("name", "", "table name (e.g. bodies, headers)")
	out := flag.String("out", "", "output cidx path (default: <dir>/<name>.cidx.recovered)")
	dryRun := flag.Bool("dry-run", false, "scan only; don't write the output cidx")
	flag.Parse()

	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: segidx-rebuild --dir <freezer> --name <table>")
		os.Exit(2)
	}
	if *out == "" {
		*out = filepath.Join(*dir, fmt.Sprintf("%s.cidx.recovered", *name))
	}

	segs, err := discoverSegments(*dir, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(segs) == 0 {
		fmt.Fprintln(os.Stderr, "no cdat segments found")
		os.Exit(1)
	}

	fmt.Printf("table:    %s\n", *name)
	fmt.Printf("segments: %d\n", len(segs))
	fmt.Printf("output:   %s%s\n", *out, dryRunSuffix(*dryRun))

	type entry struct {
		fileNum uint16
		offset  uint32
	}
	var entries []entry
	t0 := time.Now()
	for _, s := range segs {
		offsets, err := scanFrameOffsets(s.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan %s: %v\n", s.path, err)
			os.Exit(1)
		}
		for _, off := range offsets {
			entries = append(entries, entry{fileNum: uint16(s.num), offset: uint32(off)})
		}
	}
	fmt.Printf("\nscanned %d cidx entries (1/segment) in %v\n", len(entries), time.Since(t0))
	fmt.Printf("rebuilt cidx size: %d bytes (%.1f KB)\n",
		len(entries)*8, float64(len(entries)*8)/1024)
	fmt.Printf("approx block coverage: %d blocks (entries × 8192)\n", len(entries)*8192)

	if *dryRun {
		fmt.Println("\n--dry-run: NOT writing output. Re-run without --dry-run to materialize.")
		return
	}

	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	defer f.Close()
	buf := make([]byte, 0, 8*1024)
	for _, e := range entries {
		var b [8]byte
		binary.LittleEndian.PutUint16(b[0:2], e.fileNum)
		// b[2:4] reserved zero
		binary.LittleEndian.PutUint32(b[4:8], e.offset)
		buf = append(buf, b[:]...)
		if len(buf)+8 > cap(buf) {
			if _, err := f.Write(buf); err != nil {
				fmt.Fprintln(os.Stderr, "write:", err)
				os.Exit(1)
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		if _, err := f.Write(buf); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
	if err := f.Sync(); err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		os.Exit(1)
	}
	fmt.Printf("\nwrote %s\n", *out)
	fmt.Printf("\nNEXT STEPS (manual; do NOT let any tool open the freezer RW):\n")
	fmt.Printf("  1. Verify size: %d bytes\n", len(entries)*8)
	fmt.Printf("  2. Back up the damaged file:\n     mv %s %s.damaged.bak\n",
		filepath.Join(*dir, fmt.Sprintf("%s.cidx", *name)),
		filepath.Join(*dir, fmt.Sprintf("%s.cidx", *name)))
	fmt.Printf("  3. Promote the recovered file:\n     mv %s %s\n",
		*out, filepath.Join(*dir, fmt.Sprintf("%s.cidx", *name)))
}

func dryRunSuffix(dry bool) string {
	if dry {
		return "  (--dry-run: no file will be written)"
	}
	return ""
}

type seg struct {
	num  int
	path string
	size int64
}

func discoverSegments(dir, name string) ([]seg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	pattern := name + "."
	suffix := ".cdat"
	var out []seg
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
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, seg{num: num, path: filepath.Join(dir, n), size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].num < out[j].num })
	return out, nil
}

// scanFrameOffsets walks `[size:4 LE][bytes]` chunks in a cdat segment
// and returns each chunk's start offset (= the byte before the size
// prefix). Strictly O_RDONLY.
func scanFrameOffsets(path string) ([]int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	const bufSize = 1 << 20
	buf := make([]byte, bufSize)
	pos := int64(0)
	readBase := int64(-1)
	bufFilled := 0

	ensure := func(n int) ([]byte, error) {
		if pos >= readBase && pos+int64(n) <= readBase+int64(bufFilled) {
			return buf[pos-readBase:], nil
		}
		readBase = pos
		bufFilled = 0
		end := pos + int64(bufSize)
		if end > size {
			end = size
		}
		want := int(end - pos)
		if want < n {
			return nil, io.ErrUnexpectedEOF
		}
		_, err := f.ReadAt(buf[:want], pos)
		if err != nil && err != io.EOF {
			return nil, err
		}
		bufFilled = want
		return buf[:want], nil
	}

	var offsets []int64
	for pos < size {
		head, err := ensure(4)
		if err != nil {
			return nil, fmt.Errorf("read size at %d: %w", pos, err)
		}
		l := int64(binary.LittleEndian.Uint32(head[:4]))
		if l == 0 {
			return nil, fmt.Errorf("zero-length chunk at offset %d", pos)
		}
		if pos+4+l > size {
			return nil, fmt.Errorf("chunk len %d at %d exceeds segment size %d", l, pos, size)
		}
		offsets = append(offsets, pos)
		pos += 4 + l
	}
	return offsets, nil
}
