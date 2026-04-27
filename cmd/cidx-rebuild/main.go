// cidx-rebuild — rebuild a missing/truncated cidx file from intact cdat
// segments by walking zstd frames.
//
// SAFETY:
//   - cdat segments opened O_RDONLY only. Never modified, truncated, or
//     touched by any freezer API.
//   - Output is written to a NEW path (default: <name>.cidx.recovered)
//     so the existing on-disk cidx is left intact. Operator manually
//     swaps after verifying.
//
// FORMAT ASSUMPTIONS (must match the original writer):
//   - Header-less (legacy) cidx: 6 bytes per entry, no 16-byte header.
//     The N42 freezer's auto-detect treats files with no 'CIDX' magic
//     at byte 0 as legacy.
//   - BatchSize: same value used by the original writer (default 64).
//     One zstd frame == one batch == BatchSize logical items, except
//     possibly the last batch which may be partial.
//   - Each cidx entry is encodeIndex(fileNum:2B BE, offset:4B BE) where
//     `offset` is the byte offset of the zstd frame's first byte (the
//     0x28 0xB5 0x2F 0xFD magic) within its cdat segment.
//
// Last-batch handling:
//   - The final frame may contain fewer than BatchSize items. We
//     decompress it (zstd) and walk the [len:4 BE][item][len:4 BE][...]
//     framing produced by EncodeBatch in modules/rawdb/freezer/compact.go
//     to count the items present.
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

	"github.com/klauspost/compress/zstd"
)

func main() {
	dir := flag.String("dir", "", "freezer directory containing cdat segments")
	name := flag.String("name", "", "table name (e.g. senders)")
	ext := flag.String("ext", "c", "cdat extension letter")
	batchSize := flag.Int("batch", 64, "items per zstd frame (must match original writer)")
	out := flag.String("out", "", "output cidx path (default: <dir>/<name>.<ext>idx.recovered)")
	dryRun := flag.Bool("dry-run", false, "scan only; don't write the output cidx")
	flag.Parse()

	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: cidx-rebuild --dir <freezer> --name <table>")
		os.Exit(2)
	}
	if *out == "" {
		*out = filepath.Join(*dir, fmt.Sprintf("%s.%sidx.recovered", *name, *ext))
	}

	// Discover cdat segments in numeric order.
	segs, err := discoverSegments(*dir, *name, *ext)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(segs) == 0 {
		fmt.Fprintln(os.Stderr, "no cdat segments found")
		os.Exit(1)
	}

	fmt.Printf("table:      %s\n", *name)
	fmt.Printf("segments:   %d\n", len(segs))
	fmt.Printf("batch size: %d\n", *batchSize)
	fmt.Printf("output:     %s%s\n", *out, dryRunSuffix(*dryRun))

	var frames []frame
	var lastSeg = segs[len(segs)-1]
	var lastFrameOffset int64
	var lastFrameCompressed []byte
	t0 := time.Now()

	for _, s := range segs {
		offsets, err := scanFrameOffsets(s.path, *batchSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan %s: %v\n", s.path, err)
			os.Exit(1)
		}
		for _, off := range offsets {
			frames = append(frames, frame{fileNum: uint16(s.num), offset: uint32(off)})
		}
		if s.num == lastSeg.num && len(offsets) > 0 {
			lastFrameOffset = offsets[len(offsets)-1]
			lastFrameCompressed = readTailFrame(s.path, lastFrameOffset)
		}
	}
	fmt.Printf("\nscanned %d frames in %v\n", len(frames), time.Since(t0))

	// Determine last-batch item count by decompressing the last frame and
	// walking its 4-byte length prefixes.
	lastBatchItems := *batchSize
	if len(lastFrameCompressed) > 0 {
		n, err := countItemsInBatch(lastFrameCompressed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode last frame: %v\n", err)
			fmt.Fprintln(os.Stderr, "  (will assume the last batch is FULL — verify items count manually after rebuild)")
		} else {
			lastBatchItems = n
		}
	}
	fmt.Printf("last frame items: %d (full batch = %d)\n", lastBatchItems, *batchSize)

	totalItems := uint64(len(frames)-1)*uint64(*batchSize) + uint64(lastBatchItems)
	if len(frames) == 0 {
		totalItems = 0
	}
	fmt.Printf("rebuilt items count: %d\n", totalItems)
	fmt.Printf("rebuilt cidx size:   %d bytes (%.1f MB)\n",
		totalItems*6, float64(totalItems*6)/(1024*1024))

	if *dryRun {
		fmt.Println("\n--dry-run: NOT writing output. Re-run without --dry-run to materialize.")
		return
	}

	if err := writeCidx(*out, frames, *batchSize, lastBatchItems); err != nil {
		fmt.Fprintln(os.Stderr, "write cidx:", err)
		os.Exit(1)
	}
	fmt.Printf("\nwrote %s\n", *out)
	fmt.Printf("\nNEXT STEPS (manual, do NOT let any tool open the freezer in RW mode):\n")
	fmt.Printf("  1. Verify size: %d bytes\n", totalItems*6)
	fmt.Printf("  2. Backup the existing damaged file:\n     mv %s %s.damaged.bak\n",
		filepath.Join(*dir, fmt.Sprintf("%s.%sidx", *name, *ext)),
		filepath.Join(*dir, fmt.Sprintf("%s.%sidx", *name, *ext)))
	fmt.Printf("  3. Promote the recovered file:\n     mv %s %s\n",
		*out, filepath.Join(*dir, fmt.Sprintf("%s.%sidx", *name, *ext)))
	fmt.Printf("  4. Sanity check with cidx-inspect (read-only):\n     build/bin/cidx-inspect.exe --dir %q --name %s\n", *dir, *name)
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

