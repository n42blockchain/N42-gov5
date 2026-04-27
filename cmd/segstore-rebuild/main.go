// segstore-rebuild — rebuild a missing/truncated 12-byte SegmentStore
// cidx from intact cdat segments. Used for storhist / accthist / txindex,
// produced by internal/cscompact/segment_store.go.
//
// Format (matches segment_store.go: SegmentStoreWriter.WriteSegment):
//
//   - cidx entry (12 bytes):
//       [fileNum:2 LE][flags:2 LE][datOff:4 LE][riOff:4 LE]
//     `flags` is always 0 in the current writer; we preserve that.
//     `datOff` and `riOff` point to the BYTE BEFORE the size prefix
//     (the reader does ReadAt(buf[4], datOff) then ReadAt(data, datOff+4)).
//
//   - cdat layout per segment:
//       [datSize:4 LE][datBytes...][riSize:4 LE][riBytes...]
//     `riBytes` is OPTIONAL — when absent the segment ends right after
//     `datBytes`. We detect EOF / next-cdat-file vs an embedded ri block
//     by speculatively reading another size prefix and checking it fits.
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
	name := flag.String("name", "", "table name (e.g. storhist, accthist, txindex)")
	out := flag.String("out", "", "output cidx path (default: <dir>/<name>.cidx.recovered)")
	dryRun := flag.Bool("dry-run", false, "scan only; don't write the output cidx")
	hasRi := flag.Bool("has-ri", true, "true if segments embed a RecSplit block after dat (storhist/accthist/txindex all do)")
	flag.Parse()

	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: segstore-rebuild --dir <freezer> --name <table>")
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
	fmt.Printf("segments: %d cdat files\n", len(segs))
	fmt.Printf("has ri:   %v\n", *hasRi)
	fmt.Printf("output:   %s%s\n", *out, dryRunSuffix(*dryRun))

	type entry struct {
		fileNum uint16
		datOff  uint32
		riOff   uint32
	}
	var entries []entry
	t0 := time.Now()
	for _, s := range segs {
		segEntries, err := scanSegments(s.path, *hasRi)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan %s: %v\n", s.path, err)
			os.Exit(1)
		}
		for _, e := range segEntries {
			entries = append(entries, entry{
				fileNum: uint16(s.num),
				datOff:  uint32(e.datOff),
				riOff:   uint32(e.riOff),
			})
		}
	}
	fmt.Printf("\nscanned %d segments in %v\n", len(entries), time.Since(t0))
	fmt.Printf("rebuilt cidx size: %d bytes\n", len(entries)*12)
	fmt.Printf("approx block coverage: %d blocks (entries × 1,000,000)\n", len(entries)*1_000_000)

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
	for _, e := range entries {
		var b [12]byte
		binary.LittleEndian.PutUint16(b[0:2], e.fileNum)
		// b[2:4] flags = 0
		binary.LittleEndian.PutUint32(b[4:8], e.datOff)
		binary.LittleEndian.PutUint32(b[8:12], e.riOff)
		if _, err := f.Write(b[:]); err != nil {
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
	fmt.Printf("  1. Verify size: %d bytes\n", len(entries)*12)
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

type segEntry struct {
	datOff int64
	riOff  int64 // 0 if absent
}

// scanSegments walks `[len:4 LE][bytes]` chunks in a cdat file. With
// --has-ri true (the only case we ship for now), every segment is
// exactly TWO consecutive chunks: dat then ri. Without --has-ri each
// segment is a single chunk.
func scanSegments(path string, hasRi bool) ([]segEntry, error) {
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

	var entries []segEntry
	for pos < size {
		datStart := pos
		head, err := ensure(4)
		if err != nil {
			return nil, fmt.Errorf("read dat size at %d: %w", pos, err)
		}
		datLen := int64(binary.LittleEndian.Uint32(head[:4]))
		if pos+4+datLen > size {
			return nil, fmt.Errorf("dat chunk len %d at %d exceeds segment size %d (entry %d)",
				datLen, pos, size, len(entries))
		}
		pos += 4 + datLen

		entry := segEntry{datOff: datStart}
		if hasRi {
			if pos >= size {
				return nil, fmt.Errorf("expected ri chunk at %d but cdat ended (entry %d, datLen=%d)",
					pos, len(entries), datLen)
			}
			riStart := pos
			head, err := ensure(4)
			if err != nil {
				return nil, fmt.Errorf("read ri size at %d: %w", pos, err)
			}
			riLen := int64(binary.LittleEndian.Uint32(head[:4]))
			if pos+4+riLen > size {
				return nil, fmt.Errorf("ri chunk len %d at %d exceeds segment size %d (entry %d)",
					riLen, pos, size, len(entries))
			}
			entry.riOff = riStart
			pos += 4 + riLen
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
