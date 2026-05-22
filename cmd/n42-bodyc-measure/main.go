// n42-bodyc-measure samples bodyc segments and projects size
// under more aggressive zstd settings.
//
// bodyc segments are pre-compressed at SpeedBestCompression (zstd
// level 19) over a per-segment payload of 8192 blocks. The
// per-segment headers are tiny; the bulk of bytes is the single
// compressed blob.
//
// This tool reads each `.cdat` file directly, walks segment frames,
// and for a configurable sample fraction:
//
//   - decompresses the segment payload
//   - re-compresses at level 19 + 128 MiB window (--long=27)
//   - reports per-segment and aggregate ratios
//
// Reads READ-ONLY; never writes back to the freezer. No transcode
// — that's a separate job once the projection looks worthwhile.
//
// Usage:
//
//	n42-bodyc-measure --freezer D:\N42-eth1177\chain\freezer
//	  --table bodyc --sample 32   (sample every 32nd segment)
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

func main() {
	freezerDir := flag.String("freezer", `D:\N42-eth1177\chain\freezer`, "freezer dir")
	table := flag.String("table", "bodyc", "table prefix (bodyc, headerc, …)")
	sample := flag.Int("sample", 16, "sample every Nth segment (1 = all)")
	maxSegments := flag.Int("max-segments", 100, "stop after this many sampled segments")
	flag.Parse()

	if _, err := os.Stat(*freezerDir); err != nil {
		die("freezer %s not present: %v", *freezerDir, err)
	}

	cidxPath := filepath.Join(*freezerDir, *table+".cidx")
	cidx, err := os.ReadFile(cidxPath)
	if err != nil {
		die("read %s: %v", cidxPath, err)
	}
	// Columnar cidx layout: 8 B per segment (fileNum:2B LE + reserved:2B + offset:4B LE).
	const entrySize = 8
	if len(cidx)%entrySize != 0 {
		die("%s: size %d not multiple of %d", cidxPath, len(cidx), entrySize)
	}
	segments := len(cidx) / entrySize
	fmt.Printf("table %s: %d segments in %s\n", *table, segments, cidxPath)

	// Decompressor for current bodyc format.
	dec, err := zstd.NewReader(nil)
	if err != nil {
		die("zstd reader: %v", err)
	}
	defer dec.Close()
	// Re-compressor: BestCompression + long=27 window.
	encLong, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(128<<20))
	if err != nil {
		die("zstd writer (long): %v", err)
	}
	defer encLong.Close()

	t0 := time.Now()
	lastLog := t0
	var (
		sampled   int
		rawTotal  uint64
		curTotal  uint64
		longTotal uint64
	)
	// File handle cache.
	files := make(map[uint16]*os.File)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	for seg := 0; seg < segments && sampled < *maxSegments; seg += *sample {
		entry := cidx[seg*entrySize : (seg+1)*entrySize]
		fileNum := binary.LittleEndian.Uint16(entry[0:2])
		offset := binary.LittleEndian.Uint32(entry[4:8])

		// End offset = next segment's offset (or file size if last).
		var endOff uint64
		if seg+1 < segments {
			nextEntry := cidx[(seg+1)*entrySize : (seg+2)*entrySize]
			nextFileNum := binary.LittleEndian.Uint16(nextEntry[0:2])
			nextOff := binary.LittleEndian.Uint32(nextEntry[4:8])
			if nextFileNum == fileNum {
				endOff = uint64(nextOff)
			} else {
				// Segment ends at end of current file.
				st, _ := osStat(*freezerDir, *table, fileNum)
				endOff = uint64(st)
			}
		} else {
			st, _ := osStat(*freezerDir, *table, fileNum)
			endOff = uint64(st)
		}
		segLen := endOff - uint64(offset)
		if segLen == 0 {
			continue
		}

		f, ok := files[fileNum]
		if !ok {
			path := filepath.Join(*freezerDir, fmt.Sprintf("%s.%04d.cdat", *table, fileNum))
			f, err = os.Open(path)
			if err != nil {
				die("open %s: %v", path, err)
			}
			files[fileNum] = f
		}
		raw := make([]byte, segLen)
		if _, err := f.ReadAt(raw, int64(offset)); err != nil {
			die("read %s seg %d: %v", *table, seg, err)
		}

		// Skip the 4-byte size-prefix the bodyc writer adds in
		// front of each compressed payload (body_compact.go:710).
		if len(raw) < 4 {
			continue
		}
		payload := raw[4:]
		curTotal += uint64(len(payload))

		// Decompress payload.
		dec, derr := dec.DecodeAll(payload, nil)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "seg %d decode: %v\n", seg, derr)
			continue
		}
		rawTotal += uint64(len(dec))

		// Re-compress at long window.
		var buf bytes.Buffer
		compressed := encLong.EncodeAll(dec, buf.Bytes()[:0])
		longTotal += uint64(len(compressed))

		sampled++
		if time.Since(lastLog) > 10*time.Second {
			fmt.Printf("  sampled=%d  raw=%.2f GB  cur=%.2f GB  long=%.2f GB  elapsed=%s\n",
				sampled,
				float64(rawTotal)/1024/1024/1024,
				float64(curTotal)/1024/1024/1024,
				float64(longTotal)/1024/1024/1024,
				time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}

	elapsed := time.Since(t0)
	fmt.Println()
	fmt.Printf("=== %s — %d/%d segments sampled (1-in-%d) in %s ===\n",
		*table, sampled, segments, *sample, elapsed.Truncate(time.Second))
	fmt.Printf("uncompressed raw  : %.2f GB  (avg %.0f KB/seg)\n",
		float64(rawTotal)/1024/1024/1024, float64(rawTotal)/1024/float64(sampled))
	fmt.Printf("current zstd-19   : %.2f GB  (avg %.0f KB/seg, ratio %.1f%%)\n",
		float64(curTotal)/1024/1024/1024, float64(curTotal)/1024/float64(sampled),
		100*float64(curTotal)/float64(rawTotal))
	fmt.Printf("best + long=27    : %.2f GB  (avg %.0f KB/seg, ratio %.1f%%)\n",
		float64(longTotal)/1024/1024/1024, float64(longTotal)/1024/float64(sampled),
		100*float64(longTotal)/float64(rawTotal))

	saving := int64(curTotal) - int64(longTotal)
	pct := 100 * float64(saving) / float64(curTotal)
	fmt.Printf("\nProjected saving vs current: %+6.2f GB (%+5.1f%%)\n",
		float64(saving)/1024/1024/1024, pct)
	// Extrapolate to the full table by ratio of sampled-segment-coverage
	// to total-segment-count.
	extrap := float64(saving) * float64(segments) / float64(sampled)
	fmt.Printf("Extrapolated to full %d segments: %+6.2f GB\n",
		segments, extrap/1024/1024/1024)
}

func osStat(dir, table string, fileNum uint16) (int64, error) {
	st, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%s.%04d.cdat", table, fileNum)))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