// frame: one zstd frame's location within a cdat segment. Each frame
// holds one batch's worth of items (compressed).
type frame struct {
	fileNum uint16
	offset  uint32
}

func discoverSegments(dir, name, ext string) ([]seg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	pattern := name + "."
	suffix := "." + ext + "dat"
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

// scanFrameOffsets walks BATCH boundaries in a cdat segment and returns
// each batch's start offset. A "batch" here is either:
//   - a zstd-compressed frame (starts with 28 B5 2F FD magic) — produced
//     when EncodeBatch found compression beneficial, OR
//   - a RAW length-prefixed buffer [len0:4LE][item0][len1:4LE][item1]...
//     for exactly BatchSize items — produced when zstd compression did
//     NOT make the buffer smaller (freezer.go:82-89). The first 4 bytes
//     are the LE length of item 0 — small numeric, NOT the zstd magic.
//
// This is an inherent property of the freezer's encoding contract; ANY
// rebuild tool must handle both shapes. Pure os.O_RDONLY.
func scanFrameOffsets(path string, batchSize int) ([]int64, error) {
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
			return nil, fmt.Errorf("read head at %d: %w", pos, err)
		}
		offsets = append(offsets, pos)

		isZstd := head[0] == 0x28 && head[1] == 0xB5 && head[2] == 0x2F && head[3] == 0xFD
		if isZstd {
			head6, err := ensure(6)
			if err != nil {
				return nil, fmt.Errorf("read fhd at %d: %w", pos, err)
			}
			fhd := head6[4]
			fcsFlag := (fhd >> 6) & 0x03
			ssFlag := (fhd >> 5) & 0x01
			ccFlag := (fhd >> 2) & 0x01
			didFlag := fhd & 0x03

			hdrSize := 1
			if ssFlag == 0 {
				hdrSize++
			}
			switch didFlag {
			case 1:
				hdrSize += 1
			case 2:
				hdrSize += 2
			case 3:
				hdrSize += 4
			}
			switch fcsFlag {
			case 0:
				if ssFlag == 1 {
					hdrSize += 1
				}
			case 1:
				hdrSize += 2
			case 2:
				hdrSize += 4
			case 3:
				hdrSize += 8
			}
			pos += 4 + int64(hdrSize)

			for {
				bh, err := ensure(3)
				if err != nil {
					return nil, fmt.Errorf("block hdr at %d: %w", pos, err)
				}
				bhU := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
				lastBlock := bhU & 0x01
				blockType := (bhU >> 1) & 0x03
				blockSize := bhU >> 3
				pos += 3
				switch blockType {
				case 0:
					pos += int64(blockSize)
				case 1:
					pos += 1
				case 2:
					pos += int64(blockSize)
				default:
					return nil, fmt.Errorf("reserved block type at %d", pos-3)
				}
				if lastBlock == 1 {
					break
				}
			}
			if ccFlag == 1 {
				pos += 4
			}
			continue
		}

		// Raw batch: walk exactly batchSize length-prefixed items
		// [len:4 LE][item][len:4 LE][item]... The last batch may be
		// partial — stop early on EOF rather than erroring.
		for i := 0; i < batchSize; i++ {
			if pos >= size {
				break
			}
			lenBytes, err := ensure(4)
			if err != nil {
				return nil, fmt.Errorf("read raw item len at %d (batch start %d, item %d): %w",
					pos, offsets[len(offsets)-1], i, err)
			}
			itemLen := int64(binary.LittleEndian.Uint32(lenBytes[:4]))
			pos += 4
			if pos+itemLen > size {
				return nil, fmt.Errorf("raw item len %d at %d exceeds segment size %d (batch start %d, item %d)",
					itemLen, pos-4, size, offsets[len(offsets)-1], i)
			}
			pos += itemLen
		}
	}
	return offsets, nil
}

