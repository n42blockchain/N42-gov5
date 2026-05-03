// cdat-scan — STRICTLY READ-ONLY scan of cdat segment files to count
// zstd frames (= compressed batches). Does NOT touch any cidx file or
// any freezer API. Pure os.O_RDONLY + zstd frame-header parsing.
//
// A zstd frame starts with magic `28 B5 2F FD`. We walk segment files
// frame-by-frame using the Frame_Content_Size in each frame header to
// jump directly to the next frame, so a 2 GiB segment with thousands
// of frames takes a few seconds, not minutes.
//
// Output: per-segment frame count + total. With BatchSize=64 (the
// observed N42 default for senders/changesets/etc.), implied item
// count = total_frames × 64.
package main

import (
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
	dir := flag.String("dir", "", "freezer directory")
	name := flag.String("name", "", "table name (e.g. senders)")
	ext := flag.String("ext", "c", "cdat extension letter")
	batchSize := flag.Int("batch", 64, "assumed BatchSize for item-count extrapolation")
	flag.Parse()

	if *dir == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: cdat-scan --dir <freezer> --name <table>")
		os.Exit(2)
	}

	// Discover cdat segments.
	type seg struct {
		num  int
		path string
		size int64
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pattern := *name + "."
	suffix := "." + *ext + "dat"
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
		fi, err := de.Info()
		if err != nil {
			continue
		}
		segs = append(segs, seg{num: num, path: filepath.Join(*dir, n), size: fi.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].num < segs[j].num })
	if len(segs) == 0 {
		fmt.Fprintln(os.Stderr, "no cdat segments found")
		os.Exit(1)
	}

	fmt.Printf("table: %s, segments: %d, batch size assumed: %d\n", *name, len(segs), *batchSize)
	fmt.Println("|  Seg | Size         | Frames     | Last frame offset |")
	fmt.Println("|------|--------------|------------|-------------------|")

	t0 := time.Now()
	var totalFrames uint64
	var totalSize int64
	for _, s := range segs {
		frames, lastOff, err := countZstdFrames(s.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan %s: %v\n", s.path, err)
			continue
		}
		fmt.Printf("| %04d | %12d | %10d | %17d |\n", s.num, s.size, frames, lastOff)
		totalFrames += uint64(frames)
		totalSize += s.size
	}
	fmt.Println("|------|--------------|------------|-------------------|")
	fmt.Printf("scanned %d segments / %d B in %v\n", len(segs), totalSize, time.Since(t0))
	fmt.Printf("\nzstd frames total:   %d\n", totalFrames)
	fmt.Printf("implied items (=frames×%d): %d\n", *batchSize, totalFrames*uint64(*batchSize))
	fmt.Printf("expected cidx file size: %d bytes (%.1f MB)\n",
		totalFrames*uint64(*batchSize)*6, float64(totalFrames*uint64(*batchSize)*6)/(1024*1024))
}

// countZstdFrames walks the file frame-by-frame using the Frame_Content_Size
// field from each frame header. Returns (frame_count, offset_of_last_frame).
//
// zstd frame layout (RFC 8478):
//   Magic_Number          4 bytes   = 0x28 B5 2F FD (LE: FD 2F B5 28)
//   Frame_Header_Descriptor 1 byte
//   ... variable-size header fields based on descriptor flags ...
//   Block(s)
//   Content_Checksum (optional, 4 bytes if descriptor bit 2 set)
//
// To skip blocks, we'd need to parse Block_Header (3 bytes per block).
// Faster: parse only the frame descriptor to find Frame_Content_Size,
// then ALSO scan blocks to find true frame end. We do the latter for
// correctness — frames don't store their compressed size in the header.
func countZstdFrames(path string) (count int, lastOffset int64, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	const bufSize = 1 << 20 // 1 MiB streaming buffer
	buf := make([]byte, bufSize)
	pos := int64(0)
	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size := info.Size()

	// Pre-fill helper: ensure we have at least n bytes available starting
	// from `pos`. Reads into `buf` at offset readBase, returns the slice.
	readBase := int64(0)
	bufFilled := 0
	ensure := func(n int) ([]byte, error) {
		// If pos already inside the loaded window with enough bytes, reuse.
		if pos >= readBase && pos+int64(n) <= readBase+int64(bufFilled) {
			return buf[pos-readBase:], nil
		}
		readBase = pos
		bufFilled = 0
		// Read up to bufSize bytes from pos.
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

	for pos < size {
		// Frame must start with zstd magic.
		head, err := ensure(6)
		if err != nil {
			return count, lastOffset, fmt.Errorf("read magic at %d: %w", pos, err)
		}
		if !(head[0] == 0x28 && head[1] == 0xB5 && head[2] == 0x2F && head[3] == 0xFD) {
			return count, lastOffset, fmt.Errorf("at offset %d: expected zstd magic, got %x", pos, head[:4])
		}
		frameStart := pos
		// Frame_Header_Descriptor at offset 4.
		fhd := head[4]
		// Bits 7-6: Frame_Content_Size_flag (FCS_flag).
		fcsFlag := (fhd >> 6) & 0x03
		// Bit 5:   Single_Segment_flag.
		ssFlag := (fhd >> 5) & 0x01
		// Bit 3:   Reserved (must be 0).
		// Bit 2:   Content_Checksum_flag.
		ccFlag := (fhd >> 2) & 0x01
		// Bits 1-0: Dictionary_ID_flag.
		didFlag := fhd & 0x03

		hdrSize := 1 // descriptor byte
		// Window_Descriptor exists when Single_Segment_flag = 0.
		if ssFlag == 0 {
			hdrSize++
		}
		// Dictionary_ID size from didFlag.
		switch didFlag {
		case 0:
			// no dict id
		case 1:
			hdrSize += 1
		case 2:
			hdrSize += 2
		case 3:
			hdrSize += 4
		}
		// Frame_Content_Size size depends on FCS_flag and SS flag.
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

		pos += 4 + int64(hdrSize) // past magic + descriptor + variable header

		// Now walk blocks. Each block: Block_Header (3 bytes) + Block_Size bytes.
		// Block_Header: bit 0 = Last_Block, bits 1-2 = Block_Type, bits 3-23 = Block_Size.
		for {
			bh, err := ensure(3)
			if err != nil {
				return count, lastOffset, fmt.Errorf("block hdr at %d in frame %d: %w", pos, count, err)
			}
			bhU := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
			lastBlock := bhU & 0x01
			blockType := (bhU >> 1) & 0x03
			blockSize := bhU >> 3
			pos += 3
			switch blockType {
			case 0:
				// Raw_Block: literal `Block_Size` bytes.
				pos += int64(blockSize)
			case 1:
				// RLE_Block: 1 byte (the repeated value).
				pos += 1
			case 2:
				// Compressed_Block: `Block_Size` bytes.
				pos += int64(blockSize)
			default:
				return count, lastOffset, fmt.Errorf("reserved block type at frame %d block at %d", count, pos-3)
			}
			if lastBlock == 1 {
				break
			}
		}
		// Optional content checksum (4 bytes).
		if ccFlag == 1 {
			pos += 4
		}
		count++
		lastOffset = frameStart
	}
	return count, lastOffset, nil
}