// readTailFrame reads from the given offset to EOF and returns the bytes.
// Used to grab the last compressed frame for partial-batch counting.
func readTailFrame(path string, offset int64) []byte {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	size := info.Size()
	if offset >= size {
		return nil
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return nil
	}
	return buf
}

// countItemsInBatch counts items in the last batch of a cdat segment.
// Auto-detects format: zstd magic (28 B5 2F FD) → decompress first;
// otherwise treat as already-raw. Then walks the [len:4 LE][item]
// ... framing produced by freezer.EncodeBatch (freezer.go:67-90).
func countItemsInBatch(blob []byte) (int, error) {
	var raw []byte
	if len(blob) >= 4 && blob[0] == 0x28 && blob[1] == 0xB5 && blob[2] == 0x2F && blob[3] == 0xFD {
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return 0, err
		}
		defer dec.Close()
		raw, err = dec.DecodeAll(blob, nil)
		if err != nil {
			return 0, err
		}
	} else {
		raw = blob
	}
	count := 0
	pos := 0
	for pos+4 <= len(raw) {
		l := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
		pos += 4
		if pos+l > len(raw) {
			return 0, fmt.Errorf("item len %d at byte %d exceeds buffer (size %d)", l, pos-4, len(raw))
		}
		pos += l
		count++
	}
	if pos != len(raw) {
		return 0, fmt.Errorf("trailing bytes after last item: %d remain", len(raw)-pos)
	}
	return count, nil
}

// writeCidx materializes a header-less cidx file. For each frame, write
// `batchSize` entries (or `lastItems` for the final frame) all pointing
// at (frame.fileNum, frame.offset).
func writeCidx(path string, frames []frame, batchSize, lastItems int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// 64 KiB buffer ⇒ ~10K entries per syscall.
	const bufSize = 64 * 1024
	buf := make([]byte, 0, bufSize)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := f.Write(buf); err != nil {
			return err
		}
		buf = buf[:0]
		return nil
	}
	for i, fr := range frames {
		count := batchSize
		if i == len(frames)-1 {
			count = lastItems
		}
		var entry [6]byte
		binary.BigEndian.PutUint16(entry[0:2], fr.fileNum)
		binary.BigEndian.PutUint32(entry[2:6], fr.offset)
		for k := 0; k < count; k++ {
			buf = append(buf, entry[:]...)
			if len(buf)+6 > bufSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return f.Sync()
}
